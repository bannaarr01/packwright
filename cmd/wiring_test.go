package cmd

import (
	"testing"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/internal/ai/provider"
	"github.com/bannaarr01/packwright/internal/ai/tools"
	"github.com/bannaarr01/packwright/manifest"
)

// TestWiringRegistersEngines guards the blank-import seam in wiring.go. The
// shell and composite action runners and the AI provider/tool catalogue all
// self-register from init(), which only fires when their packages are linked
// into the binary. If a blank import is dropped from wiring.go the feature
// would silently vanish at runtime while still compiling and passing its own
// package tests; this test fails loudly instead.
//
// KindResource is intentionally not asserted here — the resource runner is
// wired through a different seam (action/dispatch/resource_runner.go, pulled
// in by bootstrap) and is covered by that package's tests.
func TestWiringRegistersEngines(t *testing.T) {
	for _, k := range []manifest.Kind{manifest.KindShell, manifest.KindComposite} {
		if _, ok := action.Lookup(k); !ok {
			t.Errorf("no action runner registered for kind %q — wiring.go blank import missing?", k)
		}
	}
	if got := provider.Known(); len(got) == 0 {
		t.Error("provider.Known() is empty: no AI provider registered — wiring.go blank imports missing?")
	}
	if got := tools.Default.List(); len(got) == 0 {
		t.Error("tools.Default.List() is empty: no AI tools registered — wiring.go blank import missing?")
	}
}
