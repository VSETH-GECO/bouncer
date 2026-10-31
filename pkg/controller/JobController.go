package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/kea"
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

// CoASender sends a RADIUS CoA disconnect request to a switch, forcing the client on the session to reauthenticate.
// radius.Sender is the real implementation; tests substitute a fake.
type CoASender interface {
	SendDisconnect(ctx context.Context, req radius.CoARequest) error
}

type JobController struct {
	db           Store
	switchSecret string
	coa          CoASender
}

// NewJobController returns a controller that processes the job queue in d, sending CoA requests signed with
// switchSecret through coa.
func NewJobController(d Store, switchSecret string, coa CoASender) *JobController {
	return &JobController{
		db:           d,
		switchSecret: switchSecret,
		coa:          coa,
	}
}

// ProcessMoveVLANJob processes a job that moves a host between VLANs, taking care of all database updates. On success
// the job is deleted from the queue in the same transaction; on error nothing is persisted and the caller is
// responsible for the retry bookkeeping.
func (c *JobController) ProcessMoveVLANJob(ctx context.Context, job *jobs.Job) error {
	// Let's see whether this host can actually be found
	sessions, err := freeradius.SessionsForMAC(ctx, c.db.Querier(), job.MAC)
	if err != nil {
		return fmt.Errorf("looking up sessions: %w", err)
	}
	var session *freeradius.Session
	if len(sessions) > 0 {
		session = &sessions[0]
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

		// If there is a running session, force the switch to reauth
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
				return fmt.Errorf("sending CoA: %w", err)
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

// work processes the work queue once. Only a failure to fetch the queue is returned; failing jobs are logged, their
// retry counter is bumped and the remaining jobs are still processed.
func (c *JobController) work(ctx context.Context) error {
	pending, err := jobs.Pending(ctx, c.db.Querier())
	if err != nil {
		return fmt.Errorf("fetching pending jobs: %w", err)
	}

	for i := range pending {
		job := &pending[i]
		if err := ctx.Err(); err != nil {
			return err
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

		err := c.ProcessMoveVLANJob(ctx, job)
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

// Spin loops over the work queue until ctx is cancelled, then returns ctx.Err(). It calls beat (if not nil) after every
// round, failed ones included.
func (c *JobController) Spin(ctx context.Context, beat func()) error {
	host, _ := os.Hostname()
	slog.Info("Entering main poll loop", "host", host)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := c.work(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("poll loop iteration failed", "error", err)
		}
		if beat != nil {
			beat()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
