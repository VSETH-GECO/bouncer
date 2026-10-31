package main

import (
	"log/slog"

	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/VSETH-GECO/bouncer/pkg/migrate"
	"github.com/VSETH-GECO/bouncer/pkg/run"
	"github.com/spf13/cobra"
)

// newRootCmd builds the command tree. The configuration is loaded once, before any subcommand runs, and handed to
// the subcommand explicitly.
func newRootCmd() *cobra.Command {
	loader := config.NewLoader()
	var cfg config.Config

	rootCmd := &cobra.Command{
		Use:  "bouncer",
		Long: "CoA RADIUS daemon",
		// main logs the returned error exactly once
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			cfg, err = loader.Load()
			if err != nil {
				return err
			}
			if cfg.Verbose {
				levelVar.Set(slog.LevelDebug)
			}
			return nil
		},
	}

	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Update the database schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			return migrate.RunCommand(cmd.Context(), cfg)
		},
	}

	// run applies pending migrations before starting the daemon
	serveCmd := &cobra.Command{
		Use:   "run",
		Short: "Start the bouncer daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := migrate.RunCommand(cmd.Context(), cfg); err != nil {
				return err
			}
			return run.ExecCommand(cmd.Context(), cfg)
		},
	}

	rootCmd.AddCommand(migrateCmd, serveCmd)
	loader.RegisterGlobalFlags(rootCmd.PersistentFlags())
	loader.RegisterRunFlags(serveCmd.PersistentFlags())
	loader.RegisterMigrateFlags(migrateCmd.PersistentFlags())
	return rootCmd
}
