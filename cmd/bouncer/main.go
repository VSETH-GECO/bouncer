package main

import (
	"github.com/VSETH-GECO/bouncer/pkg/database"
	"flag"
)

func main() {
	db := database.CreateHandler()
	db.RegisterFlags()
	flag.Parse()
	db.PollLoop()
}
