package loop

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Values of the "result" label of rounds.
const (
	resultSuccess = "success"
	resultError   = "error"
	resultPanic   = "panic"
)

var (
	rounds = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bouncer_loop_rounds_total",
			Help: "Rounds of each loop by outcome",
		},
		[]string{"loop", "result"},
	)
	roundDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bouncer_loop_round_duration_seconds",
			Help:    "Wall time of a round in seconds, failed rounds included",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"loop"},
	)
	lastSuccess = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bouncer_loop_last_success_timestamp_seconds",
			Help: "Unix time of the loop's last successful round",
		},
		[]string{"loop"},
	)
	consecutiveFailures = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bouncer_loop_consecutive_failures",
			Help: "Failed rounds since the loop's last success",
		},
		[]string{"loop"},
	)
	waitSeconds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bouncer_loop_wait_seconds",
			Help: "Wait before the loop's next round in seconds",
		},
		[]string{"loop"},
	)
)

// Register registers the loop collectors on r. Nothing is registered at init (same rule as internal/metrics).
func Register(r prometheus.Registerer) {
	r.MustRegister(rounds, roundDuration, lastSuccess, consecutiveFailures, waitSeconds)
}

// initSeries creates the series of a starting loop: its three rounds counters at 0 so that increase() works from the
// first scrape, no failures, and the base interval as the wait. lastSuccess stays absent until the first success.
func initSeries(name string, interval time.Duration) {
	for _, res := range []string{resultSuccess, resultError, resultPanic} {
		rounds.WithLabelValues(name, res)
	}
	consecutiveFailures.WithLabelValues(name).Set(0)
	waitSeconds.WithLabelValues(name).Set(interval.Seconds())
}

// deleteSeries removes every series of the loop from all five collectors, so that a loop that has ended (or a replica
// that stopped leading) exports nothing about it.
func deleteSeries(name string) {
	l := prometheus.Labels{"loop": name}
	rounds.DeletePartialMatch(l)
	roundDuration.DeletePartialMatch(l)
	lastSuccess.DeletePartialMatch(l)
	consecutiveFailures.DeletePartialMatch(l)
	waitSeconds.DeletePartialMatch(l)
}

// recordRound records a round that counts: its duration, its outcome, the failure streak and, on success, the time.
func recordRound(name, res string, d time.Duration, failures int) {
	roundDuration.WithLabelValues(name).Observe(d.Seconds())
	rounds.WithLabelValues(name, res).Inc()
	consecutiveFailures.WithLabelValues(name).Set(float64(failures))
	if res == resultSuccess {
		lastSuccess.WithLabelValues(name).Set(float64(time.Now().Unix()))
	}
}

// recordWait records the wait before the loop's next round.
func recordWait(name string, d time.Duration) {
	waitSeconds.WithLabelValues(name).Set(d.Seconds())
}
