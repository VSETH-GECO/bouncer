package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
)

// FindMACByIP finds a client's MAC from it's DHCP lease. It returns an error wrapping ErrNotFound if there is no
// lease for the IP.
func (h *Handler) FindMACByIP(ctx context.Context, ip string) (string, error) {
	var mac string
	err := h.db.QueryRowContext(ctx, "SELECT hex(hwaddr) FROM lease4 WHERE INET_NTOA(address)=?;", ip).Scan(&mac)
	if err != nil {
		return "", fmt.Errorf("finding MAC for %s in lease4: %w", ip, noRows(err))
	}
	return mac, nil
}

// FindMACByIPv6 finds a client's MAC from it's DHCPv6 lease. It returns an error wrapping ErrNotFound if there is no
// lease for the IP.
func (h *Handler) FindMACByIPv6(ctx context.Context, ip string) (string, error) {
	var mac string
	err := h.db.QueryRowContext(ctx, "SELECT hex(hwaddr) FROM lease6 WHERE INET6_NTOA(address)=?;", ip).Scan(&mac)
	if err != nil {
		return "", fmt.Errorf("finding MAC for %s in lease6: %w", ip, noRows(err))
	}
	return mac, nil
}

// ClearLeasesForMAC deletes all DHCP leases of the given client and returns the hostname and IP of its most recent
// DHCPv4 lease (empty/nil if there was none).
func (h *Handler) ClearLeasesForMAC(ctx context.Context, q DBTX, mac string) (string, net.IP, error) {
	// hostname is nullable in Kea's schema
	var hostname sql.NullString
	var ipStr string
	var ip net.IP
	err := q.QueryRowContext(ctx, "SELECT hostname, INET_NTOA(address) FROM lease4 WHERE hwaddr = UNHEX(?) ORDER BY expire DESC limit 1;", mac).Scan(&hostname, &ipStr)
	switch {
	case err == nil:
		ip = net.ParseIP(ipStr)
	case errors.Is(err, sql.ErrNoRows):
		// no lease to report
	default:
		return "", nil, fmt.Errorf("looking up latest DHCPv4 lease: %w", err)
	}

	_, err = q.ExecContext(ctx, "DELETE FROM lease4 WHERE hwaddr = UNHEX(?);", mac)
	if err != nil {
		return "", nil, fmt.Errorf("deleting DHCPv4 leases: %w", err)
	}

	_, err = q.ExecContext(ctx, "DELETE FROM lease6 WHERE hwaddr = UNHEX(?);", mac)
	if err != nil {
		return "", nil, fmt.Errorf("deleting DHCPv6 leases: %w", err)
	}
	return hostname.String, ip, nil
}
