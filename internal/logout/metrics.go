package logout

import "github.com/prometheus/client_golang/prometheus"

// The collectors are registered by Register, not at init, so that importing this package has no side effects.
var (
	// LockoutActive is 1 while a global logout's lockout runs. The logout loop sets it.
	LockoutActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bouncer_global_logout_lockout_active",
		Help: "1 while a global logout's lockout drops moves to other VLANs, 0 otherwise",
	})
	// IgnoredJobsTotal counts the jobs a lockout dropped. The job loop increments it.
	IgnoredJobsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_global_logout_ignored_jobs_total",
		Help: "Jobs dropped because a global logout's lockout ignores them",
	})
)

// Register registers the global logout's collectors on r.
func Register(r prometheus.Registerer) {
	r.MustRegister(LockoutActive, IgnoredJobsTotal)
}
