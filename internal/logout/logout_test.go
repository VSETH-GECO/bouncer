package logout_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/logout"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var at = time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)

func begin(t *testing.T) (context.Context, *sql.Tx) {
	testdb.LockLogouts(t)
	return context.Background(), testdb.Tx(t)
}

func create(t *testing.T, ctx context.Context, q *sql.Tx) int {
	t.Helper()
	id, err := logout.Create(ctx, q, at)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return id
}

func get(t *testing.T, ctx context.Context, q *sql.Tx, id int) logout.Logout {
	t.Helper()
	l, err := logout.Get(ctx, q, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return l
}

func TestCreateAndInFlight(t *testing.T) {
	ctx, q := begin(t)
	if _, err := logout.InFlight(ctx, q); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("InFlight with none = %v, want ErrNotFound", err)
	}

	id := create(t, ctx, q)
	l, err := logout.InFlight(ctx, q)
	if err != nil {
		t.Fatalf("InFlight: %v", err)
	}
	if l.ID != id || l.State != logout.StateProposed || !l.ScheduledFor.Equal(at) || l.LastWarning != nil || l.StartedAt != nil {
		t.Errorf("InFlight = %+v", l)
	}
	locked, err := logout.Lock(ctx, q, id)
	if err != nil || locked.ID != id {
		t.Errorf("Lock = %+v, %v", locked, err)
	}
}

func TestOnlyOneInFlight(t *testing.T) {
	ctx, q := begin(t)
	create(t, ctx, q)
	_, err := logout.Create(ctx, q, at.Add(time.Hour))
	if !db.IsConflict(err) {
		t.Fatalf("second Create = %v, want a conflict", err)
	}
}

func TestApprovers(t *testing.T) {
	ctx, q := begin(t)
	id := create(t, ctx, q)
	for _, admin := range []string{"a", "b"} {
		if err := logout.Approve(ctx, q, id, admin); err != nil {
			t.Fatalf("Approve(%s): %v", admin, err)
		}
	}
	got, err := logout.Approvers(ctx, q, id)
	if err != nil {
		t.Fatalf("Approvers: %v", err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Approvers = %v, want %v", got, want)
	}
	if err := logout.Approve(ctx, q, id, "a"); !db.IsConflict(err) {
		t.Errorf("second approval by a = %v, want a conflict", err)
	}
}

func TestTransitions(t *testing.T) {
	type change func(ctx context.Context, q db.Querier, id int) (bool, error)
	start := func(ctx context.Context, q db.Querier, id int) (bool, error) {
		return logout.Start(ctx, q, id, at, at.Add(15*time.Minute))
	}
	for _, tt := range []struct {
		name     string
		before   []change
		change   change
		wantOK   bool
		want     logout.State
		inFlight bool
	}{
		{"arm", nil, logout.Arm, true, logout.StateArmed, true},
		{"expire", nil, logout.Expire, true, logout.StateExpired, false},
		{"abort", []change{logout.Arm}, logout.Abort, true, logout.StateAborted, false},
		{"start", []change{logout.Arm}, start, true, logout.StateRunning, true},
		{"finish", []change{logout.Arm, start}, logout.Finish, true, logout.StateDone, false},
		{"arm twice", []change{logout.Arm}, logout.Arm, false, logout.StateArmed, true},
		{"start a proposal", nil, start, false, logout.StateProposed, true},
		{"abort a proposal", nil, logout.Abort, false, logout.StateProposed, true},
		{"finish an armed logout", []change{logout.Arm}, logout.Finish, false, logout.StateArmed, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, q := begin(t)
			id := create(t, ctx, q)
			for _, c := range tt.before {
				if ok, err := c(ctx, q, id); err != nil || !ok {
					t.Fatalf("setup = %v, %v", ok, err)
				}
			}
			ok, err := tt.change(ctx, q, id)
			if err != nil {
				t.Fatalf("change: %v", err)
			}
			if ok != tt.wantOK {
				t.Errorf("change reported %v, want %v", ok, tt.wantOK)
			}
			if got := get(t, ctx, q, id); got.State != tt.want {
				t.Errorf("state = %s, want %s", got.State, tt.want)
			}
			_, err = logout.InFlight(ctx, q)
			if tt.inFlight && err != nil {
				t.Errorf("InFlight = %v, want the logout", err)
			}
			if !tt.inFlight && !errors.Is(err, db.ErrNotFound) {
				t.Errorf("InFlight = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestStartSetsTheLockout(t *testing.T) {
	ctx, q := begin(t)
	id := create(t, ctx, q)
	logout.Arm(ctx, q, id)
	ends := at.Add(15 * time.Minute)
	if ok, err := logout.Start(ctx, q, id, at, ends); err != nil || !ok {
		t.Fatalf("Start = %v, %v", ok, err)
	}
	l := get(t, ctx, q, id)
	if l.StartedAt == nil || !l.StartedAt.Equal(at) || l.LockoutEndsAt == nil || !l.LockoutEndsAt.Equal(ends) {
		t.Errorf("after Start: %+v", l)
	}
}

func TestCancel(t *testing.T) {
	ctx, q := begin(t)
	id := create(t, ctx, q)
	ok, err := logout.Cancel(ctx, q, id, "a", at)
	if err != nil || !ok {
		t.Fatalf("Cancel = %v, %v", ok, err)
	}
	l := get(t, ctx, q, id)
	if l.State != logout.StateCancelled || l.CancelledBy == nil || *l.CancelledBy != "a" || l.LockoutEndsAt != nil {
		t.Errorf("after Cancel: %+v", l)
	}
	if ok, err := logout.Cancel(ctx, q, id, "b", at); err != nil || ok {
		t.Errorf("second Cancel = %v, %v, want false", ok, err)
	}
	// The next logout can start now.
	create(t, ctx, q)
}

func TestCancelEndsTheLockoutEarly(t *testing.T) {
	ctx, q := begin(t)
	id := create(t, ctx, q)
	logout.Arm(ctx, q, id)
	logout.Start(ctx, q, id, at, at.Add(15*time.Minute))

	cancelAt := at.Add(time.Minute)
	if ok, err := logout.Cancel(ctx, q, id, "a", cancelAt); err != nil || !ok {
		t.Fatalf("Cancel = %v, %v", ok, err)
	}
	if l := get(t, ctx, q, id); l.LockoutEndsAt == nil || !l.LockoutEndsAt.Equal(cancelAt) {
		t.Errorf("LockoutEndsAt = %v, want %v", l.LockoutEndsAt, cancelAt)
	}
}

func TestCancelDuringTheDrainKeepsTheLockoutEnd(t *testing.T) {
	ctx, q := begin(t)
	id := create(t, ctx, q)
	logout.Arm(ctx, q, id)
	ends := at.Add(15 * time.Minute)
	logout.Start(ctx, q, id, at, ends)

	if ok, err := logout.Cancel(ctx, q, id, "a", ends.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("Cancel = %v, %v", ok, err)
	}
	if l := get(t, ctx, q, id); l.LockoutEndsAt == nil || !l.LockoutEndsAt.Equal(ends) {
		t.Errorf("LockoutEndsAt = %v, want %v", l.LockoutEndsAt, ends)
	}
}

func TestCounters(t *testing.T) {
	ctx, q := begin(t)
	id := create(t, ctx, q)
	if err := logout.SetLastWarning(ctx, q, id, 30); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := logout.AddCoAFailure(ctx, q, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := logout.AddIgnoredJob(ctx, q, id); err != nil {
		t.Fatal(err)
	}
	l := get(t, ctx, q, id)
	if l.LastWarning == nil || *l.LastWarning != 30 || l.CoAFailures != 2 || l.IgnoredJobs != 1 {
		t.Errorf("after the counters: %+v", l)
	}
}

func TestCurrentLockoutAndBlocked(t *testing.T) {
	ctx, q := begin(t)
	const vlan, exempt = 620, 630

	if _, ok, err := logout.CurrentLockout(ctx, q); err != nil || ok {
		t.Fatalf("CurrentLockout with none = %v, %v", ok, err)
	}
	if _, blocked, err := logout.Blocked(ctx, q, vlan, at, nil); err != nil || blocked {
		t.Fatalf("Blocked with none = %v, %v", blocked, err)
	}

	// A logout that has not started locks nothing out.
	id := create(t, ctx, q)
	logout.Arm(ctx, q, id)
	if _, blocked, err := logout.Blocked(ctx, q, vlan, at, nil); err != nil || blocked {
		t.Fatalf("Blocked before the start = %v, %v", blocked, err)
	}

	ends := at.Add(15 * time.Minute)
	logout.Start(ctx, q, id, at, ends)
	for _, tt := range []struct {
		name string
		vlan int
		at   time.Time
		want bool
	}{
		{"queued before the logout", vlan, at.Add(-time.Hour), true},
		{"queued during the lockout", vlan, at.Add(time.Minute), true},
		{"queued as it ends", vlan, ends, false},
		{"queued after it", vlan, ends.Add(time.Minute), false},
		{"to the login VLAN", freeradius.LoginVLAN, at.Add(time.Minute), false},
		{"to an exempt VLAN", exempt, at.Add(time.Minute), false},
	} {
		lockout, blocked, err := logout.Blocked(ctx, q, tt.vlan, tt.at, []int{exempt})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if blocked != tt.want {
			t.Errorf("%s: blocked = %v, want %v", tt.name, blocked, tt.want)
		}
		if blocked && (lockout.LogoutID != id || !lockout.Ends.Equal(ends)) {
			t.Errorf("%s: lockout = %+v", tt.name, lockout)
		}
	}
}
