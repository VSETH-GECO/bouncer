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
	"github.com/VSETH-GECO/bouncer/internal/loop"
	"github.com/VSETH-GECO/bouncer/internal/metrics"
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/VSETH-GECO/bouncer/pkg/controller"
	"github.com/VSETH-GECO/bouncer/pkg/discord"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
)

const (
	// heartbeatMaxAge is how long a loop may go without a successful round before /healthz fails. The backoff cap
	// of a failing loop is half of it (see runLoop), so a loop gets a few retries before the probe fails.
	heartbeatMaxAge = 60 * time.Second
	// samplerInterval is the wait between two metric samples.
	samplerInterval = 7 * time.Second
	// jobInterval is the wait between two passes over the job queue.
	jobInterval = 5 * time.Second
)

// ExecCommand executes the main loop until ctx is cancelled or a component fails. Once ctx is cancelled it returns an
// error wrapping context.Canceled; main treats that as a clean shutdown.
//
// Every replica serves the probes (/healthz, /readyz) and, unless disabled, the metrics and their sampler loop. The
// leader additionally runs the job loop and the Discord bot; with leader election only the replica holding the
// Kubernetes lease leads, and losing the lease returns leader.ErrLost, so the process exits and is restarted as a
// follower. Both loops run through the loop runner (see runLoop) and report heartbeats to the probes.
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
		group.Go(func() error {
			return runLoop(groupCtx, probes, "metrics-sampler", samplerInterval, heartbeatMaxAge,
				func(ctx context.Context) error { return metrics.Sample(ctx, d.Querier()) })
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

// runLoop registers a heartbeat for the loop, runs it through loop.Run with a backoff cap of half the heartbeat
// window, and unregisters the heartbeat when the loop ends.
func runLoop(ctx context.Context, probes *health.Registry, name string, interval, maxAge time.Duration,
	round func(context.Context) error) error {
	hb := probes.Heartbeat(name, maxAge)
	defer hb.Stop()
	return loop.Run(ctx, loop.Loop{
		Name:     name,
		Interval: interval,
		MaxWait:  maxAge / 2,
		Round:    round,
		Beat:     hb.Beat,
	})
}

// runLeader runs what only the leading replica may run, the Discord bot and the job loop, until ctx is cancelled or one
// of them fails. The job loop runs through the loop runner (see runLoop), one round per jobInterval, and reports the
// "job-loop" heartbeat.
func runLeader(ctx context.Context, d *db.DB, cfg config.Config, probes *health.Registry) error {
	metrics.SetLeader(true)
	defer metrics.SetLeader(false)

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
		return runLoop(groupCtx, probes, "job-loop", jobInterval, heartbeatMaxAge, jobController.Work)
	})

	return group.Wait()
}
