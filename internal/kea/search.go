package kea

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/go-jet/jet/v2/mysql"
)

// HostnameSuffix is the domain Kea appends to the hostnames of its clients.
const HostnameSuffix = ".lan.geco.ethz.ch"

// FindMAC finds the MAC of a client from a hostname, an IPv4 or IPv6 address or a MAC, using the leases. It returns
// an error wrapping db.ErrNotFound if no lease matches.
func FindMAC(ctx context.Context, q db.Querier, value string) (mac.MAC, error) {
	cond := table.Lease4.Hostname.LIKE(mysql.String(value + HostnameSuffix + "%"))
	ip := net.ParseIP(value)
	if v4 := ip.To4(); v4 != nil {
		cond = cond.OR(table.Lease4.Address.EQ(mysql.Uint32(binary.BigEndian.Uint32(v4))))
	}
	if m, err := mac.Parse(value); err == nil {
		cond = cond.OR(table.Lease4.Hwaddr.EQ(mysql.Blob(m.Bytes())))
	}

	var row4 model.Lease4
	err := mysql.SELECT(table.Lease4.Address, table.Lease4.Hostname, table.Lease4.Hwaddr).
		FROM(table.Lease4).
		WHERE(cond).
		ORDER_BY(table.Lease4.Expire.DESC()).
		LIMIT(1).
		QueryContext(ctx, q, &row4)
	if err == nil {
		return fromHwaddr(row4.Hwaddr, fmt.Sprintf("lease4 %s", ipv4FromUint32(row4.Address)))
	}
	if nf := db.NotFound(err); nf == err {
		return mac.MAC{}, fmt.Errorf("searching lease4 for %q: %w", value, err)
	}

	if ip != nil && ip.To4() == nil {
		var row6 model.Lease6
		err = mysql.SELECT(table.Lease6.Address, table.Lease6.Hwaddr).
			FROM(table.Lease6).
			WHERE(table.Lease6.Address.EQ(mysql.Blob(ip.To16()))).
			ORDER_BY(table.Lease6.Expire.DESC()).
			LIMIT(1).
			QueryContext(ctx, q, &row6)
		if err == nil {
			return fromHwaddr(row6.Hwaddr, fmt.Sprintf("lease6 %s", net.IP(row6.Address)))
		}
		if nf := db.NotFound(err); nf == err {
			return mac.MAC{}, fmt.Errorf("searching lease6 for %q: %w", value, err)
		}
	}
	return mac.MAC{}, fmt.Errorf("no lease matches %q: %w", value, db.ErrNotFound)
}

func fromHwaddr(hw *[]byte, what string) (mac.MAC, error) {
	if hw != nil {
		if m, ok := mac.FromBytes(*hw); ok {
			return m, nil
		}
	}
	return mac.MAC{}, fmt.Errorf("%s has no usable hwaddr", what)
}
