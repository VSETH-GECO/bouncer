package controller

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
)

// TestLogoutLoad measures how long the job loop takes to move a whole event's clients to the login VLAN, with real
// commits and a CoA that answers at once, so it shows the database's share and not the switches'. It needs its own
// container and takes minutes, so it only runs when asked for:
//
//	BOUNCER_LOAD_TEST=1000 go test ./pkg/controller/ -run TestLogoutLoad -v -count=1 -timeout 30m
func TestLogoutLoad(t *testing.T) {
	var clients int
	if _, err := fmt.Sscan(os.Getenv("BOUNCER_LOAD_TEST"), &clients); err != nil || clients <= 0 {
		t.Skip("set BOUNCER_LOAD_TEST to a number of clients to run this")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	inst := testdb.Start(t)
	d, err := inst.Open(ctx)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(ctx, 0); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}

	// Build the clients in one transaction: the fixture's commits are not what this measures.
	tx, err := d.BeginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := testgen.Persistent(t, tx)
	macs := make([]mac.MAC, 0, clients)
	for range clients {
		m := p.MAC()
		macs = append(macs, m)
		p.Authorized(m, 620)
		p.Session(func(r *model.Radacct) { r.Username, r.Nasipaddress = m.Hex(), "10.10.0.1" })
		hw := m.Bytes()
		p.Lease4(func(l *model.Lease4) { l.Hwaddr = &hw })
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	targets, _, err := logoutTargets(ctx, d.Querier(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range targets {
		if err := jobs.Create(ctx, d.Querier(), m, freeradius.LoginVLAN); err != nil {
			t.Fatal(err)
		}
	}
	queued := time.Since(start)

	c := NewJobController(d, "secret", &fakeCoA{}, nil)
	moveStart := time.Now()
	rounds := 0
	for {
		pending, err := jobs.Pending(ctx, d.Querier())
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 {
			break
		}
		rounds++
		if err := c.Work(ctx); err != nil {
			t.Fatal(err)
		}
	}
	moved := time.Since(moveStart)

	for _, m := range macs {
		v, err := freeradius.AssignedVLAN(ctx, d.Querier(), m)
		if err != nil || v != freeradius.LoginVLAN {
			t.Fatalf("VLAN of %s = %d, %v", m.Hex(), v, err)
		}
	}
	t.Logf("%d clients: queued in %v, moved in %v over %d round(s), %v per client",
		clients, queued.Round(time.Millisecond), moved.Round(time.Millisecond), rounds, (moved / time.Duration(clients)).Round(time.Microsecond))
}
