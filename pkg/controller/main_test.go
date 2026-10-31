package controller

import (
	"context"
	"database/sql"
	"os"
	"testing"

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

// harness is the shared state of a controller test. Everything runs inside the test's transaction (testdb.Tx), which
// is rolled back when the test ends: the controllers get a testdb.TxStore over it, so their WithTx becomes a savepoint
// that a failing job rolls back to, and nothing is ever committed.
type harness struct {
	t   *testing.T
	ctx context.Context
	tx  *sql.Tx
	p   *testgen.Gen
	coa *fakeCoA
	c   *JobController
	dc  *DiscordController
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	tx := testdb.Tx(t)
	store := testdb.NewTxStore(tx)
	fake := &fakeCoA{}
	return &harness{
		t:   t,
		ctx: context.Background(),
		tx:  tx,
		p:   testgen.Persistent(t, tx),
		coa: fake,
		c:   NewJobController(store, "secret", fake),
		dc:  NewDiscordController(store),
	}
}

// client draws a fresh MAC for a test client.
func (h *harness) client() mac.MAC {
	h.t.Helper()
	return h.p.MAC()
}
