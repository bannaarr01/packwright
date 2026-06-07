package action

import (
	"context"
	"fmt"

	"github.com/bannaarr01/packwright/manifest"
)

// stubRunner is the shared shape used by the shell / monitor / composite
// placeholders. Each instance carries the kind it represents; Run always
// returns a *NotImplementedError tagged with that kind, so dispatch callers
// can distinguish "engine not built yet" from "no runner registered".
type stubRunner struct {
	kind manifest.Kind
}

// Kind reports the manifest kind this stub stands in for.
func (s stubRunner) Kind() manifest.Kind { return s.kind }

// Validate enforces the bare-minimum check: the manifest is non-nil and its
// declared kind matches the stub. Section-level structural validation lands
// when each engine ships its own Runner.
func (s stubRunner) Validate(m *manifest.Manifest) error {
	if m == nil {
		return fmt.Errorf("action: manifest is nil")
	}
	if m.Kind != s.kind {
		return fmt.Errorf("action: manifest kind %q does not match runner kind %q", m.Kind, s.kind)
	}
	return nil
}

// Run is a no-op placeholder: it returns a zero Result and a
// *NotImplementedError. The real engine for this kind ships in a later PR.
func (s stubRunner) Run(_ context.Context, _ *manifest.Manifest, _ Inputs) (Result, error) {
	return Result{Kind: s.kind}, &NotImplementedError{Kind: s.kind}
}

// init registers stub Runners for every kind whose engine has not landed
// yet. The resource runner is registered separately by action/dispatch (it
// adapts the real engine in action/resource).
func init() {
	Register(stubRunner{kind: manifest.KindShell})
	Register(stubRunner{kind: manifest.KindMonitor})
	Register(stubRunner{kind: manifest.KindComposite})
}
