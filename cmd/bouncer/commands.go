package main

import (
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/VSETH-GECO/bouncer/pkg/migrate"
	"github.com/VSETH-GECO/bouncer/pkg/run"
	"github.com/spf13/cobra"
)

// migrateCmd updates the database schema
var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Update the database schema",
	RunE: func(cmd *cobra.Command, args []string) error {
		return migrate.RunCommand(cmd.Context())
	},
}

// serveCmd runs the main server loop, after applying pending migrations
var serveCmd = &cobra.Command{
	Use:   "run",
	Short: "Start the bouncer daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := migrate.RunCommand(cmd.Context()); err != nil {
			return err
		}
		return run.ExecCommand(cmd.Context())
	},
}

// rootCmd is the cobra root command
var rootCmd = &cobra.Command{
	Use:  "bouncer",
	Long: "CoA RADIUS daemon",
	// main logs the returned error exactly once
	SilenceUsage:  true,
	SilenceErrors: true,
}

// setupCommands registers the subcommands and all flags
func setupCommands() {
	rootCmd.AddCommand(migrateCmd)
	rootCmd.AddCommand(serveCmd)

	config.RegisterGlobalArguments(rootCmd.PersistentFlags())

	run.RegisterArguments(serveCmd.PersistentFlags())
	migrate.RegisterArguments(migrateCmd.PersistentFlags())

	cobra.OnInitialize(config.LoadConfig)
}
