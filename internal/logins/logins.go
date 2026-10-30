package logins

import (
	"context"
	"fmt"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/go-jet/jet/v2/mysql"
)

// FindMAC finds a login_logs entry by username or MAC (case-insensitive), returning the newest MAC.
// Returns an error wrapping db.ErrNotFound if no entry is found.
// Returns a non-ErrNotFound error if the MAC field cannot be parsed.
func FindMAC(ctx context.Context, q db.Querier, value string) (mac.MAC, error) {
	row := model.LoginLogs{}
	err := mysql.SELECT(table.LoginLogs.AllColumns).
		FROM(table.LoginLogs).
		WHERE(
			mysql.LOWER(table.LoginLogs.Username).EQ(mysql.LOWER(mysql.String(value))).
				OR(mysql.LOWER(table.LoginLogs.Mac).EQ(mysql.LOWER(mysql.String(value)))),
		).
		ORDER_BY(table.LoginLogs.UpdatedAt.DESC()).
		LIMIT(1).
		QueryContext(ctx, q, &row)
	if err != nil {
		return mac.MAC{}, fmt.Errorf("searching login_logs: %w", db.NotFound(err))
	}

	if row.Mac == nil {
		return mac.MAC{}, fmt.Errorf("login_logs entry has no mac")
	}

	m, err := mac.Parse(*row.Mac)
	if err != nil {
		return mac.MAC{}, fmt.Errorf("parsing MAC %q from login_logs: %w", *row.Mac, err)
	}

	return m, nil
}

// LatestUsername returns the newest username for a given MAC from login_logs.
// Returns an empty string if the username field is nil.
// Returns an error wrapping db.ErrNotFound if no entry is found.
func LatestUsername(ctx context.Context, q db.Querier, m mac.MAC) (string, error) {
	row := model.LoginLogs{}
	err := mysql.SELECT(table.LoginLogs.AllColumns).
		FROM(table.LoginLogs).
		WHERE(mysql.LOWER(table.LoginLogs.Mac).EQ(mysql.String(m.Hex()))).
		ORDER_BY(table.LoginLogs.UpdatedAt.DESC()).
		LIMIT(1).
		QueryContext(ctx, q, &row)
	if err != nil {
		return "", fmt.Errorf("searching login_logs for MAC %s: %w", m.Hex(), db.NotFound(err))
	}

	if row.Username == nil {
		return "", nil
	}
	return *row.Username, nil
}
