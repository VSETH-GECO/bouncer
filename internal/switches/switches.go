// Package switches reads bouncer's switch mapping tables: switches, their management IPs and their event VLANs.
package switches

import (
	"context"
	"fmt"
	"net"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/go-jet/jet/v2/mysql"
)

// VLAN is an event VLAN mapped to a switch.
type VLAN struct {
	ID          int
	Name        string
	Description string
	VLANID      int
	IPRange     *net.IPNet
}

// Switch is a switch with its management IPs and event VLANs.
type Switch struct {
	ID          int
	Hostname    string
	Location    *string
	IPs         []net.IP
	VLANs       []VLAN
	PrimaryVLAN *VLAN // nil if the primary VLAN is not among VLANs
}

// ByIP loads the switch that has ip as one of its management IPs. It returns an error wrapping db.ErrNotFound if ip
// belongs to no known switch.
func ByIP(ctx context.Context, q db.Querier, ip net.IP) (Switch, error) {
	var ref model.BouncerSwitchIP
	err := mysql.SELECT(table.BouncerSwitchIP.SwitchID).
		FROM(table.BouncerSwitchIP).
		WHERE(table.BouncerSwitchIP.IP.EQ(mysql.String(ip.String()))).
		LIMIT(1).
		QueryContext(ctx, q, &ref)
	if err != nil {
		return Switch{}, fmt.Errorf("looking up switch %s: %w", ip, db.NotFound(err))
	}

	sm, si, vs, v := table.BouncerSwitchMap, table.BouncerSwitchIP, table.BouncerVlanSwitch, table.BouncerVlan
	var dest struct {
		model.BouncerSwitchMap
		IPs   []model.BouncerSwitchIP `alias:"bouncer_switch_ip"`
		VLANs []model.BouncerVlan     `alias:"bouncer_vlan"`
	}
	err = mysql.SELECT(sm.AllColumns, si.AllColumns, v.AllColumns).
		FROM(sm.
			LEFT_JOIN(si, si.SwitchID.EQ(sm.ID)).
			LEFT_JOIN(vs, vs.SwitchID.EQ(sm.ID)).
			LEFT_JOIN(v, v.ID.EQ(vs.EventVlanID))).
		WHERE(sm.ID.EQ(mysql.Int32(ref.SwitchID))).
		ORDER_BY(si.ID.ASC(), v.VlanID.ASC()).
		QueryContext(ctx, q, &dest)
	if err != nil {
		return Switch{}, fmt.Errorf("loading switch %d: %w", ref.SwitchID, db.NotFound(err))
	}

	out := Switch{ID: int(dest.ID), Hostname: dest.Hostname, Location: dest.Location}
	for _, row := range dest.IPs {
		if parsed := net.ParseIP(row.IP); parsed != nil {
			out.IPs = append(out.IPs, parsed)
		}
	}
	for _, row := range dest.VLANs {
		_, ipRange, err := net.ParseCIDR(row.IPRange)
		if err != nil {
			return Switch{}, fmt.Errorf("parsing IP range of VLAN %d: %w", row.VlanID, err)
		}
		out.VLANs = append(out.VLANs, VLAN{
			ID: int(row.ID), Name: row.Name, Description: row.Description, VLANID: int(row.VlanID), IPRange: ipRange,
		})
	}
	for i := range out.VLANs {
		if out.VLANs[i].VLANID == int(dest.PrimaryVlan) {
			out.PrimaryVLAN = &out.VLANs[i]
			break
		}
	}
	return out, nil
}
