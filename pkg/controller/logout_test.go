package controller

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/logout"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// macAddr keeps the tests' closures short.
type macAddr = mac.MAC

// logoutHarness is a controller test harness with a LogoutController on a fake clock.
type logoutHarness struct {
	*harness
	l     *LogoutController
	clock time.Time
}

func newLogoutHarness(t *testing.T, exempt ...int) *logoutHarness {
	t.Helper()
	testdb.LockLogouts(t)
	h := &logoutHarness{harness: newHarness(t), clock: time.Now().UTC().Truncate(time.Minute)}
	h.l = NewLogoutController(h.store, exempt)
	h.l.now = func() time.Time { return h.clock }
	return h
}

// addReply inserts a radreply VLAN row with the given username and value as stored, for rows bouncer would not write.
func (h *logoutHarness) addReply(username, value string) {
	h.t.Helper()
	_, err := table.Radreply.INSERT(table.Radreply.MutableColumns).
		MODEL(model.Radreply{Username: username, Attribute: freeradius.AttrVLAN, Op: freeradius.OpSet, Value: value}).
		ExecContext(h.ctx, h.tx)
	if err != nil {
		h.t.Fatalf("inserting radreply: %v", err)
	}
}

// wantRefusal fails the test unless err is a refusal whose message contains text.
func wantRefusal(t *testing.T, err error, text string) {
	t.Helper()
	var refusal *LogoutRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Message, text) {
		t.Errorf("refusal = %q, want it to contain %q", refusal.Message, text)
	}
}

// inFlight returns the logout in flight, failing the test if there is none.
func (h *logoutHarness) inFlight() logout.Logout {
	h.t.Helper()
	l, err := logout.InFlight(h.ctx, h.tx)
	if err != nil {
		h.t.Fatalf("InFlight: %v", err)
	}
	return l
}

// armed schedules a logout two hours ahead by two admins and returns its id and time.
func (h *logoutHarness) armed() (int, time.Time) {
	h.t.Helper()
	at := h.clock.Add(2 * time.Hour)
	for _, admin := range []string{"a", "b"} {
		if _, err := h.l.Schedule(h.ctx, admin, at); err != nil {
			h.t.Fatalf("Schedule(%s): %v", admin, err)
		}
	}
	return h.inFlight().ID, at
}

func TestParseTime(t *testing.T) {
	h := newLogoutHarness(t)
	h.clock = time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC)
	for _, tt := range []struct {
		in   string
		want time.Time
	}{
		{"15:00", time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)},
		{"09:00", time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}, // past: Schedule refuses it, it never rolls over
		{"2026-11-02 15:00", time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)},
	} {
		got, err := h.l.ParseTime(tt.in)
		if err != nil || !got.Equal(tt.want) {
			t.Errorf("ParseTime(%q) = %v, %v, want %v", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "tomorrow", "15", "25:00", "15:00:30", "2026-11-02", "2026-11-02T15:00:00Z"} {
		if _, err := h.l.ParseTime(in); err == nil {
			t.Errorf("ParseTime(%q) succeeded", in)
		} else {
			wantRefusal(t, err, "2026-11-01 15:00")
		}
	}
}

func TestSchedule_TwoAdminsArm(t *testing.T) {
	h := newLogoutHarness(t)
	at := h.clock.Add(2 * time.Hour)

	first, err := h.l.Schedule(h.ctx, "a", at)
	if err != nil {
		t.Fatalf("first Schedule: %v", err)
	}
	if first.Armed || first.Logout.State != logout.StateProposed || !reflect.DeepEqual(first.Approvers, []string{"a"}) {
		t.Errorf("first Schedule = %+v", first)
	}

	second, err := h.l.Schedule(h.ctx, "b", at)
	if err != nil {
		t.Fatalf("second Schedule: %v", err)
	}
	if !second.Armed || second.Logout.State != logout.StateArmed || !reflect.DeepEqual(second.Approvers, []string{"a", "b"}) {
		t.Errorf("second Schedule = %+v", second)
	}
	if l := h.inFlight(); l.State != logout.StateArmed || !l.ScheduledFor.Equal(at) {
		t.Errorf("in flight = %+v", l)
	}
}

func TestSchedule_SameAdminTwiceDoesNotArm(t *testing.T) {
	h := newLogoutHarness(t)
	at := h.clock.Add(2 * time.Hour)
	if _, err := h.l.Schedule(h.ctx, "a", at); err != nil {
		t.Fatal(err)
	}
	_, err := h.l.Schedule(h.ctx, "a", at)
	wantRefusal(t, err, "second admin")
	if l := h.inFlight(); l.State != logout.StateProposed {
		t.Errorf("state = %s, want proposed", l.State)
	}
}

func TestSchedule_DifferentTimeIsRefused(t *testing.T) {
	h := newLogoutHarness(t)
	at := h.clock.Add(2 * time.Hour)
	if _, err := h.l.Schedule(h.ctx, "a", at); err != nil {
		t.Fatal(err)
	}
	_, err := h.l.Schedule(h.ctx, "b", at.Add(5*time.Minute))
	wantRefusal(t, err, at.Format("15:04"))
	if l := h.inFlight(); l.State != logout.StateProposed || !l.ScheduledFor.Equal(at) {
		t.Errorf("in flight = %+v", l)
	}
}

func TestSchedule_MinimumNotice(t *testing.T) {
	h := newLogoutHarness(t)

	// Exactly one hour is enough; anything less is not, and neither is the past.
	for _, d := range []time.Duration{time.Hour - time.Second, 0, -time.Hour} {
		_, err := h.l.Schedule(h.ctx, "a", h.clock.Add(d))
		wantRefusal(t, err, "at least 1 hour")
	}
	if _, err := logout.InFlight(h.ctx, h.tx); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a refused Schedule left a logout in flight: %v", err)
	}
	at := h.clock.Add(time.Hour)
	if _, err := h.l.Schedule(h.ctx, "a", at); err != nil {
		t.Fatalf("Schedule exactly one hour ahead: %v", err)
	}

	// The second admin is held to the same rule.
	h.clock = h.clock.Add(time.Second)
	_, err := h.l.Schedule(h.ctx, "b", at)
	wantRefusal(t, err, "at least 1 hour")
	if l := h.inFlight(); l.State != logout.StateProposed {
		t.Errorf("state = %s, want proposed", l.State)
	}
}

func TestSchedule_RefusedWhileArmedOrRunning(t *testing.T) {
	h := newLogoutHarness(t)
	id, at := h.armed()
	_, err := h.l.Schedule(h.ctx, "c", at)
	wantRefusal(t, err, "already armed")

	if ok, err := logout.Start(h.ctx, h.tx, id, h.clock, h.clock.Add(lockoutDuration)); err != nil || !ok {
		t.Fatalf("Start = %v, %v", ok, err)
	}
	_, err = h.l.Schedule(h.ctx, "c", h.clock.Add(3*time.Hour))
	wantRefusal(t, err, "running")
}

func TestCancel_NeedsTwoNewApprovalsAfterwards(t *testing.T) {
	h := newLogoutHarness(t)
	_, at := h.armed()

	if _, err := h.l.Cancel(h.ctx, "a", 0); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	first, err := h.l.Schedule(h.ctx, "a", at)
	if err != nil {
		t.Fatalf("Schedule after cancel: %v", err)
	}
	if first.Armed || len(first.Approvers) != 1 {
		t.Errorf("Schedule after cancel = %+v, want a new proposal with one approval", first)
	}
}

func TestCancel_Refusals(t *testing.T) {
	h := newLogoutHarness(t)
	_, err := h.l.Cancel(h.ctx, "a", 0)
	wantRefusal(t, err, "No global logout")

	id, _ := h.armed()
	_, err = h.l.Cancel(h.ctx, "a", id+1)
	wantRefusal(t, err, "already finished")
	if l := h.inFlight(); l.State != logout.StateArmed {
		t.Errorf("state = %s, want armed", l.State)
	}

	if _, err := h.l.Cancel(h.ctx, "a", id); err != nil {
		t.Fatalf("Cancel by id: %v", err)
	}
	_, err = h.l.Cancel(h.ctx, "b", id)
	wantRefusal(t, err, "already finished")
	_, err = h.l.Cancel(h.ctx, "b", 0)
	wantRefusal(t, err, "No global logout")
}

func TestCancel_WhileRunningDropsItsMovesAndEndsTheLockout(t *testing.T) {
	h := newLogoutHarness(t)
	id, _ := h.armed()
	started := h.clock.Add(-time.Minute)
	if ok, err := logout.Start(h.ctx, h.tx, id, started, started.Add(lockoutDuration)); err != nil || !ok {
		t.Fatalf("Start = %v, %v", ok, err)
	}
	mine := h.client()
	h.p.Job(func(j *model.BouncerJobs) {
		j.ClientMAC, j.TargetVLAN, j.LastUpdate = mine.Hex(), freeradius.LoginVLAN, started
	})
	skewed := h.client()
	h.p.Job(func(j *model.BouncerJobs) {
		j.ClientMAC, j.TargetVLAN, j.LastUpdate = skewed.Hex(), freeradius.LoginVLAN, started.Add(-cancelClockSlack)
	})
	earlier := h.client()
	h.p.Job(func(j *model.BouncerJobs) {
		j.ClientMAC, j.TargetVLAN, j.LastUpdate = earlier.Hex(), freeradius.LoginVLAN, started.Add(-cancelClockSlack-time.Second)
	})

	before, err := h.l.Cancel(h.ctx, "a", id)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if before.State != logout.StateRunning {
		t.Errorf("Cancel returned state %s, want running", before.State)
	}
	if j := h.pendingJob(mine); j != nil {
		t.Errorf("the logout's job is still pending: %+v", j)
	}
	if j := h.pendingJob(skewed); j != nil {
		t.Errorf("a job within the clock slack is still pending: %+v", j)
	}
	if j := h.pendingJob(earlier); j == nil {
		t.Error("a job from before the logout was deleted")
	}
	l, err := logout.Get(h.ctx, h.tx, id)
	if err != nil {
		t.Fatal(err)
	}
	if l.State != logout.StateCancelled || l.LockoutEndsAt == nil || !l.LockoutEndsAt.Equal(h.clock) {
		t.Errorf("after Cancel: %+v, want the lockout ending at %v", l, h.clock)
	}
}

func TestLogoutTargets(t *testing.T) {
	h := newLogoutHarness(t)
	const exempt = 630
	onVLAN, offline := h.client(), h.client()
	h.p.Authorized(onVLAN, 620)
	h.session(onVLAN)
	h.p.Authorized(offline, 620)
	inLogin, inExempt := h.client(), h.client()
	h.p.Authorized(inLogin, freeradius.LoginVLAN)
	h.p.Authorized(inExempt, exempt)
	h.addReply("aa:bb:cc:dd:ee:ff", "620")   // not the canonical spelling
	h.addReply("not-a-mac", "620")           // not a MAC
	h.addReply(h.client().Hex(), "not-vlan") // not a VLAN

	clients, skipped, err := logoutTargets(h.ctx, h.tx, []int{exempt})
	if err != nil {
		t.Fatalf("logoutTargets: %v", err)
	}
	for _, m := range []string{onVLAN.Hex(), offline.Hex()} {
		if !slices.ContainsFunc(clients, func(c macAddr) bool { return c.Hex() == m }) {
			t.Errorf("targets lack %s", m)
		}
	}
	for _, m := range []string{inLogin.Hex(), inExempt.Hex()} {
		if slices.ContainsFunc(clients, func(c macAddr) bool { return c.Hex() == m }) {
			t.Errorf("targets include %s", m)
		}
	}
	if len(skipped) != 3 {
		t.Errorf("skipped = %q, want 3 usernames", skipped)
	}
}

func TestStatus(t *testing.T) {
	h := newLogoutHarness(t)
	if _, ok, err := h.l.Status(h.ctx); err != nil || ok {
		t.Fatalf("Status with none = %v, %v", ok, err)
	}
	m := h.client()
	h.p.Authorized(m, 620)
	id, at := h.armed()

	s, ok, err := h.l.Status(h.ctx)
	if err != nil || !ok {
		t.Fatalf("Status = %v, %v", ok, err)
	}
	if s.Logout.ID != id || !s.Logout.ScheduledFor.Equal(at) || !reflect.DeepEqual(s.Approvers, []string{"a", "b"}) || s.Clients != 1 {
		t.Errorf("Status = %+v", s)
	}
}

func TestVLANChangeBlocked(t *testing.T) {
	const exempt = 630
	h := newLogoutHarness(t, exempt)
	if _, blocked, err := h.l.VLANChangeBlocked(h.ctx, 620); err != nil || blocked {
		t.Fatalf("with no logout = %v, %v", blocked, err)
	}
	id, _ := h.armed()
	ends := h.clock.Add(lockoutDuration)
	if ok, err := logout.Start(h.ctx, h.tx, id, h.clock, ends); err != nil || !ok {
		t.Fatalf("Start = %v, %v", ok, err)
	}
	until, blocked, err := h.l.VLANChangeBlocked(h.ctx, 620)
	if err != nil || !blocked || !until.Equal(ends) {
		t.Errorf("during the lockout = %v, %v, %v", until, blocked, err)
	}
	if _, blocked, err := h.l.VLANChangeBlocked(h.ctx, exempt); err != nil || blocked {
		t.Errorf("exempt VLAN = %v, %v", blocked, err)
	}
	if _, blocked, err := h.l.VLANChangeBlocked(h.ctx, freeradius.LoginVLAN); err != nil || blocked {
		t.Errorf("login VLAN = %v, %v", blocked, err)
	}
	h.clock = ends
	if _, blocked, err := h.l.VLANChangeBlocked(h.ctx, 620); err != nil || blocked {
		t.Errorf("after the lockout = %v, %v", blocked, err)
	}
}

// TestSchedule_Concurrent runs real transactions, so it needs real commits. It gets its own container: a logout
// committed to the shared database would make other packages' inserts fail on in_flight.
func TestSchedule_Concurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	inst := testdb.Start(t)
	d, err := inst.Open(ctx)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(ctx, 0); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}
	c := NewLogoutController(d, nil)
	at := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Minute)

	schedule := func(admins ...string) []error {
		results := make(chan error, len(admins))
		start := make(chan struct{})
		for _, admin := range admins {
			go func() {
				<-start
				_, err := c.Schedule(ctx, admin, at)
				results <- err
			}()
		}
		close(start)
		var errs []error
		for range admins {
			errs = append(errs, <-results)
		}
		return errs
	}
	state := func() logout.Logout {
		t.Helper()
		l, err := logout.InFlight(ctx, d.Querier())
		if err != nil {
			t.Fatalf("InFlight: %v", err)
		}
		return l
	}

	for round := range 10 {
		// Two simultaneous first proposals both count, whichever comes first.
		for i, err := range schedule("a"+strconv.Itoa(round), "b"+strconv.Itoa(round)) {
			if err != nil {
				t.Fatalf("round %d: proposal %d: %v", round, i, err)
			}
		}
		if l := state(); l.State != logout.StateArmed {
			t.Fatalf("round %d: state = %s, want armed", round, l.State)
		}
		if _, err := c.Cancel(ctx, "a", 0); err != nil {
			t.Fatalf("round %d: Cancel: %v", round, err)
		}

		// Of two simultaneous approvals of a proposal, one arms it and the other is refused.
		if _, err := c.Schedule(ctx, "x", at); err != nil {
			t.Fatalf("round %d: proposal: %v", round, err)
		}
		refused := 0
		for _, err := range schedule("y", "z") {
			var refusal *LogoutRefusal
			switch {
			case err == nil:
			case errors.As(err, &refusal):
				refused++
			default:
				t.Fatalf("round %d: approval: %v", round, err)
			}
		}
		l := state()
		approvers, err := logout.Approvers(ctx, d.Querier(), l.ID)
		if err != nil {
			t.Fatal(err)
		}
		if l.State != logout.StateArmed || refused != 1 || len(approvers) != 2 {
			t.Fatalf("round %d: state %s, %d refused, approvers %v", round, l.State, refused, approvers)
		}
		if _, err := c.Cancel(ctx, "x", 0); err != nil {
			t.Fatalf("round %d: Cancel: %v", round, err)
		}
	}
}

// fakeNotifier records what Round posts and fails with warnErr or notifyErr if set.
type fakeNotifier struct {
	warnings  []LogoutWarning
	notes     []string
	warnErr   error
	notifyErr error
	// onWarn, if set, runs at the start of Warn, so a test can move the clock while a post is in flight.
	onWarn func()
}

func (f *fakeNotifier) Warn(_ context.Context, w LogoutWarning) error {
	if f.onWarn != nil {
		f.onWarn()
	}
	if f.warnErr != nil {
		return f.warnErr
	}
	f.warnings = append(f.warnings, w)
	return nil
}

func (f *fakeNotifier) Notify(_ context.Context, message string) error {
	if f.notifyErr != nil {
		return f.notifyErr
	}
	f.notes = append(f.notes, message)
	return nil
}

// stages returns the stages of the warnings posted so far.
func (f *fakeNotifier) stages() []int {
	var out []int
	for _, w := range f.warnings {
		out = append(out, w.Stage)
	}
	return out
}

// round runs one Round at time at.
func (h *logoutHarness) round(n Notifier, at time.Time) {
	h.t.Helper()
	h.clock = at
	if err := h.l.Round(h.ctx, n); err != nil {
		h.t.Fatalf("Round at %v: %v", at, err)
	}
}

// get returns the logout with the given id.
func (h *logoutHarness) get(id int) logout.Logout {
	h.t.Helper()
	l, err := logout.Get(h.ctx, h.tx, id)
	if err != nil {
		h.t.Fatalf("Get: %v", err)
	}
	return l
}

func TestDueWarning(t *testing.T) {
	last := func(v int) *int { return &v }
	for _, tt := range []struct {
		name      string
		last      *int
		remaining time.Duration
		want      int
		wantDue   bool
	}{
		{"arming message", nil, 90 * time.Minute, 90, true},
		{"arming message rounds up", nil, 90*time.Minute + time.Second, 91, true},
		{"nothing due yet", last(90), 31 * time.Minute, 0, false},
		{"first stage", last(90), 30 * time.Minute, 30, true},
		{"stage already posted", last(30), 29 * time.Minute, 0, false},
		{"next stage", last(30), 15 * time.Minute, 15, true},
		{"final stage", last(5), time.Minute, 1, true},
		{"final stage posted", last(1), time.Second, 0, false},
		{"backlog posts only the smallest", last(90), 4 * time.Minute, 5, true},
		{"never armed message, stage due", nil, 10 * time.Minute, 15, true},
	} {
		got, due := dueWarning(tt.last, tt.remaining)
		if got != tt.want || due != tt.wantDue {
			t.Errorf("%s: dueWarning = %d, %v, want %d, %v", tt.name, got, due, tt.want, tt.wantDue)
		}
	}
}

func TestRound_NothingInFlight(t *testing.T) {
	h := newLogoutHarness(t)
	n := &fakeNotifier{}
	h.round(n, h.clock)
	if len(n.warnings)+len(n.notes) != 0 {
		t.Errorf("posted %+v %+v with nothing in flight", n.warnings, n.notes)
	}
}

func TestRound_ProposalExpires(t *testing.T) {
	h := newLogoutHarness(t)
	at := h.clock.Add(2 * time.Hour)
	if _, err := h.l.Schedule(h.ctx, "a", at); err != nil {
		t.Fatal(err)
	}
	id := h.inFlight().ID
	n := &fakeNotifier{}

	h.round(n, at.Add(-logoutMinNotice))
	if l := h.get(id); l.State != logout.StateProposed {
		t.Fatalf("state exactly one hour ahead = %s, want proposed", l.State)
	}
	h.round(n, at.Add(-logoutMinNotice).Add(time.Second))
	if l := h.get(id); l.State != logout.StateExpired {
		t.Errorf("state = %s, want expired", l.State)
	}
	if len(n.notes) != 1 || !strings.Contains(n.notes[0], "expired") {
		t.Errorf("notes = %q, want one about the expiry", n.notes)
	}
	if len(n.warnings) != 0 {
		t.Errorf("a proposal got warnings: %+v", n.warnings)
	}
}

func TestRound_WarningsAreSentOnceEach(t *testing.T) {
	h := newLogoutHarness(t)
	client := h.client()
	h.p.Authorized(client, 620)
	id, at := h.armed()
	n := &fakeNotifier{}

	// Every 20 seconds, up to just before the logout's time.
	for tick := h.clock; tick.Before(at); tick = tick.Add(20 * time.Second) {
		h.round(n, tick)
	}
	if want := []int{120, 30, 15, 5, finalWarning}; !reflect.DeepEqual(n.stages(), want) {
		t.Errorf("stages = %v, want %v", n.stages(), want)
	}
	w := n.warnings[0]
	if w.Logout.ID != id || !reflect.DeepEqual(w.Approvers, []string{"a", "b"}) || w.Clients != 1 {
		t.Errorf("warning = %+v", w)
	}
	if l := h.get(id); l.LastWarning == nil || *l.LastWarning != finalWarning {
		t.Errorf("LastWarning = %v, want %d", l.LastWarning, finalWarning)
	}
}

func TestRound_MissedStagesAreNotBacklogged(t *testing.T) {
	h := newLogoutHarness(t)
	_, at := h.armed()
	n := &fakeNotifier{}
	h.round(n, h.clock)                // arming message
	h.round(n, at.Add(-4*time.Minute)) // 30 and 15 were missed
	if want := []int{120, 5}; !reflect.DeepEqual(n.stages(), want) {
		t.Errorf("stages = %v, want %v", n.stages(), want)
	}
}

func TestRound_FailedWarningIsRetried(t *testing.T) {
	h := newLogoutHarness(t)
	id, at := h.armed()
	n := &fakeNotifier{warnErr: errors.New("discord is down")}

	h.round(n, at.Add(-30*time.Minute))
	if l := h.get(id); l.LastWarning != nil {
		t.Errorf("LastWarning = %v after a failed post, want nil", *l.LastWarning)
	}
	n.warnErr = nil
	h.round(n, at.Add(-30*time.Minute+5*time.Second))
	if want := []int{30}; !reflect.DeepEqual(n.stages(), want) {
		t.Errorf("stages = %v, want %v", n.stages(), want)
	}
}

func TestRound_StartsAtItsTime(t *testing.T) {
	h := newLogoutHarness(t)
	id, at := h.armed()
	n := &fakeNotifier{}
	h.round(n, at.Add(-time.Minute))
	h.round(n, at.Add(-time.Second))
	if l := h.get(id); l.State != logout.StateArmed {
		t.Fatalf("state before its time = %s, want armed", l.State)
	}

	// It starts a little late: the lockout still lasts the full 15 minutes.
	startedAt := at.Add(3 * time.Second)
	h.round(n, startedAt)
	l := h.get(id)
	if l.State != logout.StateRunning || l.StartedAt == nil || !l.StartedAt.Equal(startedAt) ||
		l.LockoutEndsAt == nil || !l.LockoutEndsAt.Equal(startedAt.Add(lockoutDuration)) {
		t.Errorf("after the start: %+v", l)
	}
	if len(n.notes) == 0 || !strings.Contains(n.notes[0], "started") {
		t.Errorf("notes = %q, want the first about the start", n.notes)
	}
}

func TestRound_AbortsWithoutTheFinalWarning(t *testing.T) {
	h := newLogoutHarness(t)
	id, at := h.armed()
	n := &fakeNotifier{}
	h.round(n, at.Add(-5*time.Minute)) // posted the 5-minute warning

	n.warnErr = errors.New("discord is down")
	for tick := at.Add(-time.Minute); tick.Before(at); tick = tick.Add(5 * time.Second) {
		h.round(n, tick)
	}
	n.warnErr = nil
	h.round(n, at)

	if l := h.get(id); l.State != logout.StateAborted {
		t.Errorf("state = %s, want aborted", l.State)
	}
	if _, err := logout.InFlight(h.ctx, h.tx); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("after the abort: %v, want nothing in flight", err)
	}
	if len(n.notes) != 1 || !strings.Contains(n.notes[0], "aborted") {
		t.Errorf("notes = %q, want one about the abort", n.notes)
	}
}

func TestRound_LateStart(t *testing.T) {
	h := newLogoutHarness(t)
	id, at := h.armed()
	n := &fakeNotifier{}
	h.round(n, at.Add(-time.Minute))

	h.round(n, at.Add(logoutLateLimit).Add(time.Second))
	if l := h.get(id); l.State != logout.StateAborted {
		t.Errorf("state more than the limit late = %s, want aborted", l.State)
	}
}

func TestRound_StartAtTheLateLimit(t *testing.T) {
	h := newLogoutHarness(t)
	id, at := h.armed()
	n := &fakeNotifier{}
	h.round(n, at.Add(-time.Minute))

	h.round(n, at.Add(logoutLateLimit))
	if l := h.get(id); l.State != logout.StateRunning {
		t.Errorf("state exactly at the limit = %s, want running", l.State)
	}
}

func TestRound_NewControllerCarriesOn(t *testing.T) {
	h := newLogoutHarness(t)
	_, at := h.armed()
	n := &fakeNotifier{}
	h.round(n, at.Add(-30*time.Minute))

	// A failover: a new controller reads the same database and does not repeat the warning.
	h.l = NewLogoutController(h.store, nil)
	h.l.now = func() time.Time { return h.clock }
	h.round(n, at.Add(-29*time.Minute))
	h.round(n, at.Add(-15*time.Minute))
	if want := []int{30, 15}; !reflect.DeepEqual(n.stages(), want) {
		t.Errorf("stages = %v, want %v", n.stages(), want)
	}
}

func TestRound_CancelBeatsStart(t *testing.T) {
	h := newLogoutHarness(t)
	id, at := h.armed()
	n := &fakeNotifier{}
	h.round(n, at.Add(-time.Minute))
	if _, err := h.l.Cancel(h.ctx, "a", id); err != nil {
		t.Fatal(err)
	}
	h.round(n, at)
	if l := h.get(id); l.State != logout.StateCancelled || l.StartedAt != nil {
		t.Errorf("after cancel and round: %+v", l)
	}
}

func TestRound_FinalWarningArrivingTooLateAborts(t *testing.T) {
	h := newLogoutHarness(t)
	id, at := h.armed()
	n := &fakeNotifier{}
	n.onWarn = func() { h.clock = at.Add(3 * time.Second) }

	h.round(n, at.Add(-8*time.Second))
	if l := h.get(id); l.LastWarning != nil {
		t.Errorf("LastWarning = %d, want a warning arriving after the logout's time not recorded", *l.LastWarning)
	}

	n.onWarn = nil
	h.round(n, at.Add(8*time.Second))
	if l := h.get(id); l.State != logout.StateAborted {
		t.Errorf("state = %s, want aborted", l.State)
	}
}

// started arms a logout and runs it up to its start, and returns its id and when its lockout ends.
func (h *logoutHarness) started(n Notifier) (int, time.Time) {
	h.t.Helper()
	id, at := h.armed()
	h.round(n, at.Add(-time.Minute))
	h.round(n, at)
	l := h.get(id)
	if l.State != logout.StateRunning {
		h.t.Fatalf("state = %s, want running", l.State)
	}
	return id, *l.LockoutEndsAt
}

func TestSweep_QueuesEveryoneAtTheStart(t *testing.T) {
	const exempt = 630
	h := newLogoutHarness(t, exempt)
	online, offline, inLogin, inExempt := h.client(), h.client(), h.client(), h.client()
	h.p.Authorized(online, 620)
	h.session(online)
	h.p.Authorized(offline, 620)
	h.p.Authorized(inLogin, freeradius.LoginVLAN)
	h.p.Authorized(inExempt, exempt)
	n := &fakeNotifier{}

	h.started(n)

	for _, m := range []macAddr{online, offline} {
		j := h.pendingJob(m)
		if j == nil || j.TargetVLAN != freeradius.LoginVLAN {
			t.Errorf("job of %s = %+v, want one to the login VLAN", m.Hex(), j)
		}
	}
	for _, m := range []macAddr{inLogin, inExempt} {
		if j := h.pendingJob(m); j != nil {
			t.Errorf("%s got a job: %+v", m.Hex(), j)
		}
	}
}

func TestSweep_DoesNotQueueTwice(t *testing.T) {
	h := newLogoutHarness(t)
	m := h.client()
	h.p.Authorized(m, 620)
	n := &fakeNotifier{}
	_, ends := h.started(n)

	h.round(n, ends.Add(-14*time.Minute))
	// pendingJob fails the test on a second pending job.
	if j := h.mustPending(m); j.TargetVLAN != freeradius.LoginVLAN {
		t.Errorf("job = %+v", j)
	}
}

func TestSweep_MovesClientsThroughTheJobLoop(t *testing.T) {
	h := newLogoutHarness(t)
	online, offline := h.client(), h.client()
	h.p.Authorized(online, 620)
	h.session(online)
	h.p.Authorized(offline, 620)
	n := &fakeNotifier{}
	_, ends := h.started(n)

	if err := h.c.Work(h.ctx); err != nil {
		t.Fatalf("Work: %v", err)
	}
	for _, m := range []macAddr{online, offline} {
		if v := h.vlan(m); v != freeradius.LoginVLAN {
			t.Errorf("VLAN of %s = %d, want the login VLAN", m.Hex(), v)
		}
	}
	if len(h.coa.calls) != 1 {
		t.Errorf("CoA calls = %d, want 1: only the online client gets one", len(h.coa.calls))
	}

	before := len(n.notes)
	h.round(n, ends.Add(-14*time.Minute))
	h.round(n, ends.Add(-13*time.Minute))
	if got := n.notes[before:]; len(got) != 1 || !strings.Contains(got[0], "moved") {
		t.Errorf("notes = %q, want one saying everyone has been moved", got)
	}
}

func TestSweep_RequeuesAClientThatCameBack(t *testing.T) {
	h := newLogoutHarness(t)
	m := h.client()
	h.p.Authorized(m, 620)
	n := &fakeNotifier{}
	_, ends := h.started(n)
	if err := h.c.Work(h.ctx); err != nil {
		t.Fatal(err)
	}

	// A login job that committed just after the sweep read radreply.
	if err := freeradius.SetVLAN(h.ctx, h.tx, m, 620); err != nil {
		t.Fatal(err)
	}
	h.round(n, ends.Add(-14*time.Minute))
	if j := h.pendingJob(m); j == nil || j.TargetVLAN != freeradius.LoginVLAN {
		t.Errorf("job = %+v, want the client queued again", j)
	}
}

func TestSweep_RequeuesAfterARanOutJob(t *testing.T) {
	h := newLogoutHarness(t)
	m := h.client()
	h.p.Authorized(m, 620)
	n := &fakeNotifier{}
	_, ends := h.started(n)
	j := h.mustPending(m)
	if err := jobs.SetRetries(h.ctx, h.tx, j.ID, jobs.MaxRetries); err != nil {
		t.Fatal(err)
	}

	h.round(n, ends.Add(-14*time.Minute))
	if again := h.mustPending(m); again.ID == j.ID {
		t.Errorf("job = %+v, want a new job", again)
	}
}

func TestSweep_SkipsRowsItCannotMoveAndWarnsOnce(t *testing.T) {
	h := newLogoutHarness(t)
	h.addReply("not-a-mac", "620")
	n := &fakeNotifier{}
	logs := captureLogs(t)
	_, ends := h.started(n)
	h.round(n, ends.Add(-14*time.Minute))

	if got := logs.count(msgLogoutSkipped); got != 1 {
		t.Errorf("warnings = %d, want 1", got)
	}
	if all, err := jobs.Pending(h.ctx, h.tx); err != nil || len(all) != 0 {
		t.Errorf("pending = %+v, %v, want nothing queued", all, err)
	}
}

func TestSweep_StopsWhenTheLockoutEnds(t *testing.T) {
	h := newLogoutHarness(t)
	n := &fakeNotifier{}
	_, ends := h.started(n)

	late := h.client()
	h.p.Authorized(late, 620)
	h.round(n, ends)
	if j := h.pendingJob(late); j != nil {
		t.Errorf("job queued at the lockout's end: %+v", j)
	}
}

func TestRound_FinishesAfterTheLockoutAndDrain(t *testing.T) {
	h := newLogoutHarness(t)
	m := h.client()
	h.p.Authorized(m, 620)
	n := &fakeNotifier{}
	id, ends := h.started(n)

	// A move still pending keeps it running after the lockout.
	h.round(n, ends)
	if l := h.get(id); l.State != logout.StateRunning {
		t.Fatalf("state with a move pending = %s, want running", l.State)
	}

	if err := h.c.Work(h.ctx); err != nil {
		t.Fatal(err)
	}
	before := len(n.notes)
	h.round(n, ends.Add(5*time.Second))
	if l := h.get(id); l.State != logout.StateDone {
		t.Fatalf("state after the drain = %s, want done", l.State)
	}
	if _, err := logout.InFlight(h.ctx, h.tx); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("after finishing: %v, want nothing in flight", err)
	}
	got := n.notes[before:]
	if len(got) != 1 || !strings.Contains(got[0], "finished") || !strings.Contains(got[0], "CoA") {
		t.Errorf("notes = %q, want one summary", got)
	}
}

func TestRound_SummaryCountsTheMoves(t *testing.T) {
	h := newLogoutHarness(t)
	// bouncer_log carries the database's clock, so this logout runs in the past.
	h.clock = time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute)
	m := h.client()
	h.p.Authorized(m, 620)
	n := &fakeNotifier{}
	_, ends := h.started(n)
	if err := h.c.Work(h.ctx); err != nil {
		t.Fatal(err)
	}

	before := len(n.notes)
	h.round(n, ends)
	if got := n.notes[before:]; len(got) != 1 || !strings.Contains(got[0], "1 client(s) moved") {
		t.Errorf("notes = %q, want a summary with 1 client moved", got)
	}
}

func TestSweep_AfterACancelQueuesNothing(t *testing.T) {
	h := newLogoutHarness(t)
	n := &fakeNotifier{}
	id, _ := h.started(n)
	running := h.get(id)
	if _, err := h.l.Cancel(h.ctx, "a", id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// A round that read the logout just before the cancel carries on with its stale copy.
	m := h.client()
	h.p.Authorized(m, 620)
	if err := h.l.sweep(h.ctx, n, running); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if j := h.pendingJob(m); j != nil {
		t.Errorf("job queued after the cancel: %+v", j)
	}
}

func TestCancel_DropsTheSweepsJobsWithTheDatabaseClockBehind(t *testing.T) {
	h := newLogoutHarness(t)
	// The logout starts 30 s ahead of the database's clock, which stamps the sweep's jobs.
	h.clock = time.Now().UTC().Add(30*time.Second - 2*time.Hour).Truncate(time.Second)
	m := h.client()
	h.p.Authorized(m, 620)
	id, _ := h.started(&fakeNotifier{})
	if j := h.pendingJob(m); j == nil {
		t.Fatal("the sweep queued no job")
	}

	if _, err := h.l.Cancel(h.ctx, "a", id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if j := h.pendingJob(m); j != nil {
		t.Errorf("the sweep's job is still pending after the cancel: %+v", j)
	}
}

func TestRound_LockoutGauge(t *testing.T) {
	h := newLogoutHarness(t)
	n := &fakeNotifier{}
	h.round(n, h.clock)
	if v := gaugeValue(t, logout.LockoutActive); v != 0 {
		t.Errorf("gauge with no logout = %v, want 0", v)
	}
	_, ends := h.started(n)
	h.round(n, ends.Add(-time.Second))
	if v := gaugeValue(t, logout.LockoutActive); v != 1 {
		t.Errorf("gauge during the lockout = %v, want 1", v)
	}
	h.round(n, ends)
	if v := gaugeValue(t, logout.LockoutActive); v != 0 {
		t.Errorf("gauge after the lockout = %v, want 0", v)
	}
}

// gaugeValue reads a gauge without registering it (see internal/metrics' tests).
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("reading gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

// logCapture counts the messages logged while a test runs.
type logCapture struct {
	mu   sync.Mutex
	seen map[string]int
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *logCapture) WithGroup(string) slog.Handler            { return c }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[r.Message]++
	return nil
}

func (c *logCapture) count(message string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[message]
}

// captureLogs makes the default logger count messages until the test ends.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{seen: map[string]int{}}
	old := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(old) })
	return c
}

func TestWork_LockoutRule(t *testing.T) {
	const moveVLAN = 620
	now := time.Now().UTC().Truncate(time.Second)
	started := now.Add(-5 * time.Minute)
	ends := started.Add(lockoutDuration)
	for _, tt := range []struct {
		name        string
		vlan        int
		lastUpdate  time.Time
		wantDropped bool
	}{
		{"queued before the logout", moveVLAN, started.Add(-time.Minute), true},
		{"queued during the lockout", moveVLAN, started.Add(time.Minute), true},
		{"queued after the lockout", moveVLAN, ends.Add(time.Second), false},
		{"to the login VLAN", freeradius.LoginVLAN, started.Add(time.Minute), false},
		{"to an exempt VLAN", harnessExemptVLAN, started.Add(time.Minute), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newLogoutHarness(t)
			id, _ := h.armed()
			if ok, err := logout.Start(h.ctx, h.tx, id, started, ends); err != nil || !ok {
				t.Fatalf("Start = %v, %v", ok, err)
			}
			m := h.client()
			h.p.Authorized(m, 610)
			h.p.Job(func(j *model.BouncerJobs) {
				j.ClientMAC, j.TargetVLAN, j.LastUpdate = m.Hex(), int16(tt.vlan), tt.lastUpdate
			})

			if err := h.c.Work(h.ctx); err != nil {
				t.Fatalf("Work: %v", err)
			}
			if j := h.pendingJob(m); j != nil {
				t.Errorf("job still pending: %+v", j)
			}
			wantVLAN, wantIgnored, wantLogs := tt.vlan, 0, 1
			if tt.wantDropped {
				wantVLAN, wantIgnored, wantLogs = 610, 1, 0
			}
			if v := h.vlan(m); v != wantVLAN {
				t.Errorf("VLAN = %d, want %d", v, wantVLAN)
			}
			if got := h.get(id).IgnoredJobs; got != wantIgnored {
				t.Errorf("IgnoredJobs = %d, want %d", got, wantIgnored)
			}
			if got := len(h.logRows(m)); got != wantLogs {
				t.Errorf("log rows = %d, want %d", got, wantLogs)
			}
		})
	}
}

func TestWork_ACancelStopsTheRoundInProgress(t *testing.T) {
	h := newLogoutHarness(t)
	// The sweep's jobs carry the database's clock, so this logout starts now.
	h.clock = time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	a, b := h.client(), h.client()
	for _, m := range []macAddr{a, b} {
		h.p.Authorized(m, 620)
		h.session(m)
	}
	id, _ := h.started(&fakeNotifier{})
	// The cancel lands while the round is on its first job, with the second still on its list.
	h.coa.fail = func(radius.CoARequest) error {
		_, err := h.l.Cancel(h.ctx, "admin", id)
		return err
	}

	if err := h.c.Work(h.ctx); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got := len(h.coa.calls); got != 1 {
		t.Errorf("CoA calls = %d, want 1: the round carried on after the cancel", got)
	}
	moved := 0
	for _, m := range []macAddr{a, b} {
		if h.vlan(m) == freeradius.LoginVLAN {
			moved++
		}
	}
	if moved != 1 {
		t.Errorf("clients moved = %d, want 1, the job the round was on", moved)
	}
}

func TestWork_CancelledLogoutOnlyLocksOutUntilItsCancel(t *testing.T) {
	h := newLogoutHarness(t)
	id, _ := h.armed()
	now := time.Now().UTC().Truncate(time.Second)
	started := now.Add(-5 * time.Minute)
	if ok, err := logout.Start(h.ctx, h.tx, id, started, started.Add(lockoutDuration)); err != nil || !ok {
		t.Fatalf("Start = %v, %v", ok, err)
	}
	cancelledAt := now.Add(-time.Minute)
	if ok, err := logout.Cancel(h.ctx, h.tx, id, "a", cancelledAt); err != nil || !ok {
		t.Fatalf("Cancel = %v, %v", ok, err)
	}

	before, after := h.client(), h.client()
	h.p.Authorized(before, 610)
	h.p.Authorized(after, 610)
	h.p.Job(func(j *model.BouncerJobs) {
		j.ClientMAC, j.TargetVLAN, j.LastUpdate = before.Hex(), 620, cancelledAt.Add(-time.Second)
	})
	h.p.Job(func(j *model.BouncerJobs) { j.ClientMAC, j.TargetVLAN, j.LastUpdate = after.Hex(), 620, cancelledAt })

	if err := h.c.Work(h.ctx); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if v := h.vlan(before); v != 610 {
		t.Errorf("VLAN of the job before the cancel = %d, want 610", v)
	}
	if v := h.vlan(after); v != 620 {
		t.Errorf("VLAN of the job after the cancel = %d, want 620", v)
	}
}

func TestProcess_FailedCoADuringALogout(t *testing.T) {
	const movedDespiteCoA = "Moving a client despite its failed CoA: it stays on its old VLAN until it next re-authenticates"
	coaFails := func(radius.CoARequest) error { return errors.New("no answer") }
	for _, tt := range []struct {
		name     string
		retries  int
		vlan     int
		running  bool
		wantMove bool
	}{
		{"last retry", jobs.MaxRetries - 1, freeradius.LoginVLAN, true, true},
		{"earlier retry", jobs.MaxRetries - 2, freeradius.LoginVLAN, true, false},
		{"another VLAN", jobs.MaxRetries - 1, harnessExemptVLAN, true, false},
		{"no logout running", jobs.MaxRetries - 1, freeradius.LoginVLAN, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newLogoutHarness(t)
			logs := captureLogs(t)
			id, _ := h.armed()
			if tt.running {
				now := time.Now().UTC().Truncate(time.Second)
				if ok, err := logout.Start(h.ctx, h.tx, id, now, now.Add(lockoutDuration)); err != nil || !ok {
					t.Fatalf("Start = %v, %v", ok, err)
				}
			}
			m := h.client()
			h.p.Authorized(m, 610)
			h.session(m)
			h.lease4(m)
			h.coa.fail = coaFails
			row := h.p.Job(func(j *model.BouncerJobs) {
				j.ClientMAC, j.TargetVLAN, j.Retires = m.Hex(), int16(tt.vlan), int32(tt.retries)
			})
			job := h.pendingRaw(row.ClientMAC)

			err := h.c.ProcessMoveVLANJob(h.ctx, job)

			if tt.wantMove {
				if err != nil {
					t.Fatalf("ProcessMoveVLANJob: %v", err)
				}
				if v := h.vlan(m); v != tt.vlan {
					t.Errorf("VLAN = %d, want %d", v, tt.vlan)
				}
				if h.hasLease4(m) {
					t.Error("the lease was not cleared")
				}
				if j := h.pendingJob(m); j != nil {
					t.Errorf("job still pending: %+v", j)
				}
				if got := h.get(id).CoAFailures; got != 1 {
					t.Errorf("CoAFailures = %d, want 1", got)
				}
				if got := logs.count(movedDespiteCoA); got != 1 {
					t.Errorf("%q logged %d times, want 1", movedDespiteCoA, got)
				}
				return
			}
			if err == nil {
				t.Fatal("ProcessMoveVLANJob succeeded, want the CoA's error")
			}
			if v := h.vlan(m); v != 610 {
				t.Errorf("VLAN = %d, want 610 (rolled back)", v)
			}
			if got := h.get(id).CoAFailures; got != 0 {
				t.Errorf("CoAFailures = %d, want 0", got)
			}
			if got := logs.count(movedDespiteCoA); got != 0 {
				t.Errorf("%q logged %d times, want 0", movedDespiteCoA, got)
			}
		})
	}
}

// TestGlobalLogout_EndToEnd walks a logout from the first schedule to the end, with the logout loop and the job loop
// taking turns as they do in production.
func TestGlobalLogout_EndToEnd(t *testing.T) {
	h := newLogoutHarness(t, harnessExemptVLAN)
	online, offline, staff := h.client(), h.client(), h.client()
	h.p.Authorized(online, 620)
	h.session(online)
	h.p.Authorized(offline, 620)
	h.p.Authorized(staff, harnessExemptVLAN)
	n := &fakeNotifier{}

	at := h.clock.Add(2 * time.Hour)
	if _, err := h.l.Schedule(h.ctx, "a", at); err != nil {
		t.Fatal(err)
	}
	res, err := h.l.Schedule(h.ctx, "b", at)
	if err != nil || !res.Armed {
		t.Fatalf("second Schedule = %+v, %v", res, err)
	}
	id := res.Logout.ID

	// Up to its time, warnings only.
	for tick := h.clock; tick.Before(at); tick = tick.Add(time.Minute) {
		h.round(n, tick)
	}
	if want := []int{120, 30, 15, 5, finalWarning}; !reflect.DeepEqual(n.stages(), want) {
		t.Errorf("stages = %v, want %v", n.stages(), want)
	}
	if n.warnings[0].Clients != 2 {
		t.Errorf("warning counted %d clients, want 2", n.warnings[0].Clients)
	}
	if v := h.vlan(online); v != 620 {
		t.Errorf("VLAN before its time = %d, want 620", v)
	}

	// At its time, everyone moves.
	h.round(n, at)
	if err := h.c.Work(h.ctx); err != nil {
		t.Fatal(err)
	}
	for _, m := range []macAddr{online, offline} {
		if v := h.vlan(m); v != freeradius.LoginVLAN {
			t.Errorf("VLAN of %s = %d, want the login VLAN", m.Hex(), v)
		}
	}
	if v := h.vlan(staff); v != harnessExemptVLAN {
		t.Errorf("VLAN of the exempt client = %d, want %d", v, harnessExemptVLAN)
	}

	// A login during the lockout is dropped. Its job carries the database's clock, so give it a time in the lockout.
	h.p.Job(func(j *model.BouncerJobs) {
		j.ClientMAC, j.TargetVLAN, j.LastUpdate = online.Hex(), 620, at.Add(time.Minute)
	})
	ends := *h.get(id).LockoutEndsAt
	if err := h.c.Work(h.ctx); err != nil {
		t.Fatal(err)
	}
	if v := h.vlan(online); v != freeradius.LoginVLAN {
		t.Errorf("VLAN after a login during the lockout = %d, want the login VLAN", v)
	}

	// After it, the logout finishes and logins work again.
	h.round(n, ends.Add(5*time.Second))
	final := h.get(id)
	if final.State != logout.StateDone || final.IgnoredJobs != 1 {
		t.Errorf("at the end: %+v", final)
	}
	h.p.Job(func(j *model.BouncerJobs) {
		j.ClientMAC, j.TargetVLAN, j.LastUpdate = online.Hex(), 620, ends.Add(time.Second)
	})
	if err := h.c.Work(h.ctx); err != nil {
		t.Fatal(err)
	}
	if v := h.vlan(online); v != 620 {
		t.Errorf("VLAN after a login after the lockout = %d, want 620", v)
	}
}
