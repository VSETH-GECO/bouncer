package controller

import (
	"context"
	"os"
	"testing"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
	"github.com/VSETH-GECO/bouncer/pkg/radius"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// fakeCoA records every CoA request instead of sending it.
type fakeCoA struct {
	calls []radius.CoARequest
	fail  func(req radius.CoARequest) error // nil: every call succeeds
}

func (f *fakeCoA) SendDisconnect(_ context.Context, req radius.CoARequest) error {
	f.calls = append(f.calls, req)
	if f.fail != nil {
		return f.fail(req)
	}
	return nil
}

// harness is the shared state of a controller test. The controller commits through the real pool (WithTx), so these
// tests cannot run inside testdb.Tx; instead every row a test creates belongs to a MAC drawn through client, whose
// cleanup deletes it again.
type harness struct {
	t   *testing.T
	ctx context.Context
	d   *db.DB
	p   *testgen.Gen
	coa *fakeCoA
	c   *JobController
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d := testdb.Shared(t)
	fake := &fakeCoA{}
	return &harness{
		t:   t,
		ctx: context.Background(),
		d:   d,
		p:   testgen.Persistent(t, d.Querier()),
		coa: fake,
		c:   NewJobController(d, "secret", fake),
	}
}

// exec runs a statement through the pool and fails the test on error.
func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	if _, err := h.d.SQL().ExecContext(context.Background(), query, args...); err != nil {
		h.t.Errorf("%s: %v", query, err)
	}
}

// client draws a fresh MAC and registers a cleanup deleting every row bouncer and the tests may write for it.
func (h *harness) client() mac.MAC {
	h.t.Helper()
	m := h.p.MAC()
	hex, hw := m.Hex(), m.Bytes()
	h.t.Cleanup(func() {
		h.exec("DELETE FROM radcheck WHERE username = ?", hex)
		h.exec("DELETE FROM radreply WHERE username = ?", hex)
		h.exec("DELETE FROM radacct WHERE username = ?", hex)
		h.exec("DELETE FROM lease4 WHERE hwaddr = ?", hw)
		h.exec("DELETE FROM lease6 WHERE hwaddr = ?", hw)
		h.exec("DELETE FROM login_logs WHERE mac = ?", hex)
		h.cleanupJobs(hex)
	})
	return m
}

// cleanupJobs deletes the queue and log rows whose clientMAC is the given string.
func (h *harness) cleanupJobs(clientMAC string) {
	h.exec("DELETE FROM bouncer_jobs WHERE clientMAC = ?", clientMAC)
	h.exec("DELETE FROM bouncer_log WHERE clientMAC = ?", clientMAC)
}
