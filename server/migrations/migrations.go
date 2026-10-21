// Package migrations holds the numbered SQL migrations.
package migrations

import "embed"

// FS contains every *.sql file, applied in name order.
//
//go:embed *.sql
var FS embed.FS
