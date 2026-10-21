// Package migrations embeds the versioned SQL migrations. Files are named
// NNNN_name.up.sql and NNNN_name.down.sql; every version has both.
package migrations

import "embed"

// FS holds the migration files; pass it to migrate.New.
//
//go:embed *.sql
var FS embed.FS
