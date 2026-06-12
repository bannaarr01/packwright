package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/bannaarr01/packwright/internal/pack"
)

// gitBinary is resolved lazily on first use so a missing git installation
// produces a clear error at call-time rather than at package init. The
// indirection is also a hook tests use to override the binary (e.g. point
// at a stub) without environment-variable plumbing.
var gitBinary = "git"

// gitTimeout bounds every git invocation. Cloning a slow remote or pulling
// a large pack should not hang the TUI indefinitely; ten minutes is the
// threshold ADR-0027 names as "we'd rather fail loud than block forever".
const gitTimeout = 10 * time.Minute

// Add installs a pack from src — either a git URL (optionally pinned via
// `#<ref>`) or a local filesystem path. The flow is:
//
//  1. Resolve the source and derive the destination name.
//  2. For git URLs: `git clone` into <homeDir>/packs/<name> and check out
//     the optional ref. For local paths: hand off to addLocal (symlink on
//     POSIX, copy on Windows).
//  3. If pack.yaml carries a `name:` that differs from the URL-derived
//     name, rename the directory to match — ADR-0027's "the `name` field
//     in `pack.yaml` if cloned successfully" rule.
//  4. Compute pack.Hash and pack.Scan the surface.
//  5. Call pack.RequestConsent. On Denied, remove the directory and
//     return ErrDenied. On Trusted, persist Installed metadata.
//
// Network access happens only via the git invocation; everything else is
// pure filesystem work.
func Add(ctx context.Context, homeDir, src string) (*Installed, error) {
	if homeDir == "" {
		return nil, errors.New("install: add: empty homeDir")
	}
	source, err := parseSource(src)
	if err != nil {
		return nil, err
	}
	if source.isLocal {
		return addLocal(homeDir, source)
	}
	return addGit(ctx, homeDir, source)
}

// addGit performs the git-clone path of Add. It is split out so the
// public Add entry point can dispatch cleanly between the local and git
// cases without one branch's setup work polluting the other.
func addGit(ctx context.Context, homeDir string, src source) (*Installed, error) {
	name, err := sanitizeName(derivedName(src.url))
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(homeDir, "packs", name)

	if err := assertNotExists(dest); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("install: ensure packs dir: %w", err)
	}

	if err := gitClone(ctx, src.url, dest); err != nil {
		_ = os.RemoveAll(dest)
		return nil, err
	}
	if src.ref != "" {
		if err := gitCheckout(ctx, dest, src.ref); err != nil {
			_ = os.RemoveAll(dest)
			return nil, err
		}
	}

	// pack.yaml may carry a canonical name that differs from the URL-
	// derived stem. Rename the directory if so, then proceed with the
	// canonical name for everything downstream.
	canonical, err := canonicalNameFromPack(dest)
	if err != nil {
		_ = os.RemoveAll(dest)
		return nil, err
	}
	if canonical != "" && canonical != name {
		newDest := filepath.Join(homeDir, "packs", canonical)
		if err := assertNotExists(newDest); err != nil {
			_ = os.RemoveAll(dest)
			return nil, err
		}
		if err := os.Rename(dest, newDest); err != nil {
			_ = os.RemoveAll(dest)
			return nil, fmt.Errorf("install: rename %q -> %q: %w", dest, newDest, err)
		}
		name = canonical
		dest = newDest
	}

	hash, surface, err := scanAndHash(dest)
	if err != nil {
		_ = os.RemoveAll(dest)
		return nil, err
	}

	if pack.RequestConsent(surface, "") != pack.Trusted {
		_ = os.RemoveAll(dest)
		return nil, ErrDenied
	}

	meta := &Installed{
		Name:        name,
		URL:         src.url,
		Ref:         src.ref,
		TrustedHash: hash,
		Surface:     surface,
		InstalledAt: time.Now().UTC(),
	}
	if err := writeMeta(homeDir, meta); err != nil {
		_ = os.RemoveAll(dest)
		return nil, err
	}
	return meta, nil
}

// Update pulls the latest commits for a previously installed pack and
// re-runs the consent prompt only if the executable surface changed.
// Local-path installs (Installed.Local == true) cannot be pulled; the
// pack's working tree is whatever the symlink target currently holds,
// so Update on a local pack simply re-hashes and re-checks consent for
// any surface change. README-only edits in a local pack bypass the
// prompt the same way they do for git packs.
//
// On a denied consent change for a git pack, Update resets the working
// tree to the pre-pull commit so the user is not left in an
// intermediate state.
func Update(ctx context.Context, homeDir, name string) (*Installed, error) {
	if name == "" {
		return nil, errors.New("install: update: empty name")
	}
	prev, err := readMeta(homeDir, name)
	if err != nil {
		return nil, err
	}
	dest := prev.Dir(homeDir)
	if _, err := os.Stat(dest); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotInstalled
		}
		return nil, fmt.Errorf("install: stat %q: %w", dest, err)
	}

	var oldHead string
	if !prev.Local {
		head, err := gitRevParse(ctx, dest, "HEAD")
		if err != nil {
			return nil, err
		}
		oldHead = head
		if err := gitPull(ctx, dest); err != nil {
			return nil, err
		}
	}

	newHash, newSurface, err := scanAndHash(dest)
	if err != nil {
		// Roll back on a scan error so the user is not stranded at a
		// state we cannot evaluate.
		if !prev.Local && oldHead != "" {
			_ = gitResetHard(ctx, dest, oldHead)
		}
		return nil, err
	}

	if newHash == prev.TrustedHash {
		// Pull was a no-op or only moved metadata git itself maintains
		// — the working tree is byte-identical. Nothing to persist.
		return prev, nil
	}

	if reflect.DeepEqual(newSurface, prev.Surface) {
		// Content changed (e.g. README), surface did not — silently
		// refresh the trusted hash without prompting the user.
		updated := *prev
		updated.TrustedHash = newHash
		updated.Surface = newSurface
		updated.UpdatedAt = time.Now().UTC()
		if err := writeMeta(homeDir, &updated); err != nil {
			return nil, err
		}
		return &updated, nil
	}

	if pack.RequestConsent(newSurface, prev.TrustedHash) != pack.Trusted {
		if !prev.Local && oldHead != "" {
			if rerr := gitResetHard(ctx, dest, oldHead); rerr != nil {
				return nil, fmt.Errorf("%w (and rollback failed: %v)", ErrDenied, rerr)
			}
		}
		return nil, ErrDenied
	}

	updated := *prev
	updated.TrustedHash = newHash
	updated.Surface = newSurface
	updated.UpdatedAt = time.Now().UTC()
	if err := writeMeta(homeDir, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// scanAndHash is the (Scan, Hash) pair every install code path needs;
// it exists to keep the surface unchanged across the two calls — Scan
// and Hash both walk the same tree, so co-locating them documents the
// invariant.
//
// pack.Hash walks with filepath.WalkDir, which does not descend
// through a symlink at the root. Local installs land as symlinks
// under <home>/packs/<name>, so we resolve the path with
// filepath.EvalSymlinks before hashing — otherwise local packs would
// always hash to the empty digest. Resolution is a no-op on the git
// path (those installs are real directories), so the same call site
// works for both modes.
func scanAndHash(dir string) (string, pack.Surface, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", pack.Surface{}, fmt.Errorf("install: resolve %q: %w", dir, err)
	}
	hash, err := pack.Hash(real)
	if err != nil {
		return "", pack.Surface{}, err
	}
	surface, err := pack.Scan(real)
	if err != nil {
		return "", pack.Surface{}, err
	}
	return hash, surface, nil
}

// assertNotExists returns ErrAlreadyInstalled if dest already exists. It
// is used before clone/symlink to fail loudly rather than silently
// merge into an existing tree (which would otherwise be caught by
// `git clone`'s own "destination not empty" check but with a less
// useful error).
func assertNotExists(dest string) error {
	_, err := os.Lstat(dest)
	if err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyInstalled, dest)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("install: stat %q: %w", dest, err)
	}
	return nil
}

// gitClone runs `git clone <url> <dest>`. Stderr is captured so the
// returned error carries the underlying git diagnostic — opaque
// "exit status 128" wrapping is useless when the real problem is "ssh:
// permission denied".
//
// `--end-of-options` (git ≥ 2.24) terminates option parsing without
// the side effect `--` has on `git checkout` of forcing pathspec
// mode. This is the documented mitigation for the argv-smuggling
// attack where a hostile URL begins with `-` and would otherwise be
// parsed as a flag (e.g. `--upload-pack=<cmd>`); parseSource also
// rejects such URLs as defence in depth.
func gitClone(ctx context.Context, url, dest string) error {
	if _, err := exec.LookPath(gitBinary); err != nil {
		return fmt.Errorf("install: git binary not found in PATH: %w", err)
	}
	return runGit(ctx, "", "clone", "--end-of-options", url, dest)
}

// gitCheckout runs `git -C <dir> checkout <ref>`. The ref may be a tag,
// branch, or commit hash — `git checkout` figures it out.
//
// `--end-of-options` is the safe way to terminate flag parsing here.
// The bare `--` separator that works for most git subcommands forces
// `git checkout` into pathspec mode, which would reject a ref that
// has no matching file; `--end-of-options` (git ≥ 2.24) terminates
// option parsing without that side effect. parseSource additionally
// rejects refs starting with `-` so a regression in either layer
// alone leaves no exploitable surface.
func gitCheckout(ctx context.Context, dir, ref string) error {
	return runGit(ctx, dir, "checkout", "--end-of-options", ref)
}

// gitPull runs `git -C <dir> pull --ff-only`. --ff-only is deliberate:
// a non-fast-forward update means upstream history was rewritten,
// which we treat as a manual-intervention scenario rather than
// silently merging.
func gitPull(ctx context.Context, dir string) error {
	return runGit(ctx, dir, "pull", "--ff-only")
}

// gitRevParse returns `git -C <dir> rev-parse <ref>` as a trimmed
// string. Used by Update to remember the pre-pull commit so a denied
// consent can roll back.
func gitRevParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := captureGit(ctx, dir, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitResetHard runs `git -C <dir> reset --hard <ref>`. Used by Update
// to revert a pull whose new surface the user refused to consent to.
func gitResetHard(ctx context.Context, dir, ref string) error {
	return runGit(ctx, dir, "reset", "--hard", ref)
}

// runGit is the single point through which all git invocations flow.
// It applies the package-level timeout, sets a sane environment (no
// localisation, no terminal prompts), and surfaces stderr in the
// returned error.
func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := captureGit(ctx, dir, args...)
	return err
}

// captureGit is runGit's underlying primitive; it returns stdout for
// callers that need to parse it (e.g. gitRevParse).
func captureGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, gitBinary, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// LC_ALL=C keeps error messages in English so callers can match on
	// them deterministically. GIT_TERMINAL_PROMPT=0 means a request for
	// credentials fails instead of blocking on stdin — the user runs
	// `git clone` themselves once to cache creds, then Packwright
	// reuses them.
	cmd.Env = append(os.Environ(),
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("install: git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// canonicalNameFromPack returns the `name:` field from <dir>/pack.yaml,
// validated via sanitizeName. A pack without a pack.yaml (allowed
// during early-stage development) or without a `name:` field returns
// ("", nil) so the URL-derived name stays as the directory.
//
// Reading pack.yaml here would normally pull in the pack package's
// strict loader, but doing so creates a circular dependency
// (internal/pack/install -> pack -> internal/pack). The shape we need
// is two trivial fields; we read them with a hand-rolled regex-free
// scan instead of importing the YAML decoder.
func canonicalNameFromPack(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("install: read pack.yaml: %w", err)
	}
	name := scanTopLevelString(data, "name")
	if name == "" {
		return "", nil
	}
	return sanitizeName(name)
}

// scanTopLevelString returns the value of a top-level `<key>: <value>`
// line in a YAML document. It deliberately ignores quoted values,
// flow-mapping syntax, and nested keys — the only inputs it sees are
// pack-author-written pack.yaml files whose `name:` field is a bare
// identifier per the project convention. A more permissive parser
// would have to import yaml.v3 here, and the circular-dependency note
// in canonicalNameFromPack rules that out.
func scanTopLevelString(data []byte, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip comments and empty lines.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Only top-level keys: the line itself must start with the key
		// (no leading whitespace) so we ignore nested entries with the
		// same suffix.
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		// Strip a possible trailing comment.
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		// Strip surrounding quotes for the common cases.
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		return value
	}
	return ""
}
