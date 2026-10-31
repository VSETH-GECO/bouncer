// Package db owns the database connection pool, transactions, schema migrations and the not-found error. Queries
// themselves are built with the generated jet code under internal/db/gen.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/go-sql-driver/mysql"
)

// Config holds the connection parameters of the database.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

// DSN returns the go-sql-driver DSN for the configuration. It starts from the driver's defaults (mysql.NewConfig),
// so settings such as allowNativePasswords are kept. multiStatements lets migrations run several statements per
// Exec, parseTime makes DATETIME columns scan into time.Time. The driver parses time values as UTC, so the session's
// time_zone is pinned to UTC too, whatever the server's default is: the server then renders TIMESTAMP columns (such as
// radpostauth.authdate, bouncer_jobs.last_update, radacct's times and Kea's lease expiry) in the zone the driver
// reads them in, and interprets the times bouncer sends in the same zone.
func (c Config) DSN() string {
	mc := mysql.NewConfig()
	mc.User = c.User
	mc.Passwd = c.Password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	mc.DBName = c.Database
	mc.MultiStatements = true
	mc.ParseTime = true
	mc.Params = map[string]string{"time_zone": "'+00:00'"}
	return mc.FormatDSN()
}

// Querier is the subset of methods shared by *sql.DB and *sql.Tx, so that queries can run either inside or outside of
// a transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// DB wraps a single *sql.DB pool which is safe for concurrent use by all goroutines of the daemon.
type DB struct {
	sql *sql.DB
}

// Open connects to the database and verifies the connection with a ping, so that bad credentials or an unreachable
// server are reported at startup.
func Open(ctx context.Context, cfg Config) (*DB, error) {
	pool, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	pool.SetConnMaxLifetime(3 * time.Minute)
	pool.SetConnMaxIdleTime(time.Minute)
	pool.SetMaxOpenConns(10)
	pool.SetMaxIdleConns(5)
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	// Make the generated SQL qualify tables with the configured database. This is process-global, which is fine
	// because the daemon talks to exactly one database.
	table.UseSchema(cfg.Database)
	return &DB{sql: pool}, nil
}

// Close closes the underlying connection pool.
func (d *DB) Close() error {
	return d.sql.Close()
}

// Querier returns the connection pool for queries that should not run inside a transaction.
func (d *DB) Querier() Querier {
	return d.sql
}

// SQL returns the underlying *sql.DB.
func (d *DB) SQL() *sql.DB {
	return d.sql
}

// BeginTx starts a transaction with default options. Prefer WithTx unless the transaction must outlive one function.
func (d *DB) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return d.sql.BeginTx(ctx, nil)
}
