package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/kea"
	"github.com/VSETH-GECO/bouncer/internal/logins"
	"github.com/VSETH-GECO/bouncer/internal/mac"
)

// User is everything known about a client, assembled from the login log, the DHCP leases and the RADIUS sessions.
// Absent values are nil (or empty for Name and Email).
type User struct {
	MAC      mac.MAC
	IP, IP6  net.IP
	Hostname *string
	Name     string
	Email    string
	Sessions []freeradius.Session
}

// derefOr returns *p, or missing if p is nil or points to an empty string
func derefOr(p *string, missing string) string {
	if p == nil || *p == "" {
		return missing
	}
	return *p
}

// findMAC looks up a client's MAC by username or MAC in the login log, falling back to the DHCP leases (hostname, IP
// or MAC). It returns an error wrapping db.ErrNotFound if no source knows the value.
func findMAC(ctx context.Context, q db.Querier, search string) (mac.MAC, error) {
	m, err := logins.FindMAC(ctx, q, search)
	if errors.Is(err, db.ErrNotFound) {
		return kea.FindMAC(ctx, q, search)
	}
	return m, err
}

// loadUser assembles everything known about the client with the given MAC
func loadUser(ctx context.Context, q db.Querier, m mac.MAC) (*User, error) {
	user := &User{MAC: m}

	name, err := logins.LatestUsername(ctx, q, m)
	switch {
	case err == nil:
		user.Name = name
		if strings.ContainsRune(user.Name, '@') {
			user.Email = user.Name
			user.Name = "N/A"
		} else {
			user.Email = "N/A"
		}
	case errors.Is(err, db.ErrNotFound):
		// no login yet
	default:
		return nil, fmt.Errorf("loading username: %w", err)
	}

	lease, err := kea.LatestLease4(ctx, q, m)
	switch {
	case err == nil:
		user.IP = lease.IP
		user.Hostname = lease.Hostname
	case errors.Is(err, db.ErrNotFound):
		// no DHCPv4 lease
	default:
		return nil, fmt.Errorf("loading DHCPv4 lease: %w", err)
	}

	user.IP6, err = kea.Lease6Address(ctx, q, m)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, fmt.Errorf("loading DHCPv6 lease: %w", err)
	}

	user.Sessions, err = freeradius.SessionsForMAC(ctx, q, m)
	if err != nil {
		return nil, err
	}
	return user, nil
}
