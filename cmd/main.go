package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/VSETH-GECO/bouncer/cmd/bouncer"
	log "github.com/sirupsen/logrus"
)

func main() {
	bouncer.SetupCommands()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	go func() {
		// Restore default signal handling once a shutdown was requested, so that a second signal kills us right away
		<-ctx.Done()
		stop()
	}()

	err := bouncer.RootCmd.ExecuteContext(ctx)
	// Read before stop(), which cancels ctx as well
	shutdownRequested := ctx.Err() != nil
	stop()
	if err != nil && shutdownRequested && errors.Is(err, context.Canceled) {
		// We were asked to shut down, that's not an error
		log.Info("Shutdown requested, exiting")
		return
	}
	if err != nil {
		log.WithError(err).Error("bouncer failed")
		os.Exit(1)
	}
}
