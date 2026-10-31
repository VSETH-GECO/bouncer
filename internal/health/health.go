// Package health answers the liveness and readiness probes. Liveness means every loop succeeded recently: a loop records
// a heartbeat per successful round, and a heartbeat that is too old makes the daemon unhealthy. Readiness means the
// daemon could do its work now: it is live, every loop has succeeded at least once since it registered, and every
// readiness check (a database ping, for instance) passes.
package health

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultReadyTimeout bounds the readiness checks run by ReadyHandler.
const defaultReadyTimeout = 2 * time.Second

// Registry tracks the heartbeats of the daemon's loops and the readiness checks, and answers the probes.
type Registry struct {
	mu     sync.Mutex
	beats  map[*Heartbeat]struct{}
	checks []check

	// now and readyTimeout are fixed by New; tests replace them.
	now          func() time.Time
	readyTimeout time.Duration
}

type check struct {
	name string
	fn   func(ctx context.Context) error
}

// New returns an empty registry, which is live and ready.
func New() *Registry {
	return &Registry{
		beats:        make(map[*Heartbeat]struct{}),
		now:          time.Now,
		readyTimeout: defaultReadyTimeout,
	}
}

// Heartbeat is a loop's heartbeat in a Registry.
type Heartbeat struct {
	r      *Registry
	name   string
	maxAge time.Duration
	last   time.Time // guarded by r.mu
	beaten bool      // guarded by r.mu; set by the first Beat
}

// Heartbeat registers a loop under name. It counts as fresh from registration on, but keeps the registry from being
// ready until its first Beat; the loop calls Beat after every successful round and Stop when it ends for good (a
// follower stopping to lead, for instance), which unregisters it. A heartbeat older than maxAge makes the registry
// unhealthy.
func (r *Registry) Heartbeat(name string, maxAge time.Duration) *Heartbeat {
	h := &Heartbeat{r: r, name: name, maxAge: maxAge}
	r.mu.Lock()
	defer r.mu.Unlock()
	h.last = r.now()
	r.beats[h] = struct{}{}
	return h
}

// Beat records a successful round.
func (h *Heartbeat) Beat() {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	h.last = h.r.now()
	h.beaten = true
}

// Name returns the name the heartbeat was registered under.
func (h *Heartbeat) Name() string { return h.name }

// MaxAge returns the age beyond which the heartbeat makes the registry unhealthy.
func (h *Heartbeat) MaxAge() time.Duration { return h.maxAge }

// Stop unregisters the heartbeat. Calling it more than once is harmless.
func (h *Heartbeat) Stop() {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	delete(h.r.beats, h)
}

// AddCheck registers a readiness check; fn must honour ctx.
func (r *Registry) AddCheck(name string, fn func(ctx context.Context) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, check{name: name, fn: fn})
}

// Live returns nil when every registered heartbeat is fresh at now, else an error naming the stale ones and their age.
func (r *Registry) Live(now time.Time) error {
	r.mu.Lock()
	var stale []string
	for h := range r.beats {
		if age := now.Sub(h.last); age > h.maxAge {
			stale = append(stale, fmt.Sprintf("%s (last beat %s ago, max %s)", h.name, age.Round(time.Millisecond), h.maxAge))
		}
	}
	r.mu.Unlock()

	if len(stale) == 0 {
		return nil
	}
	sort.Strings(stale)
	return fmt.Errorf("stale heartbeat: %s", strings.Join(stale, ", "))
}

// Ready returns Live's error if any, else an error naming the registered heartbeats that have never beaten, if any,
// else the first failing check's error, named. No check runs while a heartbeat has never beaten. A check that does not
// return once ctx is done is abandoned and counts as failed with ctx's error.
func (r *Registry) Ready(ctx context.Context) error {
	if err := r.Live(r.now()); err != nil {
		return err
	}

	r.mu.Lock()
	var pending []string
	for h := range r.beats {
		if !h.beaten {
			pending = append(pending, h.name)
		}
	}
	checks := append([]check(nil), r.checks...)
	r.mu.Unlock()

	if len(pending) > 0 {
		sort.Strings(pending)
		return fmt.Errorf("no successful round yet: %s", strings.Join(pending, ", "))
	}

	for _, c := range checks {
		if err := runCheck(ctx, c.fn); err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
	}
	return nil
}

func runCheck(ctx context.Context, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result := make(chan error, 1) // buffered, so an abandoned check does not leak blocked on the send
	go func() { result <- fn(ctx) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LiveHandler answers 200 "ok" (text/plain) while Live is nil at the time of the request, else 503 with the error text.
func (r *Registry) LiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respond(w, r.Live(r.now()))
	})
}

// ReadyHandler answers 200 "ok" (text/plain) while Ready is nil, else 503 with the error text. It runs the checks
// with a 2 s timeout derived from the request context.
func (r *Registry) ReadyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), r.readyTimeout)
		defer cancel()
		respond(w, r.Ready(ctx))
	})
}

func respond(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintln(w, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintln(w, "ok")
}
