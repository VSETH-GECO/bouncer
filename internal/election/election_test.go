package election_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/election"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/go-jet/jet/v2/mysql"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// cleanupLock deletes the lock row once the test is done. These tests run against the shared pool rather than inside
// testdb.Tx because the election functions manage their own transactions.
func cleanupLock(t *testing.T, id int) {
	t.Helper()
	d := testdb.Shared(t)
	t.Cleanup(func() {
		_, err := table.BouncerElection.DELETE().
			WHERE(table.BouncerElection.ID.EQ(mysql.Int(int64(id)))).
			ExecContext(context.Background(), d.Querier())
		if err != nil {
			t.Errorf("cleaning up lock %d: %v", id, err)
		}
	})
}

func TestTryAcquire_FreshLock(t *testing.T) {
	ctx := context.Background()
	const id = 1001
	cleanupLock(t, id)
	a := election.NewWithNodeID(testdb.Shared(t), "node-a")

	held, holder, err := a.TryAcquire(ctx, id)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if !held || holder != "node-a" {
		t.Errorf("held=%v holder=%q, want true, node-a", held, holder)
	}
}

func TestTryAcquire_HeldByOther(t *testing.T) {
	ctx := context.Background()
	const id = 1002
	cleanupLock(t, id)
	d := testdb.Shared(t)
	a := election.NewWithNodeID(d, "node-a")
	b := election.NewWithNodeID(d, "node-b")

	if held, _, err := a.TryAcquire(ctx, id); err != nil || !held {
		t.Fatalf("A TryAcquire: held=%v err=%v", held, err)
	}
	if err := a.Refresh(ctx, id); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	held, holder, err := b.TryAcquire(ctx, id)
	if err != nil {
		t.Fatalf("B TryAcquire: %v", err)
	}
	if held || holder != "node-a" {
		t.Errorf("held=%v holder=%q, want false, node-a", held, holder)
	}
}

func TestRelease_ThenOtherAcquires(t *testing.T) {
	ctx := context.Background()
	const id = 1003
	cleanupLock(t, id)
	d := testdb.Shared(t)
	a := election.NewWithNodeID(d, "node-a")
	b := election.NewWithNodeID(d, "node-b")

	if held, _, err := a.TryAcquire(ctx, id); err != nil || !held {
		t.Fatalf("A TryAcquire: held=%v err=%v", held, err)
	}
	if err := a.Refresh(ctx, id); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := a.Release(ctx, id); err != nil {
		t.Fatalf("Release: %v", err)
	}

	held, holder, err := b.TryAcquire(ctx, id)
	if err != nil {
		t.Fatalf("B TryAcquire: %v", err)
	}
	if !held || holder != "node-b" {
		t.Errorf("held=%v holder=%q, want true, node-b", held, holder)
	}
}

func TestTryAcquire_StaleLockTakenOver(t *testing.T) {
	ctx := context.Background()
	const id = 1004
	cleanupLock(t, id)
	d := testdb.Shared(t)
	stale := time.Date(2000, 1, 1, 10, 0, 0, 0, time.UTC)
	_, err := table.BouncerElection.INSERT(table.BouncerElection.ID, table.BouncerElection.Time, table.BouncerElection.Nodeid).
		VALUES(id, stale, "node-a").
		ExecContext(ctx, d.Querier())
	if err != nil {
		t.Fatalf("seeding stale lock: %v", err)
	}

	b := election.NewWithNodeID(d, "node-b")
	held, holder, err := b.TryAcquire(ctx, id)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if !held || holder != "node-b" {
		t.Errorf("held=%v holder=%q, want true, node-b", held, holder)
	}
}
