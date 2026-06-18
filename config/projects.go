package config

import (
	"errors"
	"fmt"

	"github.com/bannaarr01/packwright/internal/workspace"
)

// ErrUnknownProject is returned by SetActive when the project slug is not
// present in c.Projects. Callers may match it with errors.Is.
var ErrUnknownProject = errors.New("config: unknown project")

// ErrUnknownEnv is returned by SetActive when the env slug is not present
// in the named project. Callers may match it with errors.Is.
var ErrUnknownEnv = errors.New("config: unknown env in project")

// Project returns a pointer to the entry in c.Projects matching slug
// case-insensitively, or nil. The pointer is into the underlying slice so
// the caller can mutate fields and persist with c.Save().
func (c *Config) Project(slug string) *workspace.Project {
	if c == nil {
		return nil
	}
	return workspace.FindProject(c.Projects, slug)
}

// HasProject reports whether c.Projects contains slug case-insensitively.
func (c *Config) HasProject(slug string) bool {
	return c.Project(slug) != nil
}

// AddProject appends p to c.Projects after slug validation and a
// case-insensitive duplicate check. It is the in-memory counterpart to
// workspace.CreateProject: callers materialize the project on disk first,
// then mirror it into the config so a single Save() call persists the
// active-selection metadata. The slug is normalized in place.
func (c *Config) AddProject(p workspace.Project) error {
	if c == nil {
		return errors.New("config: nil receiver in AddProject")
	}
	p.Slug = workspace.NormalizeSlug(p.Slug)
	if err := workspace.ValidateSlug(p.Slug); err != nil {
		return err
	}
	if c.HasProject(p.Slug) {
		return fmt.Errorf("%w: %q", workspace.ErrProjectExists, p.Slug)
	}
	c.Projects = append(c.Projects, p)
	return nil
}

// AddEnv appends e to the project in c.Projects matching projectSlug. It
// errors with ErrUnknownProject if there is no such project and with
// workspace.ErrEnvExists if the env slug already exists case-insensitively
// inside that project. Slugs are normalized in place.
func (c *Config) AddEnv(projectSlug string, e workspace.Env) error {
	if c == nil {
		return errors.New("config: nil receiver in AddEnv")
	}
	p := c.Project(projectSlug)
	if p == nil {
		return fmt.Errorf("%w: %q", ErrUnknownProject, projectSlug)
	}
	e.Slug = workspace.NormalizeSlug(e.Slug)
	if err := workspace.ValidateSlug(e.Slug); err != nil {
		return err
	}
	if p.FindEnv(e.Slug) != nil {
		return fmt.Errorf("%w: %q", workspace.ErrEnvExists, e.Slug)
	}
	p.Envs = append(p.Envs, e)
	return nil
}

// SetActive records projectSlug / envSlug as the active selection after
// validating that both refer to existing entries in c.Projects. An empty
// projectSlug clears the active selection (envSlug must also be empty).
// Slugs are normalized before comparison; callers may pass user-typed
// values directly.
func (c *Config) SetActive(projectSlug, envSlug string) error {
	if c == nil {
		return errors.New("config: nil receiver in SetActive")
	}
	if projectSlug == "" {
		if envSlug != "" {
			return errors.New("config: SetActive with empty project but non-empty env")
		}
		c.ActiveProject, c.ActiveEnv = "", ""
		return nil
	}
	projectSlug = workspace.NormalizeSlug(projectSlug)
	if err := workspace.ValidateSlug(projectSlug); err != nil {
		return err
	}
	p := c.Project(projectSlug)
	if p == nil {
		return fmt.Errorf("%w: %q", ErrUnknownProject, projectSlug)
	}
	if envSlug == "" {
		c.ActiveProject, c.ActiveEnv = p.Slug, ""
		return nil
	}
	envSlug = workspace.NormalizeSlug(envSlug)
	if err := workspace.ValidateSlug(envSlug); err != nil {
		return err
	}
	if p.FindEnv(envSlug) == nil {
		return fmt.Errorf("%w: %q in project %q", ErrUnknownEnv, envSlug, p.Slug)
	}
	c.ActiveProject, c.ActiveEnv = p.Slug, envSlug
	return nil
}

// Reconcile rewrites c.Projects from <home>/projects so disk is always the
// authority for tree structure (per ADR-0045). The active-selection fields
// (ActiveProject / ActiveEnv) are corrected to point at entries that still
// exist, and any drift between the cached config.yaml mirror and the disk
// state is reported as a slice of human-readable warning strings the caller
// can log or surface to the user.
//
// Reconcile does not call c.Save() — the caller decides when to persist so a
// failed launch never overwrites a previously good config. Reconcile is safe
// to call on a fresh install (a missing projects/ directory produces no
// warnings and leaves an empty c.Projects).
func (c *Config) Reconcile(home string) ([]string, error) {
	if c == nil {
		return nil, errors.New("config: nil receiver in Reconcile")
	}
	disk, loadWarnings := workspace.LoadAll(home)

	warnings := make([]string, 0, len(loadWarnings))
	for _, w := range loadWarnings {
		warnings = append(warnings, w.Error())
	}

	// Compare slug sets so we can warn about drift before we overwrite
	// the in-memory mirror. Disk wins.
	cached := map[string]workspace.Project{}
	for _, p := range c.Projects {
		cached[workspace.NormalizeSlug(p.Slug)] = p
	}
	onDisk := map[string]struct{}{}
	for _, p := range disk {
		onDisk[workspace.NormalizeSlug(p.Slug)] = struct{}{}
	}
	for slug := range cached {
		if _, ok := onDisk[slug]; !ok {
			warnings = append(warnings, fmt.Sprintf("config: removing orphan project %q from config.yaml (not present on disk)", slug))
		}
	}
	for slug := range onDisk {
		if _, ok := cached[slug]; !ok {
			warnings = append(warnings, fmt.Sprintf("config: adding project %q from disk to config.yaml", slug))
		}
	}

	c.Projects = disk

	// Repair the active selection if it points to something gone.
	if c.ActiveProject != "" {
		p := c.Project(c.ActiveProject)
		if p == nil {
			warnings = append(warnings, fmt.Sprintf("config: clearing active project %q (no longer exists on disk)", c.ActiveProject))
			c.ActiveProject, c.ActiveEnv = "", ""
		} else if c.ActiveEnv != "" && p.FindEnv(c.ActiveEnv) == nil {
			warnings = append(warnings, fmt.Sprintf("config: clearing active env %q in project %q (no longer exists on disk)", c.ActiveEnv, p.Slug))
			c.ActiveEnv = ""
		}
	} else if c.ActiveEnv != "" {
		// Defence in depth: env without project is a malformed mirror.
		warnings = append(warnings, "config: clearing active env (no active project set)")
		c.ActiveEnv = ""
	}
	return warnings, nil
}
