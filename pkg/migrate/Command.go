package migrate

import (
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

func RunCommand() {
	dbHandler := database.CreateHandlerFromConfig()

	err := dbHandler.Migrate(force)
	if err != nil {
		log.WithError(err).Fatal("Migration failed")
	}
}
