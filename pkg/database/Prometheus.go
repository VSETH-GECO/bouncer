package database

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
	"strconv"
	"time"
)

var (
	usersTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bouncer_users_total",
		Help: "Total number of users in the database",
	})
	usersAuthorized = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bouncer_users_authorized",
		Help: "Total number of users authorized",
	})
	usersActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bouncer_users_active",
		Help: "Total number of users with active sessions",
	})
	usersActivePerVLAN = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bouncer_users_active_per_vlan",
			Help: "Total number of users with active sessions per VLAN",
		},
		[]string{"vlan"},
	)
	usersAuthorizedPerVLAN = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bouncer_users_per_vlan",
			Help: "Total number of users per VLAN",
		},
		[]string{"vlan"},
	)
	usersActivePerSwitch = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bouncer_users_active_per_switch",
			Help: "Total number of users with active sessions per switch",
		},
		[]string{"switch"},
	)
)

type PrometheusHandler struct {
	handler *Handler
}

func (p *PrometheusHandler) getIntValWithSQL(query string) (value int, ok bool) {
	ok = false
	res, err := p.handler.connection.Query(query)
	if err != nil {
		log.WithError(err).Warn("Error during query execution")
		return
	}
	defer res.Close()
	if !res.Next() {
		log.Warn("Empty result set")
		return
	}
	err = res.Scan(&value)
	if err != nil {
		log.WithError(err).Warn("Error during query execution")
		return
	}
	ok = true
	return
}

func (p *PrometheusHandler) getTwoIntValWithSQL(query string, handler func(int, int)) {
	res, err := p.handler.connection.Query(query)
	if err != nil {
		log.WithError(err).Warn("Error during query execution")
		return
	}
	defer res.Close()
	for res.Next() {
		var val1, val2 int
		err = res.Scan(&val1, &val2)
		if err != nil {
			log.WithError(err).Warn("Error during query execution")
			return
		}
		handler(val1, val2)
	}
}

func (p *PrometheusHandler) getStringIntValWithSQL(query string, handler func(string, int)) {
	res, err := p.handler.connection.Query(query)
	if err != nil {
		log.WithError(err).Warn("Error during query execution")
		return
	}
	defer res.Close()
	for res.Next() {
		var val1 string
		var val2 int
		err = res.Scan(&val1, &val2)
		if err != nil {
			log.WithError(err).Warn("Error during query execution")
			return
		}
		handler(val1, val2)
	}
}

func (p *PrometheusHandler) UpdateCounters() {
	log.Debug("Updating prometheus metrics...")
	authorized, ok := p.getIntValWithSQL("SELECT count(*) FROM radreply GROUP BY username")
	if ok {
		usersAuthorized.Set(float64(authorized))
	}
	total, ok := p.getIntValWithSQL("SELECT count(DISTINCT username) FROM radacct")
	if ok {
		usersTotal.Set(float64(total))
	}
	active, ok := p.getIntValWithSQL("SELECT count(*) FROM radacct WHERE acctstoptime IS NULL GROUP BY username")
	if ok {
		usersActive.Set(float64(active))
	}
	usersAuthorizedPerVLAN.Reset()
	p.getTwoIntValWithSQL("SELECT value, count(username) FROM radreply WHERE attribute = 'Tunnel-Private-Group-ID' GROUP BY value", func(vlan int, count int) {
		usersAuthorizedPerVLAN.With(prometheus.Labels{
			"vlan": strconv.Itoa(vlan),
		}).Add(float64(count))
	})
	usersActivePerVLAN.Reset()
	p.getTwoIntValWithSQL("SELECT b.value, count(a.username) FROM radacct AS a JOIN radreply as b ON a.username = b.username WHERE a.acctstoptime IS NULL AND b.attribute = 'Tunnel-Private-Group-ID' GROUP BY b.value", func(vlan int, count int) {
		usersActivePerVLAN.With(prometheus.Labels{
			"vlan": strconv.Itoa(vlan),
		}).Add(float64(count))
	})
	usersActivePerSwitch.Reset()
	p.getStringIntValWithSQL("SELECT nasipaddress, count(username) FROM radacct WHERE acctstoptime IS NULL GROUP BY nasipaddress", func(sw string, count int) {
		usersActivePerSwitch.With(prometheus.Labels{
			"switch": sw,
		}).Add(float64(count))
	})
	log.Debug("Updating prometheus metrics done!")
}

func StartUpdating(handler *Handler) {
	obj := PrometheusHandler{
		handler: CopyHandler(handler),
	}

	obj.UpdateCounters()

	for {
		time.Sleep(7 * time.Second)
		obj.UpdateCounters()
	}
}
