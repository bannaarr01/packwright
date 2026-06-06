package gui

import "embed"

// assets carries the built Svelte bundle (web/dist) into the Go binary so the
// GUI is self-contained — no external file lookups at run time. The Vite
// build emits to web/dist; a placeholder web/dist/index.html is committed so
// `go build ./...` succeeds before the frontend has ever been built.
//
//go:embed all:web/dist
var assets embed.FS
