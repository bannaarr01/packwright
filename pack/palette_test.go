package pack

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadPaletteEmptyHomeReturnsOnlyWizards verifies that an empty
// Packwright home produces a palette consisting solely of the built-in
// scaffold wizards. The wizards must always be present so a brand-new user
// can author their first command from a fresh install.
func TestLoadPaletteEmptyHomeReturnsOnlyWizards(t *testing.T) {
	home := t.TempDir()
	got, err := LoadPalette(home, nil)
	if err != nil {
		t.Fatalf("LoadPalette: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("LoadPalette len = %d, want >= 2 (built-in wizards)", len(got))
	}
	slashes := map[string]bool{}
	for _, e := range got {
		slashes[e.Slash] = true
		if e.Source != builtinSource {
			t.Errorf("on empty home, every row should be builtin; got Source=%q for %q", e.Source, e.Slash)
		}
	}
	for _, want := range []string{"/new-command", "/new-pack"} {
		if !slashes[want] {
			t.Errorf("missing wizard %q in palette", want)
		}
	}
}

// TestLoadPaletteUserScopeManifest verifies that a manifest in
// <home>/commands shows up in the palette tagged as user scope, with its
// title rendered verbatim (no source suffix when the slash is unique).
func TestLoadPaletteUserScopeManifest(t *testing.T) {
	home := t.TempDir()
	writeManifest(t, filepath.Join(home, "commands", "restart.yaml"), "/restart-api", "Restart API")

	got, err := LoadPalette(home, nil)
	if err != nil {
		t.Fatalf("LoadPalette: %v", err)
	}
	row := findEntry(t, got, "/restart-api")
	if row.Scope != ScopeUser {
		t.Errorf("Scope = %q, want %q", row.Scope, ScopeUser)
	}
	if row.Source != string(ScopeUser) {
		t.Errorf("Source = %q, want %q", row.Source, ScopeUser)
	}
	if row.Title != "Restart API" {
		t.Errorf("Title = %q, want %q (no suffix when unique)", row.Title, "Restart API")
	}
	if row.Pinned {
		t.Errorf("Pinned = true; nothing pinned in this test")
	}
}

// TestLoadPaletteConflictAppendsSourceSuffix verifies that when two packs
// register the same slash, both rows appear in the palette and each title
// is suffixed with its source for visual disambiguation.
func TestLoadPaletteConflictAppendsSourceSuffix(t *testing.T) {
	home := t.TempDir()
	writePack(t, home, "acme", "/alb", "ALB")
	writePack(t, home, "beta", "/alb", "ALB")

	got, err := LoadPalette(home, nil)
	if err != nil {
		t.Fatalf("LoadPalette: %v", err)
	}
	rows := filterBySlash(got, "/alb")
	if len(rows) != 2 {
		t.Fatalf("got %d /alb rows, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Source != "acme" && r.Source != "beta" {
			t.Errorf("unexpected source %q", r.Source)
		}
		want := "ALB (" + r.Source + ")"
		if r.Title != want {
			t.Errorf("Title for source %q = %q, want %q", r.Source, r.Title, want)
		}
	}
}

// TestLoadPalettePinPromotesAndMarks verifies that a pin in defaults
// promotes its source to the first row of the slash group and marks it
// with Pinned=true.
func TestLoadPalettePinPromotesAndMarks(t *testing.T) {
	home := t.TempDir()
	writePack(t, home, "acme", "/alb", "ALB")
	writePack(t, home, "beta", "/alb", "ALB")

	defaults := map[string]string{"/alb": "pack:acme"}
	got, err := LoadPalette(home, defaults)
	if err != nil {
		t.Fatalf("LoadPalette: %v", err)
	}
	rows := filterBySlash(got, "/alb")
	if len(rows) != 2 {
		t.Fatalf("got %d /alb rows, want 2", len(rows))
	}
	if rows[0].Source != "acme" {
		t.Errorf("first row source = %q, want %q (pinned)", rows[0].Source, "acme")
	}
	if !rows[0].Pinned {
		t.Errorf("first row Pinned = false, want true")
	}
	if rows[1].Pinned {
		t.Errorf("second row Pinned = true, want false")
	}
}

// TestLoadPaletteWizardsAppearLast verifies that the built-in wizards are
// emitted after the discovered manifests so a pack-scope row is never
// displaced by a builtin with the same slash.
func TestLoadPaletteWizardsAppearLast(t *testing.T) {
	home := t.TempDir()
	writePack(t, home, "acme", "/alb", "ALB")
	got, err := LoadPalette(home, nil)
	if err != nil {
		t.Fatalf("LoadPalette: %v", err)
	}
	wizardSeen := false
	for _, e := range got {
		if e.Source == builtinSource {
			wizardSeen = true
			continue
		}
		if wizardSeen {
			t.Errorf("non-builtin row %+v appeared after a builtin; wizards must be last", e)
		}
	}
	if !wizardSeen {
		t.Error("no builtin rows in palette; expected /new-command and /new-pack")
	}
}

// TestWatchRootsSkipsMissingDirs verifies WatchRoots returns only the
// directories that actually exist beneath homeDir. fsnotify rejects
// missing paths on Add, so silently dropping them keeps the caller's
// loop simple.
func TestWatchRootsSkipsMissingDirs(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	got := WatchRoots(home)
	want := []string{filepath.Join(home, "commands")}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("WatchRoots = %v, want %v", got, want)
	}
}

// findEntry returns the first palette entry matching slash or fails the
// test if none is present.
func findEntry(t *testing.T, entries []PaletteEntry, slash string) PaletteEntry {
	t.Helper()
	for _, e := range entries {
		if e.Slash == slash {
			return e
		}
	}
	t.Fatalf("no palette entry for slash %q", slash)
	return PaletteEntry{}
}

// filterBySlash returns the subset of entries whose Slash equals slash.
// Order is preserved.
func filterBySlash(entries []PaletteEntry, slash string) []PaletteEntry {
	var out []PaletteEntry
	for _, e := range entries {
		if e.Slash == slash {
			out = append(out, e)
		}
	}
	return out
}

// writeManifest materialises a minimal valid manifest at path.
func writeManifest(t *testing.T, path, slash, title string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "schema_version: packwright.manifest.v1\nkind: resource\nslash: " + slash + "\ntitle: " + title + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// writePack materialises a pack directory under <home>/packs/<name> with
// a pack.yaml and one manifest under manifests/.
func writePack(t *testing.T, home, name, slash, title string) {
	t.Helper()
	dir := filepath.Join(home, "packs", name)
	if err := os.MkdirAll(filepath.Join(dir, "manifests"), 0o755); err != nil {
		t.Fatalf("mkdir pack: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"),
		[]byte("name: "+name+"\nversion: 0.0.0\n"), 0o644); err != nil {
		t.Fatalf("write pack.yaml: %v", err)
	}
	writeManifest(t, filepath.Join(dir, "manifests", "m.yaml"), slash, title)
}
