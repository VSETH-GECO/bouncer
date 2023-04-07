package database

import (
	"net"
	"strings"
)

type User struct {
	Mac      string
	IP       net.IP
	Hostname string
	Name     string
	Email    string
	Sessions []*RadiusSession
}

// FindMAC tries to find a user's MAC by any of their fields
func (h *Handler) FindMAC(value string) (mac string, err error) {
	res, err := h.connection.Query("SELECT mac FROM login_logs WHERE LOWER(username)=LOWER(?) or LOWER(mac)=LOWER(?);", value, value)
	if err != nil {
		return
	}
	defer Close(res)

	if res.Next() {
		err = res.Scan(&mac)
		return
	}

	// Let's see if we can find the user by hostname or IP instead
	res, err = h.connection.Query("SELECT HEX(hwaddr) FROM lease4 WHERE hostname = ? or address=INET_ATON(?) or hwaddr=UNHEX(?);", value, value, value)
	if err != nil {
		return
	}
	defer Close(res)

	if res.Next() {
		err = res.Scan(&mac)
		return
	}

	// Neither MAC nor hostname nor IP - maybe username?
	res, err = h.connection.Query("SELECT mac FROM login_logs WHERE username=?", value)
	if err != nil {
		return
	}
	defer Close(res)

	if res.Next() {
		err = res.Scan(&mac)
	}

	return
}

// LoadUser returns what we have about a user
func (h *Handler) LoadUser(mac string) (*User, error) {
	user := &User{
		Mac: mac,
	}

	res, err := h.connection.Query("SELECT username FROM login_logs WHERE mac=?", mac)
	if err != nil {
		Close(res)
		return nil, err
	}

	if res.Next() {
		err = res.Scan(&user.Name)
		if err != nil {
			Close(res)
			return nil, err
		}

		if strings.ContainsRune(user.Name, '@') {
			user.Email = user.Name
			user.Name = "N/A"
		} else {
			user.Email = "N/A"
		}
	}
	Close(res)

	res, err = h.connection.Query("SELECT hostname, INET_NTOA(address) FROM lease4 WHERE hwaddr=UNHEX(?)", mac)
	if err != nil {
		Close(res)
		return nil, err
	}

	if res.Next() {
		var ipStr string
		err = res.Scan(&user.Hostname, &ipStr)
		if err != nil {
			Close(res)
			return nil, err
		}
		user.IP = net.ParseIP(ipStr)
		if user.IP == nil {
			user.IP = net.IP{}
		}
	}

	user.Sessions, err = h.FindSessionsForMAC(mac)
	Close(res)
	return user, err
}
