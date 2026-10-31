package testgen_test

import (
	"context"
	"net"
	"os"
	"testing"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/kea"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/switches"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
	"github.com/go-jet/jet/v2/mysql"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// count returns the number of rows of tbl matching cond.
func count(t *testing.T, q db.Querier, tbl mysql.ReadableTable, cond mysql.BoolExpression) int64 {
	t.Helper()
	var dest struct{ N int64 }
	if err := mysql.SELECT(mysql.COUNT(mysql.STAR).AS("n")).FROM(tbl).WHERE(cond).
		QueryContext(context.Background(), q, &dest); err != nil {
		t.Fatalf("counting: %v", err)
	}
	return dest.N
}

func TestPersistent_RowsAreReadable(t *testing.T) {
	q := testdb.Tx(t)
	p := testgen.Persistent(t, q)

	s := p.Session()
	l := p.LoginLog()
	j := p.Job()
	v := p.VLAN()
	l4 := p.Lease4()
	l6 := p.Lease6()

	for _, c := range []struct {
		name string
		id   int64
		tbl  mysql.ReadableTable
		cond mysql.BoolExpression
	}{
		{"radacct", s.Radacctid, table.Radacct, table.Radacct.Radacctid.EQ(mysql.Int64(s.Radacctid))},
		{"login_logs", int64(l.ID), table.LoginLogs, table.LoginLogs.ID.EQ(mysql.Int32(l.ID))},
		{"bouncer_jobs", int64(j.ID), table.BouncerJobs, table.BouncerJobs.ID.EQ(mysql.Int32(j.ID))},
		{"bouncer_vlan", int64(v.ID), table.BouncerVlan, table.BouncerVlan.ID.EQ(mysql.Int32(v.ID))},
	} {
		if c.id == 0 {
			t.Errorf("%s: returned ID is 0", c.name)
		}
		if n := count(t, q, c.tbl, c.cond); n != 1 {
			t.Errorf("%s: %d rows with ID %d, want 1", c.name, n, c.id)
		}
	}
	if n := count(t, q, table.Lease4, table.Lease4.Address.EQ(mysql.Uint32(l4.Address))); n != 1 {
		t.Errorf("lease4: %d rows with address %d, want 1", n, l4.Address)
	}
	if n := count(t, q, table.Lease6, table.Lease6.Address.EQ(mysql.Blob(l6.Address))); n != 1 {
		t.Errorf("lease6: %d rows with address %x, want 1", n, l6.Address)
	}
}

func TestPersistent_ModIsApplied(t *testing.T) {
	q := testdb.Tx(t)
	p := testgen.Persistent(t, q)
	name := "overridden" + kea.HostnameSuffix
	l := p.Lease4(func(l *model.Lease4) { l.Hostname = &name })
	if l.Hostname == nil || *l.Hostname != name {
		t.Errorf("returned Hostname = %v, want %q", l.Hostname, name)
	}
	m, ok := mac.FromBytes(*l.Hwaddr)
	if !ok {
		t.Fatalf("bad hwaddr %x", *l.Hwaddr)
	}
	got, err := kea.LatestLease4(context.Background(), q, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname == nil || *got.Hostname != name {
		t.Errorf("stored Hostname = %v, want %q", got.Hostname, name)
	}
}

func TestLocal_InsertsNothing(t *testing.T) {
	q := testdb.Tx(t)
	j := testgen.Local(t).Job()
	if j.ID != 0 {
		t.Errorf("ID = %d, want 0", j.ID)
	}
	if n := count(t, q, table.BouncerJobs, table.BouncerJobs.ClientMAC.EQ(mysql.String(j.ClientMAC))); n != 0 {
		t.Errorf("%d jobs with MAC %s, want 0", n, j.ClientMAC)
	}
}

func TestSeed_DeterministicPerTest(t *testing.T) {
	if a, b := testgen.Local(t).MAC(), testgen.Local(t).MAC(); a != b {
		t.Errorf("first MACs differ: %v vs %v", a, b)
	}
	t.Setenv(testgen.SeedEnv, "42")
	if a, b := testgen.Local(t).MAC(), testgen.Local(t).MAC(); a != b {
		t.Errorf("first MACs with seed 42 differ: %v vs %v", a, b)
	}
}

func TestSwitch_Default(t *testing.T) {
	q := testdb.Tx(t)
	s := testgen.Persistent(t, q).Switch()
	if len(s.IPs) != 1 || len(s.VLANs) != 1 {
		t.Fatalf("got %d IPs and %d VLANs, want 1 and 1", len(s.IPs), len(s.VLANs))
	}
	if s.Map.PrimaryVlan != s.VLANs[0].VlanID {
		t.Errorf("PrimaryVlan = %d, want %d", s.Map.PrimaryVlan, s.VLANs[0].VlanID)
	}
	got, err := switches.ByIP(context.Background(), q, net.ParseIP(s.IPs[0].IP))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != int(s.Map.ID) {
		t.Errorf("ID = %d, want %d", got.ID, s.Map.ID)
	}
	if got.PrimaryVLAN == nil || len(got.VLANs) != 1 {
		t.Errorf("PrimaryVLAN = %v, VLANs = %v; want a primary VLAN and one VLAN", got.PrimaryVLAN, got.VLANs)
	}
}

func TestAuthorized(t *testing.T) {
	q := testdb.Tx(t)
	p := testgen.Persistent(t, q)
	m := p.MAC()
	p.Authorized(m, 10)

	if v, err := freeradius.AssignedVLAN(context.Background(), q, m); err != nil || v != 10 {
		t.Errorf("AssignedVLAN = %d, %v; want 10", v, err)
	}
	if n := count(t, q, table.Radcheck, table.Radcheck.Username.EQ(mysql.String(m.Hex()))); n != 1 {
		t.Errorf("%d radcheck rows, want 1", n)
	}
	if n := count(t, q, table.Radreply, table.Radreply.Username.EQ(mysql.String(m.Hex()))); n != 3 {
		t.Errorf("%d radreply rows, want 3", n)
	}
}
