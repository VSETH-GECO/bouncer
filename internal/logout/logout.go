// Package logout stores global logouts (bouncer_logout, bouncer_logout_approval) and changes their state. The rules
// for who may change what, and when, are pkg/controller's.
package logout

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/go-jet/jet/v2/mysql"
)

// State is where a logout is in its lifecycle.
type State string

const (
	// StateProposed waits for a second admin.
	StateProposed State = "proposed"
	// StateArmed has its approvals and waits for its time.
	StateArmed State = "armed"
	// StateRunning has started; it ends once its lockout is over and its moves are done.
	StateRunning State = "running"
	// The rest are final.
	StateDone      State = "done"
	StateCancelled State = "cancelled"
	StateExpired   State = "expired"
	StateAborted   State = "aborted"
)

// Logout is a global logout. Absent values are nil. LastWarning is the minutes before the logout of the last warning
// posted.
type Logout struct {
	ID                       int
	ScheduledFor             time.Time
	State                    State
	LastWarning              *int
	StartedAt, LockoutEndsAt *time.Time
	CancelledBy              *string
	CoAFailures, IgnoredJobs int
}

func fromRow(r model.BouncerLogout) Logout {
	l := Logout{
		ID:            int(r.ID),
		ScheduledFor:  r.ScheduledFor,
		State:         State(r.State),
		StartedAt:     r.StartedAt,
		LockoutEndsAt: r.LockoutEndsAt,
		CancelledBy:   r.CancelledBy,
		CoAFailures:   int(r.CoaFailures),
		IgnoredJobs:   int(r.IgnoredJobs),
	}
	if r.LastWarning != nil {
		w := int(*r.LastWarning)
		l.LastWarning = &w
	}
	return l
}

// InFlight returns the logout that is proposed, armed or running. It returns an error wrapping db.ErrNotFound if there
// is none.
func InFlight(ctx context.Context, q db.Querier) (Logout, error) {
	l := table.BouncerLogout
	var row model.BouncerLogout
	err := mysql.SELECT(l.AllColumns).
		FROM(l).
		WHERE(l.InFlight.EQ(mysql.Int(1))).
		QueryContext(ctx, q, &row)
	if err != nil {
		return Logout{}, fmt.Errorf("looking up the logout in flight: %w", db.NotFound(err))
	}
	return fromRow(row), nil
}

// Get returns the logout with the given id. It returns an error wrapping db.ErrNotFound if there is none.
func Get(ctx context.Context, q db.Querier, id int) (Logout, error) {
	l := table.BouncerLogout
	var row model.BouncerLogout
	err := mysql.SELECT(l.AllColumns).
		FROM(l).
		WHERE(l.ID.EQ(mysql.Int(int64(id)))).
		QueryContext(ctx, q, &row)
	if err != nil {
		return Logout{}, fmt.Errorf("looking up logout %d: %w", id, db.NotFound(err))
	}
	return fromRow(row), nil
}

// Lock returns the logout with the given id and locks its row until the transaction q ends. It returns an error
// wrapping db.ErrNotFound if there is none.
func Lock(ctx context.Context, q db.Querier, id int) (Logout, error) {
	l := table.BouncerLogout
	var row model.BouncerLogout
	err := mysql.SELECT(l.AllColumns).
		FROM(l).
		WHERE(l.ID.EQ(mysql.Int(int64(id)))).
		FOR(mysql.UPDATE()).
		QueryContext(ctx, q, &row)
	if err != nil {
		return Logout{}, fmt.Errorf("locking logout %d: %w", id, db.NotFound(err))
	}
	return fromRow(row), nil
}

// Create inserts a proposal for the time at and returns its id. It fails with a conflict (see db.IsConflict) if a logout
// is already in flight.
func Create(ctx context.Context, q db.Querier, at time.Time) (int, error) {
	l := table.BouncerLogout
	res, err := l.INSERT(l.ScheduledFor, l.State, l.InFlight).
		VALUES(mysql.DateTimeT(at), mysql.String(string(StateProposed)), mysql.Int(1)).
		ExecContext(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("creating a logout: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading the new logout's id: %w", err)
	}
	return int(id), nil
}

// Approve records admin's approval of the logout. It fails with a conflict (see db.IsConflict) if admin already
// approved it.
func Approve(ctx context.Context, q db.Querier, id int, admin string) error {
	a := table.BouncerLogoutApproval
	_, err := a.INSERT(a.LogoutID, a.AdminID).
		VALUES(mysql.Int(int64(id)), mysql.String(admin)).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("approving logout %d: %w", id, err)
	}
	return nil
}

// Approvers returns the admins who approved the logout, in the order they did.
func Approvers(ctx context.Context, q db.Querier, id int) ([]string, error) {
	a := table.BouncerLogoutApproval
	var rows []model.BouncerLogoutApproval
	err := mysql.SELECT(a.AllColumns).
		FROM(a).
		WHERE(a.LogoutID.EQ(mysql.Int(int64(id)))).
		ORDER_BY(a.ApprovedAt.ASC(), a.AdminID.ASC()).
		QueryContext(ctx, q, &rows)
	if err != nil {
		return nil, fmt.Errorf("looking up the approvers of logout %d: %w", id, err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.AdminID)
	}
	return out, nil
}

// changed runs a conditional update and reports whether it changed a row.
func changed(ctx context.Context, q db.Querier, stmt mysql.UpdateStatement, what string) (bool, error) {
	res, err := stmt.ExecContext(ctx, q)
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}
	return n == 1, nil
}

// setState moves the logout from state from to state to and reports whether it was in state from. A final state
// clears in_flight, which lets the next logout be created.
func setState(ctx context.Context, q db.Querier, id int, from, to State) (bool, error) {
	l := table.BouncerLogout
	var inFlight mysql.Expression = mysql.NULL
	if to == StateArmed || to == StateRunning {
		inFlight = mysql.Int(1)
	}
	return changed(ctx, q, l.UPDATE(l.State, l.InFlight).
		SET(mysql.String(string(to)), inFlight).
		WHERE(l.ID.EQ(mysql.Int(int64(id))).AND(l.State.EQ(mysql.String(string(from))))),
		fmt.Sprintf("moving logout %d from %s to %s", id, from, to))
}

// Arm moves the logout from proposed to armed and reports whether it was proposed.
func Arm(ctx context.Context, q db.Querier, id int) (bool, error) {
	return setState(ctx, q, id, StateProposed, StateArmed)
}

// Expire moves the logout from proposed to expired and reports whether it was proposed.
func Expire(ctx context.Context, q db.Querier, id int) (bool, error) {
	return setState(ctx, q, id, StateProposed, StateExpired)
}

// Abort moves the logout from armed to aborted and reports whether it was armed.
func Abort(ctx context.Context, q db.Querier, id int) (bool, error) {
	return setState(ctx, q, id, StateArmed, StateAborted)
}

// Finish moves the logout from running to done and reports whether it was running.
func Finish(ctx context.Context, q db.Querier, id int) (bool, error) {
	return setState(ctx, q, id, StateRunning, StateDone)
}

// Start moves the logout from armed to running with a lockout from at until ends, and reports whether it was armed.
func Start(ctx context.Context, q db.Querier, id int, at, ends time.Time) (bool, error) {
	l := table.BouncerLogout
	return changed(ctx, q, l.UPDATE(l.State, l.StartedAt, l.LockoutEndsAt).
		SET(mysql.String(string(StateRunning)), mysql.DateTimeT(at), mysql.DateTimeT(ends)).
		WHERE(l.ID.EQ(mysql.Int(int64(id))).AND(l.State.EQ(mysql.String(string(StateArmed))))),
		fmt.Sprintf("starting logout %d", id))
}

// Cancel moves the logout from any state in flight to cancelled, and reports whether it was in flight. A lockout ending
// after at ends at at instead.
func Cancel(ctx context.Context, q db.Querier, id int, by string, at time.Time) (bool, error) {
	l := table.BouncerLogout
	byID := l.ID.EQ(mysql.Int(int64(id)))
	done, err := changed(ctx, q, l.UPDATE(l.State, l.InFlight, l.CancelledBy).
		SET(mysql.String(string(StateCancelled)), mysql.NULL, mysql.String(by)).
		WHERE(byID.AND(l.InFlight.EQ(mysql.Int(1)))),
		fmt.Sprintf("cancelling logout %d", id))
	if err != nil || !done {
		return false, err
	}
	_, err = l.UPDATE(l.LockoutEndsAt).
		SET(mysql.DateTimeT(at)).
		WHERE(byID.AND(l.LockoutEndsAt.GT(mysql.DateTimeT(at)))).
		ExecContext(ctx, q)
	if err != nil {
		return false, fmt.Errorf("ending the lockout of logout %d: %w", id, err)
	}
	return true, nil
}

// SetLastWarning records the last warning posted, as the minutes before the logout.
func SetLastWarning(ctx context.Context, q db.Querier, id, minutes int) error {
	l := table.BouncerLogout
	_, err := l.UPDATE(l.LastWarning).
		SET(mysql.Int(int64(minutes))).
		WHERE(l.ID.EQ(mysql.Int(int64(id)))).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("recording a warning of logout %d: %w", id, err)
	}
	return nil
}

// AddCoAFailure counts a client moved although its CoA failed.
func AddCoAFailure(ctx context.Context, q db.Querier, id int) error {
	l := table.BouncerLogout
	_, err := l.UPDATE(l.CoaFailures).
		SET(l.CoaFailures.ADD(mysql.Int(1))).
		WHERE(l.ID.EQ(mysql.Int(int64(id)))).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("counting a CoA failure of logout %d: %w", id, err)
	}
	return nil
}

// AddIgnoredJob counts a job the lockout dropped.
func AddIgnoredJob(ctx context.Context, q db.Querier, id int) error {
	l := table.BouncerLogout
	_, err := l.UPDATE(l.IgnoredJobs).
		SET(l.IgnoredJobs.ADD(mysql.Int(1))).
		WHERE(l.ID.EQ(mysql.Int(int64(id)))).
		ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("counting an ignored job of logout %d: %w", id, err)
	}
	return nil
}

// Lockout is the lockout of a logout that has started.
type Lockout struct {
	LogoutID int
	Ends     time.Time
}

// CurrentLockout returns the lockout ending last among the logouts that have started, and false if none has. It may
// have ended already.
func CurrentLockout(ctx context.Context, q db.Querier) (Lockout, bool, error) {
	l := table.BouncerLogout
	var row model.BouncerLogout
	err := mysql.SELECT(l.AllColumns).
		FROM(l).
		WHERE(l.StartedAt.IS_NOT_NULL()).
		ORDER_BY(l.LockoutEndsAt.DESC()).
		LIMIT(1).
		QueryContext(ctx, q, &row)
	if err = db.NotFound(err); errors.Is(err, db.ErrNotFound) {
		return Lockout{}, false, nil
	}
	if err != nil {
		return Lockout{}, false, fmt.Errorf("looking up the current lockout: %w", err)
	}
	return Lockout{LogoutID: int(row.ID), Ends: *row.LockoutEndsAt}, true, nil
}

// Blocked reports whether the lockout rule drops a move to vlan last updated at at, and returns the lockout that does.
// A move is dropped if its target is neither the login VLAN nor exempt and it was last updated before the current
// lockout's end. Going by the job's time rather than the current time also catches jobs queued before or during the
// lockout but only reached afterwards.
func Blocked(ctx context.Context, q db.Querier, vlan int, at time.Time, exempt []int) (Lockout, bool, error) {
	if vlan == freeradius.LoginVLAN || slices.Contains(exempt, vlan) {
		return Lockout{}, false, nil
	}
	lockout, ok, err := CurrentLockout(ctx, q)
	if err != nil || !ok {
		return Lockout{}, false, err
	}
	return lockout, at.Before(lockout.Ends), nil
}
