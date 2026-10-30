package database

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// FindSessionsForMAC looks up the RADIUS RadiusSessions for a given client
func (h *Handler) FindSessionsForMAC(ctx context.Context, mac string) ([]*RadiusSession, error) {
	rows, err := h.db.QueryContext(ctx, "select acctsessionid, acctstarttime, acctstoptime, acctterminatecause, nasipaddress, nasportid, framedipaddress, acctinputoctets, acctoutputoctets from radacct where username=? order by acctstarttime desc;", mac)
	if err != nil {
		return nil, fmt.Errorf("querying RADIUS sessions: %w", err)
	}
	defer rows.Close()

	var result []*RadiusSession
	for rows.Next() {
		obj := &RadiusSession{}
		var clientIpStr string
		var switchIpStr string
		err = rows.Scan(&obj.Uid, &obj.StartDate, &obj.EndDate, &obj.EndReason, &switchIpStr, &obj.SwitchPort, &clientIpStr, &obj.BytesReceived, &obj.BytesSent)
		if err != nil {
			return nil, fmt.Errorf("scanning RADIUS session: %w", err)
		}
		obj.ClientIP = net.ParseIP(clientIpStr)
		obj.SwitchIP = net.ParseIP(switchIpStr)

		if obj.ClientIP == nil {
			obj.ClientIP = net.IP{}
		}
		if obj.SwitchIP == nil {
			obj.SwitchIP = net.IP{}
		}

		result = append(result, obj)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating RADIUS sessions: %w", err)
	}

	return result, nil
}

// AddNewUser signs in a user (identified by MAC) into the target VLAN
func (h *Handler) AddNewUser(ctx context.Context, q DBTX, clientMac string, targetVLAN int) error {
	opsProcessedNewUser.Inc()
	_, err := q.ExecContext(ctx, "INSERT INTO radcheck(username, attribute, op, value) VALUES(?, 'Cleartext-Password', ':=', ?)", clientMac, clientMac)
	if err != nil {
		return fmt.Errorf("inserting radcheck password: %w", err)
	}
	_, err = q.ExecContext(ctx, "INSERT INTO radreply(username, attribute, op, value) VALUES(?, 'Tunnel-Private-Group-ID', ':=', ?)", clientMac, strconv.Itoa(targetVLAN))
	if err != nil {
		return fmt.Errorf("inserting radreply VLAN: %w", err)
	}
	_, err = q.ExecContext(ctx, "INSERT INTO radreply(username, attribute, op, value) VALUES(?, 'Tunnel-Medium-Type', ':=', 'IEEE-802')", clientMac)
	if err != nil {
		return fmt.Errorf("inserting radreply tunnel medium type: %w", err)
	}
	_, err = q.ExecContext(ctx, "INSERT INTO radreply(username, attribute, op, value) VALUES(?, 'Tunnel-Type', ':=', 'VLAN')", clientMac)
	if err != nil {
		return fmt.Errorf("inserting radreply tunnel type: %w", err)
	}
	return nil
}

// CheckUserSignedIn returns the VLAN the given client MAC is currently assigned to. It returns an error wrapping
// ErrNotFound if the client has never been signed in.
func (h *Handler) CheckUserSignedIn(ctx context.Context, q DBTX, clientMac string) (int, error) {
	var oldVlan string
	err := q.QueryRowContext(ctx, "SELECT value FROM radreply WHERE attribute = 'Tunnel-Private-Group-ID' AND username = ?", strings.ToLower(clientMac)).Scan(&oldVlan)
	if err != nil {
		return 0, fmt.Errorf("looking up assigned VLAN: %w", noRows(err))
	}

	val, err := strconv.Atoi(oldVlan)
	if err != nil {
		return 0, fmt.Errorf("parsing assigned VLAN: %w", err)
	}

	return val, nil
}

// UpdateUser changes the VLAN setting for a given user
func (h *Handler) UpdateUser(ctx context.Context, q DBTX, clientMac string, targetVLAN int) error {
	opsProcessedNewUser.Inc()
	_, err := q.ExecContext(ctx, "UPDATE radreply SET value = ? WHERE attribute = 'Tunnel-Private-Group-ID' AND username = ?", strconv.Itoa(targetVLAN), strings.ToLower(clientMac))
	if err != nil {
		return fmt.Errorf("updating radreply VLAN: %w", err)
	}
	return nil
}
