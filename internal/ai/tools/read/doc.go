// Package read holds every PermissionRead tool in the catalogue: the
// AWS-describe wrappers and the file/manifest readers the AI uses to
// gather context without prompting the user. Each tool registers itself
// into tools.Default from a small init() block; importing this package
// (directly or transitively via ai/tools/all) wires the read half of the
// catalogue.
//
// Read tools never mutate state. ADR-0035 places them outside the consent
// flow; tools.Registry.Call invokes Execute directly without consulting
// the Gate. Every read call is still recorded via the operational log
// (PR-02 will route the dispatch loop through usage.Record as well).
package read
