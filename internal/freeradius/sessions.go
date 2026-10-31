package freeradius

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/go-jet/jet/v2/mysql"
)

// Session is one RADIUS accounting record of a client. Absent values are nil.
type Session struct {
	ID             string
	Start, Stop    *time.Time
	TerminateCause string
	SwitchIP       net.IP
	SwitchPort     *string
	ClientIP       net.IP
	BytesIn        *int64
	BytesOut       *int64
}

// SessionsForMAC returns the sessions of m, newest start first. A client without sessions yields an empty slice.
func SessionsForMAC(ctx context.Context, q db.Querier, m mac.MAC) ([]Session, error) {
	rows := []model.Radacct{}
	err := mysql.SELECT(table.Radacct.AllColumns).
		FROM(table.Radacct).
		WHERE(table.Radacct.Username.EQ(mysql.String(m.Hex()))).
		ORDER_BY(table.Radacct.Acctstarttime.DESC()).
		QueryContext(ctx, q, &rows)
	if err != nil {
		return nil, fmt.Errorf("querying RADIUS sessions: %w", err)
	}
	sessions := make([]Session, 0, len(rows))
	for _, r := range rows {
		sessions = append(sessions, Session{
			ID:             r.Acctsessionid,
			Start:          r.Acctstarttime,
			Stop:           r.Acctstoptime,
			TerminateCause: r.Acctterminatecause,
			SwitchIP:       net.ParseIP(r.Nasipaddress),
			SwitchPort:     r.Nasportid,
			ClientIP:       net.ParseIP(r.Framedipaddress),
			BytesIn:        r.Acctinputoctets,
			BytesOut:       r.Acctoutputoctets,
		})
	}
	return sessions, nil
}

// FindMAC finds the MAC of a client from its RADIUS sessions: a MAC (in any form mac.Parse accepts) that has a
// session, or an IPv4 address framed to a session, in which case the newest such session (by start time) wins. It
// returns an error wrapping db.ErrNotFound if value is neither or no session matches, and a different error if the
// matching session's username is not a MAC.
func FindMAC(ctx context.Context, q db.Querier, value string) (mac.MAC, error) {
	if m, err := mac.Parse(value); err == nil {
		var row model.Radacct
		err := mysql.SELECT(table.Radacct.Radacctid).
			FROM(table.Radacct).
			WHERE(table.Radacct.Username.EQ(mysql.String(m.Hex()))).
			LIMIT(1).
			QueryContext(ctx, q, &row)
		if err != nil {
			return mac.MAC{}, fmt.Errorf("searching RADIUS sessions of %s: %w", m.Hex(), db.NotFound(err))
		}
		return m, nil
	}

	if ip := net.ParseIP(value).To4(); ip != nil {
		var row model.Radacct
		err := mysql.SELECT(table.Radacct.Radacctid, table.Radacct.Username).
			FROM(table.Radacct).
			WHERE(table.Radacct.Framedipaddress.EQ(mysql.String(ip.String()))).
			ORDER_BY(table.Radacct.Acctstarttime.DESC()).
			LIMIT(1).
			QueryContext(ctx, q, &row)
		if err != nil {
			return mac.MAC{}, fmt.Errorf("searching RADIUS sessions framed to %s: %w", ip, db.NotFound(err))
		}
		m, err := mac.Parse(row.Username)
		if err != nil {
			return mac.MAC{}, fmt.Errorf("RADIUS session %d framed to %s has username %q, not a MAC: %w",
				row.Radacctid, ip, row.Username, err)
		}
		return m, nil
	}

	return mac.MAC{}, fmt.Errorf("no RADIUS session matches %q: %w", value, db.ErrNotFound)
}
