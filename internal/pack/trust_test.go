package pack

import (
	"reflect"
	"strings"
	"testing"
)

// TestScan_ShellManifest is the canonical surface entry: a kind: shell
// manifest with a run.command array. The Surface must contain exactly
// one Command and it must carry the manifest's relative path, slash,
// SourceCommand source, the original argv, and an empty Shell field.
func TestScan_ShellManifest(t *testing.T) {
	root := buildPack(t, map[string]string{
		"pack.yaml":              "name: shell\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifest("/restart", "aws", "ecs", "update-service"),
	})

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := Surface{
		Commands: []Command{
			{
				Manifest: "manifests/restart.yaml",
				Slash:    "/restart",
				Source:   SourceCommand,
				Argv:     []string{"aws", "ecs", "update-service"},
				Shell:    "",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Surface mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestScan_BashShellFlagged guards the ⚠ marker case from ADR-0025: a
// shell: bash manifest must be visible to the consent renderer via
// Command.Shell == Bash. The renderer reads this field to draw the
// warning glyph.
func TestScan_BashShellFlagged(t *testing.T) {
	manifest := strings.Join([]string{
		"schema_version: packwright.manifest.v1",
		"kind: shell",
		"slash: /panic",
		"title: panic",
		"run:",
		`  command: "bash -c 'git reset --hard'"`,
		"  shell: bash",
		"",
	}, "\n")
	root := buildPack(t, map[string]string{
		"pack.yaml":            "name: bash\nversion: 0.1.0\n",
		"manifests/panic.yaml": manifest,
	})

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Commands) != 1 {
		t.Fatalf("Commands len = %d, want 1 (%+v)", len(got.Commands), got)
	}
	if got.Commands[0].Shell != Bash {
		t.Fatalf("Commands[0].Shell = %q, want %q", got.Commands[0].Shell, Bash)
	}
}

// TestScan_MonitorPanel exercises the kind: monitor branch: only
// panels whose kind is shell/output contribute to the surface; other
// panel kinds are ignored. The shell-output panel's command is pulled
// from panel.spec.command, matching the monitorx panel schema.
func TestScan_MonitorPanel(t *testing.T) {
	manifest := strings.Join([]string{
		"schema_version: packwright.manifest.v1",
		"kind: monitor",
		"slash: /dash",
		"title: dash",
		"monitor:",
		"  panels:",
		"    - id: tail",
		"      kind: shell/output",
		"      spec:",
		"        command: [tail, -f, /var/log/foo.log]",
		"    - id: graph",
		"      kind: cloudwatch/metric",
		"      spec:",
		"        namespace: AWS/EC2",
		"",
	}, "\n")
	root := buildPack(t, map[string]string{
		"pack.yaml":           "name: monitor\nversion: 0.1.0\n",
		"manifests/dash.yaml": manifest,
	})

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Commands) != 1 {
		t.Fatalf("Commands len = %d, want 1 (panels not filtered by kind?): %+v", len(got.Commands), got)
	}
	c := got.Commands[0]
	if c.Source != SourceMonitorPanel {
		t.Errorf("Source = %q, want %q", c.Source, SourceMonitorPanel)
	}
	if !reflect.DeepEqual(c.Argv, []string{"tail", "-f", "/var/log/foo.log"}) {
		t.Errorf("Argv = %#v, want %#v", c.Argv, []string{"tail", "-f", "/var/log/foo.log"})
	}
}

// TestScan_CompositeStepInline covers the composite-step branch. A step
// with an inline shell: block contributes a SourceCompositeStep entry;
// a step that only references another slash via run: contributes
// nothing (the referenced manifest owns its own surface and is scanned
// independently).
func TestScan_CompositeStepInline(t *testing.T) {
	manifest := strings.Join([]string{
		"schema_version: packwright.manifest.v1",
		"kind: composite",
		"slash: /workflow",
		"title: workflow",
		"steps:",
		"  - run: /alb",
		"  - shell:",
		"      command: [echo, after-alb]",
		"  - confirm: are you sure",
		"",
	}, "\n")
	root := buildPack(t, map[string]string{
		"pack.yaml":               "name: composite\nversion: 0.1.0\n",
		"manifests/workflow.yaml": manifest,
	})

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Commands) != 1 {
		t.Fatalf("Commands len = %d, want 1 (only the inline shell step counts): %+v", len(got.Commands), got)
	}
	c := got.Commands[0]
	if c.Source != SourceCompositeStep {
		t.Errorf("Source = %q, want %q", c.Source, SourceCompositeStep)
	}
	if !reflect.DeepEqual(c.Argv, []string{"echo", "after-alb"}) {
		t.Errorf("Argv = %#v, want %#v", c.Argv, []string{"echo", "after-alb"})
	}
	if c.Slash != "/workflow" {
		t.Errorf("Slash = %q, want %q", c.Slash, "/workflow")
	}
}

// TestScan_OrderingDeterministic asserts the stable-order contract:
// manifests are visited in lexical order. This is what lets the
// consent screen diff old-vs-new versions of a pack on update without
// false positives from filesystem iteration order.
func TestScan_OrderingDeterministic(t *testing.T) {
	root := buildPack(t, map[string]string{
		"pack.yaml":        "name: order\nversion: 0.1.0\n",
		"manifests/b.yaml": shellManifest("/b", "echo", "b"),
		"manifests/a.yaml": shellManifest("/a", "echo", "a"),
		"manifests/c.yaml": shellManifest("/c", "echo", "c"),
	})

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Commands) != 3 {
		t.Fatalf("Commands len = %d, want 3: %+v", len(got.Commands), got)
	}
	wantOrder := []string{"manifests/a.yaml", "manifests/b.yaml", "manifests/c.yaml"}
	for i, want := range wantOrder {
		if got.Commands[i].Manifest != want {
			t.Errorf("Commands[%d].Manifest = %q, want %q", i, got.Commands[i].Manifest, want)
		}
	}
}

// TestScan_ResourceContributesNothing verifies that pure resource
// manifests (the MVP-1 form-driven kind) leave the surface empty. The
// consent screen for such a pack still appears (per ADR-0025) but
// shows no shell calls — Scan does not invent any.
func TestScan_ResourceContributesNothing(t *testing.T) {
	manifest := strings.Join([]string{
		"schema_version: packwright.manifest.v1",
		"kind: resource",
		"slash: /alb",
		"title: alb",
		"template:",
		"  kind: cloudformation",
		"  path: alb.yaml",
		"deploy:",
		"  driver: script",
		"  script: deploy.sh",
		"",
	}, "\n")
	root := buildPack(t, map[string]string{
		"pack.yaml":          "name: resource\nversion: 0.1.0\n",
		"manifests/alb.yaml": manifest,
	})

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Commands) != 0 {
		t.Fatalf("Commands len = %d, want 0 for resource-only pack: %+v", len(got.Commands), got)
	}
}

// TestScan_MissingManifestsDir matches the discovery convention: a
// pack without a manifests/ subdirectory is legal (templates-only
// library pack) and produces an empty Surface, not an error.
func TestScan_MissingManifestsDir(t *testing.T) {
	root := buildPack(t, map[string]string{
		"pack.yaml": "name: empty\nversion: 0.1.0\n",
	})

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Commands) != 0 {
		t.Fatalf("Commands len = %d, want 0 for pack without manifests/: %+v", len(got.Commands), got)
	}
}

// TestScan_MalformedYAMLSkipped guards the resilience contract: a
// structurally broken YAML file must not abort the whole scan. The
// strict loader downstream will surface the parse error with full
// context; Scan keeps the consent screen useful for the rest of the
// pack.
func TestScan_MalformedYAMLSkipped(t *testing.T) {
	root := buildPack(t, map[string]string{
		"pack.yaml":              "name: malformed\nversion: 0.1.0\n",
		"manifests/broken.yaml":  ":\n- not valid yaml\n  : at all\n",
		"manifests/restart.yaml": shellManifest("/restart", "aws", "ecs", "update-service"),
	})

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Commands) != 1 {
		t.Fatalf("Commands len = %d, want 1 (broken manifest must be skipped): %+v", len(got.Commands), got)
	}
	if got.Commands[0].Manifest != "manifests/restart.yaml" {
		t.Errorf("Commands[0].Manifest = %q, want %q", got.Commands[0].Manifest, "manifests/restart.yaml")
	}
}

// TestRequestConsent_DefaultDenies asserts the headless-safe default:
// without a UI-layer init() overriding RequestConsent, every call must
// return Denied. This is the safety net for CI runs, non-interactive
// scripts, and the test binary itself.
func TestRequestConsent_DefaultDenies(t *testing.T) {
	prev := RequestConsent
	t.Cleanup(func() { RequestConsent = prev })
	RequestConsent = denyConsent

	got := RequestConsent(Surface{}, "")
	if got != Denied {
		t.Fatalf("default RequestConsent = %v, want %v", got, Denied)
	}
	got = RequestConsent(Surface{Commands: []Command{{Argv: []string{"rm", "-rf", "/"}}}}, "sha256:old")
	if got != Denied {
		t.Fatalf("default RequestConsent (with surface) = %v, want %v", got, Denied)
	}
}

// TestRequestConsent_Override exercises the UI-layer override pattern:
// a front-end's init() can swap RequestConsent for its own function,
// and that function receives the Surface and oldHash verbatim.
func TestRequestConsent_Override(t *testing.T) {
	prev := RequestConsent
	t.Cleanup(func() { RequestConsent = prev })

	var (
		gotSurface Surface
		gotHash    string
	)
	RequestConsent = func(s Surface, oldHash string) Decision {
		gotSurface = s
		gotHash = oldHash
		return Trusted
	}

	surface := Surface{Commands: []Command{{
		Manifest: "manifests/restart.yaml",
		Slash:    "/restart",
		Source:   SourceCommand,
		Argv:     []string{"echo", "hi"},
	}}}
	if got := RequestConsent(surface, "sha256:old"); got != Trusted {
		t.Fatalf("override RequestConsent = %v, want %v", got, Trusted)
	}
	if !reflect.DeepEqual(gotSurface, surface) {
		t.Errorf("override saw surface %+v, want %+v", gotSurface, surface)
	}
	if gotHash != "sha256:old" {
		t.Errorf("override saw oldHash %q, want %q", gotHash, "sha256:old")
	}
}

// TestDecision_String guards the human-readable rendering of Decision.
// Audit logs and test failure messages depend on these names being
// stable identifiers, so the assertions are by literal string.
func TestDecision_String(t *testing.T) {
	cases := []struct {
		d    Decision
		want string
	}{
		{Denied, "Denied"},
		{Trusted, "Trusted"},
		{Decision(42), "Decision(?)"},
	}
	for _, c := range cases {
		if got := c.d.String(); got != c.want {
			t.Errorf("Decision(%d).String() = %q, want %q", int(c.d), got, c.want)
		}
	}
}

// TestHasHashPrefix exercises the internal helper that recognises the
// canonical "sha256:" prefix Hash produces. The follow-up consent-
// screen PR uses this when reading stored hashes that may lack the
// prefix (legacy state).
func TestHasHashPrefix(t *testing.T) {
	if !hasHashPrefix("sha256:abc") {
		t.Errorf("hasHashPrefix(sha256:abc) = false, want true")
	}
	if hasHashPrefix("abc") {
		t.Errorf("hasHashPrefix(abc) = true, want false")
	}
	if hasHashPrefix("") {
		t.Errorf("hasHashPrefix(\"\") = true, want false")
	}
}
