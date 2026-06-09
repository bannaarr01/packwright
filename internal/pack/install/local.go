package install

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/bannaarr01/packwright/internal/pack"
)

// forceCopy is the test-overridable knob that decides whether
// linkOrCopy uses a symlink or a recursive copy. Production uses the
// runtime.GOOS check; tests flip this to exercise the Windows-style
// copy path on non-Windows CI runners.
var forceCopy = runtime.GOOS == "windows"

// addLocal installs a pack from a local filesystem path. The default
// posture is to symlink <homeDir>/packs/<name> at the absolute source
// path so authors can edit in-place and see changes live; on Windows
// (where unprivileged symlink creation is disabled by default) it
// falls back to a recursive copy.
//
// As with addGit, the consent prompt runs against the hashed/scanned
// installed pack. A denied consent removes the link (or the copied
// tree) and returns ErrDenied.
func addLocal(homeDir string, src source) (*Installed, error) {
	if src.path == "" {
		return nil, errors.New("install: addLocal: empty source path")
	}
	info, err := os.Stat(src.path)
	if err != nil {
		return nil, fmt.Errorf("install: stat %q: %w", src.path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("install: %q is not a directory", src.path)
	}

	// Derive the install name from pack.yaml when possible; otherwise
	// fall back to the basename. This matches the git path's "pack.yaml
	// wins" rule and keeps the directory under <homeDir>/packs/ stable
	// across `<NAME> packs add ../foo` invocations even if the user
	// later moves the source tree.
	name, err := canonicalNameFromPack(src.path)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name, err = sanitizeName(filepath.Base(src.path))
		if err != nil {
			return nil, err
		}
	}

	dest := filepath.Join(homeDir, "packs", name)
	if err := assertNotExists(dest); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("install: ensure packs dir: %w", err)
	}

	if err := linkOrCopy(src.path, dest); err != nil {
		return nil, err
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
		Local:       true,
		LocalSource: src.path,
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

// linkOrCopy creates a symbolic link at dest pointing at src; on
// Windows (or when tests flip forceCopy) it recursively copies the
// tree instead. The symlink form is the developer-ergonomics win — a
// pack author edits files in their working tree and Packwright sees
// the change on the next discovery — while the copy form keeps the
// feature usable on platforms where symlink creation is privileged.
func linkOrCopy(src, dest string) error {
	if forceCopy {
		return copyTree(src, dest)
	}
	if err := os.Symlink(src, dest); err != nil {
		// On a system where symlink creation is gated (e.g. Windows
		// without Developer Mode), fall back to a copy rather than
		// abort. This mirrors the rule "on Windows fall back to copy"
		// from the AI implementation prompt without requiring a build-
		// tag-driven file split.
		if isSymlinkPermissionError(err) {
			return copyTree(src, dest)
		}
		return fmt.Errorf("install: symlink %q -> %q: %w", src, dest, err)
	}
	return nil
}

// isSymlinkPermissionError reports whether err looks like the system's
// way of saying "you cannot create symlinks here". POSIX systems
// return EPERM/EACCES; Windows returns ERROR_PRIVILEGE_NOT_HELD via
// syscall.Errno(1314). We match on errors.Is(fs.ErrPermission) which
// covers both cases in current Go std-lib semantics.
func isSymlinkPermissionError(err error) bool {
	return errors.Is(err, fs.ErrPermission)
}

// copyTree recursively copies src to dest. It preserves mode bits but
// not ownership or extended attributes — Packwright never relies on
// either, and trying to preserve them across operating systems
// invites flake. Symlinks inside src are dereferenced (we copy the
// target's contents), matching `cp -L` semantics.
func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("install: copy: relpath %q: %w", path, err)
		}
		target := filepath.Join(dest, rel)

		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("install: copy: stat %q: %w", path, err)
			}
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("install: copy: mkdir %q: %w", target, err)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Skip devices, sockets, named pipes — they have no
			// meaning inside a pack tree. Symlinks inside src are
			// followed by os.Open below.
			return nil
		}
		return copyFile(path, target)
	})
}

// copyFile copies a single regular file from src to dest with the
// source's mode bits. Failures are wrapped with the offending path so
// the caller can find it without inspecting stack traces.
func copyFile(src, dest string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("install: copy: stat %q: %w", src, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("install: copy: open %q: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("install: copy: mkdir %q: %w", filepath.Dir(dest), err)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("install: copy: create %q: %w", dest, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("install: copy: write %q: %w", dest, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("install: copy: close %q: %w", dest, err)
	}
	return nil
}
