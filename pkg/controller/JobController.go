package controller

import (
	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
	"net"
	"strings"
	"time"
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

// ProcessMoveVLANJob processes a job that moves a host between VLANs, taking care of all database updates
func (c *JobController) ProcessMoveVLANJob(job *database.Job) error {
	job.ClientMac = strings.ToLower(job.ClientMac)

	// Let's see whether this host can actually be found
	val, err := c.db.FindSessionsForMAC(job.ClientMac)
	if err != nil {
		return err
	}
	noRunningSession := len(val) == 0

	tx, err := c.db.BeginTx()
	if err != nil {
		return err
	}
	var success = false
	defer func() {
		if !success {
			database.Rollback(tx)
			job.Retries = job.Retries + 1
			err := c.db.SetJobErrorCount(job)
			if err != nil {
				log.WithError(err).Warn("Couldn't update job error counter!")
			}
		} else {
			err = c.db.DeleteJob(tx, job.Id)
			if err != nil {
				log.WithError(err).Warn("Couldn't delete job!")
			}
			err = tx.Commit()
			if err != nil {
				log.WithError(err).Warn("Couldn't commit job!")
			}
			opsProcessedSuccessfully.Add(1)
		}
	}()

	oldVlan, err := c.db.CheckUserSignedIn(tx, job.ClientMac)
	if err != nil {
		return err
	}

	if oldVlan == -1 {
		// No entry found - this user is completely new
		err = c.db.AddNewUser(tx, job.ClientMac, job.TargetVLAN)
		if err != nil {
			return err
		}
	} else {
		// We found an entry -> update it
		err = c.db.UpdateUser(tx, job.ClientMac, job.TargetVLAN)
		if err != nil {
			return err
		}
	}

	// I'm not sure if you want this, so let's log that case
	if oldVlan == job.TargetVLAN {
		log.WithFields(log.Fields{
			"job.Id":         job.Id,
			"job.ClientMac":  job.ClientMac,
			"job.TargetVLAN": job.TargetVLAN,
		}).Warn("Got request to move to same vlan, silently discarding this ;-)")
		err = tx.Commit()
		if err != nil {
			log.WithError(err).Warn("Error during transaction commit")
		}
		opsProcessedSuccessfully.Inc()
		opsProcessedUseless.Inc()
		return nil
	}

	// If there is a running session, force the switch to reauth
	if !noRunningSession {
		request := radius.CoARequest{
			SessionUID:       val[0].Uid,
			SessionStartDate: val[0].StartDate.Time,
			SwitchIP:         val[0].SwitchIP,
			SwitchSecret:     []byte(c.switchSecret),
		}
		err = request.SendDisconnect()
		if err != nil {
			opsFailedCoA.Inc()
			return err
		}
	}

	// Clear the old lease since we've kicked out the user
	hostname, oldIp, err := c.db.ClearLeasesForMAC(tx, job.ClientMac)
	if oldIp == nil {
		oldIp = &net.IP{}
	}

	log.WithFields(log.Fields{
		"job.Id":         job.Id,
		"job.ClientMac":  job.ClientMac,
		"oldVLAN":        oldVlan,
		"job.TargetVLAN": job.TargetVLAN,
		"oldIP":          oldIp.String(),
		"hostname":       hostname,
		"retries":        job.Retries,
	}).Info("VLAN move successful")

	switchIP := net.IP{}
	switchPort := ""

	if !noRunningSession {
		switchIP = val[0].SwitchIP
		switchPort = val[0].SwitchPort
	}

	// Also log success to database
	err = c.db.LogJobResult(tx, job.ClientMac, oldVlan, job.TargetVLAN, switchIP, switchPort, hostname, *oldIp)
	if err != nil {
		return err
	}

	success = true
	// deferred handler will make sure to commit or rollback
	return nil
}

// work processes the work queue once
func (c *JobController) work() error {
	entryList, err := c.db.FetchPendingJobs()

	for ptr := entryList.Front(); ptr != nil; ptr = ptr.Next() {
		obj := (ptr.Value).(database.Job)
		log.WithFields(log.Fields{
			"id":         obj.Id,
			"clientMAC":  obj.ClientMac,
			"targetVLAN": obj.TargetVLAN,
		}).Info("Processing")
		opsProcessed.Add(1)
		err = c.ProcessMoveVLANJob(&obj)
		if err != nil {
			return err
		}
	}
	return nil
}

// Spin permanently loops over the work queue
func (c *JobController) Spin() {
	log.WithField("node", database.GetNodeID()).Info("Entering main poll loop")

	for {
		//c.db = database.CopyHandler(c.db)
		err := c.work()
		if err != nil {
			log.WithError(err).Warn("poll loop iteration failed")
		}
		time.Sleep(5 * time.Second)
	}
}
