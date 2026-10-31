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
	"github.com/VSETH-GECO/bouncer/internal/logins"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func begin(t *testing.T) (context.Context, *sql.Tx) {
	return context.Background(), testdb.Tx(t)
}

// login returns a mod that records a login of username from the MAC stored as macHex at the given time.
func login(username, macHex string, at time.Time) func(*model.LoginLogs) {
	return func(l *model.LoginLogs) {
		l.Username, l.Mac, l.CreatedAt, l.UpdatedAt = &username, &macHex, at, at
	}
}

func TestFindMAC_ByUsernameNewestWins(t *testing.T) {
	ctx, q := begin(t)
	p := testgen.Persistent(t, q)
	m1 := mac.MustParse("00:11:22:33:44:51")
	m2 := mac.MustParse("00:11:22:33:44:52")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	p.LoginLog(login("alice", m1.Hex(), base))
	p.LoginLog(login("alice", m2.Hex(), base.Add(time.Hour)))

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
		p := testgen.Persistent(t, q)
		// Seed with uppercase hex form only
		p.LoginLog(login("user_upper", "00AABBCCDDEE", base))

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
		p := testgen.Persistent(t, q)
		// Seed with lowercase hex form only
		p.LoginLog(login("user_lower", "00aabbccddee", base))

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
		p := testgen.Persistent(t, q)
		// Seed invalid MAC with username "bob"
		p.LoginLog(login("bob", "zz", base))

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
	p := testgen.Persistent(t, q)
	m := mac.MustParse("00:11:22:33:44:53")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	p.LoginLog(login("alice", m.Hex(), base))
	p.LoginLog(login("alice_new", m.Hex(), base.Add(time.Hour)))

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
	p := testgen.Persistent(t, q)
	m := mac.MustParse("00:aa:bb:cc:dd:ff")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Seed with uppercase hex form of the MAC
	p.LoginLog(login("user_test", "00AABBCCDDFF", base))

	// Lookup with lowercase hex form should still find it
	got, err := logins.LatestUsername(ctx, q, m)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user_test" {
		t.Errorf("got %q, want %q", got, "user_test")
	}
}

func TestFindMAC_MatchesStoredSeparatorForms(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("colon_stored", func(t *testing.T) {
		ctx, q := begin(t)
		m := mac.MustParse("aa:bb:cc:dd:ee:01")
		testgen.Persistent(t, q).LoginLog(login("user_colon", "AA:BB:CC:DD:EE:01", base))
		for _, search := range []string{"aabbccddee01", "aa:bb:cc:dd:ee:01", "AA-BB-CC-DD-EE-01"} {
			got, err := logins.FindMAC(ctx, q, search)
			if err != nil {
				t.Fatalf("FindMAC(%q): %v", search, err)
			}
			if got != m {
				t.Errorf("FindMAC(%q) = %v, want %v", search, got, m)
			}
		}
		if got, err := logins.LatestUsername(ctx, q, m); err != nil || got != "user_colon" {
			t.Errorf("LatestUsername = %q, %v, want user_colon", got, err)
		}
	})

	t.Run("hex_stored", func(t *testing.T) {
		ctx, q := begin(t)
		m := mac.MustParse("aa:bb:cc:dd:ee:02")
		testgen.Persistent(t, q).LoginLog(login("user_hex", "aabbccddee02", base))
		got, err := logins.FindMAC(ctx, q, "aa:bb:cc:dd:ee:02")
		if err != nil {
			t.Fatal(err)
		}
		if got != m {
			t.Errorf("got %v, want %v", got, m)
		}
	})

	t.Run("non_mac_search_matches_usernames_only", func(t *testing.T) {
		ctx, q := begin(t)
		m := mac.MustParse("aa:bb:cc:dd:ee:03")
		testgen.Persistent(t, q).LoginLog(login("frank", "AA:BB:CC:DD:EE:03", base))
		if _, err := logins.FindMAC(ctx, q, "aabbccddee"); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("FindMAC(partial MAC): err = %v, want ErrNotFound", err)
		}
		got, err := logins.FindMAC(ctx, q, "Frank")
		if err != nil {
			t.Fatal(err)
		}
		if got != m {
			t.Errorf("got %v, want %v", got, m)
		}
	})
}
