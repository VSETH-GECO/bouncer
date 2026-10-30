package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/VSETH-GECO/bouncer/migrations"
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/golang-migrate/migrate/v4"
	mysqlDriver "github.com/golang-migrate/migrate/v4/database/mysql"
	bindata "github.com/golang-migrate/migrate/v4/source/go_bindata"
	log "github.com/sirupsen/logrus"
)

// ErrNotFound is returned (wrapped) by single-row lookups that found no matching row.
// Check for it with errors.Is(err, database.ErrNotFound).
var ErrNotFound = errors.New("not found")

// DBTX is the subset of methods shared by *sql.DB and *sql.Tx, so that queries can run either inside or outside of a
// transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// noRows maps sql.ErrNoRows to ErrNotFound and leaves every other error untouched.
func noRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// RadiusSession describes details of a (running) RADIUS RadiusSession
type RadiusSession struct {
	Uid           string
	StartDate     sql.NullTime
	EndDate       sql.NullTime
	EndReason     string
	SwitchIP      net.IP
	SwitchPort    string
	BytesSent     int
	BytesReceived int
	ClientIP      net.IP
}

// Handler is responsible for handling database-related tasks. It wraps a single *sql.DB pool which is safe for
// concurrent use by all goroutines of the daemon.
type Handler struct {
	db *sql.DB
}

// Open connects to the database and verifies the connection with a ping, so that bad credentials or an unreachable
// server are reported at startup.
func Open(ctx context.Context, host string, port int, user string, password string, database string) (*Handler, error) {
	log.Info("Establishing new DB connection")
	dsn := user + ":" + password + "@tcp(" + host + ":" + strconv.Itoa(port) + ")/" + database + "?multiStatements=true&parseTime=true"
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return &Handler{db: db}, nil
}

// OpenFromConfig connects to the database, pulling the connection parameters from our config
func OpenFromConfig(ctx context.Context) (*Handler, error) {
	return Open(ctx,
		config.CurrentOptions.DBHost,
		config.CurrentOptions.DBPort,
		config.CurrentOptions.DBUser,
		config.CurrentOptions.DBPassword,
		config.CurrentOptions.DBDatabase,
	)
}

// Close closes the underlying connection pool
func (h *Handler) Close() error {
	return h.db.Close()
}

// DB returns the connection pool for queries that should not run inside a transaction
func (h *Handler) DB() DBTX {
	return h.db
}

// WithTx runs fn inside a transaction. The transaction is committed if fn returns nil and rolled back if fn returns an
// error or panics (the panic is re-raised after the rollback).
func (h *Handler) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		// A cancelled context rolls the transaction back on its own, so ErrTxDone is expected here
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("rolling back transaction: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

// Migrate updates the database schema to the current version. The migrate library does not take a context; ctx is
// accepted for consistency with the other database methods.
func (h *Handler) Migrate(ctx context.Context, force int) error {
	s := bindata.Resource(migrations.AssetNames(),
		func(name string) ([]byte, error) {
			val, err := migrations.Asset(name)
			return val, err
		})
	source, err := bindata.WithInstance(s)
	if err != nil {
		return fmt.Errorf("loading migration sources: %w", err)
	}
	driver, err := mysqlDriver.WithInstance(h.db, &mysqlDriver.Config{})
	if err != nil {
		return fmt.Errorf("creating migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("go-bindata", source, "radius", driver)
	if err != nil {
		return fmt.Errorf("creating migrator: %w", err)
	}
	if force == 0 {
		err = m.Up()
	} else {
		err = m.Force(force)
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

// CheckDBVersion ensures that we're running with the right database version. The migrate library does not take a
// context; ctx is accepted for consistency with the other database methods.
func (h *Handler) CheckDBVersion(ctx context.Context) error {
	s := bindata.Resource(migrations.AssetNames(),
		func(name string) ([]byte, error) {
			val, err := migrations.Asset(name)
			return val, err
		})
	source, err := bindata.WithInstance(s)
	if err != nil {
		return fmt.Errorf("loading migration sources: %w", err)
	}
	driver, err := mysqlDriver.WithInstance(h.db, &mysqlDriver.Config{})
	if err != nil {
		return fmt.Errorf("creating migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("go-bindata", source, "radius", driver)
	if err != nil {
		return fmt.Errorf("creating migrator: %w", err)
	}
	version, dirty, err := m.Version()
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	names := s.Names
	sort.Strings(names)
	lastMigration := names[len(names)-1]
	nameParts := strings.Split(lastMigration, "_")
	if len(nameParts) < 2 {
		return fmt.Errorf("unexpected migration resource name %q", lastMigration)
	}
	latestVersion, err := strconv.ParseUint(nameParts[0], 10, 16)
	if err != nil {
		return fmt.Errorf("parsing version of migration %q: %w", lastMigration, err)
	}
	entry := log.WithFields(log.Fields{
		"latestVersion": latestVersion,
		"version":       version,
		"dirty":         dirty,
	})
	if uint(latestVersion) == version {
		entry.Debug("Database version is matching newest version at build time")
	} else if uint(latestVersion) > version {
		return fmt.Errorf("database schema version %d is older than newest version %d at build time", version, latestVersion)
	} else {
		entry.Warn("Database version is newer than newest version at build time")
	}
	if dirty {
		entry.Warn("Database is marked dirty!")
	}
	return nil
}
