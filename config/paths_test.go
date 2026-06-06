package config

import (
	"os"
	"path/filepath"
	"testing"
)

// envMap is a small helper that turns a literal map into the
// getenv-shaped lookup function resolveHome expects.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveHome(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want string // joined with filepath separators for the target OS
	}{
		// PACKWRIGHT_HOME wins on every OS.
		{
			name: "override wins on linux",
			goos: "linux",
			env:  map[string]string{"PACKWRIGHT_HOME": "/opt/pw", "XDG_CONFIG_HOME": "/x", "HOME": "/h"},
			want: "/opt/pw",
		},
		{
			name: "override wins on darwin",
			goos: "darwin",
			env:  map[string]string{"PACKWRIGHT_HOME": "/opt/pw", "HOME": "/h"},
			want: "/opt/pw",
		},
		{
			name: "override wins on windows",
			goos: "windows",
			env:  map[string]string{"PACKWRIGHT_HOME": `C:\opt\pw`, "APPDATA": `C:\Users\u\AppData\Roaming`},
			want: `C:\opt\pw`,
		},

		// Linux respects XDG_CONFIG_HOME.
		{
			name: "linux uses xdg when set",
			goos: "linux",
			env:  map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"},
			want: filepath.Join("/x", "packwright"),
		},
		// Linux falls back to HOME/.config when XDG is unset.
		{
			name: "linux falls back to HOME/.config",
			goos: "linux",
			env:  map[string]string{"HOME": "/h"},
			want: filepath.Join("/h", ".config", "packwright"),
		},

		// macOS ignores XDG even when set, per ADR-0010.
		{
			name: "darwin ignores xdg",
			goos: "darwin",
			env:  map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"},
			want: filepath.Join("/h", ".config", "packwright"),
		},

		// Windows uses APPDATA\Packwright.
		{
			name: "windows uses APPDATA",
			goos: "windows",
			env:  map[string]string{"APPDATA": `C:\Users\u\AppData\Roaming`},
			want: filepath.Join(`C:\Users\u\AppData\Roaming`, "Packwright"),
		},

		// Unknown GOOS uses HOME fallback (BSD etc.).
		{
			name: "unknown goos falls back to HOME",
			goos: "freebsd",
			env:  map[string]string{"HOME": "/h"},
			want: filepath.Join("/h", ".config", "packwright"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveHome(envMap(tc.env), tc.goos)
			if err != nil {
				t.Fatalf("resolveHome() error = %v, want nil", err)
			}
			if got != tc.want {
				t.Errorf("resolveHome() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveHomeErrors(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
	}{
		{"linux nothing set", "linux", map[string]string{}},
		{"darwin nothing set", "darwin", map[string]string{}},
		{"windows nothing set", "windows", map[string]string{}},
		{"unknown nothing set", "openbsd", map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveHome(envMap(tc.env), tc.goos); err == nil {
				t.Fatal("resolveHome() error = nil, want non-nil")
			}
		})
	}
}

func TestHomeCreatesDirectoryTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pw")
	t.Setenv("PACKWRIGHT_HOME", root)

	got, err := Home()
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	if got != root {
		t.Fatalf("Home() = %q, want %q", got, root)
	}

	for _, sub := range subdirs {
		p := filepath.Join(root, sub)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("subdir %q not created: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q exists but is not a directory", p)
		}
	}
}

func TestHomeIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pw")
	t.Setenv("PACKWRIGHT_HOME", root)

	if _, err := Home(); err != nil {
		t.Fatalf("first Home() error = %v", err)
	}
	// Drop a sentinel file inside one of the subdirs; a second Home() call
	// must not touch it.
	sentinel := filepath.Join(root, "packs", "keep.txt")
	if err := os.WriteFile(sentinel, []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}
	if _, err := Home(); err != nil {
		t.Fatalf("second Home() error = %v", err)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel disappeared after second Home(): %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("sentinel mutated after second Home(): got %q", string(got))
	}
}

func TestPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pw")
	t.Setenv("PACKWRIGHT_HOME", root)

	got, err := Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	want := filepath.Join(root, "config.yaml")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}
