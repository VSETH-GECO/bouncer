package freeradius

import (
	"context"
	"fmt"
	"net"
	"strconv"
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

// OpenSession is an open RADIUS session (no stop time) together with the VLAN its client is assigned to.
type OpenSession struct {
	// MAC is the client; it is zero when the session's username is not a MAC, which RawMAC then holds.
	MAC    mac.MAC
	RawMAC string
	// SessionID is the accounting session ID.
	SessionID string
	// SwitchIP is the switch (NAS) the session runs on; nil if the stored address does not parse.
	SwitchIP net.IP
	// AssignedVLAN is the client's VLAN from radreply; 0 when it has none or the stored value does not parse.
	AssignedVLAN int
}

// OpenSessions returns every open session with its client's assigned VLAN, in one query, newest start first.
func OpenSessions(ctx context.Context, q db.Querier) ([]OpenSession, error) {
	var rows []struct {
		Username      string
		Acctsessionid string
		Nasipaddress  string
		Vlan          *string
	}
	ra, rr := table.Radacct, table.Radreply
	err := mysql.SELECT(
		ra.Username.AS("username"),
		ra.Acctsessionid.AS("acctsessionid"),
		ra.Nasipaddress.AS("nasipaddress"),
		rr.Value.AS("vlan"),
	).
		FROM(ra.LEFT_JOIN(rr, rr.Username.EQ(ra.Username).AND(rr.Attribute.EQ(mysql.String(AttrVLAN))))).
		WHERE(ra.Acctstoptime.IS_NULL()).
		ORDER_BY(ra.Acctstarttime.DESC(), ra.Radacctid.DESC()).
		QueryContext(ctx, q, &rows)
	if err != nil {
		return nil, fmt.Errorf("querying open RADIUS sessions: %w", err)
	}
	sessions := make([]OpenSession, 0, len(rows))
	for _, r := range rows {
		s := OpenSession{RawMAC: r.Username, SessionID: r.Acctsessionid, SwitchIP: net.ParseIP(r.Nasipaddress)}
		if m, err := mac.Parse(r.Username); err == nil {
			s.MAC = m
		}
		if r.Vlan != nil {
			if vlan, err := strconv.Atoi(*r.Vlan); err == nil {
				s.AssignedVLAN = vlan
			}
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}
