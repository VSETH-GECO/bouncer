package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
	"github.com/go-jet/jet/v2/mysql"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// gaugeValue and counterValue read a collector without registering it (see internal/metrics' tests).
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("reading gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("reading counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// fixture is one client authorized for a VLAN with a fresh Access-Accept from a switch of the switch map.
type fixture struct {
	ctx      context.Context
	tx       *sql.Tx
	p        *testgen.Gen
	client   mac.MAC
	assigned int
	sw       testgen.Switch
	accept   model.Radpostauth
}

// switchVLANs returns the event VLAN IDs of s.
func switchVLANs(s testgen.Switch) []int {
	var ids []int
	for _, v := range s.VLANs {
		ids = append(ids, int(v.VlanID))
	}
	return ids
}

// otherVLAN draws a VLAN that is neither the login VLAN nor among taken.
func otherVLAN(p *testgen.Gen, taken []int) int {
	for {
		v := 2 + p.Rand().IntN(4093)
		if v != loginVLAN && !slices.Contains(taken, v) {
			return v
		}
	}
}

// newFixture builds a switch (with testgen's random event VLAN unless swMods change it), a client authorized for
// assigned (or, when assigned is 0, for a VLAN the switch does not carry; when assigned is negative, not authorized at
// all) and an Access-Accept of the client from the switch's first IP (a random IP when it has none), sent now.
func newFixture(t *testing.T, assigned int, swMods ...func(*testgen.Switch)) *fixture {
	t.Helper()
	tx := testdb.Tx(t)
	p := testgen.Persistent(t, tx)
	f := &fixture{ctx: context.Background(), tx: tx, p: p, client: p.MAC()}
	f.sw = p.Switch(swMods...)
	switch {
	case assigned == 0:
		f.assigned = otherVLAN(p, switchVLANs(f.sw))
		p.Authorized(f.client, f.assigned)
	case assigned > 0:
		f.assigned = assigned
		p.Authorized(f.client, assigned)
	}
	f.accept = f.acceptFrom(f.sw, time.Now().UTC().Truncate(time.Second))
	return f
}

// acceptFrom inserts an Access-Accept of the fixture's client from the first IP of sw (a random IP when sw has none)
// at at, telling the switch the client's assigned VLAN.
func (f *fixture) acceptFrom(sw testgen.Switch, at time.Time) model.Radpostauth {
	ip := f.p.IPv4().String()
	if len(sw.IPs) > 0 {
		ip = sw.IPs[0].IP
	}
	vlan := strconv.Itoa(f.assigned)
	return f.p.Accept(func(r *model.Radpostauth) {
		r.Username, r.Pass, r.Authdate, r.Nasipaddress, r.Vlan = f.client.Hex(), f.client.Hex(), at, &ip, &vlan
	})
}

// goodSwitch builds a switch whose only event VLAN is the client's assigned VLAN.
func (f *fixture) goodSwitch() testgen.Switch {
	return f.p.Switch(func(s *testgen.Switch) {
		s.VLANs[0].VlanID = int32(f.assigned)
		s.Map.PrimaryVlan = int32(f.assigned)
	})
}

// age moves the fixture's accept d into the past.
func (f *fixture) age(t *testing.T, d time.Duration) {
	t.Helper()
	at := f.accept.Authdate.Add(-d)
	_, err := table.Radpostauth.UPDATE().SET(table.Radpostauth.Authdate.SET(mysql.TimestampT(at))).
		WHERE(table.Radpostauth.ID.EQ(mysql.Int32(f.accept.ID))).ExecContext(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
	f.accept.Authdate = at
}

func (f *fixture) round(t *testing.T, r *Reconciler) {
	t.Helper()
	if err := r.Round(f.ctx); err != nil {
		t.Fatalf("Round: %v", err)
	}
}

// pending returns the pending jobs of the fixture's client.
func (f *fixture) pending(t *testing.T) []jobs.Job {
	t.Helper()
	list, err := jobs.Pending(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
	var mine []jobs.Job
	for _, j := range list {
		if j.MAC == f.client {
			mine = append(mine, j)
		}
	}
	return mine
}

func isStuck(r *Reconciler, m mac.MAC) bool {
	_, ok := r.stuck[m]
	return ok
}

func TestRound_StuckClientDryRun(t *testing.T) {
	f := newFixture(t, 0)
	r := New(f.tx, true)
	bounces := counterValue(t, stuckBounces)

	for i := range 2 {
		f.round(t, r)
		if got := gaugeValue(t, stuckClients); got != 1 {
			t.Errorf("round %d: bouncer_stuck_clients = %v, want 1", i+1, got)
		}
		if !isStuck(r, f.client) {
			t.Errorf("round %d: client not in the stuck set", i+1)
		}
		if got := f.pending(t); len(got) != 0 {
			t.Errorf("round %d: dry run queued jobs %+v", i+1, got)
		}
	}
	if got := counterValue(t, stuckBounces) - bounces; got != 0 {
		t.Errorf("bouncer_stuck_bounces_total grew by %v in dry run, want 0", got)
	}
}

func TestRound_StuckClientQueuesBounce(t *testing.T) {
	f := newFixture(t, 0)
	r := New(f.tx, false)
	bounces := counterValue(t, stuckBounces)

	f.round(t, r)
	got := f.pending(t)
	if len(got) != 1 || got[0].TargetVLAN != loginVLAN {
		t.Fatalf("pending jobs = %+v, want exactly one to VLAN %d", got, loginVLAN)
	}
	if d := counterValue(t, stuckBounces) - bounces; d != 1 {
		t.Errorf("bouncer_stuck_bounces_total grew by %v, want 1", d)
	}

	f.round(t, r)
	if got := f.pending(t); len(got) != 1 {
		t.Errorf("after a second round: pending jobs = %+v, want still one", got)
	}
	if d := counterValue(t, stuckBounces) - bounces; d != 1 {
		t.Errorf("after a second round: bouncer_stuck_bounces_total grew by %v, want 1", d)
	}
	if g := gaugeValue(t, stuckClients); g != 1 {
		t.Errorf("bouncer_stuck_clients = %v, want 1", g)
	}
}

func TestRound_CooldownAfterExhaustedJob(t *testing.T) {
	f := newFixture(t, 0)
	exhausted := f.p.Job(func(j *model.BouncerJobs) {
		j.ClientMAC, j.TargetVLAN, j.Retires = f.client.Hex(), loginVLAN, jobs.MaxRetries
		j.LastUpdate = time.Now().UTC().Truncate(time.Second)
	})
	r := New(f.tx, false)

	f.round(t, r)
	latest, err := jobs.Latest(f.ctx, f.tx, f.client)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != int(exhausted.ID) {
		t.Fatalf("a job %+v was queued within the cool-down", latest)
	}

	_, err = table.BouncerJobs.UPDATE().
		SET(table.BouncerJobs.LastUpdate.SET(mysql.TimestampT(time.Now().UTC().Add(-11*time.Minute)))).
		WHERE(table.BouncerJobs.ID.EQ(mysql.Int32(exhausted.ID))).ExecContext(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
	f.round(t, r)
	got := f.pending(t)
	if len(got) != 1 || got[0].TargetVLAN != loginVLAN || got[0].ID == int(exhausted.ID) {
		t.Errorf("after the cool-down: pending jobs = %+v, want one new job to VLAN %d", got, loginVLAN)
	}
}

// assertNotStuck runs one non-dry round and checks that the fixture's client is left alone.
func assertNotStuck(t *testing.T, f *fixture) {
	t.Helper()
	r := New(f.tx, false)
	f.round(t, r)
	if g := gaugeValue(t, stuckClients); g != 0 {
		t.Errorf("bouncer_stuck_clients = %v, want 0", g)
	}
	if isStuck(r, f.client) || len(r.stuck) != 0 {
		t.Errorf("stuck set = %v, want empty", r.stuck)
	}
	if got := f.pending(t); len(got) != 0 {
		t.Errorf("queued jobs %+v, want none", got)
	}
	if _, err := jobs.Latest(f.ctx, f.tx, f.client); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("jobs.Latest: err = %v, want db.ErrNotFound", err)
	}
}

func TestRound_NotStuckWhenSwitchCarriesVLAN(t *testing.T) {
	var carried int32
	f := newFixture(t, -1, func(s *testgen.Switch) {
		s.VLANs = append(s.VLANs, model.BouncerVlan{})
		carried = s.VLANs[0].VlanID
	})
	f.p.Authorized(f.client, int(carried))
	assertNotStuck(t, f)
}

func TestRound_SkipsUnknownSwitch(t *testing.T) {
	f := newFixture(t, 0, func(s *testgen.Switch) { s.IPs = nil })
	assertNotStuck(t, f)
}

func TestRound_SkipsLoginVLAN(t *testing.T) {
	f := newFixture(t, loginVLAN, func(s *testgen.Switch) {
		for i := range s.VLANs {
			if s.VLANs[i].VlanID == loginVLAN {
				s.VLANs[i].VlanID = loginVLAN + 1
			}
		}
	})
	assertNotStuck(t, f)
}

func TestRound_SkipsSwitchWithoutVLANs(t *testing.T) {
	f := newFixture(t, 0, func(s *testgen.Switch) { s.VLANs = nil })
	assertNotStuck(t, f)
}

func TestRound_SkipsClientWithoutRadreply(t *testing.T) {
	f := newFixture(t, -1)
	assertNotStuck(t, f)
}

// TestRound_SkipsOldAccept covers a client whose last accept is older than the accept window: it has probably left,
// so it is not judged.
func TestRound_SkipsOldAccept(t *testing.T) {
	f := newFixture(t, 0)
	f.age(t, acceptWindow+time.Minute)
	assertNotStuck(t, f)
}

func TestRound_SkipsRejects(t *testing.T) {
	f := newFixture(t, 0)
	_, err := table.Radpostauth.UPDATE().SET(table.Radpostauth.Reply.SET(mysql.String("Access-Reject"))).
		WHERE(table.Radpostauth.ID.EQ(mysql.Int32(f.accept.ID))).ExecContext(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
	assertNotStuck(t, f)
}

// TestRound_OnlyNewestAcceptCounts covers a client seen on two switches within the window: only the switch of its
// newest accept counts.
func TestRound_OnlyNewestAcceptCounts(t *testing.T) {
	t.Run("newest on a switch carrying the VLAN", func(t *testing.T) {
		f := newFixture(t, 0)
		f.age(t, 5*time.Minute)
		f.acceptFrom(f.goodSwitch(), time.Now().UTC().Truncate(time.Second))
		assertNotStuck(t, f)
	})
	t.Run("newest on a switch lacking the VLAN", func(t *testing.T) {
		f := newFixture(t, 0)
		f.acceptFrom(f.goodSwitch(), f.accept.Authdate.Add(-5*time.Minute))
		r := New(f.tx, true)
		found, err := r.find(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 1 || found[0].mac != f.client || found[0].sw.Hostname != f.sw.Map.Hostname {
			t.Errorf("found %+v, want %v on %s", found, f.client, f.sw.Map.Hostname)
		}
	})
}

// TestRound_RepatchedClientIsNotBounced covers an admin moving a stuck client to a VLAN its switch carries: the
// client's next authentication fixes it, so the loop must not override the admin with a bounce to the login VLAN,
// even though the newest accept still told the switch the old VLAN.
func TestRound_RepatchedClientIsNotBounced(t *testing.T) {
	rec := recordLogs(t)
	f := newFixture(t, 0, func(s *testgen.Switch) { s.VLANs = append(s.VLANs, model.BouncerVlan{}) })
	r := New(f.tx, true)
	f.round(t, r)
	if !isStuck(r, f.client) {
		t.Fatal("client not stuck after round 1")
	}
	rec.take(msgStuck)

	if err := freeradius.SetVLAN(f.ctx, f.tx, f.client, int(f.sw.VLANs[1].VlanID)); err != nil {
		t.Fatal(err)
	}
	r.dryRun = false
	f.round(t, r)
	if isStuck(r, f.client) || len(r.stuck) != 0 {
		t.Errorf("stuck set = %v after the re-patch, want empty", r.stuck)
	}
	if n := rec.take(msgUnstuck); n != 1 {
		t.Errorf("%d %q lines, want 1", n, msgUnstuck)
	}
	if got := f.pending(t); len(got) != 0 {
		t.Errorf("queued jobs %+v, want none", got)
	}
	if _, err := jobs.Latest(f.ctx, f.tx, f.client); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("jobs.Latest: err = %v, want db.ErrNotFound", err)
	}
}

func TestRound_ClearsWhenNoLongerStuck(t *testing.T) {
	f := newFixture(t, 0)
	f.age(t, time.Minute)
	r := New(f.tx, true)
	f.round(t, r)
	if !isStuck(r, f.client) {
		t.Fatal("client not stuck after round 1")
	}
	f.acceptFrom(f.goodSwitch(), time.Now().UTC().Truncate(time.Second))
	f.round(t, r)
	if len(r.stuck) != 0 {
		t.Errorf("stuck set = %v after an accept on a switch carrying the VLAN, want empty", r.stuck)
	}
	if g := gaugeValue(t, stuckClients); g != 0 {
		t.Errorf("bouncer_stuck_clients = %v, want 0", g)
	}
}

// record is a log record's message and attributes.
type record struct {
	msg   string
	attrs map[string]slog.Value
}

// recorder is a slog.Handler that keeps the records at Info and above.
type recorder struct {
	mu   sync.Mutex
	recs []record
}

func (h *recorder) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }
func (h *recorder) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]slog.Value{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, record{msg: r.Message, attrs: attrs})
	return nil
}
func (h *recorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recorder) WithGroup(string) slog.Handler      { return h }

// recordLogs makes a recorder the default logger for the rest of the test.
func recordLogs(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

// takeRecords returns the records with message msg since the last take and forgets all records.
func (h *recorder) takeRecords(msg string) []record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []record
	for _, r := range h.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	h.recs = nil
	return out
}

// take returns how often msg was logged since the last take and forgets the records.
func (h *recorder) take(msg string) int { return len(h.takeRecords(msg)) }

func TestRound_LogsTransitionsOnce(t *testing.T) {
	rec := recordLogs(t)
	f := newFixture(t, 0)
	f.age(t, time.Minute)
	r := New(f.tx, true)

	f.round(t, r)
	recs := rec.takeRecords(msgStuck)
	if len(recs) != 1 {
		t.Fatalf("round 1: %d %q lines, want 1", len(recs), msgStuck)
	}
	attrs := recs[0].attrs
	if v, ok := attrs["replyVLAN"]; !ok || v.Int64() != int64(f.assigned) {
		t.Errorf("replyVLAN = %v, want %d", v, f.assigned)
	}
	if v, ok := attrs["switchPort"]; !ok || v.String() != *f.accept.Nasportid {
		t.Errorf("switchPort = %v, want %s", v, *f.accept.Nasportid)
	}
	if v, ok := attrs["lastAccept"]; !ok || v.Kind() != slog.KindDuration || v.Duration() < time.Minute {
		t.Errorf("lastAccept = %v, want a duration of at least a minute", v)
	}
	if v, ok := attrs["switch"]; !ok || v.String() != f.sw.Map.Hostname {
		t.Errorf("switch = %v, want %s", v, f.sw.Map.Hostname)
	}

	f.round(t, r)
	if n := rec.take(msgStuck); n != 0 {
		t.Errorf("round 2: %d %q lines, want 0", n, msgStuck)
	}
	f.acceptFrom(f.goodSwitch(), time.Now().UTC().Truncate(time.Second))
	f.round(t, r)
	if n := rec.take(msgUnstuck); n != 1 {
		t.Errorf("round 3: %d %q lines, want 1", n, msgUnstuck)
	}
}
