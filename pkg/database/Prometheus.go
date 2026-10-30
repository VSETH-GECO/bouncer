package database

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
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
	opsProcessedNewUser = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_processed_requests_new",
		Help: "The total number of requests with new users",
	})
)

type PrometheusHandler struct {
	handler *Handler
}

// getIntValWithSQL returns the first column of the first row of the query, or 0 if it returned no rows
func (p *PrometheusHandler) getIntValWithSQL(ctx context.Context, query string) (int, error) {
	var value int
	err := p.handler.db.QueryRowContext(ctx, query).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return value, nil
}

func (p *PrometheusHandler) getTwoIntValWithSQL(ctx context.Context, query string, handler func(int, int)) error {
	rows, err := p.handler.db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var val1, val2 int
		err = rows.Scan(&val1, &val2)
		if err != nil {
			return err
		}
		handler(val1, val2)
	}
	return rows.Err()
}

func (p *PrometheusHandler) getStringIntValWithSQL(ctx context.Context, query string, handler func(string, int)) error {
	rows, err := p.handler.db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var val1 string
		var val2 int
		err = rows.Scan(&val1, &val2)
		if err != nil {
			return err
		}
		handler(val1, val2)
	}
	return rows.Err()
}

// UpdateCounters samples all metrics once. A failing query is logged and only leaves its own metric stale.
func (p *PrometheusHandler) UpdateCounters(ctx context.Context) {
	log.Debug("Updating prometheus metrics...")
	warn := func(metric string, err error) {
		if ctx.Err() == nil {
			log.WithError(err).WithField("metric", metric).Warn("Couldn't update prometheus metric")
		}
	}

	authorized, err := p.getIntValWithSQL(ctx, "SELECT count(*) FROM radreply GROUP BY username")
	if err != nil {
		warn("bouncer_users_authorized", err)
	} else {
		usersAuthorized.Set(float64(authorized))
	}
	total, err := p.getIntValWithSQL(ctx, "SELECT count(DISTINCT username) FROM radacct")
	if err != nil {
		warn("bouncer_users_total", err)
	} else {
		usersTotal.Set(float64(total))
	}
	active, err := p.getIntValWithSQL(ctx, "SELECT count(*) FROM radacct WHERE acctstoptime IS NULL GROUP BY username")
	if err != nil {
		warn("bouncer_users_active", err)
	} else {
		usersActive.Set(float64(active))
	}
	usersAuthorizedPerVLAN.Reset()
	err = p.getTwoIntValWithSQL(ctx, "SELECT value, count(username) FROM radreply WHERE attribute = 'Tunnel-Private-Group-ID' GROUP BY value", func(vlan int, count int) {
		usersAuthorizedPerVLAN.With(prometheus.Labels{
			"vlan": strconv.Itoa(vlan),
		}).Add(float64(count))
	})
	if err != nil {
		warn("bouncer_users_per_vlan", err)
	}
	usersActivePerVLAN.Reset()
	err = p.getTwoIntValWithSQL(ctx, "SELECT b.value, count(a.username) FROM radacct AS a JOIN radreply as b ON a.username = b.username WHERE a.acctstoptime IS NULL AND b.attribute = 'Tunnel-Private-Group-ID' GROUP BY b.value", func(vlan int, count int) {
		usersActivePerVLAN.With(prometheus.Labels{
			"vlan": strconv.Itoa(vlan),
		}).Add(float64(count))
	})
	if err != nil {
		warn("bouncer_users_active_per_vlan", err)
	}
	usersActivePerSwitch.Reset()
	err = p.getStringIntValWithSQL(ctx, "SELECT nasipaddress, count(username) FROM radacct WHERE acctstoptime IS NULL GROUP BY nasipaddress", func(sw string, count int) {
		usersActivePerSwitch.With(prometheus.Labels{
			"switch": sw,
		}).Add(float64(count))
	})
	if err != nil {
		warn("bouncer_users_active_per_switch", err)
	}
	log.Debug("Updating prometheus metrics done!")
}

// StartUpdating samples the metrics every few seconds until ctx is cancelled, then returns ctx.Err()
func StartUpdating(ctx context.Context, handler *Handler) error {
	obj := PrometheusHandler{
		handler: handler,
	}

	obj.UpdateCounters(ctx)

	ticker := time.NewTicker(7 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			obj.UpdateCounters(ctx)
		}
	}
}
