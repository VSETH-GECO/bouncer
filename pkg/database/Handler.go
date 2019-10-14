package database

import (
	"container/list"
	"database/sql"
	"errors"
	"github.com/VSETH-GECO/bouncer/migrations"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
	"github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate"
	mysqlDriver "github.com/golang-migrate/migrate/database/mysql"
	bindata "github.com/golang-migrate/migrate/source/go_bindata"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	opsProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_processed_requests",
		Help: "The total number of processed requests",
	})
	opsProcessedSuccessfully = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_processed_requests_success",
		Help: "The total number of successfully processed requests",
	})
	opsProcessedUseless = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_processed_useless_requests",
		Help: "The total number of requests without any effect",
	})
	opsProcessedNewUser = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_processed_requests_new",
		Help: "The total number of requests with new users",
	})
	opsFailedCoA = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_failed_coa",
		Help: "The total number of failed CoA requests",
	})
)

// Session describes details of a (running) RADIUS Session
type Session struct {
	uid        string
	startDate  mysql.NullTime
	switchIP   string
	switchPort string
}

// Handler is responsible for handling database-related tasks
type Handler struct {
	host         string
	port         int
	user         string
	password     string
	database     string
	connection   *sql.DB
	switchSecret string
}

// CreateHandler instantiates a new handler
func CreateHandler(host string, port int, user string, password string, database string, switchSecret string) *Handler {
	obj := Handler{
		host:         host,
		port:         port,
		user:         user,
		password:     password,
		database:     database,
		switchSecret: switchSecret,
	}
	return &obj
}

// CreateHandlerFromConfig instantiates a new handler, pulling the values from viper
func CreateHandlerFromConfig() *Handler {
	return CreateHandler(viper.GetString("host"),
		viper.GetInt("port"),
		viper.GetString("user"),
		viper.GetString("password"),
		viper.GetString("database"),
		viper.GetString("switch-secret"))
}

// CopyHandler instantiates a new handler from an existing one
func CopyHandler(src *Handler) *Handler {
	obj := Handler{
		host:         src.host,
		port:         src.port,
		user:         src.user,
		password:     src.password,
		database:     src.database,
		switchSecret: src.switchSecret,
		// connection is _not_ duplicated
	}
	// If src is already connected, also Connect the copy
	if src.connection != nil {
		obj.Connect()
	}
	return &obj
}

// Connect to the database
func (h *Handler) Connect() {
	var err error
	h.connection, err = sql.Open("mysql", h.user+":"+h.password+"@tcp("+h.host+":"+strconv.Itoa(h.port)+")/"+h.database)
	if err != nil {
		log.WithError(err).Fatal("Couldn't Connect to database!")
	}
}

// FindSessionForMAC looks up the RADIUS Session for a given client
func (h *Handler) FindSessionForMAC(mac string) (*Session, error) {
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

// FindMACByIP finds a client's MAC from it's DHCP lease
func (h *Handler) FindMACByIP(ip string) (mac string, err error) {
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

// Migrate updates the database schema to the current version
func (h *Handler) Migrate(force int) error {
	if h.connection == nil {
		h.Connect()
	}
	s := bindata.Resource(migrations.AssetNames(),
		func(name string) ([]byte, error) {
			val, err := migrations.Asset(name)
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
	if force == 0 {
		err = m.Up()
	} else {
		err = m.Force(force)
	}
	if err != nil && !(err.Error() == "no change") {
		return err
	}
	return nil
}

// addNewUser adds a new user for a host
func (h *Handler) addNewUser(tx *sql.Tx, id int, clientMac string, targetVLAN int) error {
	opsProcessedNewUser.Inc()
	_, err := tx.Exec("INSERT INTO radreply(username, attribute, op, value) VALUES(?, 'Tunnel-Private-Group-ID', ':=', ?)", clientMac, strconv.Itoa(targetVLAN))
	return err
}

// moveHostToVLAN moves a host between VLANs, taking care of all database updates
func (h *Handler) moveHostToVLAN(id int, clientMac string, targetVLAN int) error {
	// Let's see whether this host can actually be found
	val, err := h.FindSessionForMAC(clientMac)
	if err != nil {
		return err
	}
	noRunningSession := val == nil
	tx, err := h.connection.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("DELETE FROM bouncer_jobs WHERE id=?", id)
	if err != nil {
		return err
	}

	res, err := tx.Query("SELECT value FROM radreply WHERE attribute = 'Tunnel-Private-Group-ID' AND username = ?", strings.ToLower(clientMac))
	if err != nil {
		res.Close()
		return err
	}
	oldVlan := -1
	if !res.Next() {
		// No entry found - this user is completely new
		res.Close()
		err = h.addNewUser(tx, id, clientMac, targetVLAN)
		if err != nil {
			return err
		}
	} else {
		// We found an entry -> update it
		var oldVlanStr string
		err = res.Scan(&oldVlanStr)
		res.Close()
		if err != nil {
			return err
		}
		oldVlan, err = strconv.Atoi(oldVlanStr)
		if err != nil {
			return err
		}

		_, err = tx.Exec("UPDATE radreply SET value = ? WHERE attribute = 'Tunnel-Private-Group-ID' AND username = ?", strconv.Itoa(targetVLAN), strings.ToLower(clientMac))
		if err != nil {
			return err
		}
	}

	// I'm not sure if you want this, so let's log that case
	if oldVlan == targetVLAN {
		log.WithFields(log.Fields{
			"id":         id,
			"clientMAC":  clientMac,
			"targetVLAN": targetVLAN,
		}).Warn("Got request to move to same vlan, silently discarding this ;-)")
		tx.Commit()
		opsProcessedSuccessfully.Inc()
		opsProcessedUseless.Inc()
		return nil
	}

	// If there is a running session, force the switch to reauth
	if !noRunningSession {
		request := radius.CoARequest{
			SessionUID:       val.uid,
			SessionStartDate: val.startDate.Time,
			SwitchIP:         net.ParseIP(val.switchIP),
			SwitchSecret:     []byte(h.switchSecret),
		}
		err = request.SendDisconnect()
		if err != nil {
			opsFailedCoA.Inc()
			return err
		}
	}
	log.WithFields(log.Fields{
		"id":         id,
		"clientMAC":  clientMac,
		"oldVLAN":    oldVlan,
		"targetVLAN": targetVLAN,
	}).Info("VLAN move successful")

	// Also log success to database
	_, err = tx.Exec("INSERT INTO bouncer_log(clientMAC, oldVLAN, newVLAN, switchIP, switchPort) VALUES(?, ?, ?, ?, ?)",
		strings.ToLower(clientMac), strconv.Itoa(oldVlan), strconv.Itoa(targetVLAN), val.switchIP, val.switchPort)
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
func (h *Handler) work() error {
	res, err := h.connection.Query("SELECT id,clientMAC,targetVLAN FROM bouncer_jobs ORDER BY id ASC")
	if err != nil {
		return err
	}
	type work struct {
		id         int
		targetVLAN int
		clientMac  string
	}
	entryList := list.New()
	for res.Next() {
		obj := work{}
		err = res.Scan(&obj.id, &obj.clientMac, &obj.targetVLAN)
		if err != nil {
			return err
		}
		obj.clientMac = strings.ToUpper(obj.clientMac)
		log.WithFields(log.Fields{
			"id":         obj.id,
			"clientMAC":  obj.clientMac,
			"targetVLAN": obj.targetVLAN,
		}).Debug("New job fetched")
		entryList.PushBack(obj)
	}

	for ptr := entryList.Front(); ptr != nil; ptr = ptr.Next() {
		obj := (ptr.Value).(work)
		log.WithFields(log.Fields{
			"id":         obj.id,
			"clientMAC":  obj.clientMac,
			"targetVLAN": obj.targetVLAN,
		}).Info("Processing")
		opsProcessed.Add(1)
		err = h.moveHostToVLAN(obj.id, obj.clientMac, obj.targetVLAN)
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) CheckDBVersion() (err error) {
	if h.connection == nil {
		h.Connect()
	}

	s := bindata.Resource(migrations.AssetNames(),
		func(name string) ([]byte, error) {
			val, err := migrations.Asset(name)
			return val, err
		})
	source, err := bindata.WithInstance(s)
	if err != nil {
		return
	}
	driver, err := mysqlDriver.WithInstance(h.connection, &mysqlDriver.Config{})
	if err != nil {
		return
	}
	m, err := migrate.NewWithInstance("go-bindata", source, "radius", driver)
	if err != nil {
		return
	}
	version, dirty, err := m.Version()
	if err != nil {
		return
	}
	names := s.Names
	sort.Strings(names)
	lastMigration := names[len(names)-1]
	nameParts := strings.Split(lastMigration, "_")
	if len(nameParts) < 2 {
		err = errors.New("unexpected string split on resouce name")
		return
	}
	latestVersion, err := strconv.ParseUint(nameParts[0], 10, 16)
	entry := log.WithFields(log.Fields{
		"latestVersion": latestVersion,
		"version":       version,
		"dirty":         dirty,
	})
	if uint(latestVersion) == version {
		entry.Debug("Database version is matching newest version at build time")
	} else if uint(latestVersion) >= version {
		entry.Fatal("Database version is older than newest version at build time")
	} else {
		entry.Warn("Database version is newer than newest version at build time")
	}
	if dirty {
		entry.Warn("Database is marked dirty!")
	}
	return
}

// PollLoop permanently loops over the work queue
func (h *Handler) PollLoop() {
	log.Info("Entering main poll loop")
	if h.connection == nil {
		h.Connect()
	}

	for {
		err := h.work()
		if err != nil {
			log.WithError(err).Warn("poll loop iteration failed")
		}
		time.Sleep(5 * time.Second)
	}
}
