package database

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

type Job struct {
	Id         int
	TargetVLAN int
	ClientMac  string
	LastUpdate sql.NullTime
	Retries    int
}

func (h *Handler) DeleteJob(ctx context.Context, q DBTX, jobId int) error {
	_, err := q.ExecContext(ctx, "DELETE FROM bouncer_jobs WHERE id=?", jobId)
	if err != nil {
		return fmt.Errorf("deleting job %d: %w", jobId, err)
	}
	return nil
}

func (h *Handler) LogJobResult(ctx context.Context, q DBTX, clientMAC string, oldVLAN int, newVLAN int, switchIP net.IP, switchPort string, hostname string, oldIP net.IP) error {
	_, err := q.ExecContext(ctx, "INSERT INTO bouncer_log(clientMAC, oldVLAN, newVLAN, switchIP, switchPort, clientName, clientIP) VALUES(?, ?, ?, ?, ?, ?, ?)",
		strings.ToLower(clientMAC), strconv.Itoa(oldVLAN), strconv.Itoa(newVLAN), switchIP.String(), switchPort, hostname, oldIP.String())
	if err != nil {
		return fmt.Errorf("logging job result: %w", err)
	}
	return nil
}

func (h *Handler) SetJobErrorCount(ctx context.Context, job *Job) error {
	_, err := h.db.ExecContext(ctx, "UPDATE bouncer_jobs SET retires=?, last_update=CURRENT_TIMESTAMP() WHERE id=?", job.Retries, job.Id)
	if err != nil {
		return fmt.Errorf("updating retry counter of job %d: %w", job.Id, err)
	}
	return nil
}

func (h *Handler) FetchPendingJobs(ctx context.Context) ([]Job, error) {
	rows, err := h.db.QueryContext(ctx, "SELECT id,clientMAC,targetVLAN,last_update,retires FROM bouncer_jobs WHERE retires < 3")
	if err != nil {
		return nil, fmt.Errorf("querying jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		obj := Job{}
		err = rows.Scan(&obj.Id, &obj.ClientMac, &obj.TargetVLAN, &obj.LastUpdate, &obj.Retries)
		if err != nil {
			return nil, fmt.Errorf("scanning job: %w", err)
		}
		obj.ClientMac = strings.ToUpper(obj.ClientMac)
		log.WithFields(log.Fields{
			"id":         obj.Id,
			"clientMAC":  obj.ClientMac,
			"targetVLAN": obj.TargetVLAN,
			"lastUpdate": obj.LastUpdate,
			"retries":    obj.Retries,
		}).Debug("New job fetched")
		jobs = append(jobs, obj)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating jobs: %w", err)
	}

	return jobs, nil
}

func (h *Handler) CreateNewJob(ctx context.Context, clientMac string, targetVlan string) error {
	_, err := h.db.ExecContext(ctx, "INSERT INTO bouncer_jobs(clientMAC, targetVLAN) VALUES(?, ?);", clientMac, targetVlan)
	if err != nil {
		return fmt.Errorf("creating job: %w", err)
	}
	return nil
}
