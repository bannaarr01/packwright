// Package config owns Packwright's on-disk user state: the home directory
// layout (per ADR-0010) and the YAML-encoded config.yaml that lives at its
// root. It exposes a small, side-effecting API — Home, ConfigPath, Load,
// Save — that both the TUI and GUI front-ends share so they observe the
// same state across surfaces.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// envPackwrightHome is the top-priority override; when set, its value is the
// Packwright home directory verbatim, regardless of OS.
const envPackwrightHome = "PACKWRIGHT_HOME"

// envXDGConfigHome is consulted on Linux only, per ADR-0010 and the XDG Base
// Directory specification.
const envXDGConfigHome = "XDG_CONFIG_HOME"

// envHome is the POSIX home directory; used on macOS and Linux as the
// fallback root when no override is set.
const envHome = "HOME"

// envAppData is Windows' per-user application-data directory; used as the
// fallback root on Windows.
const envAppData = "APPDATA"

// subdirs are the directories created beneath the Packwright home on first
// use, mirroring the layout documented in ADR-0010.
var subdirs = []string{"packs", "commands", "monitors", "cache", "logs"}

// Home returns the Packwright home directory, creating it (and the standard
// subdirectory tree: packs/, commands/, monitors/, cache/, logs/) if it does
// not yet exist. It is idempotent — repeated calls re-confirm the tree but
// otherwise do no work.
//
// Resolution order (per ADR-0010):
//  1. $PACKWRIGHT_HOME, when set.
//  2. $XDG_CONFIG_HOME/packwright, on Linux only.
//  3. $HOME/.config/packwright, on macOS and Linux.
//  4. %APPDATA%\Packwright, on Windows.
//
// Home returns an error when no source for the home directory is available
// (for example, neither $HOME nor $PACKWRIGHT_HOME is set on macOS/Linux) or
// when the directory tree cannot be created.
func Home() (string, error) {
	root, err := resolveHome(os.Getenv, runtime.GOOS)
	if err != nil {
		return "", err
	}
	if err := ensureTree(root); err != nil {
		return "", err
	}
	return root, nil
}

// Path returns the absolute path to config.yaml inside the Packwright home
// directory. It calls Home, so the directory tree is created as a side
// effect.
func Path() (string, error) {
	root, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "config.yaml"), nil
}

// resolveHome computes the Packwright home directory from the given
// environment lookup and target OS, without touching the filesystem. It is
// the pure core of Home and is exercised directly by table-driven tests so
// path resolution can be verified for every OS without build tags or
// process-wide environment mutation.
func resolveHome(getenv func(string) string, goos string) (string, error) {
	if v := getenv(envPackwrightHome); v != "" {
		return v, nil
	}
	if goos == "windows" {
		appdata := getenv(envAppData)
		if appdata == "" {
			return "", errors.New("config: cannot determine home directory: PACKWRIGHT_HOME and APPDATA are both unset")
		}
		return filepath.Join(appdata, "Packwright"), nil
	}
	// macOS and other Unix-likes ignore XDG (per ADR-0010); Linux honours it.
	if goos == "linux" {
		if v := getenv(envXDGConfigHome); v != "" {
			return filepath.Join(v, "packwright"), nil
		}
	}
	home := getenv(envHome)
	if home == "" {
		return "", fmt.Errorf("config: cannot determine home directory on %s: PACKWRIGHT_HOME and HOME are both unset", goos)
	}
	return filepath.Join(home, ".config", "packwright"), nil
}

// ensureTree creates every entry in subdirs beneath root. MkdirAll creates
// intermediate parents, so root itself is materialized by the first
// iteration; explicitly MkdirAlling root would be redundant. MkdirAll is a
// no-op when the directory already exists, so this is safe to call on every
// Home invocation.
func ensureTree(root string) error {
	for _, sub := range subdirs {
		p := filepath.Join(root, sub)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("config: create %q: %w", p, err)
		}
	}
	return nil
}
