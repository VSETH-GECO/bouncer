// Package kea reads and deletes the Kea DHCP lease tables lease4 and lease6.
package kea

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/go-jet/jet/v2/mysql"
)

// Lease is an IPv4 lease of a client.
type Lease struct {
	MAC      mac.MAC
	IP       net.IP
	Hostname *string
	Expire   *time.Time
}

func ipv4FromUint32(a uint32) net.IP {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], a)
	return net.IPv4(b[0], b[1], b[2], b[3])
}

// LatestLease4 returns the IPv4 lease of m that expires last. It returns an error wrapping db.ErrNotFound if m has
// no lease.
func LatestLease4(ctx context.Context, q db.Querier, m mac.MAC) (Lease, error) {
	var row model.Lease4
	err := mysql.SELECT(table.Lease4.Address, table.Lease4.Hostname, table.Lease4.Expire).
		FROM(table.Lease4).
		WHERE(table.Lease4.Hwaddr.EQ(mysql.Blob(m.Bytes()))).
		ORDER_BY(table.Lease4.Expire.DESC()).
		LIMIT(1).
		QueryContext(ctx, q, &row)
	if err != nil {
		return Lease{}, fmt.Errorf("querying lease4 of %s: %w", m, db.NotFound(err))
	}
	return Lease{MAC: m, IP: ipv4FromUint32(row.Address), Hostname: row.Hostname, Expire: row.Expire}, nil
}

// Lease6Address returns the IPv6 address leased to m. It returns an error wrapping db.ErrNotFound if m has no
// lease.
func Lease6Address(ctx context.Context, q db.Querier, m mac.MAC) (net.IP, error) {
	var row model.Lease6
	err := mysql.SELECT(table.Lease6.Address).
		FROM(table.Lease6).
		WHERE(table.Lease6.Hwaddr.EQ(mysql.Blob(m.Bytes()))).
		ORDER_BY(table.Lease6.Expire.DESC()).
		LIMIT(1).
		QueryContext(ctx, q, &row)
	if err != nil {
		return nil, fmt.Errorf("querying lease6 of %s: %w", m, db.NotFound(err))
	}
	return net.IP(row.Address), nil
}

// DeleteLeases deletes all IPv4 and IPv6 leases of m. A MAC without leases is not an error.
func DeleteLeases(ctx context.Context, q db.Querier, m mac.MAC) error {
	hw := mysql.Blob(m.Bytes())
	if _, err := table.Lease4.DELETE().WHERE(table.Lease4.Hwaddr.EQ(hw)).ExecContext(ctx, q); err != nil {
		return fmt.Errorf("deleting lease4 of %s: %w", m, err)
	}
	if _, err := table.Lease6.DELETE().WHERE(table.Lease6.Hwaddr.EQ(hw)).ExecContext(ctx, q); err != nil {
		return fmt.Errorf("deleting lease6 of %s: %w", m, err)
	}
	return nil
}
