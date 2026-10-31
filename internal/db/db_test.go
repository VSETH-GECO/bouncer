package db_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/go-jet/jet/v2/qrm"
	"github.com/go-sql-driver/mysql"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestNotFound(t *testing.T) {
	if err := db.NotFound(qrm.ErrNoRows); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("NotFound(qrm.ErrNoRows) = %v, want ErrNotFound", err)
	}
	if err := db.NotFound(sql.ErrNoRows); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("NotFound(sql.ErrNoRows) = %v, want ErrNotFound", err)
	}
	if err := db.NotFound(nil); err != nil {
		t.Errorf("NotFound(nil) = %v, want nil", err)
	}
	if err := db.NotFound(io.EOF); err != io.EOF {
		t.Errorf("NotFound(io.EOF) = %v, want io.EOF", err)
	}
}

func TestConfig_DSN(t *testing.T) {
	cfg := db.Config{Host: "db.example", Port: 33306, User: "bouncer", Password: "s3cret", Database: "freeradius"}
	parsed, err := mysql.ParseDSN(cfg.DSN())
	if err != nil {
		t.Fatalf("ParseDSN(%q): %v", cfg.DSN(), err)
	}
	if parsed.Net != "tcp" || parsed.Addr != "db.example:33306" || parsed.User != "bouncer" ||
		parsed.Passwd != "s3cret" || parsed.DBName != "freeradius" {
		t.Errorf("parsed = %+v", parsed)
	}
	if !parsed.MultiStatements || !parsed.ParseTime {
		t.Errorf("multiStatements=%v parseTime=%v, want both true", parsed.MultiStatements, parsed.ParseTime)
	}
	if !parsed.AllowNativePasswords {
		t.Error("allowNativePasswords was disabled; build the DSN from mysql.NewConfig(), not a bare literal")
	}
}

func TestOpenCheckVersion(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, testdb.SharedConfig(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.CheckVersion(ctx); err != nil {
		t.Fatalf("CheckVersion: %v", err)
	}
	if err := d.Migrate(ctx, 0); err != nil {
		t.Fatalf("Migrate on a migrated database: %v", err)
	}
	if inUse := d.SQL().Stats().InUse; inUse != 0 {
		t.Errorf("connections in use after CheckVersion and Migrate = %d, want 0: the migrator did not release its connection", inUse)
	}
}

func TestOpen_SessionTimeZoneIsUTC(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, testdb.SharedConfig(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var tz string
	if err := d.SQL().QueryRowContext(ctx, "SELECT @@session.time_zone").Scan(&tz); err != nil {
		t.Fatal(err)
	}
	if tz != "+00:00" {
		t.Errorf("session time_zone = %q, want %q", tz, "+00:00")
	}
}

func TestOpen_BadPassword(t *testing.T) {
	cfg := testdb.SharedConfig(t)
	cfg.Password = "wrong"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d, err := db.Open(ctx, cfg)
	if err == nil {
		d.Close()
		t.Fatal("Open with wrong password succeeded")
	}
	if d != nil {
		t.Errorf("Open returned a *DB alongside error %v", err)
	}
}

// logRows counts the bouncer_log rows for mac and deletes them when the test ends. The probe rows live in bouncer_log
// rather than bouncer_jobs so that a committed probe can never be picked up by another package's work loop.
func logRows(t *testing.T, d *db.DB, mac string) func() int {
	t.Helper()
	t.Cleanup(func() {
		_, _ = d.SQL().ExecContext(context.Background(), "DELETE FROM bouncer_log WHERE clientMAC = ?", mac)
	})
	return func() int {
		t.Helper()
		var n int
		if err := d.SQL().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM bouncer_log WHERE clientMAC = ?", mac).Scan(&n); err != nil {
			t.Fatalf("counting rows: %v", err)
		}
		return n
	}
}

// insertLog inserts a bouncer_log row for mac through q.
func insertLog(ctx context.Context, q db.Querier, mac string) error {
	_, err := table.BouncerLog.INSERT(table.BouncerLog.ClientMAC, table.BouncerLog.OldVLAN, table.BouncerLog.NewVLAN).
		VALUES(mac, 10, 42).ExecContext(ctx, q)
	return err
}

func TestWithTx(t *testing.T) {
	d := testdb.Shared(t)
	ctx := context.Background()
	const mac = "020000000101"
	count := logRows(t, d, mac)
	insert := func(tx db.Querier) error { return insertLog(ctx, tx, mac) }

	boom := errors.New("boom")
	err := d.WithTx(ctx, func(tx db.Querier) error {
		if err := insert(tx); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx error = %v, want boom", err)
	}
	if n := count(); n != 0 {
		t.Fatalf("rows after rollback = %d, want 0", n)
	}

	if err := d.WithTx(ctx, insert); err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}
	if n := count(); n != 1 {
		t.Fatalf("rows after commit = %d, want 1", n)
	}
}

func TestWithTx_PanicRollsBack(t *testing.T) {
	d := testdb.Shared(t)
	ctx := context.Background()
	const mac = "020000000102"
	count := logRows(t, d, mac)

	var txErr error
	recovered := func() (r any) {
		defer func() { r = recover() }()
		txErr = d.WithTx(ctx, func(tx db.Querier) error {
			if err := insertLog(ctx, tx, mac); err != nil {
				return err
			}
			panic("boom")
		})
		return nil
	}()
	if txErr != nil {
		t.Fatalf("WithTx: %v", txErr)
	}
	if recovered != "boom" {
		t.Fatalf("recovered = %v, want boom", recovered)
	}
	if inUse := d.SQL().Stats().InUse; inUse != 0 {
		t.Errorf("connections in use after panic = %d, want 0: the transaction's connection was not released", inUse)
	}
	if n := count(); n != 0 {
		t.Fatalf("rows after panic = %d, want 0", n)
	}
}
