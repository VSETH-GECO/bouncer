package freeradius

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/go-jet/jet/v2/mysql"
)

// replyAccept is the reply column of a post-auth row FreeRADIUS answered with an Access-Accept.
const replyAccept = "Access-Accept"

// Accept is a client's newest Access-Accept within a window, with what the switch was told.
type Accept struct {
	// MAC is the client; it is zero when the row's username is not a MAC, which RawMAC then holds.
	MAC    mac.MAC
	RawMAC string
	// At is when FreeRADIUS sent the accept (authdate).
	At time.Time
	// SwitchIP is the switch (NAS) that asked; never nil, as rows without a switch address are not returned.
	SwitchIP net.IP
	// SwitchPort is the switch port the client is on; nil when the row does not name one.
	SwitchPort *string
	// ReplyVLAN is the VLAN (Tunnel-Private-Group-Id) the accept told the switch; 0 when absent or unparsable.
	ReplyVLAN int
	// AssignedVLAN is the client's current VLAN from radreply; 0 when it has none or the stored value does not parse.
	AssignedVLAN int
}

// LatestAccepts returns, for every client with an Access-Accept at or after since whose row carries a switch
// address, the newest such accept, newest first, joined with the client's current radreply VLAN in the same query.
// A client whose newest such accept has a switch address that does not parse as an IP is left out.
func LatestAccepts(ctx context.Context, q db.Querier, since time.Time) ([]Accept, error) {
	var rows []struct {
		Username     string
		Authdate     time.Time
		Nasipaddress string
		Nasportid    *string
		ReplyVLAN    *string
		Assigned     *string
	}
	pa, rr := table.Radpostauth, table.Radreply
	err := mysql.SELECT(
		pa.Username.AS("username"),
		pa.Authdate.AS("authdate"),
		pa.Nasipaddress.AS("nasipaddress"),
		pa.Nasportid.AS("nasportid"),
		pa.Vlan.AS("replyVLAN"),
		rr.Value.AS("assigned"),
	).
		FROM(pa.LEFT_JOIN(rr, rr.Username.EQ(pa.Username).AND(rr.Attribute.EQ(mysql.String(AttrVLAN))))).
		WHERE(pa.Reply.EQ(mysql.String(replyAccept)).
			AND(pa.Authdate.GT_EQ(mysql.TimestampT(since))).
			AND(pa.Nasipaddress.IS_NOT_NULL())).
		ORDER_BY(pa.Authdate.DESC(), pa.ID.DESC()).
		QueryContext(ctx, q, &rows)
	if err != nil {
		return nil, fmt.Errorf("querying RADIUS accepts: %w", err)
	}

	seen := map[string]struct{}{}
	accepts := make([]Accept, 0, len(rows))
	for _, r := range rows {
		if _, dup := seen[r.Username]; dup {
			continue // rows come newest first
		}
		seen[r.Username] = struct{}{}
		ip := net.ParseIP(r.Nasipaddress)
		if ip == nil {
			continue
		}
		a := Accept{RawMAC: r.Username, At: r.Authdate, SwitchIP: ip, SwitchPort: r.Nasportid,
			ReplyVLAN: atoi(r.ReplyVLAN), AssignedVLAN: atoi(r.Assigned)}
		if m, err := mac.Parse(r.Username); err == nil {
			a.MAC = m
		}
		accepts = append(accepts, a)
	}
	return accepts, nil
}

// atoi parses a nullable VLAN column; it returns 0 when s is nil or not a number.
func atoi(s *string) int {
	if s == nil {
		return 0
	}
	v, err := strconv.Atoi(*s)
	if err != nil {
		return 0
	}
	return v
}
