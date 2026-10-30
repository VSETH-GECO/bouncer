package database

import (
	"context"
	"fmt"
)

// GetDiscordVersion returns the version of the last applied discord command migration. It returns an error wrapping
// ErrNotFound if no migration has been applied yet.
func (h *Handler) GetDiscordVersion(ctx context.Context) (int, error) {
	var value int
	err := h.db.QueryRowContext(ctx, "select version from bouncer_discord_migration order by version desc limit 1;").Scan(&value)
	if err != nil {
		return 0, fmt.Errorf("reading discord migration version: %w", noRows(err))
	}
	return value, nil
}

func (h *Handler) SetDiscordVersion(ctx context.Context, q DBTX, version int) error {
	_, err := q.ExecContext(ctx, "insert into bouncer_discord_migration(version, updated_at) values(?, CURRENT_TIMESTAMP())", version)
	if err != nil {
		return fmt.Errorf("storing discord migration version: %w", err)
	}
	return nil
}
