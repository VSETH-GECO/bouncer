package testdb_test

import (
	"context"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/testdb"
)

// TestMigrationsApplyOnBaseSchema is the smoke test for the integration-test setup: the base schema fixture loads
// into a throwaway MySQL, bouncer's own migrations apply on top of it through the normal code path, and the result
// is the schema version this binary was built for.
func TestMigrationsApplyOnBaseSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	inst := testdb.Start(t)

	db, err := inst.Open(ctx)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(ctx, 0); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}
	// CheckDBVersion compares the database against the newest embedded migration, so this is the version
	// assertion without hard-coding a number.
	if err := db.CheckDBVersion(ctx); err != nil {
		t.Fatalf("schema version after migrating: %v", err)
	}
	// Running the migrations again must be a no-op.
	if err := db.Migrate(ctx, 0); err != nil {
		t.Fatalf("re-applying migrations: %v", err)
	}

	// The fixture's seed data and bouncer's tables coexist in the one database.
	var leaseStates int
	if err := db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM lease_state").Scan(&leaseStates); err != nil {
		t.Fatalf("querying fixture seed table: %v", err)
	}
	if leaseStates == 0 {
		t.Fatal("lease_state has no rows: base schema fixture was not loaded")
	}
	var pendingJobs int
	if err := db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM bouncer_jobs").Scan(&pendingJobs); err != nil {
		t.Fatalf("querying bouncer_jobs: %v", err)
	}
	if pendingJobs != 0 {
		t.Fatalf("bouncer_jobs should start empty, has %d rows", pendingJobs)
	}
}
