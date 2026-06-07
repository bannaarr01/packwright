package action

import (
	"fmt"
	"sync"

	"github.com/bannaarr01/packwright/manifest"
)

// NotImplementedError is the structured error returned by stub Runners whose
// engine has not been built yet. Callers can detect it with errors.As and
// branch on the kind without scraping error strings.
type NotImplementedError struct {
	// Kind is the manifest kind whose runner is still a stub.
	Kind manifest.Kind
}

// Error renders the error message; the kind is included verbatim so logs and
// CLI output identify which engine is missing.
func (e *NotImplementedError) Error() string {
	return fmt.Sprintf("action: runner for kind %q not yet implemented", e.Kind)
}

var (
	registryMu sync.RWMutex
	registry   = map[manifest.Kind]Runner{}
)

// Register installs r in the registry, replacing any prior runner for the
// same kind. Designed to be called from an init() in each runner-owning
// package so the registry is wired up at process start.
//
// Registering a nil Runner or a Runner whose Kind() returns the empty
// string panics — both are programming errors that should fail loudly.
func Register(r Runner) {
	if r == nil {
		panic("action.Register: runner is nil")
	}
	k := r.Kind()
	if k == "" {
		panic("action.Register: runner returned empty kind")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[k] = r
}

// Lookup returns the Runner registered for k, or (nil, false) if none. The
// dispatch package is the primary caller; tests use it to assert wiring.
func Lookup(k manifest.Kind) (Runner, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	r, ok := registry[k]
	return r, ok
}
