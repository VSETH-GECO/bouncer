package migrate

import (
	"context"
	"fmt"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/pkg/config"
	log "github.com/sirupsen/logrus"
)

// RunCommand applies all pending database migrations
func RunCommand(ctx context.Context, cfg config.Config) error {
	d, err := db.Open(ctx, cfg.DB.Config())
	if err != nil {
		return err
	}
	defer func() {
		if err := d.Close(); err != nil {
			log.WithError(err).Warn("Couldn't close database connection")
		}
	}()

	if err := d.Migrate(ctx, cfg.Migrate.Force); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}
	return nil
}
