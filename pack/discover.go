package pack

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/bannaarr01/packwright/manifest"
)

// packsSubdir is the directory under homeDir that holds installed packs.
const packsSubdir = "packs"

// commandsSubdir is the directory under homeDir that holds user-scoped
// custom commands. MVP-3 PR-01 owns its contents; MVP 1 only stubs it.
const commandsSubdir = "commands"

// Discover walks <homeDir>/packs/*/ and returns every loadable pack found,
// sorted by Name. Per-pack failures (missing or malformed pack.yaml, a
// manifest that fails to parse) are aggregated into the returned error via
// errors.Join while still allowing healthy packs to be reported.
//
// A missing <homeDir>/packs directory is not an error — fresh installs have
// no packs yet — and yields (nil, nil).
func Discover(homeDir string) ([]*Pack, error) {
	root := filepath.Join(homeDir, packsSubdir)

	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading packs directory %q: %w", root, err)
	}

	var (
		packs []*Pack
		errs  []error
	)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		p, err := loadPack(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		packs = append(packs, p)
	}

	sort.Slice(packs, func(i, j int) bool { return packs[i].Name < packs[j].Name })
	return packs, errors.Join(errs...)
}

// LoadUserScope returns the user-scope pack: a synthetic *Pack named
// UserScopeName whose Manifests are loaded from <homeDir>/commands/*.yaml and
// <homeDir>/monitors/*.yaml. The exported entry point lives here for
// continuity with MVP-1 PR-06's stub; the implementation is in user_scope.go
// alongside the Scope plumbing introduced by MVP-3 PR-01.
func LoadUserScope(homeDir string) (*Pack, error) {
	return loadUserScope(homeDir)
}

// loadPack reads pack.yaml and every manifests/*.yaml file beneath dir into a
// fully populated *Pack. Errors are wrapped with the offending file path so
// the discovery error chain points at the broken file without further
// inspection.
func loadPack(dir string) (*Pack, error) {
	meta, err := loadPackMeta(filepath.Join(dir, "pack.yaml"))
	if err != nil {
		return nil, err
	}

	manifests, err := loadManifests(filepath.Join(dir, "manifests"))
	if err != nil {
		return nil, err
	}

	return &Pack{
		Name:      meta.Name,
		Version:   meta.Version,
		Dir:       dir,
		Meta:      meta,
		Manifests: manifests,
	}, nil
}

// loadPackMeta strictly decodes pack.yaml. Unknown top-level keys cause an
// error so typos in author-edited files are reported at load time.
func loadPackMeta(path string) (PackMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PackMeta{}, fmt.Errorf("reading pack.yaml %q: %w", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var meta PackMeta
	if err := dec.Decode(&meta); err != nil {
		return PackMeta{}, fmt.Errorf("parsing pack.yaml %q: %w", path, err)
	}
	if meta.Name == "" {
		return PackMeta{}, fmt.Errorf("pack.yaml %q: missing required field \"name\"", path)
	}
	if meta.Version == "" {
		return PackMeta{}, fmt.Errorf("pack.yaml %q: missing required field \"version\"", path)
	}
	return meta, nil
}

// loadManifests parses every *.yaml file directly inside dir as a manifest.
// A missing manifests directory is not an error — packs may legitimately
// ship without one (e.g. a templates-only library pack).
func loadManifests(dir string) ([]*manifest.Manifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading manifests directory %q: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if ext := filepath.Ext(name); ext != ".yaml" && ext != ".yml" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	manifests := make([]*manifest.Manifest, 0, len(names))
	for _, name := range names {
		m, err := manifest.Load(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}
