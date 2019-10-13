package database

import (
	"container/list"
	"database/sql"
	"errors"
	"flag"
	"github.com/VSETH-GECO/bouncer/migrations"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"github.com/go-sql-driver/mysql"
	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate"
	mysqlDriver "github.com/golang-migrate/migrate/database/mysql"
	bindata "github.com/golang-migrate/migrate/source/go_bindata"
	log "github.com/sirupsen/logrus"
	"net"
	"strconv"
	"strings"
	"time"
)

type Session struct {
	uid string
	startDate mysql.NullTime
	switchIP string
	switchPort string
}

type Handler struct {
	host string
	port int
	user string
	password string
	database string
	connection *sql.DB
	switchSecret string
}

func CreateHandler() *Handler {
	obj := Handler{}
	return &obj
}

func (h* Handler) RegisterFlags() {
	flag.StringVar(&h.host, "host", "127.0.0.1", "MySQL host")
	flag.IntVar(&h.port, "port", 3306, "MySQL port")
	flag.StringVar(&h.user, "user", "radius", "MySQL user")
	flag.StringVar(&h.password, "password", "foobar", "MySQL password")
	flag.StringVar(&h.database, "database", "radius", "MySQL database")
	flag.StringVar(&h.switchSecret, "secret", "", "Switch CoA secret")
}

func (h* Handler) connect() {
	var err error
	h.connection, err = sql.Open("mysql", h.user + ":" + h.password + "@tcp(" + h.host + ":" + strconv.Itoa(h.port) + ")/" + h.database)
	if err != nil {
		log.WithError(err).Fatal("Couldn't connect to database!")
	}
}

func (h* Handler) FindSessionForMAC(mac string) (*Session, error){
	obj := Session{}
	rows, err := h.connection.Query("select acctsessionid, acctstarttime, nasipaddress, nasportid from radacct where username=? and (acctterminatecause='');", mac)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		err = rows.Scan(&obj.uid, &obj.startDate, &obj.switchIP, &obj.switchPort)
		if err != nil {
			return nil, err
		}
		return &obj, nil
	}
	return nil, nil
}

func (h* Handler) FindMACByIP(ip string) (mac string, err error) {
	rows, err := h.connection.Query("SELECT hex(hwaddr) FROM lease4 WHERE INET_NTOA(address)=?;", ip)
	defer rows.Close()
	if err != nil {
		return
	}
	if !rows.Next() {
		return
	}
	err = rows.Scan(&mac)
	return
}

// Update the database schema to the current version
func (h* Handler) Migrate() error {
	s := bindata.Resource(migrations.AssetNames(),
		func(name string) ([]byte, error) {
			val, err := migrations.Asset(name);
			return val, err
		})
	source, err := bindata.WithInstance(s)
	if err != nil {
		return err
	}
	driver, err := mysqlDriver.WithInstance(h.connection, &mysqlDriver.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("go-bindata", source, "radius", driver)
	if err != nil {
		return err
	}
	err = m.Up()
	if err != nil && ! (err.Error() == "no change") {
		return err
	}
	return nil
}

func (h* Handler) moveHostToVLAN(id int, clientMac string, targetVLAN int) error {
	// Let's see whether this host can actually be found
	val, err := h.FindSessionForMAC(clientMac)
	if err != nil{
		return err
	}
	if val == nil {
		return errors.New("no session for MAC found")
	}
	tx, err := h.connection.Begin()
	if err != nil{
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("DELETE FROM bouncer_jobs WHERE id=?", id)
	if err != nil{
		return err
	}

	res, err := tx.Query("SELECT value FROM radreply WHERE attribute = 'Tunnel-Private-Group-ID' AND username = ?", strings.ToLower(clientMac))
	if err != nil{
		res.Close()
		return err
	}
	if !res.Next() {
		res.Close()
		return errors.New("no login for MAC found")
	}
	var oldVlanStr string
	err = res.Scan(&oldVlanStr)
	res.Close()
	if err != nil{
		return err
	}
	oldVlan, err := strconv.Atoi(oldVlanStr)
	if err != nil{
		return err
	}

	if oldVlan == targetVLAN {
		log.WithFields(log.Fields{
			"id": id,
			"clientMAC": clientMac,
			"targetVLAN": targetVLAN,
		}).Warn("Got request to move to same vlan, silently discarding this ;-)")
		return nil
	}

	_, err = tx.Exec("UPDATE radreply SET value = ? WHERE attribute = 'Tunnel-Private-Group-ID' AND username = ?", strconv.Itoa(targetVLAN), strings.ToLower(clientMac))
	if err != nil{
		return err
	}

	_, err = tx.Exec("INSERT INTO bouncer_log(clientMAC, oldVLAN, newVLAN, switchIP, switchPort) VALUES(?, ?, ?, ?, ?)",
		strings.ToLower(clientMac), strconv.Itoa(oldVlan), strconv.Itoa(targetVLAN), val.switchIP, val.switchPort)
	if err != nil{
		return err
	}

	request := radius.CoARequest{
		SessionUid: val.uid,
		SessionStartDate: val.startDate.Time,
		SwitchIP: net.ParseIP(val.switchIP),
		SwitchSecret: []byte(h.switchSecret),
	}
	err = request.SendDisconnect()
	if err != nil {
		return err
	}
	log.WithFields(log.Fields{
		"id": id,
		"clientMAC": clientMac,
		"oldVLAN": oldVlan,
		"targetVLAN": targetVLAN,
	}).Info("VLAN move successful")
	err = tx.Commit()
	if err != nil {
		return err
	}
	return nil
}

func (h* Handler) work() error {
	res, err := h.connection.Query("SELECT id,clientMAC,targetVLAN FROM bouncer_jobs ORDER BY id ASC")
	if err != nil {
		return err
	}
	type work struct {
		id int
		targetVLAN int
		clientMac string
	}
	list := list.New()
	for res.Next() {
		obj := work{}
		err = res.Scan(&obj.id, &obj.clientMac, &obj.targetVLAN)
		if err != nil {
			return err
		}
		obj.clientMac = strings.ToUpper(obj.clientMac)
		log.WithFields(log.Fields{
			"id": obj.id,
			"clientMAC": obj.clientMac,
			"targetVLAN": obj.targetVLAN,
		}).Info("New job fetched")
		list.PushBack(obj)
	}

	log.Info("Loop A done")

	for ptr := list.Front(); ptr != nil ; ptr = ptr.Next() {
		obj := (ptr.Value).(work)
		log.WithFields(log.Fields{
			"id": obj.id,
			"clientMAC": obj.clientMac,
			"targetVLAN": obj.targetVLAN,
		}).Info("Processing")
		err = h.moveHostToVLAN(obj.id, obj.clientMac, obj.targetVLAN)
		if err != nil {
			return err
		}
	}
	return nil
}

func (h* Handler) PollLoop() {
	h.connect()
	log.Info("Migrating database...")
	err := h.Migrate()
	if err != nil {
		log.WithError(err).Fatal("Migrations failed!")
	}
	log.Info("Entering main poll loop")
	for {
		err := h.work()
		if err != nil {
			log.WithError(err).Warn("poll loop iteration failed")
		}
		time.Sleep(5 * time.Second)
	}
}