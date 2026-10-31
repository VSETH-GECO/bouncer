package run

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/health"
	"github.com/VSETH-GECO/bouncer/internal/leader"
	"github.com/VSETH-GECO/bouncer/internal/metrics"
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
	"github.com/VSETH-GECO/bouncer/pkg/discord"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
)

// heartbeatMaxAge is how long a loop may go without completing a round before /healthz fails. The sampler ticks every
// 7 s and the job loop every 5 s, so this tolerates a few slow rounds.
const heartbeatMaxAge = 60 * time.Second

// ExecCommand executes the main loop until ctx is cancelled or a component fails. Once ctx is cancelled it returns an
// error wrapping context.Canceled; main treats that as a clean shutdown.
//
// Every replica serves the probes (/healthz, /readyz) and, unless disabled, the metrics. With leader election only the replica holding the Kubernetes lease runs the jobs
// and the Discord bot; losing the lease returns leader.ErrLost, so the process exits and is restarted as a follower.
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

	// Set up the election before anything starts, so a misconfiguration fails right away
	var (
		client kubernetes.Interface
		lcfg   leader.Config
	)
	if cfg.Run.LeaderElect {
		// The client first: outside a cluster, its error is the one that explains what to do
		if client, err = leader.InClusterClient(); err != nil {
			return fmt.Errorf("leader election needs the in-cluster configuration; run with --leader-elect=false outside Kubernetes: %w", err)
		}
		if lcfg, err = leader.FromEnvironment(); err != nil {
			return fmt.Errorf("configuring leader election: %w", err)
		}
	}

	// The group's context is cancelled (with the error as cause) as soon as any component fails, which stops all
	// other components.
	group, groupCtx := errgroup.WithContext(ctx)

	// /healthz checks that the loops are not stuck; /readyz also pings the database
	probes := health.New()
	probes.AddCheck("database", d.SQL().PingContext)
	mux := http.NewServeMux()
	mux.Handle("/healthz", probes.LiveHandler())
	mux.Handle("/readyz", probes.ReadyHandler())

	if cfg.Run.Prometheus {
		mux.Handle("/metrics", metrics.Handler())
		sampler := probes.Heartbeat("metrics-sampler", heartbeatMaxAge)
		group.Go(func() error {
			defer sampler.Stop()
			metrics.RunSampler(groupCtx, d.Querier(), sampler.Beat)
			return nil
		})
	}

	group.Go(func() error {
		return health.Serve(groupCtx, cfg.Run.PrometheusPort, mux)
	})

	lead := func(ctx context.Context) error { return runLeader(ctx, d, cfg, probes) }
	if cfg.Run.LeaderElect {
		group.Go(func() error {
			return leader.Run(groupCtx, client, lcfg, lead)
		})
	} else {
		group.Go(func() error {
			return lead(groupCtx)
		})
	}

	return group.Wait()
}

// runLeader runs what only the leading replica may run, the Discord bot and the job controller, until ctx is cancelled
// or one of them fails.
func runLeader(ctx context.Context, d *db.DB, cfg config.Config, probes *health.Registry) error {
	metrics.SetLeader(true)
	defer metrics.SetLeader(false)

	jobLoop := probes.Heartbeat("job-loop", heartbeatMaxAge)
	defer jobLoop.Stop()

	group, groupCtx := errgroup.WithContext(ctx)

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
		return jobController.Spin(groupCtx, jobLoop.Beat)
	})

	return group.Wait()
}
