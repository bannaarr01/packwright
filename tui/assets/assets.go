// Package assets owns the static text files the TUI embeds into the
// binary. Right now that's just the ASCII logo shown by the header
// strip; the package exists so the embed directive can sit next to the
// files it loads, rather than the header package having to reach across
// directory boundaries.
package assets

import _ "embed"

//go:embed logo.txt
var logo string

// Logo returns the multi-line ASCII logo loaded from tui/assets/logo.txt.
// Trailing whitespace is preserved so the renderer can split on '\n'
// without losing rows.
func Logo() string { return logo }
