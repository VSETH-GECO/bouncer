package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/kea"
	"github.com/VSETH-GECO/bouncer/internal/logout"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	opsProcessedSuccessfully = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_processed_requests_success",
		Help: "The total number of successfully processed requests",
	})
	opsProcessedUseless = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_processed_useless_requests",
		Help: "The total number of requests without any effect",
	})
	opsFailedCoA = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_failed_coa",
		Help: "The total number of failed CoA requests",
	})
	opsProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_processed_requests",
		Help: "The total number of processed requests",
	})
	opsProcessedNewUser = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_processed_requests_new",
		Help: "The total number of requests with new users",
	})
)

// jobRoundBudget is how long a round of the job loop starts new jobs. It is half of the job loop's heartbeat window, so
// the heartbeat stays fresh however long the queue is.
const jobRoundBudget = 30 * time.Second

// CoASender sends a RADIUS CoA disconnect request to a switch, forcing the client on the session to reauthenticate.
// radius.Sender is the real implementation; tests substitute a fake.
type CoASender interface {
	SendDisconnect(ctx context.Context, req radius.CoARequest) error
}

type JobController struct {
	db           Store
	switchSecret string
	coa          CoASender
	exemptVLANs  []int
	now          func() time.Time
}

// NewJobController returns a controller that processes the job queue in d, sending CoA requests signed with
// switchSecret through coa. A global logout's lockout leaves moves to exemptVLANs alone.
func NewJobController(d Store, switchSecret string, coa CoASender, exemptVLANs []int) *JobController {
	return &JobController{
		db:           d,
		switchSecret: switchSecret,
		coa:          coa,
		exemptVLANs:  exemptVLANs,
		now:          time.Now,
	}
}

// ProcessMoveVLANJob processes a job that moves a host between VLANs, taking care of all database updates. On success
// the job is deleted from the queue in the same transaction; on error nothing is persisted and the caller is
// responsible for the retry bookkeeping.
//
// Only the client's newest open session (no stop time) counts: it receives the CoA and its switch and port are logged
// as where the client was kicked from. A closed session is a leftover of an earlier stay, possibly on another switch,
// and is ignored; a client without an open session gets no CoA and a log row without switch columns, and its switch
// places it in the new VLAN on its next authentication.
//
// During a global logout, the last retry of a move to the login VLAN commits even if its CoA fails, see
// movesDespiteFailedCoA.
func (c *JobController) ProcessMoveVLANJob(ctx context.Context, job *jobs.Job) error {
	// Let's see whether this host can actually be found
	sessions, err := freeradius.SessionsForMAC(ctx, c.db.Querier(), job.MAC)
	if err != nil {
		return fmt.Errorf("looking up sessions: %w", err)
	}
	var session *freeradius.Session
	for i := range sessions {
		if sessions[i].Stop == nil {
			session = &sessions[i] // sessions come newest start first
			break
		}
	}

	var (
		oldVlan  int
		useless  bool
		hostname *string
		oldIP    net.IP
	)
	// The CoA is deliberately sent while the transaction is open: if anything after it fails, the radreply update is
	// rolled back and the whole job (including the CoA) is retried. Moving the CoA after the commit changes these
	// retry semantics and is left for a separate change.
	err = c.db.WithTx(ctx, func(tx db.Querier) error {
		var err error
		oldVlan, err = freeradius.AssignedVLAN(ctx, tx, job.MAC)
		switch {
		case errors.Is(err, db.ErrNotFound):
			// No entry found - this user is completely new
			oldVlan = -1
			opsProcessedNewUser.Inc()
			if err := freeradius.Authorize(ctx, tx, job.MAC, job.TargetVLAN); err != nil {
				return fmt.Errorf("adding new user: %w", err)
			}
		case err != nil:
			return err
		default:
			// We found an entry -> update it
			opsProcessedNewUser.Inc()
			if err := freeradius.SetVLAN(ctx, tx, job.MAC, job.TargetVLAN); err != nil {
				return fmt.Errorf("updating user: %w", err)
			}
		}

		// I'm not sure if you want this, so let's log that case
		if oldVlan == job.TargetVLAN {
			slog.Warn("Got request to move to same vlan, silently discarding this ;-)",
				"job.Id", job.ID, "job.ClientMac", job.MAC.Hex(), "job.TargetVLAN", job.TargetVLAN)
			useless = true
			return jobs.Delete(ctx, tx, job.ID)
		}

		// If the client has an open session, force its switch to reauthenticate it; a closed session's switch no
		// longer knows the session, so it gets nothing
		if session != nil {
			var start time.Time
			if session.Start != nil {
				start = *session.Start
			}
			request := radius.CoARequest{
				SessionUID:       session.ID,
				SessionStartDate: start,
				SwitchIP:         session.SwitchIP,
				SwitchSecret:     []byte(c.switchSecret),
			}
			if err := c.coa.SendDisconnect(ctx, request); err != nil {
				opsFailedCoA.Inc()
				moveAnyway, checkErr := c.movesDespiteFailedCoA(ctx, tx, job)
				if checkErr != nil {
					return checkErr
				}
				if !moveAnyway {
					return fmt.Errorf("sending CoA: %w", err)
				}
				fields := []any{"job.Id", job.ID, "job.ClientMac", job.MAC.Hex(), "switchIP", session.SwitchIP.String()}
				if session.SwitchPort != nil {
					fields = append(fields, "switchPort", *session.SwitchPort)
				}
				fields = append(fields, "error", err)
				slog.Warn("Moving a client despite its failed CoA: it stays on its old VLAN until it next re-authenticates", fields...)
			}
		}

		// Clear the old lease since we've kicked out the user
		lease, err := kea.LatestLease4(ctx, tx, job.MAC)
		switch {
		case err == nil:
			hostname, oldIP = lease.Hostname, lease.IP
		case errors.Is(err, db.ErrNotFound):
			// no lease to report
		default:
			return fmt.Errorf("looking up latest DHCPv4 lease: %w", err)
		}
		if err := kea.DeleteLeases(ctx, tx, job.MAC); err != nil {
			return fmt.Errorf("clearing leases: %w", err)
		}

		result := jobs.Result{
			MAC:      job.MAC,
			OldVLAN:  oldVlan,
			NewVLAN:  job.TargetVLAN,
			Hostname: hostname,
			ClientIP: oldIP,
		}
		if session != nil {
			result.SwitchIP = session.SwitchIP
			result.SwitchPort = session.SwitchPort
		}

		// Also log success to database
		if err := jobs.LogResult(ctx, tx, result); err != nil {
			return err
		}

		return jobs.Delete(ctx, tx, job.ID)
	})
	if err != nil {
		return err
	}

	opsProcessedSuccessfully.Inc()
	if useless {
		opsProcessedUseless.Inc()
		return nil
	}

	fields := []any{
		"job.Id", job.ID,
		"job.ClientMac", job.MAC.Hex(),
		"oldVLAN", oldVlan,
		"job.TargetVLAN", job.TargetVLAN,
		"retries", job.Retries,
	}
	if hostname != nil {
		fields = append(fields, "hostname", *hostname)
	}
	if oldIP != nil {
		fields = append(fields, "oldIP", oldIP.String())
	}
	slog.Info("VLAN move successful", fields...)
	return nil
}

// Work processes the work queue once; it is the job loop's round. It stops starting jobs after jobRoundBudget and the
// next round continues with the rest. Only a failure to fetch the queue is returned: a job that fails or panics is
// logged, its retry counter is bumped and the remaining jobs are still processed. A job deleted since the round
// fetched the queue is skipped. A job the global logout's lockout ignores is dropped; a failed lockout lookup fails the
// round, so no job runs unchecked.
func (c *JobController) Work(ctx context.Context) error {
	deadline := c.now().Add(jobRoundBudget)
	pending, err := jobs.Pending(ctx, c.db.Querier())
	if err != nil {
		return fmt.Errorf("fetching pending jobs: %w", err)
	}

	for i := range pending {
		job := &pending[i]
		if err := ctx.Err(); err != nil {
			return err
		}
		if !c.now().Before(deadline) {
			slog.Debug("Job round budget used up; continuing next round", "remaining", len(pending)-i)
			return nil
		}

		// The list is from the start of the round, and a cancelled global logout deletes its jobs from under it.
		queued, err := jobs.Exists(ctx, c.db.Querier(), job.ID)
		if err != nil {
			return fmt.Errorf("checking that job %d is still queued: %w", job.ID, err)
		}
		if !queued {
			continue
		}
		dropped, err := c.dropIfLockedOut(ctx, job)
		if err != nil {
			return fmt.Errorf("checking the global logout lockout: %w", err)
		}
		if dropped {
			continue
		}

		l := slog.With("id", job.ID, "clientMAC", job.RawMAC, "targetVLAN", job.TargetVLAN)
		l.Info("Processing")
		opsProcessed.Inc()

		if job.MAC.IsZero() {
			// A row whose MAC can't be parsed can never succeed; count it as a failed attempt so that it eventually
			// drops out of the queue.
			job.Retries++
			l.Warn(fmt.Sprintf("Processing job failed: malformed client MAC %q", job.RawMAC), "retries", job.Retries)
			if err := jobs.SetRetries(ctx, c.db.Querier(), job.ID, job.Retries); err != nil {
				l.Warn("Couldn't update job error counter!", "error", err)
			}
			continue
		}

		err = c.processJob(ctx, job)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			// Aborted by shutdown: the transaction was rolled back and the job will be picked up again after
			// restart, so don't count this as a failed attempt.
			return ctx.Err()
		}

		job.Retries++
		l.Warn("Processing job failed", "error", err, "retries", job.Retries)
		if err := jobs.SetRetries(ctx, c.db.Querier(), job.ID, job.Retries); err != nil {
			l.Warn("Couldn't update job error counter!", "error", err)
		}
	}
	return nil
}

// processJob runs ProcessMoveVLANJob and turns a panic into an error, after logging it with its stack.
func (c *JobController) processJob(ctx context.Context, job *jobs.Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("Processing job panicked", "id", job.ID, "clientMAC", job.RawMAC, "panic", p,
				"stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return c.ProcessMoveVLANJob(ctx, job)
}

// dropIfLockedOut deletes job if the global logout's lockout ignores it, and reports whether it did.
func (c *JobController) dropIfLockedOut(ctx context.Context, job *jobs.Job) (bool, error) {
	lockout, blocked, err := logout.Blocked(ctx, c.db.Querier(), job.TargetVLAN, job.LastUpdate, c.exemptVLANs)
	if err != nil || !blocked {
		return false, err
	}
	// The logout's row first, then the job's, the order Cancel locks them in, so the two cannot deadlock.
	err = c.db.WithTx(ctx, func(tx db.Querier) error {
		if err := logout.AddIgnoredJob(ctx, tx, lockout.LogoutID); err != nil {
			return err
		}
		return jobs.Delete(ctx, tx, job.ID)
	})
	if err != nil {
		return false, err
	}
	logout.IgnoredJobsTotal.Inc()
	slog.Debug("Global logout lockout dropped a job", "id", job.ID, "clientMAC", job.RawMAC, "targetVLAN", job.TargetVLAN)
	return true, nil
}

// movesDespiteFailedCoA reports whether job moves its client although the CoA failed: it is the last retry of a move to
// the login VLAN during a running global logout. Otherwise a client on an unreachable switch would keep its VLAN and
// still be logged in when the switch comes back. The client stays physically on its old VLAN until it next
// re-authenticates, so it is counted.
func (c *JobController) movesDespiteFailedCoA(ctx context.Context, tx db.Querier, job *jobs.Job) (bool, error) {
	if job.TargetVLAN != freeradius.LoginVLAN || job.Retries != jobs.MaxRetries-1 {
		return false, nil
	}
	l, err := logout.InFlight(ctx, tx)
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if l.State != logout.StateRunning {
		return false, nil
	}
	return true, logout.AddCoAFailure(ctx, tx, l.ID)
}
