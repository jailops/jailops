package hashi

import "embed"

// SourceFS lets the comparison table count adapter lines of code.
//
//go:embed *.go
var SourceFS embed.FS
