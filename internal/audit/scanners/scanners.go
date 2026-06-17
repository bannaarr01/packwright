// Package scanners is the catalogue of read-only AWS inventory scanners
// the `/audit` command runs. Every file in this package contributes one
// Scanner and registers it with [audit.Default] from an init function,
// so consumers only need a blank import to wire the lot:
//
//	import _ "github.com/bannaarr01/packwright/internal/audit/scanners"
//
// Each scanner is small on purpose: one resource kind, one set of
// Describe*/List*/Get* calls, full pagination, and a mapping from the
// SDK response shape into [audit.Resource]. New kinds land as new
// files; the engine code in internal/audit never has to change.
package scanners
