package metrics

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var registerOnce sync.Once

// Handler registers the collectors in the default registry (once) and returns the handler that serves them.
func Handler() http.Handler {
	registerOnce.Do(func() { register(prometheus.DefaultRegisterer) })
	return promhttp.Handler()
}
