// Package meta holds build-time metadata about the running Packwright
// binary — currently just the version string. It is a deliberately leaf
// package so any other package (cmd for the cobra --version flag,
// action/dispatch for usage events, future packs for compatibility
// checks) can read the version without pulling in heavier dependencies.
//
// The version is overridden at release time via the linker:
//
//	go build -ldflags "-X github.com/bannaarr01/packwright/meta.Version=v1.2.3"
//
// Local and `go run` builds use the "dev" default below.
package meta

// Version is the Packwright build version. The "dev" default is
// replaced at release time by the linker; see the package comment for
// the exact -ldflags incantation.
var Version = "dev"
