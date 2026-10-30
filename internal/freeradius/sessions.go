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
