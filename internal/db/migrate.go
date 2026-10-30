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

// newMigrator builds a migrator over the embedded migrations and the connection pool.
func (d *DB) newMigrator() (*migrate.Migrate, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("loading migration sources: %w", err)
	}
	driver, err := mysqlDriver.WithInstance(d.sql, &mysqlDriver.Config{})
	if err != nil {
		return nil, fmt.Errorf("creating migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "radius", driver)
	if err != nil {
		return nil, fmt.Errorf("creating migrator: %w", err)
	}
	return m, nil
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

// Migrate updates the database schema to the current version. The migrate library does not take a context; ctx is
// accepted for consistency with the other database methods.
func (d *DB) Migrate(ctx context.Context, force int) error {
	m, err := d.newMigrator()
	if err != nil {
		return err
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

// CheckVersion ensures that we're running with the right database version. The migrate library does not take a
// context; ctx is accepted for consistency with the other database methods.
func (d *DB) CheckVersion(ctx context.Context) error {
	m, err := d.newMigrator()
	if err != nil {
		return err
	}
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
