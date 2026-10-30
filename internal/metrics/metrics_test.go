package metrics

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var uniqueID atomic.Int64

func seedSession(t *testing.T, q db.Querier, m mac.MAC, switchIP string, stop *time.Time) {
	t.Helper()
	start := time.Now().Add(-time.Hour)
	row := model.Radacct{
		Acctsessionid:   "sess",
		Acctuniqueid:    fmt.Sprintf("uniq-%d", uniqueID.Add(1)),
		Username:        m.Hex(),
		Nasipaddress:    switchIP,
		Acctstarttime:   &start,
		Acctstoptime:    stop,
		Framedipaddress: "10.20.0.50",
	}
	_, err := table.Radacct.INSERT(table.Radacct.MutableColumns).MODEL(row).ExecContext(context.Background(), q)
	if err != nil {
		t.Fatalf("seeding session: %v", err)
	}
}

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

func TestSample_DistinctUsers(t *testing.T) {
	ctx := context.Background()
	tx := testdb.Tx(t)
	const sw = "10.77.13.1" // unique to this test

	activeBefore := gaugeValue(t, usersActive)
	authorizedBefore := gaugeValue(t, usersAuthorized)

	a := mac.MustParse("02:00:00:00:13:0a")
	b := mac.MustParse("02:00:00:00:13:0b")
	if err := freeradius.Authorize(ctx, tx, a, 100); err != nil {
		t.Fatal(err)
	}
	if err := freeradius.Authorize(ctx, tx, b, 100); err != nil {
		t.Fatal(err)
	}
	stop := time.Now()
	seedSession(t, tx, a, sw, nil)
	seedSession(t, tx, a, sw, nil)
	seedSession(t, tx, b, sw, &stop)

	sample(ctx, tx)

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
