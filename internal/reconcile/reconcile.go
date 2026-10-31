// Package reconcile finds clients stuck on a switch that does not carry their assigned VLAN and bounces them to the
// login VLAN.
//
// A client that moves to another switch without logging out is authenticated there and handed its assigned VLAN,
// which the new switch may not carry; it then has no network and cannot reach the login portal. Such a client never
// gets a RADIUS accounting session: the switch cannot place it, so no Accounting-Start follows the Access-Accept and
// radacct never shows it. What it does leave is a row in FreeRADIUS's post-auth log (radpostauth) for every
// authentication, naming the switch and the VLAN the switch was told, and it keeps re-authenticating.
//
// Each round of the reconciliation loop takes every client's newest Access-Accept within the accept window,
// compares the client's current assigned VLAN with the event VLANs of that accept's switch and, unless the
// Reconciler runs dry, queues a job moving each stuck client to the login VLAN. The job loop then rewrites radreply
// and clears the leases, as for any other job, and the switch's next authentication of the client lands it in the
// login VLAN.
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
	// acceptWindow is how recent a client's newest Access-Accept must be for the client to be judged. A client
	// whose last accept is older has probably left; bouncing it would only log out an absent user.
	acceptWindow = 30 * time.Minute
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
		Help: "Clients whose newest recent Access-Accept came from a switch that does not carry their assigned VLAN",
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
	mac    mac.MAC
	sw     switches.Switch
	accept freeradius.Accept
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
// A client is stuck when its newest Access-Accept within the accept window came from a switch of the switch map that
// has event VLANs, none of which is the client's current assigned VLAN from radreply. The current VLAN is used, not
// the one the accept told the switch, so that an admin's re-patch to a VLAN the switch carries is left to the
// client's next authentication instead of being overridden. Clients without a radreply VLAN or in the login VLAN are
// skipped. A failing accept query or switch lookup fails the round before any state changes; a failing job lookup or
// insert is logged and the round goes on with the next client.
func (r *Reconciler) Round(ctx context.Context) error {
	found, err := r.find(ctx)
	if err != nil {
		return err
	}

	next := make(map[mac.MAC]struct{}, len(found))
	for _, c := range found {
		next[c.mac] = struct{}{}
		if _, was := r.stuck[c.mac]; !was {
			attrs := []any{"clientMAC", c.mac.Hex(), "switch", c.sw.Hostname, "switchIP", c.accept.SwitchIP.String()}
			if c.accept.SwitchPort != nil {
				attrs = append(attrs, "switchPort", *c.accept.SwitchPort)
			}
			attrs = append(attrs, "assignedVLAN", c.accept.AssignedVLAN, "replyVLAN", c.accept.ReplyVLAN,
				"lastAccept", time.Since(c.accept.At).Round(time.Second), "dryRun", r.dryRun)
			slog.Info(msgStuck, attrs...)
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

// find returns the stuck clients, in the order of their newest accept (newest first).
func (r *Reconciler) find(ctx context.Context) ([]stuckClient, error) {
	accepts, err := freeradius.LatestAccepts(ctx, r.q, time.Now().Add(-acceptWindow))
	if err != nil {
		return nil, err
	}

	lookups := map[string]switchLookup{}
	noVLANs := map[int]struct{}{}
	seen := map[mac.MAC]struct{}{}
	var found []stuckClient
	for _, a := range accepts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if a.MAC.IsZero() {
			continue
		}
		if _, dup := seen[a.MAC]; dup {
			continue // accepts come newest first; two usernames may spell the same MAC
		}
		seen[a.MAC] = struct{}{}
		if a.AssignedVLAN == 0 || a.AssignedVLAN == loginVLAN {
			continue
		}

		key := a.SwitchIP.String()
		l, done := lookups[key]
		if !done {
			l, err = r.lookup(ctx, a.SwitchIP)
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
		carried := slices.ContainsFunc(l.sw.VLANs, func(v switches.VLAN) bool { return v.VLANID == a.AssignedVLAN })
		if !carried {
			found = append(found, stuckClient{mac: a.MAC, sw: l.sw, accept: a})
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
		slog.Debug("Skipping accepts from a switch missing from the switch map", "switchIP", ip.String())
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
