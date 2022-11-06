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
func (c *JobController) ProcessMoveVLANJob(jobId int, clientMac string, targetVLAN int) error {
	clientMac = strings.ToLower(clientMac)

	// Let's see whether this host can actually be found
	val, err := c.db.FindSessionsForMAC(clientMac)
	if err != nil {
		return err
	}
	noRunningSession := len(val) == 0

	tx, err := c.db.BeginTx()
	if err != nil {
		return err
	}
	defer database.Rollback(tx)

	if jobId != -1 {
		err = c.db.DeleteJob(tx, jobId)
		if err != nil {
			return err
		}
	}

	oldVlan, err := c.db.CheckUserSignedIn(tx, clientMac)
	if err != nil {
		return err
	}

	if oldVlan == -1 {
		// No entry found - this user is completely new
		err = c.db.AddNewUser(tx, clientMac, targetVLAN)
		if err != nil {
			return err
		}
	} else {
		// We found an entry -> update it
		err = c.db.UpdateUser(tx, clientMac, targetVLAN)
		if err != nil {
			return err
		}
	}

	// I'm not sure if you want this, so let's log that case
	if oldVlan == targetVLAN {
		log.WithFields(log.Fields{
			"jobId":      jobId,
			"clientMAC":  clientMac,
			"targetVLAN": targetVLAN,
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
	hostname, oldIp, err := c.db.ClearLeasesForMAC(tx, clientMac)
	if oldIp == nil {
		oldIp = &net.IP{}
	}

	log.WithFields(log.Fields{
		"jobId":      jobId,
		"clientMAC":  clientMac,
		"oldVLAN":    oldVlan,
		"targetVLAN": targetVLAN,
		"oldIP":      oldIp.String(),
		"hostname":   hostname,
	}).Info("VLAN move successful")

	switchIP := net.IP{}
	switchPort := ""

	if !noRunningSession {
		switchIP = val[0].SwitchIP
		switchPort = val[0].SwitchPort
	}

	// Also log success to database
	err = c.db.LogJobResult(tx, clientMac, oldVlan, targetVLAN, switchIP, switchPort, hostname, *oldIp)
	if err != nil {
		return err
	}

	err = tx.Commit()
	if err != nil {
		return err
	}
	opsProcessedSuccessfully.Add(1)
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
		err = c.ProcessMoveVLANJob(obj.Id, obj.ClientMac, obj.TargetVLAN)
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
		err := c.work()
		if err != nil {
			log.WithError(err).Warn("poll loop iteration failed")
		}
		time.Sleep(5 * time.Second)
	}
}
