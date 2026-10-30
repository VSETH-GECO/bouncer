package run

import (
	"context"
	"fmt"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/election"
	"github.com/VSETH-GECO/bouncer/internal/metrics"
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
	"github.com/VSETH-GECO/bouncer/pkg/discord"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

var (
	leaderElect    bool
	switchSecret   string
	usePrometheus  bool
	prometheusPort int
)

// RegisterArguments registers this command's arguments
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

// leaderLockID is the id of the bouncer_election row guarding the job loop
const leaderLockID = 1

// ExecCommand executes the main loop until ctx is cancelled or a component fails. Once ctx is cancelled it returns an
// error wrapping context.Canceled; main treats that as a clean shutdown.
func ExecCommand(ctx context.Context) error {
	d, err := db.Open(ctx, config.CurrentOptions.DB())
	if err != nil {
		return err
	}
	defer func() {
		if err := d.Close(); err != nil {
			log.WithError(err).Warn("Couldn't close database connection")
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

	if viper.GetBool("leader-elect") {
		log.Info("Attempting to acquire leader lock")
		lock := election.New(d)
		if err := lock.EnsureLock(ctx, leaderLockID); err != nil {
			return fmt.Errorf("acquiring leader lock: %w", err)
		}
		// Since we hold it, let's explicitly release it on exit to save time. ctx is already cancelled at this point.
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := lock.Release(releaseCtx, leaderLockID); err != nil {
				log.WithError(err).Warn("Couldn't release lock on shutdown!")
			} else {
				log.Info("Leader lock released")
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

	if viper.GetBool("usePrometheus") {
		group.Go(func() error {
			return metrics.Serve(groupCtx, viper.GetInt("prometheusPort"), d.Querier())
		})
	}

	bot := discord.NewDiscord(d)
	if err := bot.Setup(groupCtx); err != nil {
		return fmt.Errorf("starting discord bot: %w", err)
	}
	defer func() {
		if err := bot.Close(); err != nil {
			log.WithError(err).Warn("Couldn't close discord session")
		}
	}()

	jobController := controller.NewClientController(d, config.CurrentOptions.SwitchCOASecret)
	group.Go(func() error {
		return jobController.Spin(groupCtx)
	})

	return group.Wait()
}
