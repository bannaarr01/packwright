package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/pack"
)

// TestAdd_GitClone_Trusted is the canonical happy path: clone a
// remote, consent granted, metadata recorded, directory present.
func TestAdd_GitClone_Trusted(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		"pack.yaml":              "name: demo\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
	})

	meta, err := Add(context.Background(), home, fileURL(bare))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if meta.Name != "demo" {
		t.Errorf("Name = %q, want %q (canonical name from pack.yaml)", meta.Name, "demo")
	}
	if meta.URL == "" {
		t.Errorf("URL empty; want the remote URL")
	}
	if meta.Local {
		t.Errorf("Local = true; want false for git installs")
	}
	if !strings.HasPrefix(meta.TrustedHash, "sha256:") {
		t.Errorf("TrustedHash = %q; want sha256:-prefixed", meta.TrustedHash)
	}
	if len(meta.Surface.Commands) != 1 {
		t.Errorf("Surface.Commands len = %d, want 1", len(meta.Surface.Commands))
	}

	// Cloned tree present on disk under the canonical name.
	if _, err := os.Stat(filepath.Join(home, "packs", "demo", "pack.yaml")); err != nil {
		t.Errorf("expected packs/demo/pack.yaml, got %v", err)
	}
	// Metadata file written alongside the directory.
	if _, err := os.Stat(filepath.Join(home, "packs", "demo.install.json")); err != nil {
		t.Errorf("expected install metadata, got %v", err)
	}

	// List reflects the new pack.
	listed, err := List(home)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != "demo" {
		t.Errorf("List = %+v, want exactly demo", listed)
	}
}

// TestAdd_GitClone_DeniedRemovesDir asserts the DoD requirement that
// a denied consent leaves no trace: no pack directory, no metadata.
func TestAdd_GitClone_DeniedRemovesDir(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysDeny)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		"pack.yaml":              "name: scary\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "rm", "-rf", "/"),
	})

	_, err := Add(context.Background(), home, fileURL(bare))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("Add error = %v, want ErrDenied", err)
	}
	// Pack dir and metadata both gone.
	if _, err := os.Stat(filepath.Join(home, "packs", "scary")); !os.IsNotExist(err) {
		t.Errorf("packs/scary still exists after denial: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "packs", "scary.install.json")); !os.IsNotExist(err) {
		t.Errorf("metadata still exists after denial: %v", err)
	}
}

// TestAdd_GitClone_WithRef pins a tag at install time and asserts the
// working tree matches the tagged commit (not HEAD of main).
func TestAdd_GitClone_WithRef(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		"pack.yaml": "name: tagged\nversion: 0.1.0\n",
		"README.md": "v1\n",
	})
	tagCommit(t, bare, "v1.0.0")
	// Advance main past the tag so a default clone would see v2.
	pushCommit(t, bare, map[string]string{"README.md": "v2\n"}, "v2")

	_, err := Add(context.Background(), home, fileURL(bare)+"#v1.0.0")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(home, "packs", "tagged", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if got := strings.TrimSpace(string(readme)); got != "v1" {
		t.Fatalf("README at pinned ref = %q, want %q", got, "v1")
	}
}

// TestUpdate_NoSurfaceChange covers the "README edit only" path: the
// trusted hash advances but no consent prompt fires. RequestConsent
// is set to a marker function that records calls so the test can
// assert it was not invoked.
func TestUpdate_NoSurfaceChange(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		"pack.yaml":              "name: stable\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
		"README.md":              "v1\n",
	})

	meta, err := Add(context.Background(), home, fileURL(bare))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	prevHash := meta.TrustedHash

	// Edit only the README upstream and update.
	pushCommit(t, bare, map[string]string{"README.md": "v2\n"}, "doc tweak")

	var consentCalls int
	setConsent(t, func(pack.Surface, string) pack.Decision {
		consentCalls++
		return pack.Trusted
	})

	updated, err := Update(context.Background(), home, "stable")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if consentCalls != 0 {
		t.Errorf("consent prompted %d times for a README-only change; want 0", consentCalls)
	}
	if updated.TrustedHash == prevHash {
		t.Errorf("TrustedHash unchanged after README edit: %q", prevHash)
	}
	if updated.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt zero after a real update")
	}
}

// TestUpdate_SurfaceChangedReprompts is the DoD's headline guarantee:
// a pack with new shell commands re-prompts on update. RequestConsent
// is asserted on both invocation count and the oldHash argument it
// receives (which must be the previously trusted hash, not "").
func TestUpdate_SurfaceChangedReprompts(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		"pack.yaml":              "name: reprompt\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
	})

	meta, err := Add(context.Background(), home, fileURL(bare))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	prevHash := meta.TrustedHash

	// Add a new shell manifest upstream — surface grows by one entry.
	pushCommit(t, bare, map[string]string{
		"manifests/danger.yaml": shellManifestYAML("/danger", "rm", "-rf", "/"),
	}, "add danger")

	var (
		consentCalls int
		sawOldHash   string
		sawSurface   pack.Surface
	)
	setConsent(t, func(s pack.Surface, oldHash string) pack.Decision {
		consentCalls++
		sawOldHash = oldHash
		sawSurface = s
		return pack.Trusted
	})

	if _, err := Update(context.Background(), home, "reprompt"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if consentCalls != 1 {
		t.Fatalf("consent prompted %d times; want 1", consentCalls)
	}
	if sawOldHash != prevHash {
		t.Errorf("RequestConsent oldHash = %q, want previous trusted hash %q", sawOldHash, prevHash)
	}
	if len(sawSurface.Commands) != 2 {
		t.Errorf("RequestConsent surface has %d commands, want 2 (the new one and the original)", len(sawSurface.Commands))
	}
}

// TestUpdate_DeniedRollsBack guards the rollback: a denied consent
// must leave the working tree at the pre-pull commit so the user is
// not stranded mid-update.
func TestUpdate_DeniedRollsBack(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		"pack.yaml":              "name: rollback\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
	})
	meta, err := Add(context.Background(), home, fileURL(bare))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	prevHash := meta.TrustedHash

	pushCommit(t, bare, map[string]string{
		"manifests/danger.yaml": shellManifestYAML("/danger", "rm", "-rf", "/"),
	}, "add danger")

	setConsent(t, alwaysDeny)
	_, err = Update(context.Background(), home, "rollback")
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("Update error = %v, want ErrDenied", err)
	}

	// Working tree reset: the danger manifest is gone again.
	if _, err := os.Stat(filepath.Join(home, "packs", "rollback", "manifests", "danger.yaml")); !os.IsNotExist(err) {
		t.Errorf("danger.yaml still present after denied update: %v", err)
	}
	// Metadata still points at the pre-update trusted state.
	post, err := readMeta(home, "rollback")
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if post.TrustedHash != prevHash {
		t.Errorf("post-denial TrustedHash = %q, want %q (unchanged)", post.TrustedHash, prevHash)
	}
}

// TestRemove_DeletesDirAndMetadataAndPins covers the "removes pins"
// branch of the contract. A pin in config.yaml that points at the
// pack being removed must be stripped; pins pointing elsewhere must
// be preserved.
func TestRemove_DeletesDirAndMetadataAndPins(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		"pack.yaml":              "name: pinned\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
	})
	if _, err := Add(context.Background(), home, fileURL(bare)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cfg := []byte(`profile: ""
region: ""
theme: ""
log_level: ""
defaults:
  /restart: pack:pinned
  /other: pack:keepme
`)
	cfgPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := Remove(home, "pinned"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "packs", "pinned")); !os.IsNotExist(err) {
		t.Errorf("packs/pinned still exists after Remove: %v", err)
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("re-read config.yaml: %v", err)
	}
	if strings.Contains(string(got), "pack:pinned") {
		t.Errorf("config.yaml still references removed pack:\n%s", got)
	}
	if !strings.Contains(string(got), "pack:keepme") {
		t.Errorf("config.yaml lost unrelated pin:\n%s", got)
	}
}

// TestScanTopLevelString exercises the tiny YAML peeker we use to
// pull pack.yaml's `name:` without importing the strict loader. The
// cases mirror the shapes pack-author-written files take in practice.
func TestScanTopLevelString(t *testing.T) {
	cases := []struct {
		yaml, key, want string
	}{
		{"name: foo\nversion: 0.1.0\n", "name", "foo"},
		{"# header\nname: \"quoted\"\n", "name", "quoted"},
		{"name: with-comment # trailing\n", "name", "with-comment"},
		{"requires:\n  name: inner\n", "name", ""}, // nested keys ignored
	}
	for _, c := range cases {
		got := scanTopLevelString([]byte(c.yaml), c.key)
		if got != c.want {
			t.Errorf("scanTopLevelString(%q, %q) = %q, want %q", c.yaml, c.key, got, c.want)
		}
	}
}

// TestAdd_GitClone_DerivedNameWithoutPackYAML asserts the URL-derived
// fallback fires when pack.yaml is absent: a freshly cloned tree
// without a pack.yaml retains the URL-derived name rather than
// erroring.
func TestAdd_GitClone_DerivedNameWithoutPackYAML(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		// No pack.yaml — install must fall back to the URL stem.
		"README.md": "placeholder\n",
	})

	meta, err := Add(context.Background(), home, fileURL(bare))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if meta.Name != "remote" {
		t.Errorf("Name = %q, want %q (URL-derived stem)", meta.Name, "remote")
	}
}

// TestUpdate_NotInstalled returns ErrNotInstalled cleanly without
// touching disk.
func TestUpdate_NotInstalled(t *testing.T) {
	home := makeHome(t)
	_, err := Update(context.Background(), home, "ghost")
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Update(ghost) error = %v, want ErrNotInstalled", err)
	}
}

// TestSurfaceRoundTrip is a defence-in-depth: the Installed.Surface
// field is JSON-serialised, so we want to know if a future change to
// pack.Surface breaks the round-trip in a way deep equality (used by
// Update) would silently miss.
func TestSurfaceRoundTrip(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		"pack.yaml":              "name: rt\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
	})
	meta, err := Add(context.Background(), home, fileURL(bare))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	loaded, err := readMeta(home, meta.Name)
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if !reflect.DeepEqual(meta.Surface, loaded.Surface) {
		t.Fatalf("Surface lost in JSON round-trip:\n in=%+v\nout=%+v", meta.Surface, loaded.Surface)
	}
}
