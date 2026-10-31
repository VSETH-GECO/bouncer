package run

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/health"
)

// runFailThenCancel runs a loop through runLoop whose first round fails and whose second cancels it, and returns the
// probes, the loop's heartbeat (still registered: stopping it is the caller's job) and runLoop's error.
func runFailThenCancel(t *testing.T) (*health.Registry, *health.Heartbeat, error) {
	t.Helper()
	probes := health.New()
	hb := probes.Heartbeat("t", time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := runLoop(ctx, hb, time.Millisecond, func(ctx context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("fail")
		}
		cancel()
		return ctx.Err()
	})
	return probes, hb, err
}

func TestRunLoop_BeatsOnlyOnSuccess(t *testing.T) {
	probes, hb, err := runFailThenCancel(t)
	defer hb.Stop()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("runLoop = %v, want context.Canceled", err)
	}
	if err := probes.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), "no successful round yet: t") {
		t.Errorf("Ready = %v, want an error naming t as never beaten", err)
	}
	if err := probes.Live(time.Now()); err != nil {
		t.Errorf("Live = %v, want nil", err)
	}
}

func TestRunLoop_SuccessBeats(t *testing.T) {
	probes := health.New()
	hb := probes.Heartbeat("t", time.Minute)
	defer hb.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := runLoop(ctx, hb, time.Millisecond, func(context.Context) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("runLoop = %v, want context.Canceled", err)
	}
	if err := probes.Ready(context.Background()); err != nil {
		t.Errorf("Ready = %v, want nil after a successful round", err)
	}
}

func TestRunLoop_StopIsTheCallersJob(t *testing.T) {
	probes, hb, _ := runFailThenCancel(t)
	defer hb.Stop()
	// Still registered after runLoop returned: two minutes on, the one-minute heartbeat is stale.
	if err := probes.Live(time.Now().Add(2 * time.Minute)); err == nil || !strings.Contains(err.Error(), "t (last beat") {
		t.Errorf("Live two minutes after runLoop returned = %v, want t stale", err)
	}
}
