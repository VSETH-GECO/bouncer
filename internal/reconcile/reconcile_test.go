package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
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

// fixture is one client with an open session on a switch of the switch map.
type fixture struct {
	ctx     context.Context
	tx      *sql.Tx
	p       *testgen.Gen
	client  mac.MAC
	sw      testgen.Switch
	session model.Radacct
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
// all) and an open session of the client on the switch's IP.
func newFixture(t *testing.T, assigned int, swMods ...func(*testgen.Switch)) *fixture {
	t.Helper()
	tx := testdb.Tx(t)
	p := testgen.Persistent(t, tx)
	f := &fixture{ctx: context.Background(), tx: tx, p: p, client: p.MAC()}
	f.sw = p.Switch(swMods...)
	switch {
	case assigned == 0:
		p.Authorized(f.client, otherVLAN(p, switchVLANs(f.sw)))
	case assigned > 0:
		p.Authorized(f.client, assigned)
	}
	ip := ""
	if len(f.sw.IPs) > 0 {
		ip = f.sw.IPs[0].IP
	}
	f.session = p.Session(func(r *model.Radacct) { r.Username, r.Nasipaddress = f.client.Hex(), ip })
	return f
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

func (f *fixture) closeSession(t *testing.T) {
	t.Helper()
	_, err := table.Radacct.UPDATE().SET(table.Radacct.Acctstoptime.SET(mysql.CURRENT_TIMESTAMP())).
		WHERE(table.Radacct.Radacctid.EQ(mysql.Int(f.session.Radacctid))).ExecContext(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
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

func TestRound_SkipsClosedSession(t *testing.T) {
	f := newFixture(t, 0)
	f.closeSession(t)
	assertNotStuck(t, f)
}

func TestRound_SkipsUnknownSwitch(t *testing.T) {
	f := newFixture(t, 0, func(s *testgen.Switch) { s.IPs = nil })
	_, err := table.Radacct.UPDATE().SET(table.Radacct.Nasipaddress.SET(mysql.String(f.p.IPv4().String()))).
		WHERE(table.Radacct.Radacctid.EQ(mysql.Int(f.session.Radacctid))).ExecContext(f.ctx, f.tx)
	if err != nil {
		t.Fatal(err)
	}
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

// TestRound_OnlyNewestOpenSessionCounts covers a stale open session on a switch without the client's VLAN, left
// behind by a lost Accounting-Stop: the client's newest session is on a switch that carries its VLAN, so it is fine.
func TestRound_OnlyNewestOpenSessionCounts(t *testing.T) {
	f := newFixture(t, -1)
	assigned := otherVLAN(f.p, switchVLANs(f.sw))
	f.p.Authorized(f.client, assigned)
	good := f.p.Switch(func(s *testgen.Switch) {
		s.VLANs[0].VlanID = int32(assigned)
		s.Map.PrimaryVlan = int32(assigned)
	})
	start := time.Now().UTC().Truncate(time.Second)
	f.p.Session(func(r *model.Radacct) {
		r.Username, r.Nasipaddress, r.Acctstarttime = f.client.Hex(), good.IPs[0].IP, &start
	})
	assertNotStuck(t, f)
}

func TestRound_ClearsWhenNoLongerStuck(t *testing.T) {
	f := newFixture(t, 0)
	r := New(f.tx, true)
	f.round(t, r)
	if !isStuck(r, f.client) {
		t.Fatal("client not stuck after round 1")
	}
	f.closeSession(t)
	f.round(t, r)
	if len(r.stuck) != 0 {
		t.Errorf("stuck set = %v after the session closed, want empty", r.stuck)
	}
	if g := gaugeValue(t, stuckClients); g != 0 {
		t.Errorf("bouncer_stuck_clients = %v, want 0", g)
	}
}

// recorder is a slog.Handler that keeps the messages of the records at Info and above.
type recorder struct {
	mu   sync.Mutex
	msgs []string
}

func (h *recorder) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }
func (h *recorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}
func (h *recorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recorder) WithGroup(string) slog.Handler      { return h }

// take returns how often msg was logged since the last take and forgets the records.
func (h *recorder) take(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if m == msg {
			n++
		}
	}
	h.msgs = nil
	return n
}

func TestRound_LogsTransitionsOnce(t *testing.T) {
	rec := &recorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newFixture(t, 0)
	r := New(f.tx, true)

	f.round(t, r)
	if n := rec.take(msgStuck); n != 1 {
		t.Errorf("round 1: %d %q lines, want 1", n, msgStuck)
	}
	f.round(t, r)
	if n := rec.take(msgStuck); n != 0 {
		t.Errorf("round 2: %d %q lines, want 0", n, msgStuck)
	}
	f.closeSession(t)
	f.round(t, r)
	rec.mu.Lock()
	msgs := slices.Clone(rec.msgs)
	rec.mu.Unlock()
	if n := rec.take(msgUnstuck); n != 1 {
		t.Errorf("round 3: %d %q lines (all: %q), want 1", n, msgUnstuck, msgs)
	}
}
