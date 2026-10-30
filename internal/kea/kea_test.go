package kea_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/kea"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func ptr[T any](v T) *T { return &v }

func begin(t *testing.T) (context.Context, *sql.Tx) {
	return context.Background(), testdb.Tx(t)
}

func seedLease4(t *testing.T, q db.Querier, m mac.MAC, ip net.IP, hostname *string, expire time.Time) {
	t.Helper()
	hw := m.Bytes()
	row := model.Lease4{
		Address:  binary.BigEndian.Uint32(ip.To4()),
		Hwaddr:   &hw,
		Hostname: hostname,
		Expire:   &expire,
		SubnetID: ptr(uint32(1)),
		State:    ptr(uint32(0)),
		PoolID:   0,
	}
	insertLease4(t, q, row)
}

func insertLease4(t *testing.T, q db.Querier, row model.Lease4) {
	t.Helper()
	_, err := table.Lease4.INSERT(table.Lease4.AllColumns).MODEL(row).ExecContext(context.Background(), q)
	if err != nil {
		t.Fatalf("seeding lease4: %v", err)
	}
}

func seedLease6(t *testing.T, q db.Querier, m mac.MAC, ip net.IP) {
	t.Helper()
	hw := m.Bytes()
	row := model.Lease6{
		Address:   ip.To16(),
		Hwaddr:    &hw,
		Expire:    ptr(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
		SubnetID:  ptr(uint32(1)),
		LeaseType: ptr(int8(0)),
		State:     ptr(uint32(0)),
	}
	_, err := table.Lease6.INSERT(table.Lease6.AllColumns).MODEL(row).ExecContext(context.Background(), q)
	if err != nil {
		t.Fatalf("seeding lease6: %v", err)
	}
}

func statSum(t *testing.T, q db.Querier, tbl string) int64 {
	t.Helper()
	var n int64
	rows, err := q.QueryContext(context.Background(), "SELECT IFNULL(SUM(leases),0) FROM "+tbl)
	if err != nil {
		t.Fatalf("reading %s: %v", tbl, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("reading %s: no row", tbl)
	}
	if err := rows.Scan(&n); err != nil {
		t.Fatalf("scanning %s: %v", tbl, err)
	}
	return n
}

func TestLatestLease4(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:51")
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	seedLease4(t, q, m, net.ParseIP("10.20.0.51"), ptr("old-pc"), t0)
	seedLease4(t, q, m, net.ParseIP("10.20.0.52"), ptr("test-pc"), t0.Add(time.Hour))

	got, err := kea.LatestLease4(ctx, q, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.MAC != m {
		t.Errorf("MAC = %v, want %v", got.MAC, m)
	}
	if !got.IP.Equal(net.ParseIP("10.20.0.52")) {
		t.Errorf("IP = %v, want 10.20.0.52", got.IP)
	}
	if got.Hostname == nil || *got.Hostname != "test-pc" {
		t.Errorf("Hostname = %v, want test-pc", got.Hostname)
	}
	if got.Expire == nil || !got.Expire.Equal(t0.Add(time.Hour)) {
		t.Errorf("Expire = %v, want %v", got.Expire, t0.Add(time.Hour))
	}

	_, err = kea.LatestLease4(ctx, q, mac.MustParse("00:11:22:33:44:5f"))
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("unknown MAC: err = %v, want ErrNotFound", err)
	}
}

func TestLatestLease4_NullHostname(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:52")
	seedLease4(t, q, m, net.ParseIP("10.20.0.53"), nil, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	got, err := kea.LatestLease4(ctx, q, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != nil {
		t.Errorf("Hostname = %q, want nil", *got.Hostname)
	}
}

func TestLease6Address(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:53")
	seedLease6(t, q, m, net.ParseIP("fd00::50"))

	got, err := kea.Lease6Address(ctx, q, m)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(net.ParseIP("fd00::50")) {
		t.Errorf("address = %v, want fd00::50", got)
	}

	_, err = kea.Lease6Address(ctx, q, mac.MustParse("00:11:22:33:44:5f"))
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("unknown MAC: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteLeases_FiresTriggers(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:54")
	seedLease4(t, q, m, net.ParseIP("10.20.0.54"), ptr("test-pc"), time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	seedLease6(t, q, m, net.ParseIP("fd00::54"))
	before4, before6 := statSum(t, q, "lease4_stat"), statSum(t, q, "lease6_stat")

	if err := kea.DeleteLeases(ctx, q, m); err != nil {
		t.Fatal(err)
	}
	if _, err := kea.LatestLease4(ctx, q, m); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("lease4 still there: %v", err)
	}
	if _, err := kea.Lease6Address(ctx, q, m); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("lease6 still there: %v", err)
	}
	if got := statSum(t, q, "lease4_stat"); got != before4-1 {
		t.Errorf("lease4_stat = %d, want %d", got, before4-1)
	}
	if got := statSum(t, q, "lease6_stat"); got != before6-1 {
		t.Errorf("lease6_stat = %d, want %d", got, before6-1)
	}
	if err := kea.DeleteLeases(ctx, q, m); err != nil {
		t.Errorf("deleting nothing: %v", err)
	}
}

func TestFindMAC_ByIPv4_ByIPv6_ByMAC_ByHostname(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:55")
	seedLease4(t, q, m, net.ParseIP("10.20.0.50"), ptr("test-pc"+kea.HostnameSuffix), time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	seedLease6(t, q, m, net.ParseIP("fd00::50"))

	for _, v := range []string{"10.20.0.50", "fd00::50", "00:11:22:33:44:55", "001122334455", "test-pc"} {
		got, err := kea.FindMAC(ctx, q, v)
		if err != nil {
			t.Errorf("FindMAC(%q): %v", v, err)
			continue
		}
		if got != m {
			t.Errorf("FindMAC(%q) = %v, want %v", v, got, m)
		}
	}
	if _, err := kea.FindMAC(ctx, q, "nobody"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("nobody: err = %v, want ErrNotFound", err)
	}
}

func TestFindMAC_BadHwaddrIsError(t *testing.T) {
	ctx, q := begin(t)
	insertLease4(t, q, model.Lease4{
		Address:  binary.BigEndian.Uint32(net.ParseIP("10.20.0.99").To4()),
		Hostname: ptr("ghost" + kea.HostnameSuffix),
		Expire:   ptr(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
		SubnetID: ptr(uint32(1)),
		State:    ptr(uint32(0)),
	})
	_, err := kea.FindMAC(ctx, q, "ghost")
	if err == nil || errors.Is(err, db.ErrNotFound) {
		t.Errorf("err = %v, want a non-ErrNotFound error", err)
	}
}
