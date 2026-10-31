package freeradius_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
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
	p := testgen.Persistent(t, q)
	for _, m := range []mac.MAC{a, b} {
		p.Authorized(m, 42)
	}
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
	p := testgen.Persistent(t, q)
	p.Authorized(a, 42)
	p.Authorized(b, 7)
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

func TestFindMAC_ByMAC(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:61")
	testgen.Persistent(t, q).Session(session(m, nil, nil))
	for _, search := range []string{m.Hex(), m.String(), "00-11-22-33-44-61"} {
		got, err := freeradius.FindMAC(ctx, q, search)
		if err != nil {
			t.Fatalf("FindMAC(%q): %v", search, err)
		}
		if got != m {
			t.Errorf("FindMAC(%q) = %v, want %v", search, got, m)
		}
	}
}

func TestFindMAC_ByIPNewestWins(t *testing.T) {
	ctx, q := begin(t)
	p := testgen.Persistent(t, q)
	older, newer := mac.MustParse("00:11:22:33:44:62"), mac.MustParse("00:11:22:33:44:63")
	ip := p.IPv4().String()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	later := base.Add(time.Hour)
	framed := func(r *model.Radacct) { r.Framedipaddress = ip }
	p.Session(session(newer, &later, nil), framed)
	p.Session(session(older, &base, &later), framed)

	got, err := freeradius.FindMAC(ctx, q, ip)
	if err != nil {
		t.Fatal(err)
	}
	if got != newer {
		t.Errorf("got %v, want the newest session's %v", got, newer)
	}
}

func TestFindMAC_NotFound(t *testing.T) {
	ctx, q := begin(t)
	p := testgen.Persistent(t, q)
	for _, search := range []string{"00:11:22:33:44:64", p.IPv4().String(), "some-hostname", "2001:db8::1"} {
		if m, err := freeradius.FindMAC(ctx, q, search); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("FindMAC(%q) = %v, %v; want ErrNotFound", search, m, err)
		}
	}
}

func TestFindMAC_BadUsername(t *testing.T) {
	ctx, q := begin(t)
	p := testgen.Persistent(t, q)
	ip := p.IPv4().String()
	row := p.Session(func(r *model.Radacct) { r.Username, r.Framedipaddress = "not-a-mac", ip })

	_, err := freeradius.FindMAC(ctx, q, ip)
	if err == nil || errors.Is(err, db.ErrNotFound) {
		t.Fatalf("err = %v, want a non-not-found error", err)
	}
	if !strings.Contains(err.Error(), "not-a-mac") || !strings.Contains(err.Error(), strconv.FormatInt(row.Radacctid, 10)) {
		t.Errorf("error %q does not name the row", err)
	}
}

// acceptOf returns a mod that makes a post-auth row an accept of m at at.
func acceptOf(m mac.MAC, at time.Time) func(*model.Radpostauth) {
	return func(r *model.Radpostauth) { r.Username, r.Pass, r.Authdate = m.Hex(), m.Hex(), at }
}

// acceptsOf returns the entries of accepts belonging to one of users, in their order.
func acceptsOf(accepts []freeradius.Accept, users ...string) []freeradius.Accept {
	var mine []freeradius.Accept
	for _, a := range accepts {
		for _, u := range users {
			if a.RawMAC == u {
				mine = append(mine, a)
			}
		}
	}
	return mine
}

func TestLatestAccepts(t *testing.T) {
	ctx, q := begin(t)
	p := testgen.Persistent(t, q)
	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-30 * time.Minute)
	a, b, rejected, noSwitch, old := p.MAC(), p.MAC(), p.MAC(), p.MAC(), p.MAC()
	p.Authorized(a, 20)
	p.Authorized(rejected, 21)
	p.Authorized(noSwitch, 22)
	p.Authorized(old, 23)

	p.Accept(acceptOf(a, now.Add(-10*time.Minute)))
	newer := p.Accept(acceptOf(a, now.Add(-time.Minute)))
	onB := p.Accept(acceptOf(b, now.Add(-5*time.Minute)))
	p.Accept(acceptOf(rejected, now.Add(-2*time.Minute)), func(r *model.Radpostauth) { r.Reply = "Access-Reject" })
	p.Accept(acceptOf(noSwitch, now.Add(-2*time.Minute)), func(r *model.Radpostauth) { r.Nasipaddress = nil })
	p.Accept(acceptOf(old, since.Add(-time.Second)))

	accepts, err := freeradius.LatestAccepts(ctx, q, since)
	if err != nil {
		t.Fatal(err)
	}
	got := acceptsOf(accepts, a.Hex(), b.Hex(), rejected.Hex(), noSwitch.Hex(), old.Hex())
	if len(got) != 2 {
		t.Fatalf("got %+v, want exactly the accepts of %s and %s", got, a.Hex(), b.Hex())
	}

	replyVLAN := func(r model.Radpostauth) int {
		v, err := strconv.Atoi(*r.Vlan)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	check := func(name string, got freeradius.Accept, m mac.MAC, row model.Radpostauth, assigned int) {
		t.Helper()
		if got.MAC != m || got.RawMAC != m.Hex() || !got.At.Equal(row.Authdate) ||
			!got.SwitchIP.Equal(net.ParseIP(*row.Nasipaddress)) || got.SwitchPort == nil ||
			*got.SwitchPort != *row.Nasportid || got.ReplyVLAN != replyVLAN(row) || got.AssignedVLAN != assigned {
			t.Errorf("%s: got %+v, want MAC %v at %v on %s port %s, reply VLAN %s, assigned VLAN %d", name, got, m,
				row.Authdate, *row.Nasipaddress, *row.Nasportid, *row.Vlan, assigned)
		}
	}
	check("newest first, newest accept of a", got[0], a, newer, 20)
	check("accept without radreply", got[1], b, onB, 0)
}

func TestLatestAccepts_Unparsable(t *testing.T) {
	ctx, q := begin(t)
	p := testgen.Persistent(t, q)
	row := p.Accept(func(r *model.Radpostauth) {
		r.Username, r.Vlan = "not-a-mac", ptr("guest")
	})

	accepts, err := freeradius.LatestAccepts(ctx, q, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got := acceptsOf(accepts, "not-a-mac")
	if len(got) != 1 {
		t.Fatalf("got %+v, want one accept of not-a-mac", got)
	}
	if !got[0].MAC.IsZero() || got[0].ReplyVLAN != 0 || !got[0].SwitchIP.Equal(net.ParseIP(*row.Nasipaddress)) {
		t.Errorf("got %+v, want a zero MAC, ReplyVLAN 0 and switch %s", got[0], *row.Nasipaddress)
	}
}

func ptr[T any](v T) *T { return &v }
