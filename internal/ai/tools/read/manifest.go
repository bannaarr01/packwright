package read

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/bannaarr01/packwright/internal/ai/tools"
	"github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/pack"
)

// manifestLoad is a var so tests can swap in a fake loader.
var manifestLoad = manifest.Load

// packDiscover is a var so tests can swap in a fake discovery.
var packDiscover = pack.Discover

// manifestRead loads a single manifest from disk under $PACKWRIGHT_HOME. The
// AI uses it to inspect a command's form schema and deploy/template targets
// before suggesting parameter changes.
type manifestRead struct{}

// Name reports the catalogue name.
func (manifestRead) Name() string { return "manifest/read" }

// Permission returns the const PermissionRead.
func (manifestRead) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t manifestRead) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Load a Packwright manifest YAML from disk. The path is interpreted relative to $PACKWRIGHT_HOME.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Manifest path relative to $PACKWRIGHT_HOME (e.g. packs/alb/manifests/alb-create.yaml).",
				},
			},
			"required": []string{"path"},
		},
	}
}

// Execute resolves the path against the sandbox and decodes the manifest.
func (t manifestRead) Execute(ctx context.Context, args map[string]any) (any, error) {
	rel, err := tools.ArgString(t.Name(), args, "path", true)
	if err != nil {
		return nil, err
	}
	home, err := tools.RequireHome(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	abs, err := sandboxPath(t.Name(), home, rel)
	if err != nil {
		return nil, err
	}
	m, err := manifestLoad(abs)
	if err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("loading manifest %q: %v", rel, err),
			Cause:   err,
		}
	}
	return map[string]any{
		"path":     rel,
		"manifest": summariseManifest(m),
	}, nil
}

// summariseManifest converts a *manifest.Manifest into a flat, JSON-friendly
// shape. The yaml tags would also serialise fine, but spelling the output
// inline makes the LLM-facing contract explicit and stable across manifest
// revisions.
func summariseManifest(m *manifest.Manifest) map[string]any {
	if m == nil {
		return nil
	}
	fields := make([]map[string]any, 0, len(m.Form))
	for _, f := range m.Form {
		entry := map[string]any{
			"id":       f.ID,
			"label":    f.Label,
			"type":     string(f.Type),
			"required": f.Required,
		}
		if f.Default != nil {
			entry["default"] = f.Default
		}
		if len(f.Values) > 0 {
			entry["values"] = f.Values
		}
		if len(f.DependsOn) > 0 {
			entry["depends_on"] = f.DependsOn
		}
		fields = append(fields, entry)
	}
	out := map[string]any{
		"id":             m.ID,
		"schema_version": m.SchemaVersion,
		"kind":           string(m.Kind),
		"slash":          m.Slash,
		"title":          m.Title,
		"form":           fields,
	}
	if m.Template != nil {
		out["template"] = map[string]any{
			"kind":            m.Template.Kind,
			"path":            m.Template.Path,
			"parameters_file": m.Template.ParametersFile,
		}
	}
	if m.Deploy != nil {
		out["deploy"] = map[string]any{
			"driver": m.Deploy.Driver,
			"script": m.Deploy.Script,
		}
	}
	return out
}

// manifestList enumerates the manifests registered through Packwright's pack
// discovery (and the user-scope commands), filtering by kind and pack when
// requested.
type manifestList struct{}

// Name reports the catalogue name.
func (manifestList) Name() string { return "manifest/list" }

// Permission returns the const PermissionRead.
func (manifestList) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t manifestList) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "List manifests registered through pack discovery. Optionally filter by kind (resource/shell/monitor/composite) or pack name.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": "Filter to a single manifest kind. Empty returns every kind.",
				},
				"pack": map[string]any{
					"type":        "string",
					"description": "Filter to manifests from one pack by name. Empty returns every pack.",
				},
			},
		},
	}
}

// Execute walks $PACKWRIGHT_HOME/packs/ via pack.Discover and applies the
// requested filters in memory.
func (t manifestList) Execute(ctx context.Context, args map[string]any) (any, error) {
	kindFilter, err := tools.ArgString(t.Name(), args, "kind", false)
	if err != nil {
		return nil, err
	}
	packFilter, err := tools.ArgString(t.Name(), args, "pack", false)
	if err != nil {
		return nil, err
	}
	home, err := tools.RequireHome(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	packs, err := packDiscover(home)
	if err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("discovering packs in %q: %v", home, err),
			Cause:   err,
		}
	}
	res := make([]map[string]any, 0)
	for _, p := range packs {
		if packFilter != "" && p.Name != packFilter {
			continue
		}
		for _, m := range p.Manifests {
			if kindFilter != "" && string(m.Kind) != kindFilter {
				continue
			}
			res = append(res, map[string]any{
				"pack":  p.Name,
				"path":  filepath.ToSlash(relPath(home, p.Dir, m)),
				"kind":  string(m.Kind),
				"slash": m.Slash,
				"id":    m.ID,
				"title": m.Title,
			})
		}
	}
	return map[string]any{"manifests": res}, nil
}

// relPath builds a clean home-relative path for a manifest. The manifest
// itself does not retain its on-disk filename so we approximate with the
// canonical layout packs/<name>/manifests/<id>.yaml — what the AI sees is
// a stable identifier rather than an undocumented disk path.
func relPath(home, packDir string, m *manifest.Manifest) string {
	rel, err := filepath.Rel(home, packDir)
	if err != nil || rel == "" {
		rel = packDir
	}
	id := m.ID
	if id == "" {
		id = m.Slash
	}
	return filepath.Join(rel, "manifests", id+".yaml")
}

func init() {
	tools.MustRegister(tools.Default, manifestRead{})
	tools.MustRegister(tools.Default, manifestList{})
}
