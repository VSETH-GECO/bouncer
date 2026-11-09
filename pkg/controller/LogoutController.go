package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/logout"
	"github.com/VSETH-GECO/bouncer/internal/mac"
)

const (
	// logoutMinNotice is how far ahead every admin scheduling a logout has to schedule it, so an armed logout always
	// has at least this much notice.
	logoutMinNotice = time.Hour
	// lockoutDuration is how long moves to other VLANs are ignored, counted from the logout's actual start.
	lockoutDuration = 15 * time.Minute
	// cancelClockSlack is how long before a logout's start a job can be stamped and still be dropped by a cancel. Jobs
	// carry the database's clock and started_at the pod's, and the first sweep stamps its jobs right at the start, so a
	// database running a second behind would otherwise keep them all.
	cancelClockSlack = time.Minute
	// logoutLateLimit is how long after its time a logout may still start. A later start would surprise everyone.
	logoutLateLimit = 5 * time.Minute
	// finalWarning is the last warning before a logout, in minutes. Without it, a logout does not start.
	finalWarning = 1
	// scheduleAttempts is how often Schedule runs its transaction when it conflicts with another admin's request. A
	// deadlock victim's first retry can still meet the winner's duplicate key, so one retry is not enough.
	scheduleAttempts = 3
)

// warningStages are the warnings before a logout, in minutes, smallest first.
var warningStages = []int{finalWarning, 5, 15, 30}

// errLogoutChanged reports that the logout in flight finished between reading and locking it.
var errLogoutChanged = errors.New("logout changed while scheduling")

// LogoutRefusal is a request the global logout rules turn down. Message is meant for the admin who asked.
type LogoutRefusal struct{ Message string }

func (r *LogoutRefusal) Error() string { return r.Message }

// FormatLogoutTime renders a logout's time for Discord: in UTC, and as a timestamp Discord shows in each viewer's local
// time.
func FormatLogoutTime(t time.Time) string {
	return fmt.Sprintf("%s UTC (<t:%d:F>)", t.UTC().Format("2006-01-02 15:04"), t.Unix())
}

// LogoutController holds the rules of the global logout. Schedule, Cancel, Status, ParseTime and VLANChangeBlocked are
// safe for concurrent use. Round is called from one goroutine.
type LogoutController struct {
	db          Store
	exemptVLANs []int
	now         func() time.Time

	// The rest belongs to Round, which the loop runner calls from one goroutine.
	// skipped holds the radreply usernames already warned about, so each is warned about once per leader.
	skipped map[string]struct{}
	// announcedMoved is the logout whose "everyone moved" message was posted. A failover can repeat that message.
	announcedMoved int
}

// NewLogoutController returns a controller on d. Clients on exemptVLANs are not logged out, and moves to them are
// processed during a lockout.
func NewLogoutController(d Store, exemptVLANs []int) *LogoutController {
	return &LogoutController{db: d, exemptVLANs: exemptVLANs, now: time.Now, skipped: map[string]struct{}{}}
}

// ParseTime parses the time an admin typed: "15:00" for today, or "2026-11-01 15:00", both in UTC. A time already past
// parses; Schedule refuses it.
func (c *LogoutController) ParseTime(s string) (time.Time, error) {
	now := c.now().UTC()
	if t, err := time.Parse("15:04", s); err == nil {
		return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC), nil
	}
	if t, err := time.Parse("2006-01-02 15:04", s); err == nil {
		return t, nil
	}
	return time.Time{}, &LogoutRefusal{Message: fmt.Sprintf(
		"Can't read %q as a time. Use `15:00` for today, or `2026-11-01 15:00`, both in UTC.", s)}
}

// ScheduleResult is what a successful Schedule did.
type ScheduleResult struct {
	Logout    logout.Logout
	Approvers []string
	// Armed is true if this request was the second approval.
	Armed bool
}

// Schedule records admin's request for a logout at the time at: the first request creates a proposal, and a different
// admin's request for the same time arms it. It returns a *LogoutRefusal if the rules turn the request down.
func (c *LogoutController) Schedule(ctx context.Context, admin string, at time.Time) (ScheduleResult, error) {
	var err error
	for range scheduleAttempts {
		var res ScheduleResult
		res, err = c.schedule(ctx, admin, at)
		// On a conflict, another admin's request got there first; running again sees its result.
		if !db.IsConflict(err) && !errors.Is(err, errLogoutChanged) {
			return res, err
		}
	}
	return ScheduleResult{}, err
}

func (c *LogoutController) schedule(ctx context.Context, admin string, at time.Time) (ScheduleResult, error) {
	if at.Sub(c.now()) < logoutMinNotice {
		return ScheduleResult{}, &LogoutRefusal{Message: fmt.Sprintf(
			"A global logout has to be at least 1 hour in the future; %s is too soon.", FormatLogoutTime(at))}
	}

	var res ScheduleResult
	err := c.db.WithTx(ctx, func(tx db.Querier) error {
		// Read without a lock first: locking a missing row would take gap locks, on which concurrent first proposals
		// deadlock. They collide on the unique in_flight instead, and Schedule runs the loser again.
		current, err := logout.InFlight(ctx, tx)
		if errors.Is(err, db.ErrNotFound) {
			id, err := logout.Create(ctx, tx, at)
			if err != nil {
				return err
			}
			if err := logout.Approve(ctx, tx, id, admin); err != nil {
				return err
			}
			res.Logout, err = logout.Get(ctx, tx, id)
			res.Approvers = []string{admin}
			return err
		}
		if err != nil {
			return err
		}

		current, err = logout.Lock(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		switch current.State {
		case logout.StateProposed:
		case logout.StateArmed:
			return &LogoutRefusal{Message: fmt.Sprintf(
				"A global logout for %s is already armed. Cancel it first to schedule another.",
				FormatLogoutTime(current.ScheduledFor))}
		case logout.StateRunning:
			return &LogoutRefusal{Message: "A global logout is running right now. Wait for it to finish."}
		default:
			return errLogoutChanged
		}
		if !current.ScheduledFor.Equal(at) {
			return &LogoutRefusal{Message: fmt.Sprintf(
				"A global logout for %s is already proposed. Schedule that same time to approve it, or cancel it first.",
				FormatLogoutTime(current.ScheduledFor))}
		}

		approvers, err := logout.Approvers(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		if slices.Contains(approvers, admin) {
			return &LogoutRefusal{Message: "You already scheduled this logout. A second admin has to schedule it too."}
		}
		if err := logout.Approve(ctx, tx, current.ID, admin); err != nil {
			return err
		}
		if _, err := logout.Arm(ctx, tx, current.ID); err != nil {
			return err
		}
		res.Logout, err = logout.Get(ctx, tx, current.ID)
		res.Approvers = append(approvers, admin)
		res.Armed = true
		return err
	})
	if err != nil {
		return ScheduleResult{}, err
	}
	return res, nil
}

// Cancel cancels the logout in flight on behalf of admin. With an id other than 0, it only cancels that logout, so a
// button on an old message cannot cancel a later one. Cancelling a running logout also drops its moves not yet processed
// and ends the lockout now; clients already moved stay. It returns the logout as it was before the cancel, or a
// *LogoutRefusal if there is nothing to cancel.
func (c *LogoutController) Cancel(ctx context.Context, admin string, id int) (logout.Logout, error) {
	var before logout.Logout
	err := c.db.WithTx(ctx, func(tx db.Querier) error {
		current, err := logout.InFlight(ctx, tx)
		if errors.Is(err, db.ErrNotFound) {
			if id != 0 {
				return &LogoutRefusal{Message: "That global logout has already finished."}
			}
			return &LogoutRefusal{Message: "No global logout is in flight."}
		}
		if err != nil {
			return err
		}
		if id != 0 && current.ID != id {
			return &LogoutRefusal{Message: "That global logout has already finished."}
		}

		before, err = logout.Lock(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		done, err := logout.Cancel(ctx, tx, current.ID, admin, c.now().UTC().Truncate(time.Second))
		if err != nil {
			return err
		}
		if !done {
			return &LogoutRefusal{Message: "That global logout has already finished."}
		}
		if before.StartedAt != nil {
			since := before.StartedAt.Add(-cancelClockSlack)
			if _, err := jobs.DeletePendingToVLAN(ctx, tx, freeradius.LoginVLAN, since); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return logout.Logout{}, err
	}
	return before, nil
}

// LogoutStatus describes the logout in flight.
type LogoutStatus struct {
	Logout    logout.Logout
	Approvers []string
	// Clients is how many clients it would move right now.
	Clients int
}

// Status returns the logout in flight, and false if there is none.
func (c *LogoutController) Status(ctx context.Context) (LogoutStatus, bool, error) {
	q := c.db.Querier()
	l, err := logout.InFlight(ctx, q)
	if errors.Is(err, db.ErrNotFound) {
		return LogoutStatus{}, false, nil
	}
	if err != nil {
		return LogoutStatus{}, false, err
	}
	approvers, err := logout.Approvers(ctx, q, l.ID)
	if err != nil {
		return LogoutStatus{}, false, err
	}
	clients, _, err := logoutTargets(ctx, q, c.exemptVLANs)
	if err != nil {
		return LogoutStatus{}, false, err
	}
	return LogoutStatus{Logout: l, Approvers: approvers, Clients: len(clients)}, true, nil
}

// VLANChangeBlocked reports whether a move to vlan queued now would be dropped, and until when moves are dropped.
func (c *LogoutController) VLANChangeBlocked(ctx context.Context, vlan int) (time.Time, bool, error) {
	lockout, blocked, err := logout.Blocked(ctx, c.db.Querier(), vlan, c.now().UTC(), c.exemptVLANs)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("checking the global logout lockout: %w", err)
	}
	return lockout.Ends, blocked, nil
}

// logoutTargets returns the clients a logout has to move: everyone with a VLAN in radreply, online or offline, who is
// neither in the login VLAN nor in an exempt VLAN. It reads radreply rather than open sessions because a client offline
// at the logout's time would otherwise come back still logged in.
//
// skipped holds the usernames of rows it cannot move: a VLAN that is not a number, or a username that is not a MAC in
// its canonical form. The job loop only updates rows under the canonical spelling, so any other row would be queued
// again every round.
func logoutTargets(ctx context.Context, q db.Querier, exempt []int) (clients []mac.MAC, skipped []string, err error) {
	rows, err := freeradius.Assignments(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		vlan, err := strconv.Atoi(r.VLAN)
		if err != nil {
			skipped = append(skipped, r.Username)
			continue
		}
		if vlan == freeradius.LoginVLAN || slices.Contains(exempt, vlan) {
			continue
		}
		m, err := mac.Parse(r.Username)
		if err != nil || !strings.EqualFold(m.Hex(), r.Username) {
			skipped = append(skipped, r.Username)
			continue
		}
		clients = append(clients, m)
	}
	return clients, skipped, nil
}

// Log messages, mirrored to the log channel.
const (
	msgLogoutWarning  = "Global logout warning"
	msgLogoutExpired  = "Global logout proposal expired"
	msgLogoutAborted  = "Global logout aborted"
	msgLogoutStarted  = "Global logout started"
	msgLogoutSkipped  = "Global logout cannot move this radreply row"
	msgLogoutMoved    = "Global logout moved everyone"
	msgLogoutFinished = "Global logout finished"
)

// LogoutWarning is a warning before a logout.
type LogoutWarning struct {
	Logout logout.Logout
	// Stage is the minutes before the logout this warning stands for.
	Stage     int
	Approvers []string
	// Clients is how many clients the logout would move right now.
	Clients int
}

// Notifier posts about a global logout. The Discord bot implements it.
type Notifier interface {
	// Warn posts a warning admins can act on. An error means it was not delivered, so a logout that depends on it must
	// not start.
	Warn(ctx context.Context, w LogoutWarning) error
	// Notify posts a status message.
	Notify(ctx context.Context, message string) error
}

// dueWarning returns the warning due now, given the last one posted and the time remaining: the smallest stage reached
// and not yet posted, or, if nothing was posted and no stage is reached, the arming message, recorded as the remaining
// minutes rounded up.
func dueWarning(last *int, remaining time.Duration) (int, bool) {
	for _, stage := range warningStages {
		if remaining <= time.Duration(stage)*time.Minute && (last == nil || *last > stage) {
			return stage, true
		}
	}
	if last == nil {
		return int(math.Ceil(remaining.Minutes())), true
	}
	return 0, false
}

// notify posts a status message, best effort: what it reports has already happened.
func notify(ctx context.Context, n Notifier, message string) {
	if err := n.Notify(ctx, message); err != nil {
		slog.Warn("Couldn't post a global logout message", "error", err)
	}
}

// Round runs one round of the logout loop: it acts on the logout in flight according to its state. All state is in the
// database, so a new leader continues where the old one stopped.
func (c *LogoutController) Round(ctx context.Context, n Notifier) error {
	q := c.db.Querier()
	now := c.now().UTC().Truncate(time.Second)

	lockout, ok, err := logout.CurrentLockout(ctx, q)
	if err != nil {
		return err
	}
	if ok && now.Before(lockout.Ends) {
		logout.LockoutActive.Set(1)
	} else {
		logout.LockoutActive.Set(0)
	}

	l, err := logout.InFlight(ctx, q)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	switch l.State {
	case logout.StateProposed:
		return c.expireIfDue(ctx, n, l, now)
	case logout.StateArmed:
		if now.Before(l.ScheduledFor) {
			return c.warnIfDue(ctx, n, l, now)
		}
		return c.startOrAbort(ctx, n, l, now)
	case logout.StateRunning:
		return c.runningRound(ctx, n, l, now)
	default:
		return nil
	}
}

// expireIfDue ends a proposal that can no longer reach its approval in time.
func (c *LogoutController) expireIfDue(ctx context.Context, n Notifier, l logout.Logout, now time.Time) error {
	if l.ScheduledFor.Sub(now) >= logoutMinNotice {
		return nil
	}
	expired, err := logout.Expire(ctx, c.db.Querier(), l.ID)
	if err != nil || !expired {
		return err
	}
	slog.Warn(msgLogoutExpired, "logoutID", l.ID, "scheduledFor", l.ScheduledFor)
	notify(ctx, n, fmt.Sprintf("**Global logout proposal expired:** no second admin scheduled %s in time.",
		FormatLogoutTime(l.ScheduledFor)))
	return nil
}

// warnIfDue posts the warning due now, if any, and records it. A failed post does not fail the round: the next round
// tries again, and startOrAbort deals with a final warning that never got through.
func (c *LogoutController) warnIfDue(ctx context.Context, n Notifier, l logout.Logout, now time.Time) error {
	stage, due := dueWarning(l.LastWarning, l.ScheduledFor.Sub(now))
	if !due {
		return nil
	}
	q := c.db.Querier()
	approvers, err := logout.Approvers(ctx, q, l.ID)
	if err != nil {
		return err
	}
	clients, _, err := logoutTargets(ctx, q, c.exemptVLANs)
	if err != nil {
		return err
	}

	// Post first, then record: a failover can repeat a warning but cannot skip one.
	w := LogoutWarning{Logout: l, Stage: stage, Approvers: approvers, Clients: len(clients)}
	if err := n.Warn(ctx, w); err != nil {
		slog.Warn("Couldn't post a global logout warning", "logoutID", l.ID, "stage", stage, "error", err)
		return nil
	}
	// A final warning that arrives at or after the logout's time gave admins no chance to cancel, so it does not count
	// and startOrAbort aborts.
	if stage == finalWarning && !c.now().Before(l.ScheduledFor) {
		slog.Warn("Global logout's final warning arrived too late", "logoutID", l.ID, "scheduledFor", l.ScheduledFor)
		return nil
	}
	if err := logout.SetLastWarning(ctx, q, l.ID, stage); err != nil {
		return err
	}
	slog.Warn(msgLogoutWarning, "logoutID", l.ID, "scheduledFor", l.ScheduledFor, "minutesBefore", stage, "clients", len(clients))
	return nil
}

// startOrAbort handles an armed logout whose time has come. It starts only if the final warning got through and it is
// not too late: during a Discord outage admins could neither see a warning nor cancel, and a logout at an unexpected
// time is worse than none.
func (c *LogoutController) startOrAbort(ctx context.Context, n Notifier, l logout.Logout, now time.Time) error {
	if now.Sub(l.ScheduledFor) > logoutLateLimit {
		return c.abort(ctx, n, l, "no leader was running it at its time")
	}
	if l.LastWarning == nil || *l.LastWarning != finalWarning {
		return c.abort(ctx, n, l, "its final warning could not be posted")
	}

	started, err := logout.Start(ctx, c.db.Querier(), l.ID, now, now.Add(lockoutDuration))
	if err != nil || !started {
		return err // not started: it was cancelled just now
	}
	slog.Warn(msgLogoutStarted, "logoutID", l.ID, "lockoutEnds", now.Add(lockoutDuration))
	notify(ctx, n, fmt.Sprintf("**Global logout started.** Moves to other VLANs are ignored until %s.",
		FormatLogoutTime(now.Add(lockoutDuration))))
	return c.sweep(ctx, n, l)
}

func (c *LogoutController) abort(ctx context.Context, n Notifier, l logout.Logout, reason string) error {
	aborted, err := logout.Abort(ctx, c.db.Querier(), l.ID)
	if err != nil || !aborted {
		return err
	}
	slog.Error(msgLogoutAborted, "logoutID", l.ID, "scheduledFor", l.ScheduledFor, "reason", reason)
	notify(ctx, n, fmt.Sprintf("**Global logout aborted:** the logout for %s did not run because %s. Schedule it again if you still need it.",
		FormatLogoutTime(l.ScheduledFor), reason))
	return nil
}

// runningRound sweeps during the lockout. After it, the logout finishes once its moves are done, so the slowest of them
// still get the job loop's treatment of a running logout.
func (c *LogoutController) runningRound(ctx context.Context, n Notifier, l logout.Logout, now time.Time) error {
	if now.Before(*l.LockoutEndsAt) {
		return c.sweep(ctx, n, l)
	}

	q := c.db.Querier()
	pending, err := pendingLoginMoves(ctx, q)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return nil
	}
	moved, err := jobs.CountMovesSince(ctx, q, freeradius.LoginVLAN, *l.StartedAt)
	if err != nil {
		return err
	}
	finished, err := logout.Finish(ctx, q, l.ID)
	if err != nil || !finished {
		return err
	}
	slog.Warn(msgLogoutFinished, "logoutID", l.ID, "moved", moved, "coaFailures", l.CoAFailures, "ignoredJobs", l.IgnoredJobs)
	notify(ctx, n, fmt.Sprintf(
		"**Global logout finished.** %d client(s) moved, %d move(s) to other VLANs ignored, %d CoA failure(s). Clients counted as CoA failures stay physically on their old VLAN until they next re-authenticate.",
		moved, l.IgnoredJobs, l.CoAFailures))
	return nil
}

// sweep queues a move to the login VLAN for every client that still needs one. It runs every round of the lockout, not
// once: a login job can commit just after a sweep read radreply, and a client can come back in other ways.
func (c *LogoutController) sweep(ctx context.Context, n Notifier, l logout.Logout) error {
	q := c.db.Querier()
	clients, skipped, err := logoutTargets(ctx, q, c.exemptVLANs)
	if err != nil {
		return err
	}
	for _, username := range skipped {
		if _, warned := c.skipped[username]; !warned {
			c.skipped[username] = struct{}{}
			slog.Warn(msgLogoutSkipped, "username", username)
		}
	}
	if len(clients) == 0 {
		if c.announcedMoved != l.ID {
			c.announcedMoved = l.ID
			slog.Warn(msgLogoutMoved, "logoutID", l.ID)
			notify(ctx, n, "**Global logout:** everyone has been moved.")
		}
		return nil
	}

	pending, err := pendingLoginMoves(ctx, q)
	if err != nil {
		return err
	}
	// One transaction holding the logout's row: a cancel either waits for it and then deletes these jobs too, or commits
	// first, and then this queues nothing.
	queued := 0
	err = c.db.WithTx(ctx, func(tx db.Querier) error {
		current, err := logout.Lock(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		if current.State != logout.StateRunning {
			return nil
		}
		for _, m := range clients {
			if _, has := pending[m]; has {
				continue
			}
			if err := jobs.Create(ctx, tx, m, freeradius.LoginVLAN); err != nil {
				return fmt.Errorf("queueing the global logout's move of %s: %w", m.Hex(), err)
			}
			queued++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if queued > 0 {
		slog.Debug("Global logout queued moves", "logoutID", l.ID, "queued", queued, "remaining", len(clients))
	}
	return nil
}

// pendingLoginMoves returns the clients with a pending job to the login VLAN.
func pendingLoginMoves(ctx context.Context, q db.Querier) (map[mac.MAC]struct{}, error) {
	pending, err := jobs.Pending(ctx, q)
	if err != nil {
		return nil, err
	}
	out := map[mac.MAC]struct{}{}
	for _, j := range pending {
		if j.TargetVLAN == freeradius.LoginVLAN && !j.MAC.IsZero() {
			out[j.MAC] = struct{}{}
		}
	}
	return out, nil
}
