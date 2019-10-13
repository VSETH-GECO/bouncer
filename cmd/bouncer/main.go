package main

import (
	"flag"
	"github.com/VSETH-GECO/bouncer/pkg/database"
)

func main() {
	db := database.CreateHandler()
	db.RegisterFlags()
	flag.Parse()
	db.PollLoop()
}
