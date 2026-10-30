package db_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/go-jet/jet/v2/qrm"
)

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

func config(inst *testdb.Instance) db.Config {
	return db.Config{Host: inst.Host, Port: inst.Port, User: inst.User, Password: inst.Password, Database: inst.Database}
}

func TestOpenMigrateCheck(t *testing.T) {
	inst := testdb.Start(t)
	ctx := context.Background()
	d, err := db.Open(ctx, config(inst))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.Migrate(ctx, 0); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := d.CheckVersion(ctx); err != nil {
		t.Fatalf("CheckVersion: %v", err)
	}
	if err := d.Migrate(ctx, 0); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestOpen_BadPassword(t *testing.T) {
	inst := testdb.Start(t)
	cfg := config(inst)
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

func TestWithTx(t *testing.T) {
	inst := testdb.Start(t)
	ctx := context.Background()
	d, err := inst.OpenDB(ctx)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(ctx, 0); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const mac = "001122334455"
	count := func() int {
		t.Helper()
		var n int
		if err := d.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM bouncer_jobs WHERE clientMAC = ?", mac).Scan(&n); err != nil {
			t.Fatalf("counting rows: %v", err)
		}
		return n
	}
	insert := func(tx db.Querier) error {
		_, err := table.BouncerJobs.INSERT(table.BouncerJobs.ClientMAC, table.BouncerJobs.TargetVLAN).
			VALUES(mac, 42).ExecContext(ctx, tx)
		return err
	}
	t.Cleanup(func() {
		_, _ = d.SQL().ExecContext(context.Background(), "DELETE FROM bouncer_jobs WHERE clientMAC = ?", mac)
	})

	boom := errors.New("boom")
	err = d.WithTx(ctx, func(tx db.Querier) error {
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
