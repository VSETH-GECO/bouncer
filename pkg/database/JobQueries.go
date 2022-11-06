package database

import (
	"container/list"
	"database/sql"
	log "github.com/sirupsen/logrus"
	"net"
	"strconv"
	"strings"
)

type Job struct {
	Id         int
	TargetVLAN int
	ClientMac  string
}

func (h *Handler) DeleteJob(tx *sql.Tx, jobId int) error {
	_, err := tx.Exec("DELETE FROM bouncer_jobs WHERE id=?", jobId)
	return err
}

func (h *Handler) LogJobResult(tx *sql.Tx, clientMAC string, oldVLAN int, newVLAN int, switchIP net.IP, switchPort string, hostname string, oldIP net.IP) error {
	_, err := tx.Exec("INSERT INTO bouncer_log(clientMAC, oldVLAN, newVLAN, switchIP, switchPort, clientName, clientIP) VALUES(?, ?, ?, ?, ?, ?, ?)",
		strings.ToLower(clientMAC), strconv.Itoa(oldVLAN), strconv.Itoa(newVLAN), switchIP.String(), switchPort, hostname, oldIP.String())
	return err
}

func (h *Handler) FetchPendingJobs() (*list.List, error) {
	res, err := h.connection.Query("SELECT id,clientMAC,targetVLAN FROM bouncer_jobs ORDER BY id ASC")
	defer Close(res)
	if err != nil {
		return nil, err
	}

	entryList := list.New()
	for res.Next() {
		obj := Job{}
		err = res.Scan(&obj.Id, &obj.ClientMac, &obj.TargetVLAN)
		if err != nil {
			return nil, err
		}
		obj.ClientMac = strings.ToUpper(obj.ClientMac)
		log.WithFields(log.Fields{
			"id":         obj.Id,
			"clientMAC":  obj.ClientMac,
			"targetVLAN": obj.TargetVLAN,
		}).Debug("New job fetched")
		entryList.PushBack(obj)
	}

	return entryList, nil
}

func (h *Handler) CreateNewJob(clientMac string, targetVlan string) error {
	_, err := h.connection.Exec("INSERT INTO bouncer_jobs(clientMAC, targetVLAN) VALUES(?, ?);", clientMac, targetVlan)
	return err
}
