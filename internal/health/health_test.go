package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a settable time source for a registry.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestRegistry() (*Registry, *clock) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r := New()
	r.now = c.now
	return r, c
}

func probe(t *testing.T, h http.Handler) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

func expect(t *testing.T, name string, h http.Handler, code int, bodyContains string) {
	t.Helper()
	gotCode, body := probe(t, h)
	if gotCode != code || !strings.Contains(body, bodyContains) {
		t.Errorf("%s = %d %q, want %d containing %q", name, gotCode, body, code, bodyContains)
	}
}

func TestFreshRegistry_LiveAndReady(t *testing.T) {
	r, _ := newTestRegistry()
	expect(t, "/healthz", r.LiveHandler(), http.StatusOK, "ok")
	expect(t, "/readyz", r.ReadyHandler(), http.StatusOK, "ok")

	r.Heartbeat("loop", time.Minute)
	r.AddCheck("db", func(context.Context) error { return nil })
	expect(t, "/healthz", r.LiveHandler(), http.StatusOK, "ok")
	expect(t, "/readyz", r.ReadyHandler(), http.StatusOK, "ok")
}

func TestStaleHeartbeat(t *testing.T) {
	r, c := newTestRegistry()
	hb := r.Heartbeat("job-loop", time.Minute)
	r.Heartbeat("metrics-sampler", 2*time.Minute)

	c.advance(time.Minute)
	expect(t, "/healthz at maxAge", r.LiveHandler(), http.StatusOK, "ok")

	c.advance(time.Second)
	expect(t, "/healthz", r.LiveHandler(), http.StatusServiceUnavailable, "job-loop")
	expect(t, "/readyz", r.ReadyHandler(), http.StatusServiceUnavailable, "job-loop")
	if _, body := probe(t, r.LiveHandler()); strings.Contains(body, "metrics-sampler") {
		t.Errorf("/healthz body %q names the fresh heartbeat", body)
	}
	if err := r.Live(c.now()); err == nil || !strings.Contains(err.Error(), "1m1s") {
		t.Errorf("Live = %v, want the age 1m1s", err)
	}

	hb.Beat()
	expect(t, "/healthz after Beat", r.LiveHandler(), http.StatusOK, "ok")
	expect(t, "/readyz after Beat", r.ReadyHandler(), http.StatusOK, "ok")
}

func TestStop_Unregisters(t *testing.T) {
	r, c := newTestRegistry()
	hb := r.Heartbeat("job-loop", time.Minute)
	c.advance(time.Hour)
	expect(t, "/healthz", r.LiveHandler(), http.StatusServiceUnavailable, "job-loop")

	hb.Stop()
	hb.Stop() // harmless
	expect(t, "/healthz after Stop", r.LiveHandler(), http.StatusOK, "ok")
	expect(t, "/readyz after Stop", r.ReadyHandler(), http.StatusOK, "ok")
}

func TestFailingCheck_NotReadyButLive(t *testing.T) {
	r, _ := newTestRegistry()
	r.Heartbeat("job-loop", time.Minute)
	r.AddCheck("ok", func(context.Context) error { return nil })
	r.AddCheck("database", func(context.Context) error { return errors.New("connection refused") })
	r.AddCheck("later", func(context.Context) error { return errors.New("not reached") })

	expect(t, "/healthz", r.LiveHandler(), http.StatusOK, "ok")
	expect(t, "/readyz", r.ReadyHandler(), http.StatusServiceUnavailable, "database: connection refused")
	if _, body := probe(t, r.ReadyHandler()); strings.Contains(body, "not reached") {
		t.Errorf("/readyz body %q reports more than the first failing check", body)
	}
}

func TestReadyHandler_TimesOutBlockedChecks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(release <-chan struct{}) func(context.Context) error
	}{
		{"honours ctx", func(<-chan struct{}) func(context.Context) error {
			return func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
		}},
		{"ignores ctx", func(release <-chan struct{}) func(context.Context) error {
			return func(context.Context) error { <-release; return nil }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			r, _ := newTestRegistry()
			r.readyTimeout = 50 * time.Millisecond
			r.AddCheck("database", tc.check(release))

			done := make(chan struct{})
			var code int
			var body string
			go func() {
				defer close(done)
				code, body = probe(t, r.ReadyHandler())
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("ReadyHandler did not return after its timeout")
			}
			if code != http.StatusServiceUnavailable || !strings.Contains(body, "database") ||
				!strings.Contains(body, context.DeadlineExceeded.Error()) {
				t.Errorf("/readyz = %d %q, want 503 naming the check and the deadline", code, body)
			}
		})
	}
}

func TestNew_ReadyTimeoutIsTwoSeconds(t *testing.T) {
	if got := New().readyTimeout; got != 2*time.Second {
		t.Errorf("readyTimeout = %v, want 2s", got)
	}
}

func TestConcurrentBeats(t *testing.T) {
	r := New() // the real clock, so that Beat and Live race on it too
	r.AddCheck("db", func(context.Context) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hb := r.Heartbeat("loop"+string(rune('a'+i)), time.Minute)
			defer hb.Stop()
			for ctx.Err() == nil {
				hb.Beat()
			}
		}()
	}
	for range 200 {
		if err := r.Live(time.Now()); err != nil {
			t.Fatalf("Live = %v", err)
		}
		if err := r.Ready(context.Background()); err != nil {
			t.Fatalf("Ready = %v", err)
		}
		r.AddCheck("more", func(context.Context) error { return nil })
	}
	cancel()
	wg.Wait()
}
