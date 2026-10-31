package loop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// collect reads every series of c without registering it. It stands in for prometheus/testutil, whose import would add
// a dependency to go.mod.
func collect(t *testing.T, c prometheus.Collector) []*dto.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	c.Collect(ch)
	close(ch)
	var out []*dto.Metric
	for m := range ch {
		var d dto.Metric
		if err := m.Write(&d); err != nil {
			t.Fatalf("reading metric: %v", err)
		}
		out = append(out, &d)
	}
	return out
}

// value returns the gauge or counter value of the series of c whose label pairs equal labels, and whether there is one.
func value(t *testing.T, c prometheus.Collector, labels map[string]string) (float64, bool) {
	t.Helper()
	for _, m := range collect(t, c) {
		pairs := m.GetLabel()
		if len(pairs) != len(labels) {
			continue
		}
		match := true
		for _, p := range pairs {
			if v, ok := labels[p.GetName()]; !ok || v != p.GetValue() {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if g := m.GetGauge(); g != nil {
			return g.GetValue(), true
		}
		return m.GetCounter().GetValue(), true
	}
	return 0, false
}

// count returns the number of series of c.
func count(t *testing.T, c prometheus.Collector) int {
	t.Helper()
	return len(collect(t, c))
}

// collectors lists the package's collectors by name, for checks that apply to every one of them.
func collectors() map[string]prometheus.Collector {
	return map[string]prometheus.Collector{
		"rounds":              rounds,
		"roundDuration":       roundDuration,
		"lastSuccess":         lastSuccess,
		"consecutiveFailures": consecutiveFailures,
		"waitSeconds":         waitSeconds,
	}
}

// expectValue fails t unless the series of c with labels exists and has value want.
func expectValue(t *testing.T, name string, c prometheus.Collector, labels map[string]string, want float64) {
	t.Helper()
	got, ok := value(t, c, labels)
	if !ok {
		t.Errorf("%s%v: no series", name, labels)
		return
	}
	if got != want {
		t.Errorf("%s%v = %v, want %v", name, labels, got, want)
	}
}

// expectNoSeries fails t unless every collector of the package is empty.
func expectNoSeries(t *testing.T) {
	t.Helper()
	for name, c := range collectors() {
		if n := count(t, c); n != 0 {
			t.Errorf("%s has %d series after Run returned, want 0", name, n)
		}
	}
}

func TestRegister_RegistersAllCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg)
	for name, c := range collectors() {
		err := reg.Register(c)
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			t.Errorf("registering %s again = %v, want AlreadyRegisteredError", name, err)
		}
	}
}

func TestRun_InitialisesAndDeletesSeries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &recorder{}
	err := Run(ctx, Loop{
		Name:     "t",
		Interval: 5 * time.Second,
		MaxWait:  30 * time.Second,
		Round: func(context.Context) error {
			if n := count(t, rounds); n != 3 {
				t.Errorf("rounds has %d series, want 3", n)
			}
			for _, res := range []string{resultSuccess, resultError, resultPanic} {
				expectValue(t, "rounds", rounds, map[string]string{"loop": "t", "result": res}, 0)
			}
			expectValue(t, "consecutiveFailures", consecutiveFailures, map[string]string{"loop": "t"}, 0)
			expectValue(t, "waitSeconds", waitSeconds, map[string]string{"loop": "t"}, 5)
			if n := count(t, lastSuccess); n != 0 {
				t.Errorf("lastSuccess has %d series before the first success, want 0", n)
			}
			cancel()
			return nil
		},
		sleep: rec.sleep,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	expectNoSeries(t)
}

func TestRun_RecordsResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	rec := &recorder{}
	err := Run(ctx, Loop{
		Name:     "t",
		Interval: 5 * time.Second,
		MaxWait:  30 * time.Second,
		Round: func(context.Context) error {
			calls++
			switch calls {
			case 1:
				return errors.New("fail")
			case 2:
				panic("boom")
			}
			expectValue(t, "rounds", rounds, map[string]string{"loop": "t", "result": resultError}, 1)
			expectValue(t, "rounds", rounds, map[string]string{"loop": "t", "result": resultPanic}, 1)
			expectValue(t, "rounds", rounds, map[string]string{"loop": "t", "result": resultSuccess}, 0)
			expectValue(t, "consecutiveFailures", consecutiveFailures, map[string]string{"loop": "t"}, 2)
			expectValue(t, "waitSeconds", waitSeconds, map[string]string{"loop": "t"}, 10)
			if n := count(t, lastSuccess); n != 0 {
				t.Errorf("lastSuccess has %d series before the first success, want 0", n)
			}
			durations := collect(t, roundDuration)
			if len(durations) != 1 {
				t.Errorf("roundDuration has %d series, want 1", len(durations))
			} else if n := durations[0].GetHistogram().GetSampleCount(); n != 2 {
				t.Errorf("roundDuration sample count = %d, want 2", n)
			}
			cancel()
			return nil
		},
		sleep: rec.sleep,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	expectNoSeries(t)
}
