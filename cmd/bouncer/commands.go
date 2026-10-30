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
	RunE: func(cmd *cobra.Command, args []string) error {
		return migrate.RunCommand(cmd.Context())
	},
}

// ServeCmd is the cobra command object for the main server loop
var ServeCmd = &cobra.Command{
	Use:     "run starts the bouncer daemon",
	Aliases: []string{"run"},
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := migrate.RunCommand(cmd.Context()); err != nil {
			return err
		}
		return run.ExecCommand(cmd.Context())
	},
}

// RootCmd is the cobra root command object
var RootCmd = &cobra.Command{
	Use:  "bouncer",
	Long: "CoA RADIUS daemon",
	// main logs the returned error exactly once
	SilenceUsage:  true,
	SilenceErrors: true,
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
