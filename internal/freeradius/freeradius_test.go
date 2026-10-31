package freeradius_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
	"github.com/go-jet/jet/v2/mysql"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// session returns mods that make a session belong to m and run from start until stop (nil: still active).
func session(m mac.MAC, start, stop *time.Time) func(*model.Radacct) {
	return func(r *model.Radacct) {
		r.Username, r.Acctstarttime, r.Acctstoptime = m.Hex(), start, stop
	}
}

func begin(t *testing.T) (context.Context, *sql.Tx) {
	return context.Background(), testdb.Tx(t)
}

func TestSessionsForMAC_OrderAndMapping(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:51")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := testgen.Persistent(t, q)
	stop, newest := base.Add(time.Hour), base.Add(2*time.Hour)
	p.Session(session(m, &base, &stop))
	p.Session(session(m, &newest, nil), func(r *model.Radacct) {
		port, in, out := "GigabitEthernet1/0/5", int64(10), int64(20)
		r.Nasipaddress, r.Framedipaddress = "10.10.0.1", "10.20.0.50"
		r.Nasportid, r.Acctinputoctets, r.Acctoutputoctets = &port, &in, &out
	})

	got, err := freeradius.SessionsForMAC(ctx, q, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	s := got[0]
	if s.Stop != nil || got[1].Stop == nil {
		t.Errorf("newest session should be the open one: %+v", got)
	}
	if !s.SwitchIP.Equal(net.ParseIP("10.10.0.1")) || !s.ClientIP.Equal(net.ParseIP("10.20.0.50")) {
		t.Errorf("bad IPs: %v %v", s.SwitchIP, s.ClientIP)
	}
	if s.SwitchPort == nil || *s.SwitchPort != "GigabitEthernet1/0/5" {
		t.Errorf("bad port: %v", s.SwitchPort)
	}
	if s.BytesIn == nil || *s.BytesIn != 10 || s.BytesOut == nil || *s.BytesOut != 20 {
		t.Errorf("bad octets: %v %v", s.BytesIn, s.BytesOut)
	}
}

func TestSessionsForMAC_NullableColumns(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:52")
	testgen.Persistent(t, q).Session(session(m, nil, nil), func(r *model.Radacct) {
		r.Nasportid, r.Acctinputoctets, r.Acctoutputoctets = nil, nil, nil
		r.Framedipaddress = ""
	})

	got, err := freeradius.SessionsForMAC(ctx, q, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	s := got[0]
	if s.Start != nil || s.SwitchPort != nil || s.BytesIn != nil || s.BytesOut != nil || s.ClientIP != nil {
		t.Errorf("absent values should be nil: %+v", s)
	}
}

func TestSessionsForMAC_None(t *testing.T) {
	ctx, q := begin(t)
	got, err := freeradius.SessionsForMAC(ctx, q, mac.MustParse("00:11:22:33:44:53"))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("want empty non-nil slice, got %#v", got)
	}
}

func TestAssignedVLAN(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:54")
	if _, err := freeradius.AssignedVLAN(ctx, q, m); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := freeradius.Authorize(ctx, q, m, 42); err != nil {
		t.Fatal(err)
	}
	if v, err := freeradius.AssignedVLAN(ctx, q, m); err != nil || v != 42 {
		t.Fatalf("got %d, %v; want 42", v, err)
	}
	if err := freeradius.SetVLAN(ctx, q, m, 7); err != nil {
		t.Fatal(err)
	}
	if v, err := freeradius.AssignedVLAN(ctx, q, m); err != nil || v != 7 {
		t.Fatalf("got %d, %v; want 7", v, err)
	}
}

func TestAuthorize_Rows(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:55")
	if err := freeradius.Authorize(ctx, q, m, 42); err != nil {
		t.Fatal(err)
	}
	var checks []model.Radcheck
	if err := mysql.SELECT(table.Radcheck.AllColumns).FROM(table.Radcheck).
		WHERE(table.Radcheck.Username.EQ(mysql.String(m.Hex()))).QueryContext(ctx, q, &checks); err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Attribute != "Cleartext-Password" || checks[0].Op != ":=" || checks[0].Value != m.Hex() {
		t.Errorf("bad radcheck rows: %+v", checks)
	}
	var replies []model.Radreply
	if err := mysql.SELECT(table.Radreply.AllColumns).FROM(table.Radreply).
		WHERE(table.Radreply.Username.EQ(mysql.String(m.Hex()))).QueryContext(ctx, q, &replies); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Tunnel-Private-Group-ID": "42", "Tunnel-Medium-Type": "IEEE-802", "Tunnel-Type": "VLAN"}
	if len(replies) != 3 {
		t.Fatalf("want 3 radreply rows, got %+v", replies)
	}
	for _, r := range replies {
		if r.Op != ":=" || want[r.Attribute] != r.Value {
			t.Errorf("unexpected radreply row %+v", r)
		}
	}
}

type counts struct{ known, authorized, active int }

func takeCounts(t *testing.T, ctx context.Context, q db.Querier) counts {
	t.Helper()
	var c counts
	var err error
	if c.known, err = freeradius.CountKnown(ctx, q); err != nil {
		t.Fatal(err)
	}
	if c.authorized, err = freeradius.CountAuthorized(ctx, q); err != nil {
		t.Fatal(err)
	}
	if c.active, err = freeradius.CountActive(ctx, q); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCounts(t *testing.T) {
	ctx, q := begin(t)
	before := takeCounts(t, ctx, q)
	a, b, c := mac.MustParse("00:11:22:33:44:5a"), mac.MustParse("00:11:22:33:44:5b"), mac.MustParse("00:11:22:33:44:5c")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, m := range []mac.MAC{a, b} {
		if err := freeradius.Authorize(ctx, q, m, 42); err != nil {
			t.Fatal(err)
		}
	}
	p := testgen.Persistent(t, q)
	stop := now.Add(time.Hour)
	p.Session(session(a, &now, nil))
	p.Session(session(a, &now, nil))
	p.Session(session(b, &now, &stop))
	p.Session(session(c, &now, &stop))

	after := takeCounts(t, ctx, q)
	got := counts{after.known - before.known, after.authorized - before.authorized, after.active - before.active}
	if want := (counts{3, 2, 1}); got != want {
		t.Errorf("count deltas = %+v, want %+v", got, want)
	}
}

func TestPerVLANAndPerSwitch(t *testing.T) {
	ctx, q := begin(t)
	vlans := func(f func(context.Context, db.Querier) ([]freeradius.VLANCount, error)) map[int]int {
		r, err := f(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		m := map[int]int{}
		for _, v := range r {
			m[v.VLAN] = v.Count
		}
		return m
	}
	switches := func() map[string]int {
		r, err := freeradius.ActivePerSwitch(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]int{}
		for _, v := range r {
			m[v.SwitchIP] = v.Count
		}
		return m
	}
	authBefore, actBefore, swBefore := vlans(freeradius.AuthorizedPerVLAN), vlans(freeradius.ActivePerVLAN), switches()

	a, b := mac.MustParse("00:11:22:33:44:5d"), mac.MustParse("00:11:22:33:44:5e")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := freeradius.Authorize(ctx, q, a, 42); err != nil {
		t.Fatal(err)
	}
	if err := freeradius.Authorize(ctx, q, b, 7); err != nil {
		t.Fatal(err)
	}
	p := testgen.Persistent(t, q)
	p.Session(session(a, &now, nil), func(r *model.Radacct) { r.Nasipaddress = "10.10.0.1" })
	p.Session(session(b, &now, nil), func(r *model.Radacct) { r.Nasipaddress = "10.10.0.2" })

	authAfter, actAfter, swAfter := vlans(freeradius.AuthorizedPerVLAN), vlans(freeradius.ActivePerVLAN), switches()
	for _, v := range []int{42, 7} {
		if authAfter[v]-authBefore[v] != 1 || actAfter[v]-actBefore[v] != 1 {
			t.Errorf("vlan %d: authorized delta %d, active delta %d; want 1, 1", v, authAfter[v]-authBefore[v], actAfter[v]-actBefore[v])
		}
	}
	for _, s := range []string{"10.10.0.1", "10.10.0.2"} {
		if swAfter[s]-swBefore[s] != 1 {
			t.Errorf("switch %s delta %d, want 1", s, swAfter[s]-swBefore[s])
		}
	}
}
