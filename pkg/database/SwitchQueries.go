package database

import "net"

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
	PrimaryVlan *VLAN
}

// VLANs finds and loads all event VLANs for a switch
func (h *Handler) VLANs(switchID string) ([]*VLAN, error) {
	res, err := h.connection.Query("select ev.id, name, description, vlan_id, ip_range from bouncer_vlan as ev join bouncer_vlan_switch evs on ev.id = evs.event_vlan_id where evs.switch_id=?", switchID)
	defer Close(res)
	if err != nil {
		return nil, err
	}

	var results []*VLAN

	for res.Next() {
		obj := &VLAN{}
		var ipRange string
		err = res.Scan(&obj.Id, &obj.Name, &obj.Description, &obj.VlanID, &ipRange)
		if err != nil {
			return nil, err
		}

		_, obj.IpRange, err = net.ParseCIDR(ipRange)
		if err != nil {
			return nil, err
		}

		results = append(results, obj)
	}

	return results, nil
}

// IPs finds all IPs of a switch
func (h *Handler) IPs(switchID string) ([]*net.IP, error) {
	res, err := h.connection.Query("select IP from bouncer_switch_ip where switch_id=?", switchID)
	defer Close(res)
	if err != nil {
		return nil, err
	}

	var results []*net.IP

	for res.Next() {
		var ipStr string
		err = res.Scan(&ipStr)
		if err != nil {
			return nil, err
		}

		ip := net.ParseIP(ipStr)

		if ip != nil {
			results = append(results, &ip)
		}
	}

	return results, nil
}

func (h *Handler) SwitchByIP(ip string) (*Switch, error) {
	res, err := h.connection.Query("select sw.id, primary_vlan, hostname, location from bouncer_switch_ip as swip join bouncer_switch_map as sw on sw.id = swip.switch_id where ip=?", ip)
	defer Close(res)
	if err != nil {
		return nil, err
	}

	if !res.Next() {
		return nil, nil
	}

	switchObj := &Switch{}
	var primaryVlan int
	err = res.Scan(&switchObj.Id, &primaryVlan, &switchObj.Hostname, &switchObj.Location)
	if err != nil {
		return nil, err
	}

	switchObj.Vlans, err = h.VLANs(switchObj.Id)
	if err != nil {
		return nil, err
	}
	for _, vlan := range switchObj.Vlans {
		if vlan.VlanID == primaryVlan {
			switchObj.PrimaryVlan = vlan
			break
		}
	}

	switchObj.Ips, err = h.IPs(switchObj.Id)

	return switchObj, err
}
