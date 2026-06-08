package scaffold

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/manifest"
)

// Built-in kinds for the two scaffolder wizards. They live under the
// "builtin/" prefix so a pack author cannot accidentally collide with them
// in a YAML manifest (the loader rejects unknown kinds).
const (
	KindNewCommand manifest.Kind = "builtin/new-command"
	KindNewPack    manifest.Kind = "builtin/new-pack"
)

// Form-field IDs the wizard runners read out of action.Inputs. Kept as
// constants so the front-end and the runner agree on key names without
// stringly-typed drift.
const (
	FieldKind            = "Kind"
	FieldSlash           = "Slash"
	FieldTitle           = "Title"
	FieldSaveDir         = "SaveDir"
	FieldTemplatePath    = "TemplatePath"
	FieldDeployScript    = "DeployScript"
	FieldPackName        = "Name"
	FieldPackDescription = "Description"
	FieldPackAuthor      = "Author"
	FieldPackHomepage    = "Homepage"
	FieldPackParentDir   = "ParentDir"
)

// WizardManifests returns the two built-in wizard manifests in slash order.
// Front-ends call this on startup and feed the results into their command
// palette so /new-command and /new-pack appear alongside pack-installed
// commands. The returned slice is a fresh copy on every call; callers may
// mutate it freely.
func WizardManifests() []*manifest.Manifest {
	return []*manifest.Manifest{newCommandWizard(), newPackWizard()}
}

// newCommandWizard is the form schema the multi-step engine renders when
// the user invokes /new-command. The form mirrors ADR-0022 §"4-step
// wizard": kind selection, identity (slash + title + save dir), then
// kind-specific body fields. Body fields land in MVP-3 PR-04 (conflict
// resolution) when the form engine learns conditional sections; for this
// PR the wizard collects the canonical core only and the user fills in
// `template`/`deploy` paths via the resource-only fields below.
func newCommandWizard() *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: "packwright.manifest.v1",
		Kind:          KindNewCommand,
		Slash:         "/new-command",
		Title:         "New command",
		Form: []manifest.Field{
			{
				ID:       FieldKind,
				Label:    "Kind",
				Type:     manifest.TypeEnum,
				Required: true,
				Values: []string{
					string(manifest.KindResource),
					string(manifest.KindShell),
					string(manifest.KindMonitor),
					string(manifest.KindComposite),
				},
			},
			{
				ID: FieldSlash, Label: "Slash command (e.g. /restart-api)",
				Type: manifest.TypeString, Required: true,
			},
			{
				ID: FieldTitle, Label: "Title",
				Type: manifest.TypeString, Required: true,
			},
			{
				ID: FieldSaveDir, Label: "Save directory (manifests/ inside a pack)",
				Type: manifest.TypeString, Required: true,
			},
			{
				ID: FieldTemplatePath, Label: "Template path (resource only, relative to manifest)",
				Type:      manifest.TypeString,
				DependsOn: []string{FieldKind},
			},
			{
				ID: FieldDeployScript, Label: "Deploy script (resource only, relative to manifest)",
				Type:      manifest.TypeString,
				DependsOn: []string{FieldKind},
			},
		},
	}
}

// newPackWizard is the form schema rendered when the user invokes
// /new-pack. The fields cover the three steps in ADR-0022: identity,
// (implicit) seed = blank, result. Seed-from-git lands in MVP-4 PR-01.
func newPackWizard() *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: "packwright.manifest.v1",
		Kind:          KindNewPack,
		Slash:         "/new-pack",
		Title:         "New pack",
		Form: []manifest.Field{
			{
				ID: FieldPackName, Label: "Pack name (directory segment)",
				Type: manifest.TypeString, Required: true,
			},
			{
				ID: FieldPackDescription, Label: "Description",
				Type: manifest.TypeString,
			},
			{
				ID: FieldPackAuthor, Label: "Author",
				Type: manifest.TypeString,
			},
			{
				ID: FieldPackHomepage, Label: "Homepage URL",
				Type: manifest.TypeString,
			},
			{
				ID: FieldPackParentDir, Label: "Parent directory (pack root is created beneath this)",
				Type: manifest.TypeString, Required: true,
			},
		},
	}
}

// newCommandRunner is the dispatcher-side adapter for /new-command. Validate
// confirms the kind on the manifest; Run pulls the typed inputs out, builds
// a Spec, renders YAML via Generate, and writes the result to the chosen
// save directory.
type newCommandRunner struct{}

// Kind reports the synthetic built-in kind so action.Lookup finds this
// runner for the wizard manifest emitted by WizardManifests.
func (newCommandRunner) Kind() manifest.Kind { return KindNewCommand }

// Validate ensures the runner is being dispatched against its own kind.
// Field-level checks happen inside Run because they depend on user-supplied
// input rather than the manifest shape.
func (newCommandRunner) Validate(m *manifest.Manifest) error {
	if m == nil {
		return errors.New("scaffold: manifest is nil")
	}
	if m.Kind != KindNewCommand {
		return fmt.Errorf("scaffold: manifest kind %q does not match %q", m.Kind, KindNewCommand)
	}
	return nil
}

// NewCommandResult is the kind-specific value carried in action.Result.Value
// after a successful /new-command run. Path is the absolute filename written
// to disk; YAML is the generated bytes so the caller can preview them in the
// review screen without re-reading the file.
type NewCommandResult struct {
	Path string
	YAML []byte
}

// Run extracts the wizard inputs, builds a Spec, generates the manifest
// YAML, and writes it to <SaveDir>/<slug>.yaml. The slug strips the leading
// slash from the slash command so the filename is filesystem-safe. Existing
// files are not overwritten — the user must choose a new slash or delete
// the colliding file first.
func (newCommandRunner) Run(_ context.Context, _ *manifest.Manifest, in action.Inputs) (action.Result, error) {
	spec, saveDir, err := specFromInputs(in)
	if err != nil {
		return action.Result{Kind: KindNewCommand}, err
	}
	out, err := Generate(spec)
	if err != nil {
		return action.Result{Kind: KindNewCommand}, err
	}
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		return action.Result{Kind: KindNewCommand}, fmt.Errorf("scaffold: mkdir %s: %w", saveDir, err)
	}
	path := filepath.Join(saveDir, slugFromSlash(spec.Slash)+".yaml")
	if _, err := os.Stat(path); err == nil {
		return action.Result{Kind: KindNewCommand}, fmt.Errorf("scaffold: refusing to overwrite existing manifest: %s", path)
	} else if !os.IsNotExist(err) {
		return action.Result{Kind: KindNewCommand}, fmt.Errorf("scaffold: stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return action.Result{Kind: KindNewCommand}, fmt.Errorf("scaffold: write %s: %w", path, err)
	}
	return action.Result{Kind: KindNewCommand, Value: &NewCommandResult{Path: path, YAML: out}}, nil
}

// newPackRunner is the dispatcher-side adapter for /new-pack. Inputs are
// turned into a PackSpec and passed to NewPack; the resulting absolute pack
// root is returned to the caller for use in the post-creation "open in
// editor / now run /new-command" affordance.
type newPackRunner struct{}

// Kind reports the synthetic built-in kind for the /new-pack wizard.
func (newPackRunner) Kind() manifest.Kind { return KindNewPack }

// Validate confirms the manifest matches this runner's kind. Pack-name
// validation lives in NewPack itself so a single source of truth handles
// both the wizard path and direct programmatic callers.
func (newPackRunner) Validate(m *manifest.Manifest) error {
	if m == nil {
		return errors.New("scaffold: manifest is nil")
	}
	if m.Kind != KindNewPack {
		return fmt.Errorf("scaffold: manifest kind %q does not match %q", m.Kind, KindNewPack)
	}
	return nil
}

// NewPackResult is the kind-specific value carried in action.Result.Value
// after a successful /new-pack run.
type NewPackResult struct {
	Root string
}

// Run reads the wizard inputs, dispatches to NewPack, and returns the path
// of the freshly created pack root.
func (newPackRunner) Run(_ context.Context, _ *manifest.Manifest, in action.Inputs) (action.Result, error) {
	parent, _ := stringInput(in, FieldPackParentDir)
	spec := PackSpec{
		Name:        firstString(in, FieldPackName),
		Description: firstString(in, FieldPackDescription),
		Author:      firstString(in, FieldPackAuthor),
		Homepage:    firstString(in, FieldPackHomepage),
	}
	root, err := NewPack(parent, spec)
	if err != nil {
		return action.Result{Kind: KindNewPack}, err
	}
	return action.Result{Kind: KindNewPack, Value: &NewPackResult{Root: root}}, nil
}

// specFromInputs converts the /new-command form inputs into a Spec plus the
// SaveDir the manifest will be written into. Type and presence are
// validated inline so the error message names the wizard field that needs
// attention rather than a downstream YAML path.
func specFromInputs(in action.Inputs) (Spec, string, error) {
	kindStr := firstString(in, FieldKind)
	saveDir := firstString(in, FieldSaveDir)
	if saveDir == "" {
		return Spec{}, "", fmt.Errorf("scaffold: %s is required", FieldSaveDir)
	}

	spec := Spec{
		Kind:  manifest.Kind(kindStr),
		Slash: firstString(in, FieldSlash),
		Title: firstString(in, FieldTitle),
	}

	if spec.Kind == manifest.KindResource {
		spec.Template = &TemplateSpec{
			Kind: "cloudformation",
			Path: firstString(in, FieldTemplatePath),
		}
		spec.Deploy = &DeploySpec{
			Driver: "script",
			Script: firstString(in, FieldDeployScript),
		}
	}
	return spec, saveDir, nil
}

// stringInput coerces an action.Inputs entry into a string, distinguishing
// "key missing" from "value present but empty". The form engine may deliver
// values as raw strings or as fmt.Stringer-implementing widgets; both pass
// through. Other types fall back to fmt.Sprintf so the runner never panics
// on an unexpected payload.
func stringInput(in action.Inputs, key string) (string, bool) {
	raw, ok := in[key]
	if !ok {
		return "", false
	}
	switch v := raw.(type) {
	case nil:
		return "", true
	case string:
		return v, true
	case fmt.Stringer:
		return v.String(), true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

// firstString is stringInput collapsed to its first return value, used when
// the caller does not need to distinguish "missing" from "empty string".
func firstString(in action.Inputs, key string) string {
	s, _ := stringInput(in, key)
	return s
}

// slugFromSlash strips the leading slash and replaces inner slashes with
// dashes so the resulting filename is filesystem-safe on every supported
// platform. Examples: "/restart-api" → "restart-api"; "/aws/alb" → "aws-alb".
func slugFromSlash(slash string) string {
	s := strings.TrimPrefix(slash, "/")
	return strings.ReplaceAll(s, "/", "-")
}

// init registers the two scaffolder runners with the dispatcher. The
// resource_runner.go pattern (in action/dispatch) is the established way to
// wire a kind without coupling the dispatcher to the runner's package; we
// follow the same convention from inside internal/scaffold.
func init() {
	action.Register(newCommandRunner{})
	action.Register(newPackRunner{})
}
