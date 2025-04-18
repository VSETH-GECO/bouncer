package prometheus

import (
	"net/http"
	"strconv"

	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
)

func StartServing(port int, handler *database.Handler) {
	go database.StartUpdating(handler)

	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe("0.0.0.0:"+strconv.Itoa(port), nil)
	if err != nil {
		log.WithError(err).Fatal("Couldn't start prometheus handler")
	}
}
