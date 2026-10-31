// Package election is a poor man's leader election built on the shared SQL database (bouncer_election).
package election

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/go-jet/jet/v2/mysql"
)

// expiredBefore matches lock rows that were not refreshed within the last 30 seconds.
var expiredBefore = mysql.RawBool("time < TIMESTAMPADD(SECOND, -30, CURRENT_TIMESTAMP())")

// Lock elects a leader per lock id among the nodes sharing the database.
type Lock struct {
	db     *db.DB
	nodeID string
}

// NodeID returns the id identifying this process: hostname and pid.
func NodeID() string {
	host, err := os.Hostname()
	if err != nil {
		slog.Warn("Couldn't determine hostname!", "error", err)
	}
	return host + "-" + strconv.Itoa(os.Getpid())
}

// New prepares a new leader election, using a combination of hostname and pid as unique id.
func New(d *db.DB) *Lock {
	return NewWithNodeID(d, NodeID())
}

// NewWithNodeID is like New with an explicit node id, so tests can model several nodes.
func NewWithNodeID(d *db.DB, nodeID string) *Lock {
	return &Lock{db: d, nodeID: nodeID}
}

// prepare ensures that the lock row exists (any error inserting is ignored).
func (l *Lock) prepare(ctx context.Context, id int) {
	_, _ = table.BouncerElection.INSERT(table.BouncerElection.ID, table.BouncerElection.Time, table.BouncerElection.Nodeid).
		VALUES(id, mysql.Raw("'0000-00-00 00:00:00'"), mysql.NULL).
		ExecContext(ctx, l.db.Querier())
}

// TryAcquire attempts to acquire the lock with the given id and returns whether it was successful and who currently
// holds the lock.
func (l *Lock) TryAcquire(ctx context.Context, id int) (bool, string, error) {
	var holder string
	// MariaDB does not have an equivalent to UPDATE ... RETURNING, so let's do that as transaction
	err := l.db.WithTx(ctx, func(tx db.Querier) error {
		_, err := table.BouncerElection.UPDATE(table.BouncerElection.Nodeid).
			SET(l.nodeID).
			WHERE(table.BouncerElection.ID.EQ(mysql.Int(int64(id))).AND(expiredBefore)).
			ExecContext(ctx, tx)
		if err != nil {
			return fmt.Errorf("updating lock entry: %w", err)
		}

		// nodeid is NULL after a release, which reads as an empty holder
		var row model.BouncerElection
		err = mysql.SELECT(table.BouncerElection.Nodeid).
			FROM(table.BouncerElection).
			WHERE(table.BouncerElection.ID.EQ(mysql.Int(int64(id)))).
			QueryContext(ctx, tx, &row)
		if errors.Is(db.NotFound(err), db.ErrNotFound) {
			// No lock entry is in the table - lets add ourselves!
			_, err = table.BouncerElection.INSERT(table.BouncerElection.Nodeid, table.BouncerElection.ID, table.BouncerElection.Time).
				VALUES(l.nodeID, id, mysql.Raw("TIMESTAMPADD(SECOND, -30, CURRENT_TIMESTAMP())")).
				ExecContext(ctx, tx)
			if err != nil {
				return fmt.Errorf("creating new lock entry: %w", err)
			}
			holder = l.nodeID
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading lock holder: %w", err)
		}
		holder = ""
		if row.Nodeid != nil {
			holder = *row.Nodeid
		}
		return nil
	})
	if err != nil {
		return false, "", err
	}
	return holder == l.nodeID, holder, nil
}

// Refresh refreshes the given lock. If the current node doesn't hold it, this is a no-op.
func (l *Lock) Refresh(ctx context.Context, id int) error {
	_, err := table.BouncerElection.UPDATE(table.BouncerElection.Time).
		SET(mysql.CURRENT_TIMESTAMP()).
		WHERE(table.BouncerElection.ID.EQ(mysql.Int(int64(id))).AND(table.BouncerElection.Nodeid.EQ(mysql.String(l.nodeID)))).
		ExecContext(ctx, l.db.Querier())
	if err != nil {
		return fmt.Errorf("refreshing leader lock: %w", err)
	}
	return nil
}

// holder returns the node currently holding the given lock.
func (l *Lock) holder(ctx context.Context, id int) (string, error) {
	var row model.BouncerElection
	err := mysql.SELECT(table.BouncerElection.Nodeid).
		FROM(table.BouncerElection).
		WHERE(table.BouncerElection.ID.EQ(mysql.Int(int64(id)))).
		QueryContext(ctx, l.db.Querier(), &row)
	if err != nil {
		return "", fmt.Errorf("reading lock holder: %w", db.NotFound(err))
	}
	if row.Nodeid == nil {
		return "", nil
	}
	return *row.Nodeid, nil
}

// KeepAlive periodically refreshes the given lock, which must be held by this node. It only returns on failure: when
// the lock can't be refreshed or checked, when it was taken over by another node, or with ctx.Err() once ctx is
// cancelled. The caller is expected to stop all work when it returns.
func (l *Lock) KeepAlive(ctx context.Context, id int) error {
	for {
		if err := l.Refresh(ctx, id); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(15+rand.Intn(5)) * time.Second):
		}

		// Sanity check
		actualNodeID, err := l.holder(ctx, id)
		if errors.Is(err, db.ErrNotFound) {
			slog.Warn("Error checking my leader lock - empty result?")
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("checking leader lock: %w", err)
		}
		if actualNodeID != l.nodeID {
			return fmt.Errorf("lost leader lock %d: held by %q, I am %q", id, actualNodeID, l.nodeID)
		}
	}
}

// Release gives up the given lock so that another replica can take over without waiting for it to expire. It is a
// no-op if this node does not hold the lock. Since this typically runs during shutdown, pass a fresh context rather
// than the (already cancelled) run context.
func (l *Lock) Release(ctx context.Context, id int) error {
	// All zeros is no longer a valid timestamp in MySQL 8
	_, err := table.BouncerElection.UPDATE(table.BouncerElection.Time, table.BouncerElection.Nodeid).
		SET(mysql.Raw("'2000-01-01 10:00:00'"), mysql.NULL).
		WHERE(table.BouncerElection.ID.EQ(mysql.Int(int64(id))).AND(table.BouncerElection.Nodeid.EQ(mysql.String(l.nodeID)))).
		ExecContext(ctx, l.db.Querier())
	if err != nil {
		return fmt.Errorf("releasing leader lock: %w", err)
	}
	return nil
}

// EnsureLock blocks until the given lock is acquired successfully. It returns ctx.Err() if ctx is cancelled first.
// Once it returns nil, the caller must run KeepAlive to hold on to the lock and Release it on shutdown.
func (l *Lock) EnsureLock(ctx context.Context, id int) error {
	l.prepare(ctx, id)

	for {
		haveLock, leaderName, err := l.TryAcquire(ctx, id)
		if haveLock {
			// Yay, we got the lock!
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entry := slog.With("haveLock", haveLock, "id", id, "leaderName", leaderName)
		if err != nil {
			entry.Warn("Didn't acquire lock", "error", err)
		} else {
			entry.Debug("Didn't acquire lock")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(15+rand.Intn(15)) * time.Second):
		}
	}
}
