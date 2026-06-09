package pack

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// manifestsSubdir is the directory beneath a pack root where action
// manifests live (per ADR-0009). Scan reads exclusively from this
// subdirectory; pack.yaml, templates/, and README.md never declare a
// shell-execution surface.
const manifestsSubdir = "manifests"

// shellOutputPanelKind is the monitor panel kind that runs a shell command
// for its data source — the only panel kind that contributes to the
// executable surface per ADR-0025.
const shellOutputPanelKind = "shell/output"

// kindShell, kindComposite, kindMonitor are the manifest kinds Scan
// inspects. They are duplicated from internal/manifest's Kind constants
// rather than imported because Scan reads raw YAML — internal/manifest's
// strict decoder rejects the shell/composite/monitor-specific top-level
// keys those manifests carry (see scanManifest's doc comment).
const (
	kindShell     = "shell"
	kindComposite = "composite"
	kindMonitor   = "monitor"
)

// Scan walks <packDir>/manifests/*.yaml and returns the Surface — the
// ordered list of every shell-execution site declared by the pack.
//
// Scan is deliberately tolerant of YAML keys internal/manifest does not
// yet know about (run, steps, monitor). The strict loader is the right
// gate for runtime execution; for the trust prompt we need to see every
// shell call regardless of whether the rest of the manifest is currently
// runnable. Manifests whose YAML is structurally broken are skipped with
// no entries — the strict loader will surface that error at install time.
//
// The order of Commands is stable: manifests are visited in lexical
// order of their file names, and calls within a manifest follow the
// source order they appear in. Identical pack content always produces
// the same Surface, so consent-screen diffing across versions is
// byte-deterministic.
//
// A missing manifests/ directory is treated as an empty pack (no
// surface). Permission errors and other filesystem failures are
// returned to the caller.
func Scan(packDir string) (Surface, error) {
	manifestsDir := filepath.Join(packDir, manifestsSubdir)
	entries, err := os.ReadDir(manifestsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Surface{}, nil
		}
		return Surface{}, fmt.Errorf("pack: scan: read %q: %w", manifestsDir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch strings.ToLower(filepath.Ext(name)) {
		case ".yaml", ".yml":
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var commands []Command
	for _, name := range names {
		path := filepath.Join(manifestsDir, name)
		rel := filepath.ToSlash(filepath.Join(manifestsSubdir, name))

		data, err := os.ReadFile(path)
		if err != nil {
			return Surface{}, fmt.Errorf("pack: scan: read %q: %w", path, err)
		}

		var m scanManifest
		if err := yaml.Unmarshal(data, &m); err != nil {
			// Malformed YAML cannot contribute a surface entry. The strict
			// loader downstream will reject the manifest with a precise
			// diagnostic; surfacing the same error here would conflate the
			// trust step with manifest validation.
			continue
		}

		commands = append(commands, m.extract(rel)...)
	}

	return Surface{Commands: commands}, nil
}

// scanManifest is the permissive decoding shape used by Scan. It includes
// every field needed to identify a shell-execution surface across the
// shell, composite, and monitor kinds in a single pass — internal/manifest
// cannot fill this role because its strict decoder rejects unknown keys
// for the non-resource kinds (those kinds' schemas land alongside their
// own MVP-3 PRs).
//
// Unrecognised top-level keys decode silently into the catch-all map.
// Type mismatches inside a known key short-circuit that branch only;
// other branches continue to decode independently.
type scanManifest struct {
	Kind  string     `yaml:"kind"`
	Slash string     `yaml:"slash"`
	Run   *scanRun   `yaml:"run,omitempty"`
	Steps []scanStep `yaml:"steps,omitempty"`
	// Monitor mirrors the layout of internal/action/monitor.Spec: a
	// dashboard with a list of panels. We only care about panels whose
	// kind is "shell/output"; everything else is ignored.
	Monitor *scanMonitor `yaml:"monitor,omitempty"`
}

// scanRun captures a shell manifest's run: block. Command may be either
// a YAML string (single token) or a sequence; commandTokens normalises
// both forms into a string slice.
type scanRun struct {
	Command commandValue `yaml:"command"`
	Shell   string       `yaml:"shell,omitempty"`
}

// scanStep is one composite-manifest step. A step that targets another
// slash via run: contributes no surface entry — the referenced manifest
// owns its own surface and is scanned independently in this pass. A
// step that carries an inline shell: block contributes a single
// SourceCompositeStep entry so the consent screen can show inline
// shell calls embedded in workflows.
type scanStep struct {
	Run   string   `yaml:"run,omitempty"`
	Shell *scanRun `yaml:"shell,omitempty"`
	// The remaining composite-step keys (confirm, on_failure, inputs_from,
	// with) do not influence the surface; their decoding is intentionally
	// omitted so a typo in those keys does not silently shadow a shell
	// block. yaml.v3 ignores unknown keys by default.
}

// scanMonitor and scanPanel mirror the panel-list shape of a monitor
// manifest. Only panels of kind "shell/output" contribute to the
// surface; their command lives at spec.command (per ADR-0015 / the
// monitorx registry's shell-output panel).
type scanMonitor struct {
	Panels []scanPanel `yaml:"panels"`
}

type scanPanel struct {
	ID   string         `yaml:"id"`
	Kind string         `yaml:"kind"`
	Spec map[string]any `yaml:"spec"`
}

// commandValue is the decoded form of a manifest's command: field. YAML
// permits both a scalar (a single token) and a sequence (one entry per
// arg); the UnmarshalYAML method normalises both into a string slice so
// downstream code never branches on the YAML node kind.
type commandValue []string

// UnmarshalYAML accepts either a YAML scalar or a YAML sequence and
// populates the slice form. Other node kinds (mappings, aliases of
// non-string nodes) become an error so manifests that put a structured
// value where a command line belongs are not silently dropped.
func (c *commandValue) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		*c = commandValue{s}
		return nil
	case yaml.SequenceNode:
		var s []string
		if err := node.Decode(&s); err != nil {
			return err
		}
		*c = commandValue(s)
		return nil
	default:
		return fmt.Errorf("pack: command must be a string or a sequence of strings (got yaml kind %d)", node.Kind)
	}
}

// extract returns every shell-execution surface site declared by m. The
// relativePath argument is the pack-relative path of the manifest file
// the data came from; it is copied into each Command.Manifest field so
// the consent screen can group entries by source file.
//
// extract intentionally tolerates manifests that mix sections (e.g. a
// future kind that carries both a run: and steps:). Each section is
// inspected independently; a missing or malformed branch contributes
// nothing and does not block the rest.
func (m scanManifest) extract(relativePath string) []Command {
	var out []Command

	switch m.Kind {
	case kindShell:
		if m.Run != nil && len(m.Run.Command) > 0 {
			out = append(out, Command{
				Manifest: relativePath,
				Slash:    m.Slash,
				Source:   SourceCommand,
				Argv:     append([]string(nil), m.Run.Command...),
				Shell:    m.Run.Shell,
			})
		}
	case kindComposite:
		for _, step := range m.Steps {
			if step.Shell == nil || len(step.Shell.Command) == 0 {
				continue
			}
			out = append(out, Command{
				Manifest: relativePath,
				Slash:    m.Slash,
				Source:   SourceCompositeStep,
				Argv:     append([]string(nil), step.Shell.Command...),
				Shell:    step.Shell.Shell,
			})
		}
	case kindMonitor:
		if m.Monitor == nil {
			break
		}
		for _, p := range m.Monitor.Panels {
			if p.Kind != shellOutputPanelKind {
				continue
			}
			argv := panelCommand(p.Spec)
			if len(argv) == 0 {
				continue
			}
			out = append(out, Command{
				Manifest: relativePath,
				Slash:    m.Slash,
				Source:   SourceMonitorPanel,
				Argv:     argv,
				Shell:    panelShell(p.Spec),
			})
		}
	}
	return out
}

// panelCommand pulls the command tokens out of a shell/output panel's
// spec map. The panel schema (monitorx) accepts both a scalar string
// and a sequence, mirroring the manifest-level command field; the type
// switch reflects that. Anything else returns an empty slice — Scan
// then skips the panel without producing a Command entry, leaving the
// load-time strict validator to surface the type error.
func panelCommand(spec map[string]any) []string {
	if spec == nil {
		return nil
	}
	raw, ok := spec["command"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil
			}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

// panelShell extracts the optional shell-mode string from a shell/output
// panel spec. Missing or non-string entries return "" (the default array
// form), matching the panel-schema default in monitorx.
func panelShell(spec map[string]any) string {
	if spec == nil {
		return ""
	}
	if s, ok := spec["shell"].(string); ok {
		return s
	}
	return ""
}
