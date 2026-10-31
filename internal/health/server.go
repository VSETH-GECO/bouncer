package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Serve serves handler via HTTP on 0.0.0.0:<port> until ctx is cancelled. It returns nil after a clean shutdown and an
// error if the server fails (e.g. because the port is taken).
func Serve(ctx context.Context, port int, handler http.Handler) error {
	server := &http.Server{
		Addr:    "0.0.0.0:" + strconv.Itoa(port),
		Handler: handler,
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("serving health and metrics: %w", err)
	case <-ctx.Done():
		var err error
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
			err = fmt.Errorf("shutting down health and metrics server: %w", shutdownErr)
		}
		if serverErr := <-serveErr; !errors.Is(serverErr, http.ErrServerClosed) {
			err = errors.Join(err, fmt.Errorf("serving health and metrics: %w", serverErr))
		}
		return err
	}
}
