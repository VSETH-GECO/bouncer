package switches_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/switches"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/go-jet/jet/v2/mysql"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func begin(t *testing.T) (context.Context, *sql.Tx) {
	return context.Background(), testdb.Tx(t)
}

func exec(t *testing.T, q db.Querier, what string, stmt mysql.Statement) {
	t.Helper()
	if _, err := stmt.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("seeding %s: %v", what, err)
	}
}

func seedSwitch(t *testing.T, q db.Querier, id, primaryVlan int32, host string, loc *string, ips ...string) {
	t.Helper()
	exec(t, q, "switch map", table.BouncerSwitchMap.INSERT(table.BouncerSwitchMap.AllColumns).
		MODEL(model.BouncerSwitchMap{ID: id, PrimaryVlan: primaryVlan, Hostname: host, Location: loc}))
	for i, ip := range ips {
		exec(t, q, "switch ip", table.BouncerSwitchIP.INSERT(table.BouncerSwitchIP.AllColumns).
			MODEL(model.BouncerSwitchIP{ID: id*10 + int32(i), SwitchID: id, IP: ip}))
	}
}

func seedVlan(t *testing.T, q db.Querier, id, vlanID int32, ipRange string, switchID int32) {
	t.Helper()
	exec(t, q, "vlan", table.BouncerVlan.INSERT(table.BouncerVlan.AllColumns).
		MODEL(model.BouncerVlan{ID: id, Name: "vlan-test", Description: "desc-test", VlanID: vlanID, IPRange: ipRange}))
	exec(t, q, "vlan switch", table.BouncerVlanSwitch.INSERT(table.BouncerVlanSwitch.AllColumns).
		MODEL(model.BouncerVlanSwitch{ID: id, EventVlanID: id, SwitchID: switchID}))
}

func TestByIP_FullSwitch(t *testing.T) {
	ctx, q := begin(t)
	loc := "room-1"
	seedSwitch(t, q, 101, 101, "sw-test-1", &loc, "10.10.0.1", "10.10.0.2")
	seedVlan(t, q, 101, 101, "10.201.0.0/16", 101)
	seedVlan(t, q, 102, 102, "10.202.0.0/16", 101)

	for _, ip := range []string{"10.10.0.1", "10.10.0.2"} {
		got, err := switches.ByIP(ctx, q, net.ParseIP(ip))
		if err != nil {
			t.Fatalf("ByIP(%s): %v", ip, err)
		}
		if got.ID != 101 || got.Hostname != "sw-test-1" {
			t.Errorf("ID/Hostname = %d/%q", got.ID, got.Hostname)
		}
		if got.Location == nil || *got.Location != "room-1" {
			t.Errorf("Location = %v", got.Location)
		}
		if len(got.IPs) != 2 {
			t.Fatalf("IPs = %v, want 2", got.IPs)
		}
		if !got.IPs[0].Equal(net.ParseIP("10.10.0.1")) || !got.IPs[1].Equal(net.ParseIP("10.10.0.2")) {
			t.Errorf("IPs = %v", got.IPs)
		}
		if len(got.VLANs) != 2 {
			t.Fatalf("VLANs = %v, want 2", got.VLANs)
		}
		for _, v := range got.VLANs {
			want := map[int]string{101: "10.201.0.0/16", 102: "10.202.0.0/16"}[v.VLANID]
			if v.IPRange == nil || v.IPRange.String() != want {
				t.Errorf("VLAN %d IPRange = %v, want %s", v.VLANID, v.IPRange, want)
			}
			if v.Name != "vlan-test" || v.Description != "desc-test" {
				t.Errorf("VLAN %d name/description = %q/%q", v.VLANID, v.Name, v.Description)
			}
		}
		if got.PrimaryVLAN == nil || got.PrimaryVLAN.VLANID != 101 {
			t.Errorf("PrimaryVLAN = %+v, want VLANID 101", got.PrimaryVLAN)
		}
	}
}

func TestByIP_SwitchWithoutVLANs(t *testing.T) {
	ctx, q := begin(t)
	seedSwitch(t, q, 111, 111, "sw-test-2", nil, "10.10.0.11")

	got, err := switches.ByIP(ctx, q, net.ParseIP("10.10.0.11"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "sw-test-2" || got.Location != nil {
		t.Errorf("Hostname/Location = %q/%v", got.Hostname, got.Location)
	}
	if len(got.IPs) != 1 {
		t.Errorf("IPs = %v, want 1", got.IPs)
	}
	if len(got.VLANs) != 0 || got.PrimaryVLAN != nil {
		t.Errorf("VLANs = %v, PrimaryVLAN = %v, want none", got.VLANs, got.PrimaryVLAN)
	}
}

func TestByIP_PrimaryVLANNotMapped(t *testing.T) {
	ctx, q := begin(t)
	seedSwitch(t, q, 121, 199, "sw-test-3", nil, "10.10.0.21")
	seedVlan(t, q, 121, 121, "10.203.0.0/16", 121)

	got, err := switches.ByIP(ctx, q, net.ParseIP("10.10.0.21"))
	if err != nil {
		t.Fatal(err)
	}
	if got.PrimaryVLAN != nil {
		t.Errorf("PrimaryVLAN = %+v, want nil", got.PrimaryVLAN)
	}
	if len(got.VLANs) != 1 || got.VLANs[0].VLANID != 121 {
		t.Errorf("VLANs = %+v", got.VLANs)
	}
}

func TestByIP_Unknown(t *testing.T) {
	ctx, q := begin(t)
	_, err := switches.ByIP(ctx, q, net.ParseIP("10.10.0.99"))
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestByIP_NilIP(t *testing.T) {
	ctx, q := begin(t)
	_, err := switches.ByIP(ctx, q, nil)
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("ByIP(nil) err = %v, want ErrNotFound", err)
	}
}

func TestByIP_BadIPRangeIsError(t *testing.T) {
	ctx, q := begin(t)
	seedSwitch(t, q, 131, 131, "sw-test-4", nil, "10.10.0.31")
	seedVlan(t, q, 131, 131, "nonsense", 131)

	_, err := switches.ByIP(ctx, q, net.ParseIP("10.10.0.31"))
	if err == nil || errors.Is(err, db.ErrNotFound) {
		t.Errorf("err = %v, want a parse error", err)
	}
}
