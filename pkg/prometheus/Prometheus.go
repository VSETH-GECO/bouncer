package prometheus

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// StartServing runs the metrics sampler and serves the metrics via HTTP until ctx is cancelled. It returns nil after a
// clean shutdown and an error if the server fails (e.g. because the port is taken).
func StartServing(ctx context.Context, port int, handler *database.Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		// Only returns ctx.Err() once we're shutting down
		_ = database.StartUpdating(ctx, handler)
	}()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:    "0.0.0.0:" + strconv.Itoa(port),
		Handler: mux,
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	var err error
	select {
	case err = <-serveErr:
		err = fmt.Errorf("serving prometheus metrics: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
			err = fmt.Errorf("shutting down prometheus server: %w", shutdownErr)
		}
		if serverErr := <-serveErr; !errors.Is(serverErr, http.ErrServerClosed) {
			err = errors.Join(err, fmt.Errorf("serving prometheus metrics: %w", serverErr))
		}
	}

	cancel()
	<-samplerDone
	return err
}
