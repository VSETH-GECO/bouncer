package run

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/election"
	"github.com/VSETH-GECO/bouncer/internal/metrics"
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
	"github.com/VSETH-GECO/bouncer/pkg/discord"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"golang.org/x/sync/errgroup"
)

// leaderLockID is the id of the bouncer_election row guarding the job loop
const leaderLockID = 1

// ExecCommand executes the main loop until ctx is cancelled or a component fails. Once ctx is cancelled it returns an
// error wrapping context.Canceled; main treats that as a clean shutdown.
func ExecCommand(ctx context.Context, cfg config.Config) error {
	d, err := db.Open(ctx, cfg.DB.Config())
	if err != nil {
		return err
	}
	defer func() {
		if err := d.Close(); err != nil {
			slog.Warn("Couldn't close database connection", "error", err)
		}
	}()

	if err := d.CheckVersion(ctx); err != nil {
		return fmt.Errorf("checking database version: %w", err)
	}

	// The group's context is cancelled (with the error as cause) as soon as any component fails, which stops all
	// other components.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	group, groupCtx := errgroup.WithContext(runCtx)

	if cfg.Run.LeaderElect {
		slog.Info("Attempting to acquire leader lock")
		lock := election.New(d)
		if err := lock.EnsureLock(ctx, leaderLockID); err != nil {
			return fmt.Errorf("acquiring leader lock: %w", err)
		}
		// Since we hold it, let's explicitly release it on exit to save time. ctx is already cancelled at this point.
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := lock.Release(releaseCtx, leaderLockID); err != nil {
				slog.Warn("Couldn't release lock on shutdown!", "error", err)
			} else {
				slog.Info("Leader lock released")
			}
		}()
		// Losing the lock stops the whole daemon, so that another replica can take over
		group.Go(func() error {
			return lock.KeepAlive(groupCtx, leaderLockID)
		})
	}

	// On early returns, make sure no goroutine outlives this function. Deferred after the lock release so that the
	// keep-alive has stopped before the lock is released.
	defer func() {
		stop()
		_ = group.Wait()
	}()

	if cfg.Run.Prometheus {
		group.Go(func() error {
			return metrics.Serve(groupCtx, cfg.Run.PrometheusPort, d.Querier())
		})
	}

	bot := discord.NewDiscord(d, cfg.Discord)
	if err := bot.Setup(groupCtx); err != nil {
		return fmt.Errorf("starting discord bot: %w", err)
	}
	defer func() {
		if err := bot.Close(); err != nil {
			slog.Warn("Couldn't close discord session", "error", err)
		}
	}()

	jobController := controller.NewJobController(d, cfg.SwitchCOASecret, radius.Sender{})
	group.Go(func() error {
		return jobController.Spin(groupCtx)
	})

	return group.Wait()
}
