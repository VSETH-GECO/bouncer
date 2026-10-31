// Package reconcile finds clients stuck on a switch that does not carry their assigned VLAN and bounces them to the
// login VLAN.
//
// A client that moves to another switch without logging out is authenticated there and handed its assigned VLAN,
// which the new switch may not carry; it then has no network and cannot reach the login portal. Each round of the
// reconciliation loop compares every open RADIUS session with the event VLANs of its switch and, unless the
// Reconciler runs dry, queues a job moving each stuck client to the login VLAN. The job loop then does the CoA, the
// lease cleanup and the log row, as for any other job.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/freeradius"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/switches"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// loginVLAN is the VLAN stuck clients are moved to; from there they reach the login portal.
	loginVLAN = 499
	// cooldown is how long after a client's last job (by its last_update) the loop waits before queueing another,
	// so a client the job loop cannot fix is retried every cooldown and not every round.
	cooldown = 10 * time.Minute
)

// Log messages, shared with the tests.
const (
	msgStuck   = "Client stuck on a switch without its VLAN"
	msgUnstuck = "Client no longer stuck"
	msgQueued  = "Queued bounce to login VLAN"
)

// The collectors are registered by Register, not at init, so that importing this package has no side effects.
var (
	stuckClients = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bouncer_stuck_clients",
		Help: "Clients with an open session on a switch that does not carry their assigned VLAN",
	})
	stuckBounces = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "bouncer_stuck_bounces_total",
		Help: "Jobs the reconciliation loop queued to move stuck clients to the login VLAN",
	})
)

// Register registers the reconciliation loop's collectors on r.
func Register(r prometheus.Registerer) {
	r.MustRegister(stuckClients, stuckBounces)
}

// Reconciler is the state of the reconciliation loop: the clients that were stuck in the previous round. It is not
// safe for concurrent use; the loop runner calls Round from one goroutine.
type Reconciler struct {
	q      db.Querier
	dryRun bool
	// stuck holds the clients stuck in the previous round, so that only transitions are logged.
	stuck map[mac.MAC]struct{}
	// noVLANs holds the switches (by ID) that had no event VLAN in the previous round, so that each is warned
	// about once until it gets one.
	noVLANs map[int]struct{}
}

// New returns a Reconciler reading and writing through q. With dryRun it detects, logs and exports the stuck clients
// but queues no job.
func New(q db.Querier, dryRun bool) *Reconciler {
	return &Reconciler{q: q, dryRun: dryRun, stuck: map[mac.MAC]struct{}{}, noVLANs: map[int]struct{}{}}
}

// stuckClient is a client found stuck in a round.
type stuckClient struct {
	mac      mac.MAC
	assigned int
	sw       switches.Switch
	switchIP net.IP
}

// switchLookup is the outcome of resolving one switch IP within a round.
type switchLookup struct {
	sw    switches.Switch
	known bool
}

// Round runs one round of the reconciliation loop: it finds the stuck clients, logs the ones that became or stopped
// being stuck, updates bouncer_stuck_clients and, unless the Reconciler runs dry, queues a job moving each stuck
// client to the login VLAN, skipping a client with a pending job or one updated within the cool-down.
//
// Only a client's newest open session counts: an older one still open is a leftover of a lost Accounting-Stop. A
// failing session query or switch lookup fails the round before any state changes; a failing job lookup or insert is
// logged and the round goes on with the next client.
func (r *Reconciler) Round(ctx context.Context) error {
	found, err := r.find(ctx)
	if err != nil {
		return err
	}

	next := make(map[mac.MAC]struct{}, len(found))
	for _, c := range found {
		next[c.mac] = struct{}{}
		if _, was := r.stuck[c.mac]; !was {
			slog.Info(msgStuck, "clientMAC", c.mac.Hex(), "switch", c.sw.Hostname, "switchIP", c.switchIP.String(),
				"assignedVLAN", c.assigned, "dryRun", r.dryRun)
		}
	}
	for m := range r.stuck {
		if _, still := next[m]; !still {
			slog.Info(msgUnstuck, "clientMAC", m.Hex())
		}
	}
	r.stuck = next
	stuckClients.Set(float64(len(next)))

	if r.dryRun {
		return nil
	}
	for _, c := range found {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.bounce(ctx, c)
	}
	return nil
}

// find returns the stuck clients, in the order of their newest open session (newest first).
func (r *Reconciler) find(ctx context.Context) ([]stuckClient, error) {
	sessions, err := freeradius.OpenSessions(ctx, r.q)
	if err != nil {
		return nil, err
	}

	lookups := map[string]switchLookup{}
	noVLANs := map[int]struct{}{}
	seen := map[mac.MAC]struct{}{}
	var found []stuckClient
	for _, s := range sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.MAC.IsZero() {
			continue
		}
		if _, dup := seen[s.MAC]; dup {
			continue // sessions come newest first
		}
		seen[s.MAC] = struct{}{}
		if s.AssignedVLAN == 0 || s.AssignedVLAN == loginVLAN {
			continue
		}

		key := s.SwitchIP.String()
		l, done := lookups[key]
		if !done {
			l, err = r.lookup(ctx, s.SwitchIP)
			if err != nil {
				return nil, err
			}
			lookups[key] = l
			if l.known && len(l.sw.VLANs) == 0 {
				noVLANs[l.sw.ID] = struct{}{}
				if _, warned := r.noVLANs[l.sw.ID]; !warned {
					slog.Warn("Switch has no event VLAN mapped; not checking its clients",
						"switch", l.sw.Hostname, "switchIP", key)
				}
			}
		}
		if !l.known || len(l.sw.VLANs) == 0 {
			continue
		}
		carried := slices.ContainsFunc(l.sw.VLANs, func(v switches.VLAN) bool { return v.VLANID == s.AssignedVLAN })
		if !carried {
			found = append(found, stuckClient{mac: s.MAC, assigned: s.AssignedVLAN, sw: l.sw, switchIP: s.SwitchIP})
		}
	}
	r.noVLANs = noVLANs
	return found, nil
}

// lookup resolves a switch IP through the switch map. An unknown switch is not an error.
func (r *Reconciler) lookup(ctx context.Context, ip net.IP) (switchLookup, error) {
	sw, err := switches.ByIP(ctx, r.q, ip)
	switch {
	case err == nil:
		return switchLookup{sw: sw, known: true}, nil
	case errors.Is(err, db.ErrNotFound):
		slog.Debug("Skipping sessions on a switch missing from the switch map", "switchIP", ip.String())
		return switchLookup{}, nil
	default:
		return switchLookup{}, fmt.Errorf("resolving switch %s: %w", ip, err)
	}
}

// bounce queues a job moving c to the login VLAN, unless c has a pending job or its last job is younger than the
// cool-down. Errors are logged, not returned, so one client cannot block the others.
func (r *Reconciler) bounce(ctx context.Context, c stuckClient) {
	latest, err := jobs.Latest(ctx, r.q, c.mac)
	switch {
	case err == nil:
		if latest.Retries < jobs.MaxRetries {
			slog.Debug("Stuck client has a pending job; not queueing another", "clientMAC", c.mac.Hex(), "jobID", latest.ID)
			return
		}
		if age := time.Since(latest.LastUpdate); age < cooldown {
			slog.Debug("Stuck client's last job is within the cool-down", "clientMAC", c.mac.Hex(), "jobID", latest.ID,
				"age", age)
			return
		}
	case errors.Is(err, db.ErrNotFound):
		// never had a job
	default:
		slog.Warn("Couldn't look up the stuck client's last job", "clientMAC", c.mac.Hex(), "error", err)
		return
	}

	if err := jobs.Create(ctx, r.q, c.mac, loginVLAN); err != nil {
		slog.Warn("Couldn't queue the stuck client's bounce", "clientMAC", c.mac.Hex(), "error", err)
		return
	}
	stuckBounces.Inc()
	slog.Info(msgQueued, "clientMAC", c.mac.Hex(), "switch", c.sw.Hostname, "vlan", loginVLAN)
}
