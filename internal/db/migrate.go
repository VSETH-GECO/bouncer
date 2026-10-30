package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/VSETH-GECO/bouncer/migrations"
	"github.com/golang-migrate/migrate/v4"
	mysqlDriver "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	log "github.com/sirupsen/logrus"
)

// newMigrator builds a migrator over the embedded migrations and a connection of its own taken from the pool. The
// caller must close it with closeMigrator, which returns the connection to the pool.
func (d *DB) newMigrator(ctx context.Context) (*migrate.Migrate, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("loading migration sources: %w", err)
	}
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("taking a connection for migrations: %w", err)
	}
	driver, err := mysqlDriver.WithConnection(ctx, conn, &mysqlDriver.Config{})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("creating migration driver: %w", err), conn.Close())
	}
	m, err := migrate.NewWithInstance("iofs", source, "radius", driver)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("creating migrator: %w", err), conn.Close())
	}
	return m, nil
}

// closeMigrator closes m, which closes its connection and so returns it to the pool.
func closeMigrator(m *migrate.Migrate) error {
	srcErr, dbErr := m.Close()
	var errs []error
	if srcErr != nil {
		errs = append(errs, fmt.Errorf("closing migration source: %w", srcErr))
	}
	if dbErr != nil {
		errs = append(errs, fmt.Errorf("closing migration database: %w", dbErr))
	}
	return errors.Join(errs...)
}

// latestMigrationVersion returns the largest version among the embedded migrations.
func latestMigrationVersion() (uint, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return 0, fmt.Errorf("listing embedded migrations: %w", err)
	}
	var latest uint64
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix, _, _ := strings.Cut(name, "_")
		v, err := strconv.ParseUint(prefix, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("parsing version of migration %q: %w", name, err)
		}
		if !found || v > latest {
			latest, found = v, true
		}
	}
	if !found {
		return 0, errors.New("no embedded migrations found")
	}
	return uint(latest), nil
}

// Migrate updates the database schema to the current version. ctx bounds taking the migrator's connection; the
// migrate library itself does not take a context.
func (d *DB) Migrate(ctx context.Context, force int) (err error) {
	m, err := d.newMigrator(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeMigrator(m)) }()
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

// CheckVersion ensures that we're running with the right database version. ctx bounds taking the migrator's
// connection; the migrate library itself does not take a context.
func (d *DB) CheckVersion(ctx context.Context) (err error) {
	m, err := d.newMigrator(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeMigrator(m)) }()
	version, dirty, err := m.Version()
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	latestVersion, err := latestMigrationVersion()
	if err != nil {
		return err
	}
	entry := log.WithFields(log.Fields{
		"latestVersion": latestVersion,
		"version":       version,
		"dirty":         dirty,
	})
	if latestVersion == version {
		entry.Debug("Database version is matching newest version at build time")
	} else if latestVersion > version {
		return fmt.Errorf("database schema version %d is older than newest version %d at build time", version, latestVersion)
	} else {
		entry.Warn("Database version is newer than newest version at build time")
	}
	if dirty {
		entry.Warn("Database is marked dirty!")
	}
	return nil
}
