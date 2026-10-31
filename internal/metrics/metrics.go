// Package metrics samples the user counts from the database into Prometheus gauges and serves them over HTTP.
package metrics

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/prometheus/client_golang/prometheus"
)

// The collectors are registered by Serve, not at init, so that importing this package has no side effects.
var (
	usersTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bouncer_users_total",
		Help: "Total number of users in the database",
	})
	usersAuthorized = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bouncer_users_authorized",
		Help: "Total number of users authorized",
	})
	usersActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bouncer_users_active",
		Help: "Total number of users with active sessions",
	})
	usersActivePerVLAN = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bouncer_users_active_per_vlan",
			Help: "Total number of users with active sessions per VLAN",
		},
		[]string{"vlan"},
	)
	usersAuthorizedPerVLAN = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bouncer_users_per_vlan",
			Help: "Total number of users per VLAN",
		},
		[]string{"vlan"},
	)
	usersActivePerSwitch = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bouncer_users_active_per_switch",
			Help: "Total number of users with active sessions per switch",
		},
		[]string{"switch"},
	)
	leader = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bouncer_leader",
		Help: "1 on the replica currently leading, 0 otherwise",
	})
)

func register(r prometheus.Registerer) {
	r.MustRegister(usersTotal, usersAuthorized, usersActive, usersActivePerVLAN, usersAuthorizedPerVLAN, usersActivePerSwitch,
		leader)
}

// SetLeader records whether this replica currently leads (runs the jobs and the Discord bot).
func SetLeader(leading bool) {
	if leading {
		leader.Set(1)
	} else {
		leader.Set(0)
	}
}

// sample samples all metrics once. A failing query is logged and only leaves its own metric stale.
func sample(ctx context.Context, q db.Querier) {
	slog.Debug("Updating prometheus metrics...")
	warn := func(metric string, err error) {
		if ctx.Err() == nil {
			slog.Warn("Couldn't update prometheus metric", "error", err, "metric", metric)
		}
	}
	setGauge := func(g prometheus.Gauge, name string, count func(context.Context, db.Querier) (int, error)) {
		n, err := count(ctx, q)
		if err != nil {
			warn(name, err)
			return
		}
		g.Set(float64(n))
	}

	setGauge(usersTotal, "bouncer_users_total", freeradius.CountKnown)
	setGauge(usersAuthorized, "bouncer_users_authorized", freeradius.CountAuthorized)
	setGauge(usersActive, "bouncer_users_active", freeradius.CountActive)

	if counts, err := freeradius.AuthorizedPerVLAN(ctx, q); err != nil {
		warn("bouncer_users_per_vlan", err)
	} else {
		usersAuthorizedPerVLAN.Reset()
		for _, c := range counts {
			usersAuthorizedPerVLAN.WithLabelValues(strconv.Itoa(c.VLAN)).Set(float64(c.Count))
		}
	}
	if counts, err := freeradius.ActivePerVLAN(ctx, q); err != nil {
		warn("bouncer_users_active_per_vlan", err)
	} else {
		usersActivePerVLAN.Reset()
		for _, c := range counts {
			usersActivePerVLAN.WithLabelValues(strconv.Itoa(c.VLAN)).Set(float64(c.Count))
		}
	}
	if counts, err := freeradius.ActivePerSwitch(ctx, q); err != nil {
		warn("bouncer_users_active_per_switch", err)
	} else {
		usersActivePerSwitch.Reset()
		for _, c := range counts {
			usersActivePerSwitch.WithLabelValues(c.SwitchIP).Set(float64(c.Count))
		}
	}
	slog.Debug("Updating prometheus metrics done!")
}

// runSampler samples immediately and then every few seconds until ctx is cancelled.
func runSampler(ctx context.Context, q db.Querier) {
	sample(ctx, q)

	ticker := time.NewTicker(7 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample(ctx, q)
		}
	}
}
