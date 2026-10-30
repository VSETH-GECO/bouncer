package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-jet/jet/v2/qrm"
)

// ErrNotFound is returned (wrapped) by single-row lookups that found no matching row.
// Check for it with errors.Is(err, db.ErrNotFound).
var ErrNotFound = errors.New("not found")

// NotFound maps the "no rows" errors of jet (qrm.ErrNoRows) and database/sql (sql.ErrNoRows) to an error wrapping
// ErrNotFound. It returns nil for nil and any other error unchanged.
func NotFound(err error) error {
	if errors.Is(err, qrm.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}
