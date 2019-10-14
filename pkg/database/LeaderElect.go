package database

import (
	"database/sql"
	"errors"
	log "github.com/sirupsen/logrus"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"time"
)

// LeaderElect provides a poor man's leader election based on our shared SQL database
type LeaderElect struct {
	connection *Handler
	nodeID     string
}

// CreateLeaderElect prepares a new leader election object, using a combination of hostname and pid as unique id
func CreateLeaderElect(connection *Handler) *LeaderElect {
	host, err := os.Hostname()
	if err != nil {
		log.WithError(err).Warn("Couldn't determine hostname!")
	}
	obj := &LeaderElect{
		connection: connection,
		nodeID:     host + "-" + strconv.Itoa(os.Getpid()),
	}
	return obj
}

func (l *LeaderElect) Prepare(lock int) {
	// Ensure that the lock exists (we simply ignore any errors inserting)
	_, _ = l.connection.connection.Exec("INSERT INTO bouncer_election(id, time, nodeid) VALUES(?, '0000-00-00 00:00:00', NULL);", lock)
}

// TryAquire attempts to acquire the lock with the given id and returns whether it was successful
func (l *LeaderElect) TryAcquire(lock int) (isLeader bool, leaderName string, err error) {
	isLeader = false

	// MariaDB does not have an equivalent to UPDATE ... RETURNING, so let's do that as transaction
	tx, err := l.connection.connection.Begin()
	defer tx.Rollback()
	if err != nil {
		return
	}
	_, err = tx.Exec("UPDATE bouncer_election SET nodeid=? WHERE id=? AND time < TIMESTAMPADD(SECOND, -30, CURRENT_TIMESTAMP());", l.nodeID, lock)
	if err != nil {
		return
	}
	result, err := tx.Query("SELECT nodeid FROM bouncer_election WHERE id=?", lock)
	if err != nil {
		return
	}
	defer result.Close()
	if !result.Next() {
		err = errors.New("unexpected empty result")
		return
	}
	var actualNodeID string
	err = result.Scan(&actualNodeID)
	if err != nil {
		return
	}
	result.Close()
	err = tx.Commit()
	if err != nil {
		return
	}
	isLeader = actualNodeID == l.nodeID
	leaderName = actualNodeID
	return
}

// Refresh refreshes the given lock. If the current node doesn't hold it, this is a no-op
func (l *LeaderElect) Refresh(lock int, connection *sql.DB) (err error) {
	_, err = connection.Exec("UPDATE bouncer_election SET time=CURRENT_TIMESTAMP() WHERE id=? AND nodeid=?;", lock, l.nodeID)
	return
}

func (l *LeaderElect) updateOrDie(lock int) {
	myConnection := CopyHandler(l.connection)
	for {
		err := l.Refresh(lock, myConnection.connection)
		if err != nil {
			log.WithError(err).Fatal("Error updating log time!")
		}
		time.Sleep(time.Duration(15+rand.Intn(5)) * time.Second)

		// Sanity check
		result, err := myConnection.connection.Query("SELECT nodeid FROM bouncer_election WHERE id=?", lock)
		if err != nil {
			log.WithError(err).Fatal("Error checking my leader lock!")
		}
		defer result.Close()
		if !result.Next() {
			log.Warn("Error checking my leader lock - empty result?")
		}
		var actualNodeID string
		err = result.Scan(&actualNodeID)
		if err != nil {
			log.WithError(err).Fatal("Error checking my leader lock!")
		}
		if actualNodeID != l.nodeID {
			log.WithFields(log.Fields{
				"nodeID":       l.nodeID,
				"actualNodeID": actualNodeID,
			}).Fatal("Lost leader lock, aborting!")
		}
	}
}

func (l *LeaderElect) releaseLockOnShutdown(lock int) {
	myConnection := CopyHandler(l.connection)

	handler := func() {
		_, err := myConnection.connection.Exec("UPDATE bouncer_election SET time='0000-00-00 00:00:00', nodeid=NULL WHERE id=? AND nodeid=?;", lock, l.nodeID)
		if err != nil {
			log.WithError(err).Warn("Couldn't release lock on shutdown!")
		} else {
			log.Info("Leader lock released")
		}
	}

	sigChan := make(chan os.Signal)
	signal.Notify(sigChan, os.Interrupt, os.Kill)

	go func() {
		<-sigChan
		handler()
		os.Exit(0)
	}()

	log.RegisterExitHandler(handler)
}

// EnsureLock blocks until the given lock is acquired successfully. If we ever loose it, it will terminate the process
func (l *LeaderElect) EnsureLock(lock int) {
	if l.connection.connection == nil {
		l.connection.Connect()
	}

	l.Prepare(lock)

	for {
		haveLock, leaderName, err := l.TryAcquire(lock)
		if haveLock {
			// Yay, we got the lock!
			go l.updateOrDie(lock)

			// Since we hold it, let's explicitly release it on exit to save time
			l.releaseLockOnShutdown(lock)
			return
		}
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"haveLock":   haveLock,
				"id":         lock,
				"leaderName": leaderName,
			}).Warn("Didn't acquire lock")
		} else {
			log.WithFields(log.Fields{
				"haveLock":   haveLock,
				"id":         lock,
				"leaderName": leaderName,
			}).Debug("Didn't acquire lock")
		}
		time.Sleep(time.Duration(15+rand.Intn(15)) * time.Second)
	}
}
