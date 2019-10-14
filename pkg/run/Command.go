package run

import (
	"github.com/VSETH-GECO/bouncer/pkg/database"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var (
	leaderElect bool
	switchSecret string
)

func RegisterArguments(flags *pflag.FlagSet) {
	flags.BoolVarP(&leaderElect, "leader-elect", "l", true, "Use leader-election via SQL database")
	_ = viper.BindPFlag("leader-elect", flags.Lookup("leader-elect"))
	flags.StringVarP(&switchSecret, "switch-secret", "s", "", "Secret to use when authenticating to switches")
	_ = viper.BindPFlag("switch-secret", flags.Lookup("switch-secret"))
}

func RunCommand() {
	dbHandler := database.CreateHandlerFromConfig()

	err := dbHandler.CheckDBVersion()
	if err != nil {
		log.WithError(err).Fatal("Couldn't check database version!")
	}

	if viper.GetBool("leader-elect") {
		log.Info("Attempting to aquire leader lock")
		election := database.CreateLeaderElect(dbHandler)
		election.EnsureLock(1)
	}

	dbHandler.PollLoop()
}