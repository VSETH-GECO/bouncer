package database

import (
	"net"
	"strings"
)

type User struct {
	Mac      string
	IP       net.IP
	IP6      net.IP
	Hostname string
	Name     string
	Email    string
	Sessions []*RadiusSession
}

// FindMAC tries to find a user's MAC by any of their fields
func (h *Handler) FindMAC(value string) (mac string, err error) {
	res, err := h.connection.Query("SELECT mac FROM login_logs WHERE LOWER(username)=LOWER(?) or LOWER(mac)=LOWER(?) ORDER BY updated_at DESC;", value, value)
	if err != nil {
		return
	}
	defer Close(res)

	if res.Next() {
		err = res.Scan(&mac)
		return
	}

	// Let's see if we can find the user by hostname or IP instead
	hostNameCondition := value + ".lan.geco.ethz.ch" + "%" // wildcard at the end because some end with a dot and some not
	res, err = h.connection.Query("SELECT HEX(hwaddr) FROM lease4 WHERE hostname LIKE ? or address=INET_ATON(?) or hwaddr=UNHEX(?);", hostNameCondition, value, value)
	if err != nil {
		return
	}
	defer Close(res)

	if res.Next() {
		err = res.Scan(&mac)
		return
	}

	// Maybe its an ipv6?
	res, err = h.connection.Query("SELECT HEX(hwaddr) FROM lease6 WHERE address=INET6_ATON(?);", value)
	if err != nil {
		return
	}
	defer Close(res)

	if res.Next() {
		err = res.Scan(&mac)
		return
	}

	// Neither MAC nor hostname nor IP - maybe username?
	res, err = h.connection.Query("SELECT mac FROM login_logs WHERE username=? ORDER BY updated_at DESC;", value)
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

	res, err := h.connection.Query("SELECT username FROM login_logs WHERE mac=? ORDER BY updated_at DESC;", mac)
	if err != nil {
		return nil, err
	}
	defer Close(res)

	if res.Next() {
		err = res.Scan(&user.Name)
		if err != nil {
			return nil, err
		}

		if strings.ContainsRune(user.Name, '@') {
			user.Email = user.Name
			user.Name = "N/A"
		} else {
			user.Email = "N/A"
		}
	}

	res, err = h.connection.Query("SELECT hostname, INET_NTOA(address) FROM lease4 WHERE hwaddr=UNHEX(?);", mac)
	if err != nil {
		return nil, err
	}
	defer Close(res)

	if res.Next() {
		var ipStr string
		err = res.Scan(&user.Hostname, &ipStr)
		if err != nil {
			return nil, err
		}
		user.IP = net.ParseIP(ipStr)
		if user.IP == nil {
			user.IP = net.IP{}
		}
	}

	res, err = h.connection.Query("SELECT INET6_NTOA(address) FROM lease6 WHERE hwaddr=UNHEX(?);", mac)
	if err != nil {
		return nil, err
	}
	defer Close(res)

	if res.Next() {
		var ip6Str string
		err = res.Scan(&ip6Str)
		if err != nil {
			return nil, err
		}
		user.IP6 = net.ParseIP(ip6Str)
		if user.IP6 == nil {
			user.IP6 = net.IP{}
		}
	}

	user.Sessions, err = h.FindSessionsForMAC(mac)
	return user, err
}
