package database

import (
	"database/sql"
	"errors"
	"github.com/VSETH-GECO/bouncer/migrations"
	"github.com/VSETH-GECO/bouncer/pkg/config"
	"github.com/golang-migrate/migrate/v4"
	mysqlDriver "github.com/golang-migrate/migrate/v4/database/mysql"
	bindata "github.com/golang-migrate/migrate/v4/source/go_bindata"
	log "github.com/sirupsen/logrus"
	"net"
	"sort"
	"strconv"
	"strings"
)

// RadiusSession describes details of a (running) RADIUS RadiusSession
type RadiusSession struct {
	Uid           string
	StartDate     sql.NullTime
	EndDate       sql.NullTime
	EndReason     string
	SwitchIP      net.IP
	SwitchPort    string
	BytesSent     int
	BytesReceived int
	ClientIP      net.IP
}

// Handler is responsible for handling database-related tasks
type Handler struct {
	host       string
	port       int
	user       string
	password   string
	database   string
	connection *sql.DB
}

// CreateHandler instantiates a new handler
func CreateHandler(host string, port int, user string, password string, database string, switchSecret string) *Handler {
	obj := Handler{
		host:     host,
		port:     port,
		user:     user,
		password: password,
		database: database,
	}
	return &obj
}

// CreateHandlerFromConfig instantiates a new handler, pulling the values from our config
func CreateHandlerFromConfig() *Handler {
	return CreateHandler(
		config.CurrentOptions.DBHost,
		config.CurrentOptions.DBPort,
		config.CurrentOptions.DBUser,
		config.CurrentOptions.DBPassword,
		config.CurrentOptions.DBDatabase,
		config.CurrentOptions.SwitchCOASecret,
	)
}

// CopyHandler instantiates a new handler from an existing one
func CopyHandler(src *Handler) *Handler {
	obj := Handler{
		host:     src.host,
		port:     src.port,
		user:     src.user,
		password: src.password,
		database: src.database,
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
	log.Info("Establishing new DB connection")
	h.connection, err = sql.Open("mysql", h.user+":"+h.password+"@tcp("+h.host+":"+strconv.Itoa(h.port)+")/"+h.database+"?multiStatements=true&parseTime=true")
	if err != nil {
		log.WithError(err).Fatal("Couldn't Connect to database!")
	}
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

// CheckDBVersion ensures that we're running with the right database version
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
		err = errors.New("unexpected string split on resource name")
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

func Close(rows *sql.Rows) {
	if rows != nil {
		err := rows.Close()
		if err != nil {
			log.WithError(err).Warn("Couldn't properly close sql.Rows")
		}
	}
}

func Rollback(tx *sql.Tx) {
	if tx != nil {
		err := tx.Rollback()
		if err != sql.ErrTxDone && err != nil {
			log.WithError(err).Warn("Transaction rollback failed")
		}
	}
}

func (h *Handler) BeginTx() (*sql.Tx, error) {
	return h.connection.Begin()
}
