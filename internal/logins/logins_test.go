package logins_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/logins"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func ptr[T any](v T) *T { return &v }

func begin(t *testing.T) (context.Context, *sql.Tx) {
	return context.Background(), testdb.Tx(t)
}

func seedLoginLog(t *testing.T, q db.Querier, username *string, mac *string, updatedAt time.Time) {
	t.Helper()
	row := model.LoginLogs{
		Username:  username,
		Mac:       mac,
		UpdatedAt: updatedAt,
		CreatedAt: updatedAt,
	}
	_, err := table.LoginLogs.INSERT(table.LoginLogs.MutableColumns).MODEL(row).ExecContext(context.Background(), q)
	if err != nil {
		t.Fatalf("seeding login log: %v", err)
	}
}

func TestFindMAC_ByUsernameNewestWins(t *testing.T) {
	ctx, q := begin(t)
	m1 := mac.MustParse("00:11:22:33:44:51")
	m2 := mac.MustParse("00:11:22:33:44:52")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	seedLoginLog(t, q, ptr("alice"), ptr(m1.Hex()), base)
	seedLoginLog(t, q, ptr("alice"), ptr(m2.Hex()), base.Add(time.Hour))

	got, err := logins.FindMAC(ctx, q, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got != m2 {
		t.Errorf("got %v, want %v", got, m2)
	}

	// Test case-insensitive match
	got, err = logins.FindMAC(ctx, q, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if got != m2 {
		t.Errorf("got %v with 'Alice', want %v", got, m2)
	}
}

func TestFindMAC_MatchesStoredCaseVariants(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := mac.MustParse("00:aa:bb:cc:dd:ee")

	t.Run("uppercase_stored", func(t *testing.T) {
		ctx, q := begin(t)
		// Seed with uppercase hex form only
		seedLoginLog(t, q, ptr("user_upper"), ptr("00AABBCCDDEE"), base)

		// FindMAC should find it with uppercase hex form
		got, err := logins.FindMAC(ctx, q, "00AABBCCDDEE")
		if err != nil {
			t.Fatal(err)
		}
		if got != m {
			t.Errorf("got %v, want %v", got, m)
		}
	})

	t.Run("lowercase_stored", func(t *testing.T) {
		ctx, q := begin(t)
		// Seed with lowercase hex form only
		seedLoginLog(t, q, ptr("user_lower"), ptr("00aabbccddee"), base)

		// FindMAC should find it with lowercase hex form
		got, err := logins.FindMAC(ctx, q, "00aabbccddee")
		if err != nil {
			t.Fatal(err)
		}
		if got != m {
			t.Errorf("got %v, want %v", got, m)
		}
	})

	t.Run("invalid_mac", func(t *testing.T) {
		ctx, q := begin(t)
		// Seed invalid MAC with username "bob"
		seedLoginLog(t, q, ptr("bob"), ptr("zz"), base)

		// Should return non-ErrNotFound error for bob
		_, err := logins.FindMAC(ctx, q, "bob")
		if err == nil {
			t.Fatal("expected error for invalid MAC")
		}
		if errors.Is(err, db.ErrNotFound) {
			t.Fatalf("expected non-ErrNotFound error for invalid MAC, got %v", err)
		}
	})
}

func TestLatestUsername(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:53")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	seedLoginLog(t, q, ptr("alice"), ptr(m.Hex()), base)
	seedLoginLog(t, q, ptr("alice_new"), ptr(m.Hex()), base.Add(time.Hour))

	got, err := logins.LatestUsername(ctx, q, m)
	if err != nil {
		t.Fatal(err)
	}
	if got != "alice_new" {
		t.Errorf("got %q, want %q", got, "alice_new")
	}

	// Unknown MAC should return ErrNotFound
	unknown := mac.MustParse("00:11:22:33:44:54")
	_, err = logins.LatestUsername(ctx, q, unknown)
	if !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("unknown MAC: err = %v, want ErrNotFound", err)
	}
}

func TestLatestUsername_MatchesUppercaseStored(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:aa:bb:cc:dd:ff")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Seed with uppercase hex form of the MAC
	seedLoginLog(t, q, ptr("user_test"), ptr("00AABBCCDDFF"), base)

	// Lookup with lowercase hex form should still find it
	got, err := logins.LatestUsername(ctx, q, m)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user_test" {
		t.Errorf("got %q, want %q", got, "user_test")
	}
}
