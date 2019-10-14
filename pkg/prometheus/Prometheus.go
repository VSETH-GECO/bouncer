package prometheus

import (
	"github.com/VSETH-GECO/bouncer/pkg/database"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"net/http"
	"strconv"
)

func StartServing(port int, handler *database.Handler) {
	go database.StartUpdating(handler)

	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe("127.0.0.1:"+strconv.Itoa(port), nil)
	if err != nil {
		log.WithError(err).Fatal("Couldn't start prometheus handler")
	}
}
