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

// jobRows counts the bouncer_jobs rows for mac and deletes them when the test ends.
func jobRows(t *testing.T, d *db.DB, mac string) func() int {
	t.Helper()
	t.Cleanup(func() {
		_, _ = d.SQL().ExecContext(context.Background(), "DELETE FROM bouncer_jobs WHERE clientMAC = ?", mac)
	})
	return func() int {
		t.Helper()
		var n int
		if err := d.SQL().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM bouncer_jobs WHERE clientMAC = ?", mac).Scan(&n); err != nil {
			t.Fatalf("counting rows: %v", err)
		}
		return n
	}
}

// insertJob inserts a bouncer_jobs row for mac through q.
func insertJob(ctx context.Context, q db.Querier, mac string) error {
	_, err := table.BouncerJobs.INSERT(table.BouncerJobs.ClientMAC, table.BouncerJobs.TargetVLAN).
		VALUES(mac, 42).ExecContext(ctx, q)
	return err
}

func TestWithTx(t *testing.T) {
	d := testdb.Shared(t)
	ctx := context.Background()
	const mac = "020000000101"
	count := jobRows(t, d, mac)
	insert := func(tx db.Querier) error { return insertJob(ctx, tx, mac) }

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
	count := jobRows(t, d, mac)

	recovered := func() (r any) {
		defer func() { r = recover() }()
		_ = d.WithTx(ctx, func(tx db.Querier) error {
			if err := insertJob(ctx, tx, mac); err != nil {
				t.Fatalf("insert: %v", err)
			}
			panic("boom")
		})
		return nil
	}()
	if recovered != "boom" {
		t.Fatalf("recovered = %v, want boom", recovered)
	}
	if n := count(); n != 0 {
		t.Fatalf("rows after panic = %d, want 0", n)
	}
}
