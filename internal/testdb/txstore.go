package testdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/VSETH-GECO/bouncer/internal/db"
)

// TxStore is a controller Store over one test transaction: Querier returns the transaction and WithTx runs fn under
// a savepoint, so nothing is ever committed. Savepoint statements use a background context so that a request
// context cancelled inside fn cannot tear down the test transaction (the same rule Tx follows).
type TxStore struct {
	tx         *sql.Tx
	savepoints int
}

// NewTxStore returns a TxStore over tx. The caller owns tx and rolls it back.
func NewTxStore(tx *sql.Tx) *TxStore {
	return &TxStore{tx: tx}
}

// Store returns a TxStore over the test's transaction (Tx), which is rolled back when the test ends.
func Store(t *testing.T) *TxStore {
	t.Helper()
	return NewTxStore(Tx(t))
}

// Tx returns the underlying transaction, for the test's own reads and writes.
func (s *TxStore) Tx() *sql.Tx { return s.tx }

// Querier returns the test transaction.
func (s *TxStore) Querier() db.Querier { return s.tx }

// WithTx mirrors db.DB.WithTx on a savepoint of the test transaction: fn's changes are released into the transaction
// if it returns nil and rolled back to the savepoint if it returns an error or panics (the panic is re-raised after
// the rollback). A context that is already done fails like BeginTx would, without running fn.
func (s *TxStore) WithTx(ctx context.Context, fn func(tx db.Querier) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	s.savepoints++
	name := fmt.Sprintf("sp%d", s.savepoints)
	if _, err := s.tx.ExecContext(context.Background(), "SAVEPOINT "+name); err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	rollback := func() error {
		_, err := s.tx.ExecContext(context.Background(), "ROLLBACK TO SAVEPOINT "+name)
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = rollback()
			panic(p)
		}
	}()

	if err := fn(s.tx); err != nil {
		if rbErr := rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("rolling back transaction: %w", rbErr))
		}
		return err
	}
	if _, err := s.tx.ExecContext(context.Background(), "RELEASE SAVEPOINT "+name); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}
