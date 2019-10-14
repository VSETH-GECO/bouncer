package main

import (
	"github.com/VSETH-GECO/bouncer/cmd/bouncer"
	log "github.com/sirupsen/logrus"
)

func main() {
	bouncer.SetupCommands()

	err := bouncer.RootCmd.Execute()
	if err != nil {
		log.WithError(err).Fatal()
	}
}
