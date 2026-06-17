// Package all is the convenience import that wires every read and write
// tool into tools.Default. The AI dispatch loop (PR-02) imports this
// package once at startup; individual subcommands or tests that only need
// a subset of tools can import internal/ai/tools/read or
// internal/ai/tools/write directly.
//
// The package itself contains no symbols — the imports below trigger the
// per-tool init() registrations. The unused-import lint is avoided by
// renaming each subpackage to _ explicitly.
package all

import (
	_ "github.com/bannaarr01/packwright/internal/ai/tools/read"
	_ "github.com/bannaarr01/packwright/internal/ai/tools/write"
)
