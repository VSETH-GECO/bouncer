package bouncer

import (
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/VSETH-GECO/bouncer/pkg/migrate"
	"github.com/VSETH-GECO/bouncer/pkg/run"
	"github.com/spf13/cobra"
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
		migrate.RunCommand()
		run.ExecCommand()
	},
}

// RootCmd is the cobra root command object
var RootCmd = &cobra.Command{
	Use:  "bouncer",
	Long: "CoA RADIUS daemon",
}

// SetupCommands initializes all commands
func SetupCommands() {
	RootCmd.AddCommand(MigrateCmd)
	RootCmd.AddCommand(ServeCmd)

	config.RegisterGlobalArguments(RootCmd.PersistentFlags())

	run.RegisterArguments(ServeCmd.PersistentFlags())
	migrate.RegisterArguments(MigrateCmd.PersistentFlags())

	cobra.OnInitialize(config.LoadConfig)
}
