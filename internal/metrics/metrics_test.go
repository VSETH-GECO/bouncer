package metrics

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// gaugeValue reads a gauge without registering it. It stands in for testutil.ToFloat64, whose import would add
// github.com/kylelemons/godebug to go.mod.
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("reading gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

func resetVectors() {
	usersActivePerSwitch.Reset()
	usersActivePerVLAN.Reset()
	usersAuthorizedPerVLAN.Reset()
}

func TestSample_DistinctUsers(t *testing.T) {
	t.Cleanup(resetVectors)
	ctx := context.Background()
	tx := testdb.Tx(t)
	const sw = "10.77.13.1" // unique to this test

	// Sample first so the baseline reflects what the shared database already holds, not the gauges' initial zero.
	if err := Sample(ctx, tx); err != nil {
		t.Fatalf("Sample: %v", err)
	}
	activeBefore := gaugeValue(t, usersActive)
	authorizedBefore := gaugeValue(t, usersAuthorized)

	a := mac.MustParse("02:00:00:00:13:0a")
	b := mac.MustParse("02:00:00:00:13:0b")
	p := testgen.Persistent(t, tx)
	p.Authorized(a, 100)
	p.Authorized(b, 100)
	stop := time.Now()
	on := func(m mac.MAC, stop *time.Time) func(*model.Radacct) {
		return func(r *model.Radacct) { r.Username, r.Nasipaddress, r.Acctstoptime = m.Hex(), sw, stop }
	}
	p.Session(on(a, nil))
	p.Session(on(a, nil))
	p.Session(on(b, &stop))

	if err := Sample(ctx, tx); err != nil {
		t.Fatalf("Sample: %v", err)
	}

	if got := gaugeValue(t, usersActive) - activeBefore; got != 1 {
		t.Errorf("usersActive delta = %v, want 1", got)
	}
	if got := gaugeValue(t, usersAuthorized) - authorizedBefore; got != 2 {
		t.Errorf("usersAuthorized delta = %v, want 2", got)
	}
	if got := gaugeValue(t, usersActivePerSwitch.WithLabelValues(sw)); got != 1 {
		t.Errorf("active on %s = %v, want 1", sw, got)
	}
}

type failingQuerier struct{}

func (failingQuerier) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("boom")
}
func (failingQuerier) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("boom")
}

func TestSample_FailedQueryKeepsValues(t *testing.T) {
	activePrev := gaugeValue(t, usersActive)
	t.Cleanup(func() {
		resetVectors()
		usersActive.Set(activePrev)
	})
	const sw = "10.77.13.2" // unique to this test

	resetVectors()
	usersActive.Set(7)
	usersActivePerSwitch.WithLabelValues(sw).Set(5)

	err := Sample(context.Background(), failingQuerier{})
	if err == nil {
		t.Fatal("Sample with a failing querier returned nil")
	}
	for _, name := range []string{"bouncer_users_total", "bouncer_users_authorized", "bouncer_users_active",
		"bouncer_users_per_vlan", "bouncer_users_active_per_vlan", "bouncer_users_active_per_switch"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}

	if got := gaugeValue(t, usersActive); got != 7 {
		t.Errorf("usersActive = %v, want 7", got)
	}
	if got := gaugeValue(t, usersActivePerSwitch.WithLabelValues(sw)); got != 5 {
		t.Errorf("active on %s = %v, want 5", sw, got)
	}
	ch := make(chan prometheus.Metric, 16)
	usersActivePerSwitch.Collect(ch)
	close(ch)
	n := 0
	for range ch {
		n++
	}
	if n != 1 {
		t.Errorf("usersActivePerSwitch has %d children, want 1", n)
	}
}

func TestSetLeader(t *testing.T) {
	t.Cleanup(func() { SetLeader(false) })
	SetLeader(true)
	if v := gaugeValue(t, leader); v != 1 {
		t.Fatalf("bouncer_leader = %v after SetLeader(true), want 1", v)
	}
	SetLeader(false)
	if v := gaugeValue(t, leader); v != 0 {
		t.Fatalf("bouncer_leader = %v after SetLeader(false), want 0", v)
	}
}
