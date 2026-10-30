package migrate

import (
	"context"
	"fmt"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/pkg/config"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
)

var (
	force int
)

func RegisterArguments(flags *pflag.FlagSet) {
	flags.IntVarP(&force, "force", "f", 0, "Forcibly set version of DB to this version")
}

// RunCommand applies all pending database migrations
func RunCommand(ctx context.Context) error {
	d, err := db.Open(ctx, config.CurrentOptions.DB())
	if err != nil {
		return err
	}
	defer func() {
		if err := d.Close(); err != nil {
			log.WithError(err).Warn("Couldn't close database connection")
		}
	}()

	if err := d.Migrate(ctx, force); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}
	return nil
}
