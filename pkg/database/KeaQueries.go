package database

import (
	"database/sql"
	"net"
)

// FindMACByIP finds a client's MAC from it's DHCP lease
func (h *Handler) FindMACByIP(ip string) (mac string, err error) {
	rows, err := h.connection.Query("SELECT hex(hwaddr) FROM lease4 WHERE INET_NTOA(address)=?;", ip)
	defer Close(rows)
	if err != nil {
		return
	}
	if !rows.Next() {
		return
	}
	err = rows.Scan(&mac)
	return
}

// FindMACByIPv6 finds a client's MAC from it's DHCPv6 lease
func (h *Handler) FindMACByIPv6(ip string) (mac string, err error) {
	rows, err := h.connection.Query("SELECT hex(hwaddr) FROM lease6 WHERE INET6_NTOA(address)=?;", ip)
	defer Close(rows)
	if err != nil {
		return
	}
	if !rows.Next() {
		return
	}
	err = rows.Scan(&mac)
	return
}

func (h *Handler) ClearLeasesForMAC(tx *sql.Tx, mac string) (string, *net.IP, error) {
	res, err := tx.Query("SELECT hostname, INET_NTOA(address) FROM lease4 WHERE hwaddr = UNHEX(?) ORDER BY expire DESC limit 1;", mac)
	if err != nil {
		Close(res)
		return "", nil, err
	}
	// Care: close is not deferred!

	ip := net.IP{}
	var hostname string
	if res.Next() {
		var ipStr string
		err = res.Scan(&hostname, &ipStr)
		if err != nil {
			Close(res)
			return "", nil, err
		}
		ip = net.ParseIP(ipStr)
	}
	Close(res)

	_, err = tx.Exec("DELETE FROM lease4 WHERE hwaddr = UNHEX(?);", mac)
	if err != nil {
		return hostname, &ip, err
	}

	_, err = tx.Exec("DELETE FROM lease6 WHERE hwaddr = UNHEX(?);", mac)
	return hostname, &ip, err
}
