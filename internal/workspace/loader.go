package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Layout constants for the on-disk project tree. They are exported so the
// scaffolders in later PRs (and the slash-command layer here) can build
// paths without re-deriving the convention.
const (
	// ProjectsSubdir is the home-relative top-level directory that owns all
	// projects. Mirrored in config/paths.go as an additive entry in subdirs.
	ProjectsSubdir = "projects"
	// ProjectFile is the per-project metadata file under
	// <Home>/projects/<slug>/.
	ProjectFile = "project.yaml"
	// EnvFile is the per-env metadata file under
	// <Home>/projects/<project>/<env>/.
	EnvFile = "env.yaml"
	// ManifestsSubdir is the per-env folder that holds project-scoped
	// manifests. Created lazily by CreateEnv so a fresh env tree is
	// drop-target ready without an explicit mkdir from the caller.
	ManifestsSubdir = "manifests"
	// DraftsSubdir holds ADR-0047 draft manifests inside an env.
	DraftsSubdir = "drafts"
	// StacksSubdir holds ADR-0046 stack records inside an env.
	StacksSubdir = "stacks"
)

// Common errors returned by CreateProject / CreateEnv. They are wrapped with
// the offending slug for the user-visible message and matched by callers via
// errors.Is.
var (
	// ErrProjectExists is returned when CreateProject is asked to materialize
	// a slug that already exists on disk (case-insensitive).
	ErrProjectExists = errors.New("workspace: project already exists")
	// ErrProjectMissing is returned by CreateEnv when the target project
	// directory does not exist.
	ErrProjectMissing = errors.New("workspace: project does not exist")
	// ErrEnvExists is returned when CreateEnv is asked to materialize an env
	// slug that already exists within its parent project (case-insensitive).
	ErrEnvExists = errors.New("workspace: env already exists in project")
)

// ProjectsRoot returns <home>/projects. It does not touch the filesystem.
func ProjectsRoot(home string) string {
	return filepath.Join(home, ProjectsSubdir)
}

// ProjectDir returns <home>/projects/<slug>.
func ProjectDir(home, slug string) string {
	return filepath.Join(ProjectsRoot(home), slug)
}

// EnvDir returns <home>/projects/<project>/<env>.
func EnvDir(home, project, env string) string {
	return filepath.Join(ProjectDir(home, project), env)
}

// LoadAll reads every project + env from <home>/projects. The returned list
// is sorted by Slug and each project's Envs slice is sorted by Slug too. A
// missing projects/ directory is not an error — LoadAll returns an empty
// slice in that case so a fresh install is still usable.
//
// Entries whose directory name fails ValidateSlug are skipped silently;
// other recoverable problems (malformed YAML, missing project.yaml when a
// directory exists, invalid YAML slug) are collected into the second return
// value so callers can log them. The first return value reflects the
// successful load.
func LoadAll(home string) ([]Project, []error) {
	root := ProjectsRoot(home)
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("workspace: read %q: %w", root, err)}
	}

	var (
		projects []Project
		warnings []error
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		if err := ValidateSlug(slug); err != nil {
			warnings = append(warnings, fmt.Errorf("workspace: skipping project dir %q: %w", slug, err))
			continue
		}
		p, err := loadProject(home, slug)
		if err != nil {
			warnings = append(warnings, err)
			continue
		}
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Slug < projects[j].Slug })
	return projects, warnings
}

// loadProject reads <home>/projects/<slug>/project.yaml plus every env
// underneath it. The directory's basename is the authority for the slug;
// the YAML's slug field, if present, is overwritten with that name so a
// rename on disk is always observed.
func loadProject(home, slug string) (Project, error) {
	projDir := ProjectDir(home, slug)
	projPath := filepath.Join(projDir, ProjectFile)

	p := Project{Slug: slug}
	data, err := os.ReadFile(projPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A bare directory under projects/ with no project.yaml is
			// treated as a stub: keep the slug, no metadata.
		} else {
			return Project{}, fmt.Errorf("workspace: read %q: %w", projPath, err)
		}
	} else {
		if err := yaml.Unmarshal(data, &p); err != nil {
			return Project{}, fmt.Errorf("workspace: parse %q: %w", projPath, err)
		}
		// Re-anchor the slug to the directory name regardless of what the
		// YAML claimed, then validate it once more so a hand-edited file
		// never smuggles in an invalid slug.
		p.Slug = slug
		if err := ValidateSlug(p.Slug); err != nil {
			return Project{}, fmt.Errorf("workspace: project %q: %w", projPath, err)
		}
	}

	envs, envWarnings := loadEnvs(home, slug)
	p.Envs = envs
	if len(envWarnings) > 0 {
		// Collapse env warnings into a single error string so the per-project
		// failure remains in the project's own loadProject scope. Callers
		// surface this through LoadAll's []error.
		return p, fmt.Errorf("workspace: project %q: %v", slug, envWarnings)
	}
	return p, nil
}

// loadEnvs walks the project directory looking for <env>/env.yaml children.
// A subdir without env.yaml is silently skipped (it might be an artefact
// from a half-finished migration); invalid slug names are reported as
// warnings.
func loadEnvs(home, projectSlug string) ([]Env, []error) {
	projDir := ProjectDir(home, projectSlug)
	entries, err := os.ReadDir(projDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("workspace: read %q: %w", projDir, err)}
	}

	var (
		envs     []Env
		warnings []error
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		if err := ValidateSlug(slug); err != nil {
			warnings = append(warnings, fmt.Errorf("env dir %q: %w", slug, err))
			continue
		}
		envPath := filepath.Join(projDir, slug, EnvFile)
		data, err := os.ReadFile(envPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Probably manifests/ or drafts/ or stacks/ — not an env.
				continue
			}
			warnings = append(warnings, fmt.Errorf("read %q: %w", envPath, err))
			continue
		}
		env := Env{Slug: slug}
		if err := yaml.Unmarshal(data, &env); err != nil {
			warnings = append(warnings, fmt.Errorf("parse %q: %w", envPath, err))
			continue
		}
		env.Slug = slug
		if err := ValidateSlug(env.Slug); err != nil {
			warnings = append(warnings, fmt.Errorf("env %q: %w", envPath, err))
			continue
		}
		envs = append(envs, env)
	}
	sort.Slice(envs, func(i, j int) bool { return envs[i].Slug < envs[j].Slug })
	return envs, warnings
}

// CreateProject materializes p on disk and returns the canonicalized
// project with its slug lowercased. The caller must have set p.Slug and
// p.Name; other fields are optional. The slug is normalized first so a
// /new-project "Acme" call writes <home>/projects/acme/.
//
// On a duplicate-slug collision (case-insensitive against the directory
// listing under <home>/projects) CreateProject returns ErrProjectExists
// without writing anything. The YAML is written through writeAtomic so a
// crash mid-write leaves either the old file intact or the new one fully
// written — never a half-written project.
func CreateProject(home string, p Project) (Project, error) {
	p.Slug = NormalizeSlug(p.Slug)
	if err := ValidateSlug(p.Slug); err != nil {
		return Project{}, err
	}
	existing, _ := listProjectSlugs(home)
	if SlugExists(existing, p.Slug) {
		return Project{}, fmt.Errorf("%w: %q", ErrProjectExists, p.Slug)
	}
	dir := ProjectDir(home, p.Slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Project{}, fmt.Errorf("workspace: mkdir %q: %w", dir, err)
	}
	// Marshal a copy so we never persist a non-normalized slug even if the
	// caller mutates p after the call.
	out := p
	if out.Envs == nil {
		out.Envs = []Env{}
	}
	data, err := yaml.Marshal(&out)
	if err != nil {
		return Project{}, fmt.Errorf("workspace: marshal project %q: %w", p.Slug, err)
	}
	if err := writeAtomic(filepath.Join(dir, ProjectFile), data); err != nil {
		return Project{}, err
	}
	return out, nil
}

// CreateEnv materializes e under projectSlug on disk and returns the
// canonicalized env. Both slugs are normalized and validated, the parent
// project must already exist (CreateProject is not called transitively),
// and an existing env with the same slug under projectSlug is rejected
// with ErrEnvExists. The env directory is initialized with the standard
// subtree (manifests/, drafts/, stacks/) so future writes have a place to
// land without a separate mkdir round-trip.
func CreateEnv(home, projectSlug string, e Env) (Env, error) {
	projectSlug = NormalizeSlug(projectSlug)
	if err := ValidateSlug(projectSlug); err != nil {
		return Env{}, err
	}
	e.Slug = NormalizeSlug(e.Slug)
	if err := ValidateSlug(e.Slug); err != nil {
		return Env{}, err
	}

	projDir := ProjectDir(home, projectSlug)
	info, err := os.Stat(projDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Env{}, fmt.Errorf("%w: %q", ErrProjectMissing, projectSlug)
		}
		return Env{}, fmt.Errorf("workspace: stat %q: %w", projDir, err)
	}
	if !info.IsDir() {
		return Env{}, fmt.Errorf("workspace: %q is not a directory", projDir)
	}

	existing, _ := listEnvSlugs(home, projectSlug)
	if SlugExists(existing, e.Slug) {
		return Env{}, fmt.Errorf("%w: %q", ErrEnvExists, e.Slug)
	}

	envDir := EnvDir(home, projectSlug, e.Slug)
	for _, sub := range []string{"", ManifestsSubdir, DraftsSubdir, StacksSubdir} {
		p := filepath.Join(envDir, sub)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return Env{}, fmt.Errorf("workspace: mkdir %q: %w", p, err)
		}
	}

	out := e
	data, err := yaml.Marshal(&out)
	if err != nil {
		return Env{}, fmt.Errorf("workspace: marshal env %q: %w", e.Slug, err)
	}
	if err := writeAtomic(filepath.Join(envDir, EnvFile), data); err != nil {
		return Env{}, err
	}
	return out, nil
}

// listProjectSlugs returns the directory basenames under <home>/projects.
// Used by CreateProject for the collision check; surfacing it as a helper
// keeps the duplicate-detection logic close to the writer so the two stay
// consistent.
func listProjectSlugs(home string) ([]string, error) {
	root := ProjectsRoot(home)
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// listEnvSlugs returns the directory basenames under
// <home>/projects/<projectSlug> that have an env.yaml inside them. A bare
// subdir without env.yaml (e.g. a leftover manifests/) is ignored so it
// cannot collide with a real env slug.
func listEnvSlugs(home, projectSlug string) ([]string, error) {
	projDir := ProjectDir(home, projectSlug)
	entries, err := os.ReadDir(projDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(projDir, e.Name(), EnvFile)); err == nil {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// writeAtomic writes data to a sibling temp file, fsyncs, and renames over
// dest with mode 0o644. The temp file shares dest's directory so the rename
// stays on the same filesystem (atomic on POSIX; on Windows os.Rename
// replaces the target since Go 1.5).
//
// On any error the temp file is removed via a deferred cleanup. The defer
// also closes the file handle first — on Windows os.Remove fails while a
// handle is open, so closing before removing is mandatory there even when
// it looks redundant on POSIX. Mirrors config.writeAtomic by design; kept
// separate so the workspace package has no config dependency.
func writeAtomic(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	f, err := os.CreateTemp(dir, filepath.Base(dest)+".*.tmp")
	if err != nil {
		return fmt.Errorf("workspace: create temp in %q: %w", dir, err)
	}
	tmp := f.Name()
	success := false
	defer func() {
		_ = f.Close()
		if !success {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("workspace: write temp %q: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("workspace: fsync temp %q: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("workspace: close temp %q: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return fmt.Errorf("workspace: chmod temp %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("workspace: rename %q to %q: %w", tmp, dest, err)
	}
	success = true
	return nil
}
