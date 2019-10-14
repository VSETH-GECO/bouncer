package run

import (
	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/VSETH-GECO/bouncer/pkg/prometheus"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var (
	leaderElect    bool
	switchSecret   string
	usePrometheus  bool
	prometheusPort int
)

func RegisterArguments(flags *pflag.FlagSet) {
	flags.BoolVarP(&leaderElect, "leader-elect", "l", true, "Use leader-election via SQL database")
	_ = viper.BindPFlag("leader-elect", flags.Lookup("leader-elect"))
	flags.StringVarP(&switchSecret, "switch-secret", "s", "", "Secret to use when authenticating to switches")
	_ = viper.BindPFlag("switch-secret", flags.Lookup("switch-secret"))
	flags.BoolVar(&usePrometheus, "usePrometheus", true, "Whether to enable usePrometheus exporter")
	_ = viper.BindPFlag("usePrometheus", flags.Lookup("usePrometheus"))
	flags.IntVar(&prometheusPort, "prometheusPort", 2112, "Which port to bind the prometheus server on")
	_ = viper.BindPFlag("prometheusPort", flags.Lookup("prometheusPort"))
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

	if viper.GetBool("usePrometheus") {
		go prometheus.StartServing(viper.GetInt("prometheusPort"), dbHandler)
	}

	dbHandler.PollLoop()
}
