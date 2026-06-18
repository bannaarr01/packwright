package gui

import (
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/manifest"
)

// stubResolveRunnable swaps the package-level resolver for the duration of a
// test and restores it afterwards.
func stubResolveRunnable(t *testing.T, fn func(slash string) (*manifest.Manifest, string, bool)) {
	t.Helper()
	prev := resolveRunnable
	resolveRunnable = fn
	t.Cleanup(func() { resolveRunnable = prev })
}

// TestSlashCommandFormResolved verifies the form schema is surfaced to the
// frontend for a manifest-backed slash.
func TestSlashCommandFormResolved(t *testing.T) {
	stubResolveRunnable(t, func(string) (*manifest.Manifest, string, bool) {
		return &manifest.Manifest{
			Slash: "/alb", Title: "Deploy ALB",
			Form: []manifest.Field{
				{ID: "VpcId", Label: "VPC", Type: manifest.TypeAWSVpcID, Required: true},
				{ID: "Name", Label: "Name", Type: manifest.TypeString, Placeholder: "my-alb"},
			},
		}, "/some/dir", true
	})

	a := newApp(nil)
	got := a.SlashCommandForm("/alb")
	if !got.Resolved {
		t.Fatal("Resolved = false, want true for a backed slash")
	}
	if got.Title != "Deploy ALB" || len(got.Fields) != 2 {
		t.Fatalf("payload = %+v, want title + 2 fields", got)
	}
	if got.Fields[0].ID != "VpcId" || !got.Fields[0].Required {
		t.Errorf("field[0] = %+v, want required VpcId", got.Fields[0])
	}
	if got.Fields[1].Placeholder != "my-alb" {
		t.Errorf("field[1].Placeholder = %q, want my-alb", got.Fields[1].Placeholder)
	}
}

// TestSlashCommandFormUnresolved verifies an unbacked slash reports
// Resolved=false so the frontend declines to open a run panel.
func TestSlashCommandFormUnresolved(t *testing.T) {
	stubResolveRunnable(t, func(string) (*manifest.Manifest, string, bool) {
		return nil, "", false
	})
	if got := newApp(nil).SlashCommandForm("/nope"); got.Resolved {
		t.Errorf("Resolved = true, want false for an unbacked slash")
	}
}

// TestRunSlashCommandUnknownSlash verifies a slash with no manifest yields a
// clear error rather than a silent no-op.
func TestRunSlashCommandUnknownSlash(t *testing.T) {
	stubResolveRunnable(t, func(string) (*manifest.Manifest, string, bool) {
		return nil, "", false
	})
	got := newApp(nil).RunSlashCommand("/nope", nil)
	if got.OK || got.Error == "" {
		t.Fatalf("result = %+v, want OK=false with an error", got)
	}
	if !strings.Contains(got.Error, "no command found") {
		t.Errorf("Error = %q, want it to mention 'no command found'", got.Error)
	}
}

// TestRunSlashCommandDispatchError verifies RunSlashCommand actually drives the
// engine and surfaces its error. The shell engine is not linked into the gui
// test binary, so dispatching a shell manifest hits the action package's stub
// runner ("runner for kind shell not yet implemented") — proving the binding
// resolved the manifest and called dispatch.Dispatch rather than no-opping.
func TestRunSlashCommandDispatchError(t *testing.T) {
	stubResolveRunnable(t, func(string) (*manifest.Manifest, string, bool) {
		return &manifest.Manifest{Slash: "/sh", Kind: manifest.KindShell}, "", true
	})
	got := newApp(nil).RunSlashCommand("/sh", map[string]string{})
	if got.OK || got.Error == "" {
		t.Fatalf("result = %+v, want OK=false with a dispatch error", got)
	}
	if !strings.Contains(got.Error, "shell") {
		t.Errorf("Error = %q, want it to reference the dispatched kind", got.Error)
	}
}
