package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
)

// LeaderElect provides a poor man's leader election based on our shared SQL database
type LeaderElect struct {
	handler *Handler
	nodeID  string
}

func GetNodeID() string {
	host, err := os.Hostname()
	if err != nil {
		log.WithError(err).Warn("Couldn't determine hostname!")
	}
	return host + "-" + strconv.Itoa(os.Getpid())
}

// CreateLeaderElect prepares a new leader election object, using a combination of hostname and pid as unique id
func CreateLeaderElect(handler *Handler) *LeaderElect {
	obj := &LeaderElect{
		handler: handler,
		nodeID:  GetNodeID(),
	}
	return obj
}

func (l *LeaderElect) Prepare(ctx context.Context, lock int) {
	// Ensure that the lock exists (we simply ignore any errors inserting)
	_, _ = l.handler.db.ExecContext(ctx, "INSERT INTO bouncer_election(id, time, nodeid) VALUES(?, '0000-00-00 00:00:00', NULL);", lock)
}

// TryAcquire attempts to acquire the lock with the given id and returns whether it was successful and who currently
// holds the lock
func (l *LeaderElect) TryAcquire(ctx context.Context, lock int) (bool, string, error) {
	var actualNodeID string
	// MariaDB does not have an equivalent to UPDATE ... RETURNING, so let's do that as transaction
	err := l.handler.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE bouncer_election SET nodeid=? WHERE id=? AND time < TIMESTAMPADD(SECOND, -30, CURRENT_TIMESTAMP());", l.nodeID, lock)
		if err != nil {
			return fmt.Errorf("updating lock entry: %w", err)
		}
		// nodeid is NULL after a release, so scan into a NullString
		var holder sql.NullString
		err = tx.QueryRowContext(ctx, "SELECT nodeid FROM bouncer_election WHERE id=?", lock).Scan(&holder)
		if errors.Is(err, sql.ErrNoRows) {
			// No lock entry is in the table - lets add ourselves!
			_, err = tx.ExecContext(ctx, "INSERT INTO bouncer_election(nodeid, id, time) VALUES (?, ?, TIMESTAMPADD(SECOND, -30, CURRENT_TIMESTAMP()));", l.nodeID, lock)
			if err != nil {
				return fmt.Errorf("creating new lock entry: %w", err)
			}
			actualNodeID = l.nodeID
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading lock holder: %w", err)
		}
		actualNodeID = holder.String
		return nil
	})
	if err != nil {
		return false, "", err
	}
	return actualNodeID == l.nodeID, actualNodeID, nil
}

// Refresh refreshes the given lock. If the current node doesn't hold it, this is a no-op
func (l *LeaderElect) Refresh(ctx context.Context, lock int) error {
	_, err := l.handler.db.ExecContext(ctx, "UPDATE bouncer_election SET time=CURRENT_TIMESTAMP() WHERE id=? AND nodeid=?;", lock, l.nodeID)
	if err != nil {
		return fmt.Errorf("refreshing leader lock: %w", err)
	}
	return nil
}

// holder returns the node currently holding the given lock
func (l *LeaderElect) holder(ctx context.Context, lock int) (string, error) {
	var holder sql.NullString
	err := l.handler.db.QueryRowContext(ctx, "SELECT nodeid FROM bouncer_election WHERE id=?", lock).Scan(&holder)
	if err != nil {
		return "", fmt.Errorf("reading lock holder: %w", noRows(err))
	}
	return holder.String, nil
}

// KeepAlive periodically refreshes the given lock, which must be held by this node. It only returns on failure: when
// the lock can't be refreshed or checked, when it was taken over by another node, or with ctx.Err() once ctx is
// cancelled. The caller is expected to stop all work when it returns.
func (l *LeaderElect) KeepAlive(ctx context.Context, lock int) error {
	for {
		if err := l.Refresh(ctx, lock); err != nil {
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
		actualNodeID, err := l.holder(ctx, lock)
		if errors.Is(err, ErrNotFound) {
			log.Warn("Error checking my leader lock - empty result?")
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("checking leader lock: %w", err)
		}
		if actualNodeID != l.nodeID {
			return fmt.Errorf("lost leader lock %d: held by %q, I am %q", lock, actualNodeID, l.nodeID)
		}
	}
}

// Release gives up the given lock so that another replica can take over without waiting for it to expire. It is a
// no-op if this node does not hold the lock. Since this typically runs during shutdown, pass a fresh context rather
// than the (already cancelled) run context.
func (l *LeaderElect) Release(ctx context.Context, lock int) error {
	// All zeros is no longer a valid timestamp in MySQL 8
	_, err := l.handler.db.ExecContext(ctx, "UPDATE bouncer_election SET time='2000-01-01 10:00:00', nodeid=NULL WHERE id=? AND nodeid=?;", lock, l.nodeID)
	if err != nil {
		return fmt.Errorf("releasing leader lock: %w", err)
	}
	return nil
}

// EnsureLock blocks until the given lock is acquired successfully. It returns ctx.Err() if ctx is cancelled first.
// Once it returns nil, the caller must run KeepAlive to hold on to the lock and Release it on shutdown.
func (l *LeaderElect) EnsureLock(ctx context.Context, lock int) error {
	l.Prepare(ctx, lock)

	for {
		haveLock, leaderName, err := l.TryAcquire(ctx, lock)
		if haveLock {
			// Yay, we got the lock!
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entry := log.WithFields(log.Fields{
			"haveLock":   haveLock,
			"id":         lock,
			"leaderName": leaderName,
		})
		if err != nil {
			entry.WithError(err).Warn("Didn't acquire lock")
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
