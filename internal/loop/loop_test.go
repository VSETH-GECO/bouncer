package loop

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// recorder stands in for timerSleep: it notes every wait it is asked for and returns at once, so tests can drive many
// rounds without real time passing.
type recorder struct {
	waits []time.Duration
}

func (r *recorder) sleep(_ context.Context, d time.Duration) error {
	r.waits = append(r.waits, d)
	return nil
}

func TestWait(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 5 * time.Second}, {1, 5 * time.Second}, {2, 10 * time.Second}, {3, 20 * time.Second},
		{4, 30 * time.Second}, {5, 30 * time.Second}, {100, 30 * time.Second},
	}
	for _, c := range cases {
		if got := wait(5*time.Second, 30*time.Second, c.failures); got != c.want {
			t.Errorf("wait(5s, 30s, %d) = %v, want %v", c.failures, got, c.want)
		}
	}
}

func TestRun_ReturnsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rounds := 0
	err := Run(ctx, Loop{
		Name:     "test",
		Interval: time.Second,
		MaxWait:  time.Second,
		Round: func(context.Context) error {
			rounds++
			return nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if rounds != 0 {
		t.Errorf("rounds = %d, want 0", rounds)
	}
}

func TestRun_BacksOffAndResetsOnSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fail := errors.New("fail")
	results := []error{fail, fail, fail, fail, nil, fail, nil}
	rounds, beats := 0, 0
	rec := &recorder{}
	err := Run(ctx, Loop{
		Name:     "test",
		Interval: 5 * time.Second,
		MaxWait:  30 * time.Second,
		Round: func(context.Context) error {
			res := results[rounds]
			rounds++
			if rounds == 7 {
				cancel()
			}
			return res
		},
		Beat:  func() { beats++ },
		sleep: rec.sleep,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if rounds != 7 {
		t.Errorf("rounds = %d, want 7", rounds)
	}
	if beats != 2 {
		t.Errorf("beats = %d, want 2", beats)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 5 * time.Second, 5 * time.Second}
	if !slices.Equal(rec.waits, want) {
		t.Errorf("waits = %v, want %v", rec.waits, want)
	}
}

func TestRun_RecoversPanicAndContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rounds, beats := 0, 0
	rec := &recorder{}
	err := Run(ctx, Loop{
		Name:     "test",
		Interval: 5 * time.Second,
		MaxWait:  30 * time.Second,
		Round: func(context.Context) error {
			rounds++
			if rounds == 1 {
				panic("boom")
			}
			cancel()
			return nil
		},
		Beat:  func() { beats++ },
		sleep: rec.sleep,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if rounds != 2 {
		t.Errorf("rounds = %d, want 2", rounds)
	}
	if beats != 1 {
		t.Errorf("beats = %d, want 1", beats)
	}
	if want := []time.Duration{5 * time.Second}; !slices.Equal(rec.waits, want) {
		t.Errorf("waits = %v, want %v", rec.waits, want)
	}
}

func TestRun_ErrorAfterCancelIsNotAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	beats := 0
	rec := &recorder{}
	err := Run(ctx, Loop{
		Name:     "test",
		Interval: 5 * time.Second,
		MaxWait:  30 * time.Second,
		Round: func(ctx context.Context) error {
			cancel()
			return ctx.Err()
		},
		Beat:  func() { beats++ },
		sleep: rec.sleep,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if beats != 0 {
		t.Errorf("beats = %d, want 0", beats)
	}
	if len(rec.waits) != 0 {
		t.Errorf("waits = %v, want none", rec.waits)
	}
}

func TestRun_CancelInterruptsBackoffWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var rounds atomic.Int32
	waiting := make(chan time.Duration, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Loop{
			Name:     "test",
			Interval: time.Hour,
			MaxWait:  time.Hour,
			Round: func(context.Context) error {
				rounds.Add(1)
				return errors.New("fail")
			},
			// Reports the wait it is asked for, then really waits: once the test hears from it, Run is in its wait.
			sleep: func(ctx context.Context, d time.Duration) error {
				waiting <- d
				return timerSleep(ctx, d)
			},
		})
	}()
	if d := <-waiting; d != time.Hour {
		t.Errorf("backoff wait = %v, want 1h", d)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancel")
	}
	if n := rounds.Load(); n != 1 {
		t.Errorf("rounds = %d, want 1", n)
	}
}

func TestRun_RoundsNeverOverlap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var inFlight atomic.Int32
	var overlap atomic.Bool
	calls := 0
	err := Run(ctx, Loop{
		Name:     "test",
		Interval: time.Millisecond,
		MaxWait:  time.Millisecond,
		Round: func(context.Context) error {
			if inFlight.Add(1) > 1 {
				overlap.Store(true)
			}
			time.Sleep(10 * time.Millisecond)
			inFlight.Add(-1)
			calls++
			if calls == 5 {
				cancel()
			}
			return nil
		},
	})
	if overlap.Load() {
		t.Error("two rounds ran at the same time")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func TestRun_PanicsOnMisconfiguration(t *testing.T) {
	round := func(context.Context) error { return nil }
	cases := []struct {
		name string
		l    Loop
	}{
		{"empty name", Loop{Interval: time.Second, MaxWait: time.Second, Round: round}},
		{"zero interval", Loop{Name: "test", MaxWait: time.Second, Round: round}},
		{"zero max wait", Loop{Name: "test", Interval: time.Second, Round: round}},
		{"nil round", Loop{Name: "test", Interval: time.Second, MaxWait: time.Second}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("Run did not panic")
				}
			}()
			_ = Run(context.Background(), c.l)
		})
	}
}
