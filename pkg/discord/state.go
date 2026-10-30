package discord

import (
	"context"
	"fmt"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/go-jet/jet/v2/mysql"
)

// commandVersion returns the version of the last applied discord command migration. It returns an error wrapping
// db.ErrNotFound if no migration has been applied yet.
func commandVersion(ctx context.Context, q db.Querier) (int, error) {
	var row model.BouncerDiscordMigration
	err := mysql.SELECT(table.BouncerDiscordMigration.Version).
		FROM(table.BouncerDiscordMigration).
		ORDER_BY(table.BouncerDiscordMigration.Version.DESC()).
		LIMIT(1).
		QueryContext(ctx, q, &row)
	if err != nil {
		return 0, fmt.Errorf("reading discord migration version: %w", db.NotFound(err))
	}
	return int(row.Version), nil
}

// setCommandVersion records that the discord command migration v has been applied
func setCommandVersion(ctx context.Context, q db.Querier, v int) error {
	_, err := table.BouncerDiscordMigration.INSERT(table.BouncerDiscordMigration.Version, table.BouncerDiscordMigration.UpdatedAt).
		VALUES(v, mysql.CURRENT_TIMESTAMP()).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("storing discord migration version: %w", err)
	}
	return nil
}
