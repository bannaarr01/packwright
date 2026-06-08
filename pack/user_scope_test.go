package pack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUserManifest drops a minimal valid manifest at <home>/<subdir>/<name>.yaml.
func writeUserManifest(t *testing.T, home, subdir, name, slash string) {
	t.Helper()
	dir := filepath.Join(home, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", dir, err)
	}
	body := "schema_version: packwright.manifest.v1\nkind: shell\nslash: " + slash + "\ntitle: " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestLoadUserScopeEmptyHomeReturnsSyntheticPack(t *testing.T) {
	// Fresh install: no commands/ or monitors/ subdirs present. LoadUserScope
	// must still return a usable Pack so callers do not need to nil-check.
	home := t.TempDir()
	p, err := LoadUserScope(home)
	if err != nil {
		t.Fatalf("LoadUserScope error: %v", err)
	}
	if p == nil {
		t.Fatal("LoadUserScope returned nil pack")
	}
	if p.Name != UserScopeName {
		t.Errorf("Name = %q, want %q", p.Name, UserScopeName)
	}
	if len(p.Manifests) != 0 {
		t.Errorf("Manifests = %d, want 0", len(p.Manifests))
	}
}

func TestLoadUserScopeLoadsCommandFile(t *testing.T) {
	// DoD: <home>/commands/foo.yaml must be loaded and reported with
	// Scope=User via Tag.
	home := t.TempDir()
	writeUserManifest(t, home, commandsSubdir, "foo", "/foo")

	p, err := LoadUserScope(home)
	if err != nil {
		t.Fatalf("LoadUserScope error: %v", err)
	}
	if len(p.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want 1", len(p.Manifests))
	}
	if got, want := p.Manifests[0].Slash, "/foo"; got != want {
		t.Errorf("Slash = %q, want %q", got, want)
	}

	tagged := Tag([]*Pack{p})
	if len(tagged) != 1 {
		t.Fatalf("Tag len = %d, want 1", len(tagged))
	}
	if tagged[0].Scope != ScopeUser {
		t.Errorf("Scope = %q, want %q", tagged[0].Scope, ScopeUser)
	}
	if tagged[0].SourcePack != "" {
		t.Errorf("SourcePack = %q, want empty for user scope", tagged[0].SourcePack)
	}
}

func TestLoadUserScopeLoadsCommandsAndMonitors(t *testing.T) {
	home := t.TempDir()
	writeUserManifest(t, home, commandsSubdir, "deploy", "/deploy")
	writeUserManifest(t, home, monitorsSubdir, "latency", "/latency")

	p, err := LoadUserScope(home)
	if err != nil {
		t.Fatalf("LoadUserScope error: %v", err)
	}
	if len(p.Manifests) != 2 {
		t.Fatalf("Manifests = %d, want 2", len(p.Manifests))
	}
	// commands/ is walked before monitors/ — both subdirs must contribute.
	if got, want := p.Manifests[0].Slash, "/deploy"; got != want {
		t.Errorf("Manifests[0].Slash = %q, want %q", got, want)
	}
	if got, want := p.Manifests[1].Slash, "/latency"; got != want {
		t.Errorf("Manifests[1].Slash = %q, want %q", got, want)
	}
}

func TestLoadUserScopeOnlyMonitorsPresent(t *testing.T) {
	// Missing commands/ must not prevent monitors/ from being read.
	home := t.TempDir()
	writeUserManifest(t, home, monitorsSubdir, "burn", "/burn")

	p, err := LoadUserScope(home)
	if err != nil {
		t.Fatalf("LoadUserScope error: %v", err)
	}
	if len(p.Manifests) != 1 || p.Manifests[0].Slash != "/burn" {
		t.Fatalf("Manifests = %v, want exactly [/burn]", p.Manifests)
	}
}

func TestLoadUserScopeSortsWithinSubdirectory(t *testing.T) {
	// Lexical order inside each subdir keeps results deterministic across OSes.
	home := t.TempDir()
	writeUserManifest(t, home, commandsSubdir, "zeta", "/zeta")
	writeUserManifest(t, home, commandsSubdir, "alpha", "/alpha")
	writeUserManifest(t, home, commandsSubdir, "mike", "/mike")

	p, err := LoadUserScope(home)
	if err != nil {
		t.Fatalf("LoadUserScope error: %v", err)
	}
	got := []string{p.Manifests[0].Slash, p.Manifests[1].Slash, p.Manifests[2].Slash}
	want := []string{"/alpha", "/mike", "/zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestLoadUserScopeIgnoresNonYAMLFiles(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, commandsSubdir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, commandsSubdir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	p, err := LoadUserScope(home)
	if err != nil {
		t.Fatalf("LoadUserScope error: %v", err)
	}
	if len(p.Manifests) != 0 {
		t.Errorf("Manifests = %d, want 0", len(p.Manifests))
	}
}

func TestLoadUserScopePropagatesParseError(t *testing.T) {
	// A malformed manifest must surface as an error so authors notice typos
	// immediately rather than seeing a silently missing command at runtime.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, commandsSubdir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Tab inside a mapping triggers a YAML parse error.
	if err := os.WriteFile(
		filepath.Join(home, commandsSubdir, "bad.yaml"),
		[]byte("schema_version: x\n\tslash: /bad\n"),
		0o644,
	); err != nil {
		t.Fatalf("write bad manifest: %v", err)
	}

	_, err := LoadUserScope(home)
	if err == nil {
		t.Fatal("LoadUserScope accepted malformed YAML, want error")
	}
	if !strings.Contains(err.Error(), "bad.yaml") {
		t.Errorf("error %q does not name the offending file", err)
	}
}
