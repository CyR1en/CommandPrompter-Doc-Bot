package migrations

import "embed"

// FS contains the single unreleased-app baseline.
//
//go:embed *.sql
var FS embed.FS
