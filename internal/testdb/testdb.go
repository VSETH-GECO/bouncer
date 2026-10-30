// Package testdb starts a throwaway MySQL loaded with bouncer's base schema fixture, for integration tests.
//
// base-schema.sql is the FreeRADIUS and Kea schema from production (tables, triggers, routines and the Kea lookup
// rows), without bouncer's own tables. Those come from bouncer's migrations, which callers apply through
// database.Handler.Migrate, exactly as the daemon does at startup.
//
// The container listens on a random host port chosen by Docker. Never point tests at 127.0.0.1:3306: on developer
// machines that port may be a port-forward to the production database.
package testdb

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
)

// Image is the MySQL version production runs (8.0.36-28.1 when the fixture was taken).
const Image = "mysql:8.0.36"

// BaseSchema is the production FreeRADIUS and Kea schema fixture. It is exported so that other tooling (for example
// query code generation) can load the same schema.
//
//go:embed base-schema.sql
var BaseSchema []byte

const (
	databaseName = "freeradius"
	rootPassword = "root"
)

// Instance is a running MySQL container with the base schema loaded.
type Instance struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string

	container testcontainers.Container
}

// Run starts a MySQL container, loads the base schema into it and waits until it accepts connections. The caller
// must Terminate it. Tests should use Start instead.
func Run(ctx context.Context) (*Instance, error) {
	// The MySQL module takes init scripts as host paths; hand it the embedded fixture through a temp file. The file
	// is copied into the container at creation, so it can go as soon as Run returns.
	script, err := os.CreateTemp("", "bouncer-base-schema-*.sql")
	if err != nil {
		return nil, fmt.Errorf("creating temp file for base schema: %w", err)
	}
	defer os.Remove(script.Name())
	_, err = script.Write(BaseSchema)
	if cerr := script.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("writing base schema to %s: %w", script.Name(), err)
	}

	ctr, err := tcmysql.Run(ctx, Image,
		tcmysql.WithDatabase(databaseName),
		tcmysql.WithUsername("root"),
		tcmysql.WithPassword(rootPassword),
		tcmysql.WithScripts(script.Name()),
	)
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", Image, err)
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("resolving host of %s: %w", Image, err), ctr.Terminate(ctx))
	}
	port, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("resolving port of %s: %w", Image, err), ctr.Terminate(ctx))
	}
	return &Instance{
		Host:      host,
		Port:      int(port.Num()),
		User:      "root",
		Password:  rootPassword,
		Database:  databaseName,
		container: ctr,
	}, nil
}

// Open connects a database.Handler to the instance.
func (i *Instance) Open(ctx context.Context) (*database.Handler, error) {
	return database.Open(ctx, i.Host, i.Port, i.User, i.Password, i.Database)
}

// Terminate stops and removes the container.
func (i *Instance) Terminate(ctx context.Context) error {
	return i.container.Terminate(ctx)
}

// Start runs an Instance for the duration of a test and terminates it in the test's cleanup. Without a usable Docker
// daemon the test is skipped, except when the CI environment variable is set, where that is a failure.
func Start(t *testing.T) *Instance {
	t.Helper()
	if os.Getenv("CI") == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	inst, err := Run(ctx)
	if err != nil {
		t.Fatalf("starting test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := inst.Terminate(ctx); err != nil {
			t.Logf("terminating test database: %v", err)
		}
	})
	return inst
}
