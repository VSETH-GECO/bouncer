package controller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
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
)

type JobController struct {
	db           *database.Handler
	switchSecret string
}

func NewClientController(db *database.Handler, switchSecret string) *JobController {
	return &JobController{
		db:           db,
		switchSecret: switchSecret,
	}
}

// ProcessMoveVLANJob processes a job that moves a host between VLANs, taking care of all database updates. On success
// the job is deleted from the queue in the same transaction; on error nothing is persisted and the caller is
// responsible for the retry bookkeeping.
func (c *JobController) ProcessMoveVLANJob(ctx context.Context, job *database.Job) error {
	job.ClientMac = strings.ToLower(job.ClientMac)

	// Let's see whether this host can actually be found
	sessions, err := c.db.FindSessionsForMAC(ctx, job.ClientMac)
	if err != nil {
		return fmt.Errorf("looking up sessions: %w", err)
	}
	var session *database.RadiusSession
	if len(sessions) > 0 {
		session = sessions[0]
	}

	var (
		oldVlan  int
		useless  bool
		hostname string
		oldIP    net.IP
	)
	// The CoA is deliberately sent while the transaction is open: if anything after it fails, the radreply update is
	// rolled back and the whole job (including the CoA) is retried. Moving the CoA after the commit changes these
	// retry semantics and is left for a separate change.
	err = c.db.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		oldVlan, err = c.db.CheckUserSignedIn(ctx, tx, job.ClientMac)
		switch {
		case errors.Is(err, database.ErrNotFound):
			// No entry found - this user is completely new
			oldVlan = -1
			if err := c.db.AddNewUser(ctx, tx, job.ClientMac, job.TargetVLAN); err != nil {
				return fmt.Errorf("adding new user: %w", err)
			}
		case err != nil:
			return err
		default:
			// We found an entry -> update it
			if err := c.db.UpdateUser(ctx, tx, job.ClientMac, job.TargetVLAN); err != nil {
				return fmt.Errorf("updating user: %w", err)
			}
		}

		// I'm not sure if you want this, so let's log that case
		if oldVlan == job.TargetVLAN {
			log.WithFields(log.Fields{
				"job.Id":         job.Id,
				"job.ClientMac":  job.ClientMac,
				"job.TargetVLAN": job.TargetVLAN,
			}).Warn("Got request to move to same vlan, silently discarding this ;-)")
			useless = true
			return c.db.DeleteJob(ctx, tx, job.Id)
		}

		// If there is a running session, force the switch to reauth
		if session != nil {
			request := radius.CoARequest{
				SessionUID:       session.Uid,
				SessionStartDate: session.StartDate.Time,
				SwitchIP:         session.SwitchIP,
				SwitchSecret:     []byte(c.switchSecret),
			}
			if err := request.SendDisconnect(ctx); err != nil {
				opsFailedCoA.Inc()
				return fmt.Errorf("sending CoA: %w", err)
			}
		}

		// Clear the old lease since we've kicked out the user
		hostname, oldIP, err = c.db.ClearLeasesForMAC(ctx, tx, job.ClientMac)
		if err != nil {
			return fmt.Errorf("clearing leases: %w", err)
		}

		switchIP := net.IP{}
		switchPort := ""
		if session != nil {
			switchIP = session.SwitchIP
			switchPort = session.SwitchPort
		}

		// Also log success to database
		if err := c.db.LogJobResult(ctx, tx, job.ClientMac, oldVlan, job.TargetVLAN, switchIP, switchPort, hostname, oldIP); err != nil {
			return err
		}

		return c.db.DeleteJob(ctx, tx, job.Id)
	})
	if err != nil {
		return err
	}

	opsProcessedSuccessfully.Inc()
	if useless {
		opsProcessedUseless.Inc()
		return nil
	}

	log.WithFields(log.Fields{
		"job.Id":         job.Id,
		"job.ClientMac":  job.ClientMac,
		"oldVLAN":        oldVlan,
		"job.TargetVLAN": job.TargetVLAN,
		"oldIP":          oldIP.String(),
		"hostname":       hostname,
		"retries":        job.Retries,
	}).Info("VLAN move successful")
	return nil
}

// work processes the work queue once. Only a failure to fetch the queue is returned; failing jobs are logged, their
// retry counter is bumped and the remaining jobs are still processed.
func (c *JobController) work(ctx context.Context) error {
	jobs, err := c.db.FetchPendingJobs(ctx)
	if err != nil {
		return fmt.Errorf("fetching pending jobs: %w", err)
	}

	for i := range jobs {
		job := &jobs[i]
		if err := ctx.Err(); err != nil {
			return err
		}
		l := log.WithFields(log.Fields{
			"id":         job.Id,
			"clientMAC":  job.ClientMac,
			"targetVLAN": job.TargetVLAN,
		})
		l.Info("Processing")
		opsProcessed.Inc()

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
		l.WithError(err).WithField("retries", job.Retries).Warn("Processing job failed")
		if err := c.db.SetJobErrorCount(ctx, job); err != nil {
			l.WithError(err).Warn("Couldn't update job error counter!")
		}
	}
	return nil
}

// Spin loops over the work queue until ctx is cancelled, then returns ctx.Err()
func (c *JobController) Spin(ctx context.Context) error {
	log.WithField("node", database.GetNodeID()).Info("Entering main poll loop")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := c.work(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.WithError(err).Warn("poll loop iteration failed")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
