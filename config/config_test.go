package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// withHome plants an isolated Packwright home in a per-test temp dir and
// also clears AWS_REGION so default-region tests do not inherit the
// developer's shell environment.
func withHome(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "pw")
	t.Setenv("PACKWRIGHT_HOME", root)
	t.Setenv("AWS_REGION", "")
	return root
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	withHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Region != defaultRegion {
		t.Errorf("Region = %q, want %q", cfg.Region, defaultRegion)
	}
	if cfg.Theme != "auto" {
		t.Errorf("Theme = %q, want %q", cfg.Theme, "auto")
	}
	if cfg.Profile != "" {
		t.Errorf("Profile = %q, want empty", cfg.Profile)
	}
}

func TestLoadMissingFileDoesNotWrite(t *testing.T) {
	root := withHome(t)

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, err := os.Stat(filepath.Join(root, "config.yaml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load() unexpectedly created config.yaml (stat err = %v)", err)
	}
}

func TestDefaultsHonorAWSRegionEnv(t *testing.T) {
	withHome(t)
	t.Setenv("AWS_REGION", "eu-west-2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Region != "eu-west-2" {
		t.Errorf("Region = %q, want %q (AWS_REGION should override default)", cfg.Region, "eu-west-2")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withHome(t)

	want := &Config{
		Profile:        "prod",
		Region:         "us-east-1",
		Theme:          "dark",
		LogLevel:       "debug",
		Packs:          []string{"reference", "experimental"},
		PinnedDefaults: map[string]string{"/alb": "pack:reference"},
		AI:             map[string]any{"provider": "anthropic"},
	}
	if err := want.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got = %#v\nwant = %#v", got, want)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	root := withHome(t)

	cfg := &Config{Region: "eu-central-1", Theme: "light"}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// No leftover .tmp file in the home directory.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read home: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind after Save: %q", e.Name())
		}
	}

	// And config.yaml is present.
	if _, err := os.Stat(filepath.Join(root, "config.yaml")); err != nil {
		t.Fatalf("config.yaml missing after Save: %v", err)
	}
}

func TestSaveOverwritesExistingFile(t *testing.T) {
	root := withHome(t)
	path := filepath.Join(root, "config.yaml")

	// Plant an existing file with stale content.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fresh := &Config{Region: "ap-southeast-1", Theme: "auto"}
	if err := fresh.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() after overwrite error = %v", err)
	}
	if got.Region != "ap-southeast-1" {
		t.Errorf("Region after overwrite = %q, want %q", got.Region, "ap-southeast-1")
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	root := withHome(t)

	// Ensure home dir exists so we can drop a bad file in it directly.
	if _, err := Home(); err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	bad := []byte("profile: prod\nregion: [this is not a string\n")
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), bad, 0o644); err != nil {
		t.Fatalf("seed malformed: %v", err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want non-nil for malformed YAML")
	}
}
