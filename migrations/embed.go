package migrations

import "embed"

// FS holds the SQL migrations applied by the database handler.
//
//go:embed *.sql
var FS embed.FS
