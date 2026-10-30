package testdb_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
)

// TestMigrationsApplyOnBaseSchema is the smoke test for the integration-test setup: the base schema fixture loads
// into a throwaway MySQL, bouncer's own migrations apply on top of it through the normal code path, and the result
// is the schema version this binary was built for.
func TestMigrationsApplyOnBaseSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	inst := testdb.Start(t)

	d, err := inst.Open(ctx)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	defer d.Close()

	if err := d.Migrate(ctx, 0); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}
	// CheckVersion compares the database against the newest embedded migration, so this is the version
	// assertion without hard-coding a number.
	if err := d.CheckVersion(ctx); err != nil {
		t.Fatalf("schema version after migrating: %v", err)
	}
	// Running the migrations again must be a no-op.
	if err := d.Migrate(ctx, 0); err != nil {
		t.Fatalf("re-applying migrations: %v", err)
	}

	// The fixture's seed data and bouncer's tables coexist in the one database.
	if n := count(t, ctx, d, "SELECT COUNT(*) FROM lease_state"); n == 0 {
		t.Fatal("lease_state has no rows: base schema fixture was not loaded")
	}
	if n := count(t, ctx, d, "SELECT COUNT(*) FROM bouncer_jobs"); n != 0 {
		t.Fatalf("bouncer_jobs should start empty, has %d rows", n)
	}

	nullable := queryString(t, ctx, d,
		"SELECT GROUP_CONCAT(is_nullable ORDER BY column_name) FROM information_schema.columns WHERE table_schema='freeradius' AND table_name='bouncer_log' AND column_name IN ('switchIP','switchPort')")
	if nullable != "YES,YES" {
		t.Fatalf("bouncer_log switchIP/switchPort nullable = %q, want YES,YES", nullable)
	}
}

// count runs a single-row, single-integer query.
func count(t *testing.T, ctx context.Context, d *db.DB, query string) int {
	t.Helper()
	var n int
	scanOne(t, ctx, d, query, &n)
	return n
}

func queryString(t *testing.T, ctx context.Context, d *db.DB, query string) string {
	t.Helper()
	var s string
	scanOne(t, ctx, d, query, &s)
	return s
}

func scanOne(t *testing.T, ctx context.Context, d *db.DB, query string, dest any) {
	t.Helper()
	rows, err := d.Querier().QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("%s returned no row: %v", query, rows.Err())
	}
	if err := rows.Scan(dest); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// TestSharedTxRollsBack checks that a test's writes through Tx are gone once its cleanup has run.
func TestSharedTxRollsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Run("insert inside tx", func(t *testing.T) {
		tx := testdb.Tx(t)
		if _, err := tx.ExecContext(ctx, "INSERT INTO bouncer_jobs(clientMAC, targetVLAN) VALUES ('001122334455', 42)"); err != nil {
			t.Fatalf("inserting inside the transaction: %v", err)
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM bouncer_jobs WHERE clientMAC='001122334455'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("row not visible inside its own transaction: count = %d", n)
		}
	})

	var n int
	rows, err := testdb.Shared(t).Querier().QueryContext(ctx, "SELECT COUNT(*) FROM bouncer_jobs WHERE clientMAC='001122334455'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("count query returned no row: %v", rows.Err())
	}
	if err := rows.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("row survived the rollback: count = %d", n)
	}
}
