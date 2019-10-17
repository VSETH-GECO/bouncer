package bouncer

import (
	"github.com/VSETH-GECO/bouncer/pkg/migrate"
	"github.com/VSETH-GECO/bouncer/pkg/run"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Options contains the list of global options
type Options struct {
	Verbose        bool
	ConfigLocation string
	DBHost         string
	DBPort         int
	DBUser         string
	DBPassword     string
	DBDatabase     string
}

var (
	// CurrentOptions provides storage for global options
	CurrentOptions = Options{}
)

// MigrateCmd is the cobra command object for database migrations
var MigrateCmd = &cobra.Command{
	Use:     "migrate updates the database schema",
	Aliases: []string{"migrate"},
	Run: func(cmd *cobra.Command, args []string) {
		migrate.RunCommand()
	},
}

// ServeCmd is the cobra command object for the main server loop
var ServeCmd = &cobra.Command{
	Use:     "run starts the bouncer daemon",
	Aliases: []string{"run"},
	Run: func(cmd *cobra.Command, args []string) {
		run.RunCommand()
	},
}

// RootCmd is the cobra root command object
var RootCmd = &cobra.Command{
	Use:  "bouncer",
	Long: "CoA RADIUS daemon",
}

// LoadConfig tries to load the global configuration
func LoadConfig() {
	if CurrentOptions.Verbose {
		log.SetLevel(log.DebugLevel)
	}

	viper.SetConfigName("config")
	viper.AddConfigPath("/etc/bouncer")
	if CurrentOptions.ConfigLocation != "" {
		log.WithFields(log.Fields{
			"config": CurrentOptions.ConfigLocation,
		}).Info("Loading config from explicit location")
		viper.SetConfigFile(CurrentOptions.ConfigLocation)
	}
	err := viper.ReadInConfig()
	if err != nil {
		log.WithError(err).Warn("Config load failed!")
	}
}

// SetupCommands initializes all commands
func SetupCommands() {
	RootCmd.AddCommand(MigrateCmd)
	RootCmd.AddCommand(ServeCmd)

	flags := RootCmd.PersistentFlags()
	flags.BoolVar(&CurrentOptions.Verbose, "verbose", false, "Output verbose log messages")
	_ = viper.BindPFlag("verbose", flags.Lookup("verbose"))
	flags.StringVar(&CurrentOptions.DBDatabase, "database", "", "Database to use")
	_ = viper.BindPFlag("database", flags.Lookup("database"))
	flags.StringVar(&CurrentOptions.DBHost, "host", "", "Database host")
	_ = viper.BindPFlag("host", flags.Lookup("host"))
	flags.StringVar(&CurrentOptions.DBUser, "user", "", "Database user")
	_ = viper.BindPFlag("user", flags.Lookup("user"))
	flags.StringVar(&CurrentOptions.DBPassword, "password", "", "Database password")
	_ = viper.BindPFlag("password", flags.Lookup("password"))
	flags.StringVar(&CurrentOptions.ConfigLocation, "config", "", "Extra config file location to check first")
	// config is not bound to viper!

	run.RegisterArguments(ServeCmd.PersistentFlags())
	migrate.RegisterArguments(MigrateCmd.PersistentFlags())

	cobra.OnInitialize(LoadConfig)
}
