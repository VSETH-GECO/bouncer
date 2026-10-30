package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// FindMAC tries to find a user's MAC by any of their fields. It returns an error wrapping ErrNotFound if no source
// knows the value.
func (h *Handler) FindMAC(ctx context.Context, value string) (string, error) {
	var mac string

	// find by username or MAC
	err := h.db.QueryRowContext(ctx, "SELECT mac FROM login_logs WHERE LOWER(username)=LOWER(?) or LOWER(mac)=LOWER(?) ORDER BY updated_at DESC;", value, value).Scan(&mac)
	if err == nil {
		return mac, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("finding MAC in login logs: %w", err)
	}

	// Let's see if we can find the user by hostname or IP instead
	hostNameCondition := value + ".lan.geco.ethz.ch" + "%" // wildcard at the end because some end with a dot and some not
	err = h.db.QueryRowContext(ctx, "SELECT HEX(hwaddr) FROM lease4 WHERE hostname LIKE ? or address=INET_ATON(?) or hwaddr=UNHEX(?);", hostNameCondition, value, value).Scan(&mac)
	if err == nil {
		return mac, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("finding MAC in lease4: %w", err)
	}

	// Maybe its an ipv6?
	err = h.db.QueryRowContext(ctx, "SELECT HEX(hwaddr) FROM lease6 WHERE address=INET6_ATON(?);", value).Scan(&mac)
	if err == nil {
		return mac, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("finding MAC in lease6: %w", err)
	}

	return "", fmt.Errorf("finding MAC for %q: %w", value, ErrNotFound)
}

// LoadUser returns what we have about a user
func (h *Handler) LoadUser(ctx context.Context, mac string) (*User, error) {
	user := &User{
		Mac: mac,
	}

	err := h.db.QueryRowContext(ctx, "SELECT username FROM login_logs WHERE mac=? ORDER BY updated_at DESC;", mac).Scan(&user.Name)
	switch {
	case err == nil:
		if strings.ContainsRune(user.Name, '@') {
			user.Email = user.Name
			user.Name = "N/A"
		} else {
			user.Email = "N/A"
		}
	case errors.Is(err, sql.ErrNoRows):
		// no login yet
	default:
		return nil, fmt.Errorf("loading username: %w", err)
	}

	// hostname is nullable in Kea's schema
	var hostname sql.NullString
	var ipStr string
	err = h.db.QueryRowContext(ctx, "SELECT hostname, INET_NTOA(address) FROM lease4 WHERE hwaddr=UNHEX(?);", mac).Scan(&hostname, &ipStr)
	switch {
	case err == nil:
		user.Hostname = hostname.String
		user.IP = net.ParseIP(ipStr)
		if user.IP == nil {
			user.IP = net.IP{}
		}
	case errors.Is(err, sql.ErrNoRows):
		// no DHCPv4 lease
	default:
		return nil, fmt.Errorf("loading DHCPv4 lease: %w", err)
	}

	var ip6Str string
	err = h.db.QueryRowContext(ctx, "SELECT INET6_NTOA(address) FROM lease6 WHERE hwaddr=UNHEX(?);", mac).Scan(&ip6Str)
	switch {
	case err == nil:
		user.IP6 = net.ParseIP(ip6Str)
		if user.IP6 == nil {
			user.IP6 = net.IP{}
		}
	case errors.Is(err, sql.ErrNoRows):
		// no DHCPv6 lease
	default:
		return nil, fmt.Errorf("loading DHCPv6 lease: %w", err)
	}

	user.Sessions, err = h.FindSessionsForMAC(ctx, mac)
	if err != nil {
		return nil, err
	}
	return user, nil
}
