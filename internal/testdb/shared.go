package testdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/testcontainers/testcontainers-go"
)

// Package-level state of the shared container, set by Main before any test runs and read-only afterwards.
var (
	shared       *db.DB
	sharedConfig db.Config
	sharedErr    error
)

// dockerHealth reports why Docker is unusable, or nil when it is healthy. It mirrors testcontainers'
// SkipIfProviderIsNotHealthy, which needs a *testing.T that TestMain does not have.
func dockerHealth(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("checking docker health panicked: %v", r)
		}
	}()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return fmt.Errorf("getting docker provider: %w", err)
	}
	defer provider.Close()
	if err := provider.Health(ctx); err != nil {
		return fmt.Errorf("docker is not healthy: %w", err)
	}
	return nil
}

// Main is the body of a test package's TestMain: os.Exit(testdb.Main(m)). It starts (or attaches to) the MySQL
// container shared by every package of the `go test` run, migrates it to the current schema and runs the tests.
// Tests reach the database through Shared and Tx. The container outlives the package: testcontainers' reaper removes
// it after the whole run (see runShared). Concurrent packages migrating the same database are serialized by the
// migration lock, and a package that finds it already migrated gets ErrNoChange, which Migrate treats as success.
//
// Without a usable Docker daemon the tests still run, and those that call Shared or Tx are skipped, unless the CI
// environment variable is set, where failing to start the container makes Main return 1.
func Main(m *testing.M) int {
	ci := os.Getenv("CI") != ""
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if !ci {
		if err := dockerHealth(ctx); err != nil {
			sharedErr = err
			return m.Run()
		}
	}

	inst, err := runShared(ctx)
	if err != nil {
		return startFailed(ci, m, err)
	}
	sharedConfig = inst.Config

	d, err := openRetrying(ctx, inst)
	if err != nil {
		return startFailed(ci, m, fmt.Errorf("connecting to test database: %w", err))
	}
	defer d.Close()
	if err := d.Migrate(ctx, 0); err != nil {
		return startFailed(ci, m, fmt.Errorf("migrating test database: %w", err))
	}

	shared = d
	return m.Run()
}

// openRetrying connects to inst, retrying every 500ms until it succeeds or ctx is done, and then returns the last
// error. A package that attaches to a container another package is still initializing sees refused connections until
// MySQL has finished its init scripts and listens on its final port.
func openRetrying(ctx context.Context, inst *Instance) (*db.DB, error) {
	for {
		d, err := inst.Open(ctx)
		if err == nil {
			return d, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// startFailed handles a container that could not be brought up: a failure in CI, a skip reason elsewhere.
func startFailed(ci bool, m *testing.M, err error) int {
	if ci {
		fmt.Fprintf(os.Stderr, "testdb: starting shared test database: %v\n", err)
		return 1
	}
	sharedErr = err
	return m.Run()
}

// Shared returns the package's migrated database, or skips the test when it could not be started.
func Shared(t *testing.T) *db.DB {
	t.Helper()
	if shared == nil {
		if sharedErr == nil {
			t.Fatal("testdb: Shared used without TestMain calling testdb.Main")
		}
		t.Skipf("test database unavailable: %v", sharedErr)
	}
	return shared
}

// SharedConfig returns the connection configuration of the package's shared container, for tests that open their own
// connection. It skips or fails the test exactly like Shared.
func SharedConfig(t *testing.T) db.Config {
	t.Helper()
	Shared(t)
	return sharedConfig
}

// Tx begins a transaction on the shared database and rolls it back when the test ends, so tests leave no rows
// behind. Tests that use it must not call t.Parallel.
func Tx(t *testing.T) *sql.Tx {
	t.Helper()
	d := Shared(t)
	// The context bounds the transaction's whole life, so it must not be cancelled before the cleanup runs.
	tx, err := d.BeginTx(context.Background())
	if err != nil {
		t.Fatalf("beginning test transaction: %v", err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("rolling back test transaction: %v", err)
		}
	})
	return tx
}
