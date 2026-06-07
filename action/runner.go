// Package action defines the cross-kind command-runtime contract: a Runner
// interface that every command kind (resource, shell, monitor, composite)
// implements, the registry that maps kinds to their Runner, and the typed
// error used by stub runners until their own engines ship.
//
// Concrete runners live in sibling sub-packages — action/resource owns the
// resource runtime today, action/shell and action/monitor join in later
// MVP-2 PRs. The action package itself imports none of them: registration
// is done from the importing side via init() so this package stays a leaf.
package action

import (
	"context"

	"github.com/bannaarr01/packwright/manifest"
)

// Inputs is a manifest's form data, keyed by field ID. Front-ends produce it
// from user input; each Runner is free to interpret the value types it
// supports (string, []string, int, bool, ...).
type Inputs map[string]any

// Result is the cross-kind return value of Runner.Run. It carries the kind
// that produced it and an opaque, kind-specific Value that callers cast back
// to the underlying engine's native result type (e.g. *resource.Result for
// kind resource).
//
// Keeping Value as any avoids forcing the action package to import its own
// children (which would invert the dependency direction).
type Result struct {
	Kind  manifest.Kind
	Value any
}

// Runner is the cross-kind contract for executing a manifest. One Runner is
// registered per manifest.Kind via Register; Dispatch (in action/dispatch)
// looks the Runner up and forwards.
type Runner interface {
	// Kind reports the manifest kind this Runner handles.
	Kind() manifest.Kind

	// Validate performs kind-specific manifest-structure checks (does the
	// manifest declare the sections this kind requires?). It does not
	// inspect form input — that lives inside Run.
	Validate(m *manifest.Manifest) error

	// Run executes the manifest with the supplied form inputs. The returned
	// Result.Kind matches Kind(); Result.Value is the runner's native
	// result, set on success and zero-valued on error.
	Run(ctx context.Context, m *manifest.Manifest, in Inputs) (Result, error)
}
