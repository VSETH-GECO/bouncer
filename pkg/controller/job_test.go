package controller

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/kea"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"github.com/go-jet/jet/v2/mysql"
)

const (
	switchIP   = "10.10.0.1"
	switchPort = "GigabitEthernet1/0/5"
)

// pendingJob returns the pending job of m, or nil if m has none.
func (h *harness) pendingJob(m mac.MAC) *jobs.Job {
	h.t.Helper()
	return h.pendingRaw(m.Hex())
}

// pendingRaw returns the pending job whose stored clientMAC is raw, or nil if there is none.
func (h *harness) pendingRaw(raw string) *jobs.Job {
	h.t.Helper()
	pending, err := jobs.Pending(h.ctx, h.tx)
	if err != nil {
		h.t.Fatalf("Pending: %v", err)
	}
	var found *jobs.Job
	for i := range pending {
		if pending[i].RawMAC == raw {
			if found != nil {
				h.t.Fatalf("more than one pending job for %q", raw)
			}
			found = &pending[i]
		}
	}
	return found
}

// mustPending is pendingJob that fails the test if m has no pending job.
func (h *harness) mustPending(m mac.MAC) *jobs.Job {
	h.t.Helper()
	j := h.pendingJob(m)
	if j == nil {
		h.t.Fatalf("no pending job for %s", m.Hex())
	}
	return j
}

// queue creates a job moving m to vlan and returns it as Pending reports it.
func (h *harness) queue(m mac.MAC, vlan int) *jobs.Job {
	h.t.Helper()
	if err := jobs.Create(h.ctx, h.tx, m, vlan); err != nil {
		h.t.Fatalf("Create: %v", err)
	}
	return h.mustPending(m)
}

// vlan returns the VLAN assigned to m, or -1 if m is not authorized.
func (h *harness) vlan(m mac.MAC) int {
	h.t.Helper()
	v, err := freeradius.AssignedVLAN(h.ctx, h.tx, m)
	if errors.Is(err, db.ErrNotFound) {
		return -1
	}
	if err != nil {
		h.t.Fatalf("AssignedVLAN: %v", err)
	}
	return v
}

// logRows returns the bouncer_log rows of m.
func (h *harness) logRows(m mac.MAC) []model.BouncerLog {
	h.t.Helper()
	var rows []model.BouncerLog
	err := mysql.SELECT(table.BouncerLog.AllColumns).
		FROM(table.BouncerLog).
		WHERE(table.BouncerLog.ClientMAC.EQ(mysql.String(m.Hex()))).
		ORDER_BY(table.BouncerLog.ID.ASC()).
		QueryContext(h.ctx, h.tx, &rows)
	if err != nil {
		h.t.Fatalf("querying bouncer_log: %v", err)
	}
	return rows
}

// session inserts an active session of m on the test switch.
func (h *harness) session(m mac.MAC, mods ...func(*model.Radacct)) model.Radacct {
	h.t.Helper()
	base := func(r *model.Radacct) {
		r.Username, r.Nasipaddress, r.Nasportid = m.Hex(), switchIP, ptr(switchPort)
	}
	return h.p.Session(append([]func(*model.Radacct){base}, mods...)...)
}

// lease4 inserts an IPv4 lease of m.
func (h *harness) lease4(m mac.MAC, mods ...func(*model.Lease4)) model.Lease4 {
	h.t.Helper()
	base := func(l *model.Lease4) { hw := m.Bytes(); l.Hwaddr = &hw }
	return h.p.Lease4(append([]func(*model.Lease4){base}, mods...)...)
}

// lease6 inserts an IPv6 lease of m.
func (h *harness) lease6(m mac.MAC, mods ...func(*model.Lease6)) model.Lease6 {
	h.t.Helper()
	base := func(l *model.Lease6) { hw := m.Bytes(); l.Hwaddr = &hw }
	return h.p.Lease6(append([]func(*model.Lease6){base}, mods...)...)
}

func (h *harness) hasLease4(m mac.MAC) bool {
	h.t.Helper()
	_, err := kea.LatestLease4(h.ctx, h.tx, m)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		h.t.Fatalf("LatestLease4: %v", err)
	}
	return err == nil
}

// assertNullSwitch checks that a log row carries no switch or lease data.
func assertNullSwitch(t *testing.T, r model.BouncerLog) {
	t.Helper()
	if r.SwitchIP != nil || r.SwitchPort != nil || r.ClientName != nil || r.ClientIP != nil {
		t.Errorf("log row switch/client columns = %v %v %v %v, want all NULL",
			deref(r.SwitchIP), deref(r.SwitchPort), deref(r.ClientName), deref(r.ClientIP))
	}
}

func ptr[T any](v T) *T { return &v }

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestProcess_NewClientWithoutSession(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	job := h.queue(m, 20)

	if err := h.c.ProcessMoveVLANJob(h.ctx, job); err != nil {
		t.Fatalf("ProcessMoveVLANJob: %v", err)
	}

	if v := h.vlan(m); v != 20 {
		t.Errorf("AssignedVLAN = %d, want 20", v)
	}
	var n int
	if err := h.tx.QueryRowContext(h.ctx, "SELECT COUNT(*) FROM radcheck WHERE username = ?", m.Hex()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("radcheck rows = %d, want 1", n)
	}
	if j := h.pendingJob(m); j != nil {
		t.Errorf("job still pending: %+v", j)
	}
	rows := h.logRows(m)
	if len(rows) != 1 {
		t.Fatalf("log rows = %d, want 1", len(rows))
	}
	if rows[0].OldVLAN != -1 || rows[0].NewVLAN != 20 {
		t.Errorf("log row VLANs = %d -> %d, want -1 -> 20", rows[0].OldVLAN, rows[0].NewVLAN)
	}
	assertNullSwitch(t, rows[0])
	if len(h.coa.calls) != 0 {
		t.Errorf("CoA calls = %d, want 0", len(h.coa.calls))
	}
}

func TestProcess_KnownClientWithSessionAndLeases(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	h.p.Authorized(m, 10)
	start := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	s := h.session(m, func(r *model.Radacct) { r.Acctstarttime = &start })
	l4 := h.lease4(m)
	h.lease6(m)
	job := h.queue(m, 20)

	if err := h.c.ProcessMoveVLANJob(h.ctx, job); err != nil {
		t.Fatalf("ProcessMoveVLANJob: %v", err)
	}

	if v := h.vlan(m); v != 20 {
		t.Errorf("AssignedVLAN = %d, want 20", v)
	}
	if len(h.coa.calls) != 1 {
		t.Fatalf("CoA calls = %d, want 1", len(h.coa.calls))
	}
	req := h.coa.calls[0]
	if req.SessionUID != s.Acctsessionid {
		t.Errorf("SessionUID = %q, want %q", req.SessionUID, s.Acctsessionid)
	}
	if !req.SessionStartDate.Equal(start) {
		t.Errorf("SessionStartDate = %v, want %v", req.SessionStartDate, start)
	}
	if !req.SwitchIP.Equal(net.ParseIP(switchIP)) {
		t.Errorf("SwitchIP = %v, want %s", req.SwitchIP, switchIP)
	}
	if string(req.SwitchSecret) != "secret" {
		t.Errorf("SwitchSecret = %q, want %q", req.SwitchSecret, "secret")
	}
	if _, err := kea.LatestLease4(h.ctx, h.tx, m); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("LatestLease4 after move: err = %v, want ErrNotFound", err)
	}
	if _, err := kea.Lease6Address(h.ctx, h.tx, m); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Lease6Address after move: err = %v, want ErrNotFound", err)
	}
	rows := h.logRows(m)
	if len(rows) != 1 {
		t.Fatalf("log rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.OldVLAN != 10 || r.NewVLAN != 20 {
		t.Errorf("log row VLANs = %d -> %d, want 10 -> 20", r.OldVLAN, r.NewVLAN)
	}
	wantIP := kea4(l4.Address)
	if deref(r.SwitchIP) != switchIP || deref(r.SwitchPort) != switchPort ||
		deref(r.ClientName) != *l4.Hostname || deref(r.ClientIP) != wantIP {
		t.Errorf("log row = %s %s %s %s, want %s %s %s %s",
			deref(r.SwitchIP), deref(r.SwitchPort), deref(r.ClientName), deref(r.ClientIP),
			switchIP, switchPort, *l4.Hostname, wantIP)
	}
	if j := h.pendingJob(m); j != nil {
		t.Errorf("job still pending: %+v", j)
	}
}

// kea4 renders a lease4 address column as dotted quad.
func kea4(a uint32) string {
	return net.IPv4(byte(a>>24), byte(a>>16), byte(a>>8), byte(a)).String()
}

func TestProcess_SameVLANIsDiscarded(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	h.p.Authorized(m, 10)
	h.session(m)
	h.lease4(m)
	job := h.queue(m, 10)

	if err := h.c.ProcessMoveVLANJob(h.ctx, job); err != nil {
		t.Fatalf("ProcessMoveVLANJob: %v", err)
	}

	if j := h.pendingJob(m); j != nil {
		t.Errorf("job still pending: %+v", j)
	}
	if len(h.coa.calls) != 0 {
		t.Errorf("CoA calls = %d, want 0", len(h.coa.calls))
	}
	if rows := h.logRows(m); len(rows) != 0 {
		t.Errorf("log rows = %d, want 0", len(rows))
	}
	if !h.hasLease4(m) {
		t.Error("lease4 was deleted, want it kept")
	}
	if v := h.vlan(m); v != 10 {
		t.Errorf("AssignedVLAN = %d, want 10", v)
	}
}

func TestProcess_CoAFailureRollsBack(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	h.p.Authorized(m, 10)
	h.session(m)
	h.lease4(m)
	nack := errors.New("nack")
	h.coa.fail = func(radius.CoARequest) error { return nack }
	job := h.queue(m, 20)

	err := h.c.ProcessMoveVLANJob(h.ctx, job)
	if !errors.Is(err, nack) {
		t.Fatalf("ProcessMoveVLANJob: err = %v, want it to wrap %v", err, nack)
	}

	if v := h.vlan(m); v != 10 {
		t.Errorf("AssignedVLAN = %d, want 10", v)
	}
	j := h.pendingJob(m)
	if j == nil {
		t.Fatal("job no longer pending")
	}
	if j.Retries != 0 {
		t.Errorf("Retries = %d, want 0", j.Retries)
	}
	if rows := h.logRows(m); len(rows) != 0 {
		t.Errorf("log rows = %d, want 0", len(rows))
	}
	if !h.hasLease4(m) {
		t.Error("lease4 was deleted, want it kept")
	}
}

func TestProcess_SessionWithoutStartTime(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	h.p.Authorized(m, 10)
	h.session(m, func(r *model.Radacct) { r.Acctstarttime = nil })
	job := h.queue(m, 20)

	if err := h.c.ProcessMoveVLANJob(h.ctx, job); err != nil {
		t.Fatalf("ProcessMoveVLANJob: %v", err)
	}
	if len(h.coa.calls) != 1 {
		t.Fatalf("CoA calls = %d, want 1", len(h.coa.calls))
	}
	if d := h.coa.calls[0].SessionStartDate; !d.IsZero() {
		t.Errorf("SessionStartDate = %v, want zero", d)
	}
}

func TestProcess_KnownClientWithoutSession(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	h.p.Authorized(m, 10)
	job := h.queue(m, 20)

	if err := h.c.ProcessMoveVLANJob(h.ctx, job); err != nil {
		t.Fatalf("ProcessMoveVLANJob: %v", err)
	}
	if v := h.vlan(m); v != 20 {
		t.Errorf("AssignedVLAN = %d, want 20", v)
	}
	if len(h.coa.calls) != 0 {
		t.Errorf("CoA calls = %d, want 0", len(h.coa.calls))
	}
	rows := h.logRows(m)
	if len(rows) != 1 {
		t.Fatalf("log rows = %d, want 1", len(rows))
	}
	if rows[0].OldVLAN != 10 || rows[0].NewVLAN != 20 {
		t.Errorf("log row VLANs = %d -> %d, want 10 -> 20", rows[0].OldVLAN, rows[0].NewVLAN)
	}
	assertNullSwitch(t, rows[0])
}

func TestWork_MalformedMACRetires(t *testing.T) {
	h := newHarness(t)
	const bad = "not-a-mac"
	h.p.Job(func(j *model.BouncerJobs) { j.ClientMAC, j.TargetVLAN = bad, 20 })
	m := h.client()
	h.queue(m, 20)

	if err := h.c.work(h.ctx); err != nil {
		t.Fatalf("work: %v", err)
	}
	j := h.pendingRaw(bad)
	if j == nil {
		t.Fatal("malformed job no longer pending after one pass")
	}
	if j.Retries != 1 {
		t.Errorf("malformed job Retries = %d, want 1", j.Retries)
	}
	if j := h.pendingJob(m); j != nil {
		t.Errorf("valid job still pending: %+v", j)
	}
	if v := h.vlan(m); v != 20 {
		t.Errorf("AssignedVLAN = %d, want 20", v)
	}

	for range 2 {
		if err := h.c.work(h.ctx); err != nil {
			t.Fatalf("work: %v", err)
		}
	}
	if j := h.pendingRaw(bad); j != nil {
		t.Errorf("malformed job still pending after %d passes: %+v", jobs.MaxRetries, j)
	}
}

func TestWork_FailedJobBumpsRetries(t *testing.T) {
	h := newHarness(t)
	m := h.client()
	h.p.Authorized(m, 10)
	h.session(m)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	h.p.Job(func(j *model.BouncerJobs) { j.ClientMAC, j.TargetVLAN, j.LastUpdate = m.Hex(), 20, old })
	h.coa.fail = func(radius.CoARequest) error { return errors.New("nack") }

	if err := h.c.work(h.ctx); err != nil {
		t.Fatalf("work: %v", err)
	}
	j := h.mustPending(m)
	if j.Retries != 1 {
		t.Errorf("Retries = %d, want 1", j.Retries)
	}
	if cutoff := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC); !j.LastUpdate.After(cutoff) {
		t.Errorf("LastUpdate = %v, want after %v", j.LastUpdate, cutoff)
	}
	if v := h.vlan(m); v != 10 {
		t.Errorf("AssignedVLAN = %d, want 10", v)
	}
}

func TestWork_CancelDuringCoADoesNotCountAsFailure(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	m := h.client()
	h.p.Authorized(m, 10)
	h.session(m)
	h.queue(m, 20)
	h.coa.fail = func(radius.CoARequest) error {
		cancel()
		return ctx.Err()
	}

	err := h.c.work(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("work: err = %v, want context.Canceled", err)
	}
	if j := h.mustPending(m); j.Retries != 0 {
		t.Errorf("Retries = %d, want 0", j.Retries)
	}
	if v := h.vlan(m); v != 10 {
		t.Errorf("AssignedVLAN = %d, want 10", v)
	}
}

func TestWork_ContinuesAfterFailure(t *testing.T) {
	h := newHarness(t)
	a := h.client()
	h.p.Authorized(a, 10)
	sa := h.session(a)
	h.queue(a, 20)
	b := h.client()
	h.queue(b, 30)
	h.coa.fail = func(req radius.CoARequest) error {
		if req.SessionUID == sa.Acctsessionid {
			return errors.New("nack")
		}
		return nil
	}

	if err := h.c.work(h.ctx); err != nil {
		t.Fatalf("work: %v", err)
	}
	if j := h.mustPending(a); j.Retries != 1 {
		t.Errorf("job A Retries = %d, want 1", j.Retries)
	}
	if v := h.vlan(a); v != 10 {
		t.Errorf("A AssignedVLAN = %d, want 10", v)
	}
	if j := h.pendingJob(b); j != nil {
		t.Errorf("job B still pending: %+v", j)
	}
	if v := h.vlan(b); v != 30 {
		t.Errorf("B AssignedVLAN = %d, want 30", v)
	}
}

func TestSpin_ReturnsOnCancelledContext(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()

	done := make(chan error, 1)
	go func() { done <- h.c.Spin(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Spin: err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Spin did not return within a second of a cancelled context")
	}
}
