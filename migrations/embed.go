// Package migrations embeds the SQL migrations so the binary can self-migrate
// via the goose library without any external tooling.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
