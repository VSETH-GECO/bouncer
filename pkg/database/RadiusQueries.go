package database

import (
	"database/sql"
	"net"
	"strconv"
	"strings"
)

// FindSessionsForMAC looks up the RADIUS RadiusSessions for a given client
func (h *Handler) FindSessionsForMAC(mac string) ([]*RadiusSession, error) {
	rows, err := h.connection.Query("select acctsessionid, acctstarttime, acctstoptime, acctterminatecause, nasipaddress, nasportid, framedipaddress, acctinputoctets, acctoutputoctets from radacct where username=? order by acctstarttime desc;", mac)
	defer Close(rows)
	if err != nil {
		return nil, err
	}

	var result []*RadiusSession
	for rows.Next() {
		obj := &RadiusSession{}
		var clientIpStr string
		var switchIpStr string
		err = rows.Scan(&obj.Uid, &obj.StartDate, &obj.EndDate, &obj.EndReason, &switchIpStr, &obj.SwitchPort, &clientIpStr, &obj.BytesReceived, &obj.BytesSent)
		if err != nil {
			return nil, err
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

	return result, nil
}

// AddNewUser signs in a user (identified by MAC) into the target VLAN
func (h *Handler) AddNewUser(tx *sql.Tx, clientMac string, targetVLAN int) error {
	opsProcessedNewUser.Inc()
	_, err := tx.Exec("INSERT INTO radcheck(username, attribute, op, value) VALUES(?, 'Cleartext-Password', ':=', ?)", clientMac, clientMac)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO radreply(username, attribute, op, value) VALUES(?, 'Tunnel-Private-Group-ID', ':=', ?)", clientMac, strconv.Itoa(targetVLAN))
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO radreply(username, attribute, op, value) VALUES(?, 'Tunnel-Medium-Type', ':=', 'IEEE-802')", clientMac)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO radreply(username, attribute, op, value) VALUES(?, 'Tunnel-Type', ':=', 'VLAN')", clientMac)
	return err
}

// CheckUserSignedIn finds out if the given client Mac has logged in previously
func (h *Handler) CheckUserSignedIn(tx *sql.Tx, clientMac string) (int, error) {
	res, err := tx.Query("SELECT value FROM radreply WHERE attribute = 'Tunnel-Private-Group-ID' AND username = ?", strings.ToLower(clientMac))
	if err != nil {
		return -1, err
	}
	defer Close(res)

	if !res.Next() {
		return -1, nil
	}

	var oldVlan string
	err = res.Scan(&oldVlan)
	if err != nil {
		return -1, err
	}

	val, err := strconv.Atoi(oldVlan)
	if err != nil {
		return -1, err
	}

	return val, nil
}

// CheckUserSignedInNoTx does the same as CheckUserSignedIn, but without running in a TX
func (h *Handler) CheckUserSignedInNoTx(clientMac string) (int, error) {
	tx, err := h.connection.Begin()
	if err != nil {
		return -1, err
	}
	defer Rollback(tx)

	return h.CheckUserSignedIn(tx, clientMac)
}

// UpdateUser changes the VLAN setting for a given user
func (h *Handler) UpdateUser(tx *sql.Tx, clientMac string, targetVLAN int) error {
	opsProcessedNewUser.Inc()
	_, err := tx.Exec("UPDATE radreply SET value = ? WHERE attribute = 'Tunnel-Private-Group-ID' AND username = ?", strconv.Itoa(targetVLAN), strings.ToLower(clientMac))
	return err
}
