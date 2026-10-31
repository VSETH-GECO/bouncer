package controller

import (
	"context"

	"github.com/VSETH-GECO/bouncer/internal/db"
)

// Store is the database the controllers run on: a querier for reads outside a transaction and a transaction runner
// for writes. *db.DB is the production implementation; tests supply one over a single rolled-back transaction.
type Store interface {
	// Querier returns the querier for statements that run outside a transaction.
	Querier() db.Querier
	// WithTx runs fn inside a transaction that is committed if fn returns nil and rolled back if it returns an error
	// or panics (the panic is re-raised after the rollback).
	WithTx(ctx context.Context, fn func(tx db.Querier) error) error
}

// *db.DB is the production Store.
var _ Store = (*db.DB)(nil)
