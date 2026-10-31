// Package loop runs the daemon's periodic loops: a round, a wait, again; failing rounds back off, panicking rounds are
// recovered, and nothing but the context ends a loop.
package loop

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"
)

// Loop describes a periodic activity of the daemon. Run runs its rounds one after another, never overlapping, waiting
// Interval after a successful round and backing off towards MaxWait while rounds fail.
type Loop struct {
	// Name identifies the loop in logs and in the "loop" label.
	Name string
	// Interval is the wait after a successful round, and the first wait after a failed one.
	Interval time.Duration
	// MaxWait caps the wait after consecutive failed rounds.
	MaxWait time.Duration
	// Round does one unit of the loop's work. A non-nil error or a panic counts as a failed round.
	Round func(ctx context.Context) error
	// Beat is optional; Run calls it after every successful round only, so a heartbeat fed by it goes stale while
	// the loop keeps failing.
	Beat func()

	// sleep waits between rounds; nil means timerSleep. In-package tests set it to avoid real waits.
	sleep func(ctx context.Context, d time.Duration) error
}

// result is what one round came to: an error, or a recovered panic with the stack it was raised on.
type result struct {
	err      error
	panicked bool
	panic    any
	stack    string
}

// Run runs l until ctx is cancelled and returns ctx.Err(). The first round starts at once. A round that fails or
// panics is logged and retried after a backoff; a round that fails because ctx was cancelled is not a failure, Run just
// returns. It panics if l is misconfigured (empty Name, Interval <= 0, MaxWait <= 0, nil Round), since that is a
// programming error that no retry can fix.
func Run(ctx context.Context, l Loop) error {
	validate(l)
	if err := ctx.Err(); err != nil {
		return err
	}
	sleep := l.sleep
	if sleep == nil {
		sleep = timerSleep
	}
	initSeries(l.Name, l.Interval)
	defer deleteSeries(l.Name)
	slog.Info("Loop started", "loop", l.Name, "interval", l.Interval)

	failures := 0
	for {
		start := time.Now()
		r := runRound(ctx, l.Round)
		took := time.Since(start)

		// Classify the round. Every outcome is decided here and nowhere else.
		var res string
		var next time.Duration
		switch {
		case r.panicked:
			failures++
			res, next = resultPanic, wait(l.Interval, l.MaxWait, failures)
			slog.Error("Loop round panicked", "loop", l.Name, "panic", r.panic, "stack", r.stack,
				"failures", failures, "retryIn", next)
		case r.err == nil:
			failures = 0
			res, next = resultSuccess, l.Interval
			if l.Beat != nil {
				l.Beat()
			}
		case ctx.Err() != nil:
			// The round most likely failed because it was cancelled; that says nothing about its health.
			return ctx.Err()
		default:
			failures++
			res, next = resultError, wait(l.Interval, l.MaxWait, failures)
			slog.Warn("Loop round failed", "loop", l.Name, "error", r.err,
				"failures", failures, "retryIn", next)
		}
		recordRound(l.Name, res, took, failures)

		// A round that ended with ctx done is recorded, but no wait follows it.
		if err := ctx.Err(); err != nil {
			return err
		}
		recordWait(l.Name, next)
		if err := sleep(ctx, next); err != nil {
			return err
		}
	}
}

// validate panics if l cannot be run.
func validate(l Loop) {
	switch {
	case l.Name == "":
		panic("loop: Loop.Name is empty")
	case l.Interval <= 0:
		panic(fmt.Sprintf("loop %q: Interval must be positive, got %v", l.Name, l.Interval))
	case l.MaxWait <= 0:
		panic(fmt.Sprintf("loop %q: MaxWait must be positive, got %v", l.Name, l.MaxWait))
	case l.Round == nil:
		panic(fmt.Sprintf("loop %q: Round is nil", l.Name))
	}
}

// runRound runs one round, turning a panic into a result so that one bad round does not take the daemon down. The
// stack is captured inside the deferred recover, where it still shows where the panic was raised.
func runRound(ctx context.Context, round func(context.Context) error) (r result) {
	defer func() {
		if p := recover(); p != nil {
			r = result{panicked: true, panic: p, stack: string(debug.Stack())}
		}
	}()
	return result{err: round(ctx)}
}

// wait is the wait after a round: interval after a success (failures == 0), else min(interval * 2^(failures-1),
// maxWait). It doubles step by step and stops at the cap, so no number of failures can overflow.
func wait(interval, maxWait time.Duration, failures int) time.Duration {
	if failures == 0 {
		return interval
	}
	d := interval
	for i := 1; i < failures; i++ {
		if d > maxWait/2 {
			return maxWait
		}
		d *= 2
	}
	return min(d, maxWait)
}

// timerSleep waits d or until ctx is done, returning ctx.Err() in the latter case.
func timerSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
