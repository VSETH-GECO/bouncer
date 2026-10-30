package migrate

import (
	"context"
	"fmt"

	"github.com/VSETH-GECO/bouncer/pkg/database"
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
	dbHandler, err := database.OpenFromConfig(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := dbHandler.Close(); err != nil {
			log.WithError(err).Warn("Couldn't close database connection")
		}
	}()

	if err := dbHandler.Migrate(ctx, force); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}
	return nil
}
