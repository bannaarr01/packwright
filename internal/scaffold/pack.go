package scaffold

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// packSubdirs is the standard pack layout from ADR-0009: a manifests/
// directory plus the conventional sibling folders. Each is created on disk
// with an empty .gitkeep file so version control tracks them even when no
// manifests have been added yet.
var packSubdirs = []string{"manifests", "templates", "commands", "monitors"}

// NewPack creates a new pack directory under parentDir using the supplied
// PackSpec. The full layout (pack.yaml, README.md, the four standard
// sub-directories with .gitkeep placeholders) is written in one pass. The
// pack root must not exist beforehand — NewPack refuses to overwrite an
// existing tree so the wizard can never silently clobber author work.
//
// The pack name is validated as a portable directory segment: non-empty,
// no path separators, no leading dot. Anything richer (semver, namespacing)
// is the caller's responsibility.
func NewPack(parentDir string, spec PackSpec) (string, error) {
	if err := validatePackSpec(spec); err != nil {
		return "", err
	}
	if parentDir == "" {
		return "", fmt.Errorf("scaffold: parentDir is required")
	}

	root := filepath.Join(parentDir, spec.Name)
	if _, err := os.Stat(root); err == nil {
		return "", fmt.Errorf("scaffold: pack directory already exists: %s", root)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("scaffold: stat %s: %w", root, err)
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("scaffold: mkdir %s: %w", root, err)
	}

	for _, sub := range packSubdirs {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("scaffold: mkdir %s: %w", dir, err)
		}
		gitkeep := filepath.Join(dir, ".gitkeep")
		if err := os.WriteFile(gitkeep, nil, 0o644); err != nil {
			return "", fmt.Errorf("scaffold: write %s: %w", gitkeep, err)
		}
	}

	packYAML, err := renderTemplate("pack.yaml.gotmpl", spec)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "pack.yaml"), packYAML, 0o644); err != nil {
		return "", fmt.Errorf("scaffold: write pack.yaml: %w", err)
	}

	readme, err := renderTemplate("readme.md.gotmpl", spec)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), readme, 0o644); err != nil {
		return "", fmt.Errorf("scaffold: write README.md: %w", err)
	}

	return root, nil
}

// validatePackSpec enforces the portability rules a pack name must satisfy
// to round-trip through git, tar archives, and case-sensitive filesystems.
// The checks intentionally reject characters that work today but bite later
// (slashes, leading dots, whitespace) so packs published by one author
// install cleanly on another author's machine.
func validatePackSpec(spec PackSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("scaffold: PackSpec.Name is required")
	}
	if strings.ContainsAny(spec.Name, `/\`) {
		return fmt.Errorf("scaffold: PackSpec.Name must not contain path separators")
	}
	if strings.HasPrefix(spec.Name, ".") {
		return fmt.Errorf("scaffold: PackSpec.Name must not start with a dot")
	}
	if strings.ContainsAny(spec.Name, " \t\n") {
		return fmt.Errorf("scaffold: PackSpec.Name must not contain whitespace")
	}
	return nil
}

// renderTemplate is the byte-returning equivalent of templates.ExecuteTemplate.
// Wrapped here so command.go's Generate stays focused on manifests while
// pack.go reuses the same parsed template set.
func renderTemplate(name string, data any) ([]byte, error) {
	tmpl := templates.Lookup(name)
	if tmpl == nil {
		return nil, fmt.Errorf("scaffold: template %q not found", name)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("scaffold: render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}
