package database

import (
	"context"
	"fmt"
	"net"
)

type VLAN struct {
	Id          string
	Name        string
	Description string
	VlanID      int
	IpRange     *net.IPNet
}

type Switch struct {
	Id          string
	Hostname    string
	Location    string
	Ips         []*net.IP
	Vlans       []*VLAN
	PrimaryVlan *VLAN // nil if the primary VLAN is not among Vlans
}

// VLANs finds and loads all event VLANs for a switch
func (h *Handler) VLANs(ctx context.Context, switchID string) ([]*VLAN, error) {
	rows, err := h.db.QueryContext(ctx, "select ev.id, name, description, vlan_id, ip_range from bouncer_vlan as ev join bouncer_vlan_switch evs on ev.id = evs.event_vlan_id where evs.switch_id=?", switchID)
	if err != nil {
		return nil, fmt.Errorf("querying VLANs of switch %s: %w", switchID, err)
	}
	defer rows.Close()

	var results []*VLAN

	for rows.Next() {
		obj := &VLAN{}
		var ipRange string
		err = rows.Scan(&obj.Id, &obj.Name, &obj.Description, &obj.VlanID, &ipRange)
		if err != nil {
			return nil, fmt.Errorf("scanning VLAN of switch %s: %w", switchID, err)
		}

		_, obj.IpRange, err = net.ParseCIDR(ipRange)
		if err != nil {
			return nil, fmt.Errorf("parsing IP range of VLAN %d: %w", obj.VlanID, err)
		}

		results = append(results, obj)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating VLANs of switch %s: %w", switchID, err)
	}

	return results, nil
}

// IPs finds all IPs of a switch
func (h *Handler) IPs(ctx context.Context, switchID string) ([]*net.IP, error) {
	rows, err := h.db.QueryContext(ctx, "select IP from bouncer_switch_ip where switch_id=?", switchID)
	if err != nil {
		return nil, fmt.Errorf("querying IPs of switch %s: %w", switchID, err)
	}
	defer rows.Close()

	var results []*net.IP

	for rows.Next() {
		var ipStr string
		err = rows.Scan(&ipStr)
		if err != nil {
			return nil, fmt.Errorf("scanning IP of switch %s: %w", switchID, err)
		}

		ip := net.ParseIP(ipStr)

		if ip != nil {
			results = append(results, &ip)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating IPs of switch %s: %w", switchID, err)
	}

	return results, nil
}

// SwitchByIP loads the switch with the given management IP. It returns an error wrapping ErrNotFound if the IP
// belongs to no known switch.
func (h *Handler) SwitchByIP(ctx context.Context, ip string) (*Switch, error) {
	switchObj := &Switch{}
	var primaryVlan int
	err := h.db.QueryRowContext(ctx, "select sw.id, primary_vlan, hostname, location from bouncer_switch_ip as swip join bouncer_switch_map as sw on sw.id = swip.switch_id where ip=?", ip).
		Scan(&switchObj.Id, &primaryVlan, &switchObj.Hostname, &switchObj.Location)
	if err != nil {
		return nil, fmt.Errorf("looking up switch %s: %w", ip, noRows(err))
	}

	switchObj.Vlans, err = h.VLANs(ctx, switchObj.Id)
	if err != nil {
		return nil, err
	}
	for _, vlan := range switchObj.Vlans {
		if vlan.VlanID == primaryVlan {
			switchObj.PrimaryVlan = vlan
			break
		}
	}

	switchObj.Ips, err = h.IPs(ctx, switchObj.Id)
	if err != nil {
		return nil, err
	}

	return switchObj, nil
}
