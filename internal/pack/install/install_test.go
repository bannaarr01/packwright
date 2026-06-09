package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/pack"
)

// setConsent installs a RequestConsent override for the duration of t
// and restores the prior value via t.Cleanup. Tests use it to script
// the consent flow: pass pack.Trusted for a happy-path install, or a
// custom function to assert on the surface argument before deciding.
func setConsent(t *testing.T, fn func(pack.Surface, string) pack.Decision) {
	t.Helper()
	prev := pack.RequestConsent
	t.Cleanup(func() { pack.RequestConsent = prev })
	pack.RequestConsent = fn
}

// alwaysTrust is the canonical setConsent value for happy-path tests.
func alwaysTrust(pack.Surface, string) pack.Decision { return pack.Trusted }

// alwaysDeny is the canonical setConsent value for denial-path tests.
func alwaysDeny(pack.Surface, string) pack.Decision { return pack.Denied }

// requireGit aborts the test (via t.Skip) when no git binary is
// available. The install package's git operations are exercised
// against a real local repo because os/exec is the only abstraction
// between us and git; faking it out via interface stubs would test
// the stub and not the code.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary unavailable: %v", err)
	}
}

// makeHome creates a fresh tempdir laid out the way config.Home
// produces — a `packs/` subdirectory ready for installs.
func makeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "packs"), 0o755); err != nil {
		t.Fatalf("mkdir packs: %v", err)
	}
	return home
}

// writePackFiles writes a map of pack-relative paths to file content
// under root, creating intermediate directories. It is the helper the
// hash_test.go suite uses, duplicated here so the install tests stay
// self-contained.
func writePackFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %q: %v", p, err)
		}
	}
}

// shellManifestYAML renders a minimal kind: shell manifest with the
// given slash and argv. Mirrors the helper in internal/pack/hash_test.go
// — pack-side tests use the same shape, and copying it here avoids
// exporting the helper from internal/pack just for tests.
func shellManifestYAML(slash string, argv ...string) string {
	var b strings.Builder
	b.WriteString("schema_version: packwright.manifest.v1\n")
	b.WriteString("kind: shell\n")
	b.WriteString("slash: ")
	b.WriteString(slash)
	b.WriteString("\n")
	b.WriteString("title: ")
	b.WriteString(strings.TrimPrefix(slash, "/"))
	b.WriteString("\n")
	b.WriteString("run:\n  command:\n")
	for _, a := range argv {
		b.WriteString("    - ")
		b.WriteString(a)
		b.WriteString("\n")
	}
	return b.String()
}

// initRemote sets up a bare git repository at <tmp>/remote.git seeded
// with the supplied pack files. The flow is the conventional "two
// repos sharing one commit" dance:
//
//  1. Initialise a working tree at <tmp>/seed.
//  2. Write the supplied files there.
//  3. `git init -b main`, commit, push to the bare repo.
//
// Returns the absolute path to the bare repo. Callers pass
// "file://<path>" to Add so git treats it as a remote.
func initRemote(t *testing.T, files map[string]string) string {
	t.Helper()
	requireGit(t)

	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")

	runOrFail(t, "", "git", "init", "--bare", "-b", "main", bare)
	runOrFail(t, "", "git", "init", "-b", "main", seed)
	configRepo(t, seed)
	writePackFiles(t, seed, files)

	runOrFail(t, seed, "git", "add", ".")
	runOrFail(t, seed, "git", "commit", "-m", "seed")
	runOrFail(t, seed, "git", "push", bare, "main")
	return bare
}

// configRepo applies the local user identity every commit needs. We
// scope this with `git config` (not `--global`) so the test never
// touches the developer's ~/.gitconfig.
func configRepo(t *testing.T, dir string) {
	t.Helper()
	runOrFail(t, dir, "git", "config", "user.email", "test@packwright.dev")
	runOrFail(t, dir, "git", "config", "user.name", "Packwright Test")
	runOrFail(t, dir, "git", "config", "commit.gpgsign", "false")
}

// runOrFail runs cmd in dir and fails the test on non-zero exit,
// surfacing stderr verbatim so the failure message points at the real
// problem rather than just "exit status 1".
func runOrFail(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// pushCommit writes additional files into the seed working tree (the
// "remote" peer prepared by initRemote does not have one of its own
// since it is bare) and pushes a new commit to the bare repo. The
// seed directory's path is reconstructed from the bare path: the two
// live as siblings under the same parent.
func pushCommit(t *testing.T, bare string, files map[string]string, message string) {
	t.Helper()
	seed := filepath.Join(filepath.Dir(bare), "seed")
	writePackFiles(t, seed, files)
	runOrFail(t, seed, "git", "add", ".")
	runOrFail(t, seed, "git", "commit", "-m", message)
	runOrFail(t, seed, "git", "push", bare, "main")
}

// tagCommit creates an annotated tag in the seed working tree and
// pushes it to the bare repo. Used by the ref-pinning test so the
// derived install can `git checkout <tag>`.
func tagCommit(t *testing.T, bare, tag string) {
	t.Helper()
	seed := filepath.Join(filepath.Dir(bare), "seed")
	runOrFail(t, seed, "git", "tag", "-a", tag, "-m", tag)
	runOrFail(t, seed, "git", "push", bare, tag)
}

// fileURL turns a filesystem path into a file:// URL. git accepts the
// bare path too, but the file:// form is what users see in error
// messages, so the tests round-trip the same form.
func fileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}

// TestList_Empty asserts that a fresh home directory with no packs
// yields a nil slice and a nil error. The GUI palette refreshes on
// every focus change, so this hot path must be cheap and silent.
func TestList_Empty(t *testing.T) {
	home := makeHome(t)
	got, err := List(home)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got != nil {
		t.Fatalf("List = %+v, want nil", got)
	}
}

// TestList_MissingPacksDir guards the "fresh install" case where
// `<home>/packs` itself hasn't been materialised yet — Discover and
// the install package must both treat that as "no packs", not an
// error.
func TestList_MissingPacksDir(t *testing.T) {
	home := t.TempDir() // no packs/ subdir
	got, err := List(home)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got != nil {
		t.Fatalf("List = %+v, want nil", got)
	}
}

// TestList_SkipsDirsWithoutMetadata exercises the manual-install
// tolerance: a pack-shaped directory dropped into <home>/packs/ by
// hand (no install metadata) is invisible to List but still
// discoverable by pack.Discover. Mixing manual and managed packs
// must not cause List to error.
func TestList_SkipsDirsWithoutMetadata(t *testing.T) {
	home := makeHome(t)
	writePackFiles(t, filepath.Join(home, "packs", "manual"), map[string]string{
		"pack.yaml": "name: manual\nversion: 0.1.0\n",
	})
	got, err := List(home)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List = %+v, want empty (manual installs are not listed)", got)
	}
}

// TestRemove_NotInstalled returns ErrNotInstalled for a missing
// pack — Remove is idempotent only for managed packs, not for
// arbitrary names.
func TestRemove_NotInstalled(t *testing.T) {
	home := makeHome(t)
	err := Remove(home, "ghost")
	if !errorsIs(err, ErrNotInstalled) {
		t.Fatalf("Remove(ghost) error = %v, want ErrNotInstalled", err)
	}
}

// errorsIs is a tiny shim so tests can read like "err matches X"
// without `errors.Is(err, X)` noise on every line. Behaves
// identically to errors.Is for nil and non-nil err.
func errorsIs(err, target error) bool {
	return err != nil && (err == target || strings.Contains(err.Error(), target.Error()))
}

// TestDerivedName covers the URL-to-directory-name rule from
// ADR-0027. The cases mirror the forms `git clone` itself recognises
// so install agrees with users' expectations.
func TestDerivedName(t *testing.T) {
	cases := []struct {
		url, want string
	}{
		{"https://github.com/acme/foo.git", "foo"},
		{"https://github.com/acme/foo", "foo"},
		{"git@github.com:acme/foo.git", "foo"},
		{"https://github.com/acme/foo/", "foo"},
		{"file:///tmp/repo.git", "repo"},
		{"https://example.com/x/y/z/long-name.git", "long-name"},
	}
	for _, c := range cases {
		if got := derivedName(c.url); got != c.want {
			t.Errorf("derivedName(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// TestParseSource is a smoke test for the URL/path classifier. The
// hard cases are: a bare name (rejected — user must prefix `./`), a
// `#ref` pin attached to an absolute path (kept as part of the path),
// and a URL with both `?query` and `#ref`.
func TestParseSource(t *testing.T) {
	cases := []struct {
		arg        string
		wantLocal  bool
		wantURL    string
		wantRef    string
		wantErrSub string
	}{
		{arg: "./local", wantLocal: true},
		{arg: "../local", wantLocal: true},
		{arg: "/abs/path", wantLocal: true},
		{arg: "https://github.com/a/b.git", wantURL: "https://github.com/a/b.git"},
		{arg: "https://github.com/a/b.git#v1", wantURL: "https://github.com/a/b.git", wantRef: "v1"},
		{arg: "git@github.com:a/b.git", wantURL: "git@github.com:a/b.git"},
		{arg: "bare-name", wantErrSub: "does not look like a git URL"},
		{arg: "", wantErrSub: "empty source"},
	}
	for _, c := range cases {
		t.Run(c.arg, func(t *testing.T) {
			s, err := parseSource(c.arg)
			if c.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrSub) {
					t.Fatalf("parseSource(%q) error = %v, want substring %q", c.arg, err, c.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSource(%q) error = %v", c.arg, err)
			}
			if s.isLocal != c.wantLocal {
				t.Errorf("isLocal = %v, want %v", s.isLocal, c.wantLocal)
			}
			if s.url != c.wantURL {
				t.Errorf("url = %q, want %q", s.url, c.wantURL)
			}
			if s.ref != c.wantRef {
				t.Errorf("ref = %q, want %q", s.ref, c.wantRef)
			}
		})
	}
}

// TestParseSource_RejectsFlagSmuggling guards the argv-injection
// defence: a URL or ref beginning with `-` would, if it reached git's
// argv, be parsed as a flag — the `--upload-pack=<cmd>` variant on
// `git clone` is a documented RCE channel. parseSource rejects these
// before any exec call is constructed.
func TestParseSource_RejectsFlagSmuggling(t *testing.T) {
	cases := []string{
		"-evil",
		"--upload-pack=evil",
		"https://example.com/x.git#-evil",
		"https://example.com/x.git#--upload-pack=evil",
	}
	for _, arg := range cases {
		if _, err := parseSource(arg); err == nil {
			t.Errorf("parseSource(%q) accepted flag-shaped input", arg)
		}
	}
}

// TestSanitizeName guards the metadata-file naming invariants: no
// path separators, no leading dot, non-empty.
func TestSanitizeName(t *testing.T) {
	good := []string{"foo", "foo-bar", "foo_bar", "foo.bar"}
	for _, n := range good {
		if _, err := sanitizeName(n); err != nil {
			t.Errorf("sanitizeName(%q) error = %v, want nil", n, err)
		}
	}
	bad := []string{"", " ", ".", "..", ".hidden", "a/b", `a\b`}
	for _, n := range bad {
		if _, err := sanitizeName(n); err == nil {
			t.Errorf("sanitizeName(%q) error = nil, want error", n)
		}
	}
}

// TestShortHash trims the canonical sha256 prefix output to twelve
// hex chars — the form CLI listings render.
func TestShortHash(t *testing.T) {
	full := "sha256:" + strings.Repeat("a", 64)
	if got := shortHash(full); got != "sha256:aaaaaaaaaaaa" {
		t.Errorf("shortHash = %q", got)
	}
	if got := shortHash("not-prefixed"); got != "not-prefixed" {
		t.Errorf("shortHash unprefixed = %q", got)
	}
}

// TestRun_Add_Update_List_Remove drives the CLI surface end-to-end
// against a local bare-repo remote. It is the canonical "the verbs
// hold together" test: each verb is exercised against the same home
// and the final state matches expectations.
func TestRun_Add_Update_List_Remove(t *testing.T) {
	requireGit(t)
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	bare := initRemote(t, map[string]string{
		"pack.yaml":              "name: cli-demo\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
	})

	ctx := context.Background()
	if err := Run(ctx, &strings.Builder{}, home, []string{"add", fileURL(bare)}); err != nil {
		t.Fatalf("Run add: %v", err)
	}

	var listOut strings.Builder
	if err := Run(ctx, &listOut, home, []string{"list"}); err != nil {
		t.Fatalf("Run list: %v", err)
	}
	if !strings.Contains(listOut.String(), "cli-demo") {
		t.Fatalf("list output missing pack name: %q", listOut.String())
	}

	if err := Run(ctx, &strings.Builder{}, home, []string{"update", "--all"}); err != nil {
		t.Fatalf("Run update --all: %v", err)
	}

	if err := Run(ctx, &strings.Builder{}, home, []string{"remove", "cli-demo"}); err != nil {
		t.Fatalf("Run remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "packs", "cli-demo")); !os.IsNotExist(err) {
		t.Fatalf("after Run remove, pack dir still exists: %v", err)
	}
}
