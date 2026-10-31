package migrate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/pkg/config"
)

// RunCommand applies all pending database migrations
func RunCommand(ctx context.Context, cfg config.Config) error {
	d, err := db.Open(ctx, cfg.DB.Config())
	if err != nil {
		return err
	}
	defer func() {
		if err := d.Close(); err != nil {
			slog.Warn("Couldn't close database connection", "error", err)
		}
	}()

	if err := d.Migrate(ctx, cfg.Migrate.Force); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}
	return nil
}
