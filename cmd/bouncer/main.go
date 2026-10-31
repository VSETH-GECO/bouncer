package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// levelVar is the minimum level of the default logger; the root command raises it to Debug with --verbose.
var levelVar slog.LevelVar

func main() {
	levelVar.Set(slog.LevelInfo)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &levelVar})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	go func() {
		// Restore default signal handling once a shutdown was requested, so that a second signal kills us right away
		<-ctx.Done()
		stop()
	}()

	err := newRootCmd().ExecuteContext(ctx)
	// Read before stop(), which cancels ctx as well
	shutdownRequested := ctx.Err() != nil
	stop()
	if err != nil && shutdownRequested && errors.Is(err, context.Canceled) {
		// We were asked to shut down, that's not an error
		slog.Info("Shutdown requested, exiting")
		return
	}
	if err != nil {
		slog.Error("bouncer failed", "error", err)
		os.Exit(1)
	}
}
