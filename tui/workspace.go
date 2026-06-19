package tui

// This file routes the workspace and template-management slash commands into
// the TUI. Unlike the deploy flow (run.go), these slashes are not backed by a
// dispatch manifest — they run the same operations the `packwright project`,
// `copy-template`, and `promote-template` cobra commands do (config + on-disk
// workspace state, no AWS). pack.LoadPalette therefore never surfaces them, so
// the TUI seeds its own built-in palette rows (workspacePaletteItems) and
// handlePaletteSelection routes them here.
//
// The shape mirrors run.go's form flow: a slash that needs input opens a
// screens.FormScreen and stashes a pendingWorkspace; handleFormSubmit hands the
// submitted values to finishWorkspace, which runs the operation and shows the
// result in a noticeScreen. /list-projects needs no input, so it renders its
// notice immediately.
//
// These rows are TUI-only on purpose: the GUI does not yet route them (this is
// the TUI surface), so seeding them only here keeps the GUI palette from
// offering commands it cannot run.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/config"
	imanifest "github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/internal/scaffold"
	"github.com/bannaarr01/packwright/internal/workspace"
	"github.com/bannaarr01/packwright/tui/screens"
)

// Built-in workspace / template slash labels. They mirror the cobra command
// names (cmd.SlashNewProject etc. and the copy-template / promote-template
// subcommands); they are duplicated here as literals rather than imported so
// the tui package never has to import cmd — that would invert the registry
// wiring direction (AGENTS.md) and risk an import cycle.
const (
	slashNewProject      = "/new-project"
	slashNewEnv          = "/new-env"
	slashSwitchProject   = "/switch-project"
	slashListProjects    = "/list-projects"
	slashCopyTemplate    = "/copy-template"
	slashPromoteTemplate = "/promote-template"
)

// workspaceKind identifies which workspace/template operation a form is
// collecting input for.
type workspaceKind int

const (
	wsNewProject workspaceKind = iota
	wsNewEnv
	wsSwitchProject
	wsCopyTemplate
	wsPromoteTemplate
)

// pendingWorkspace records the workspace action awaiting form input while its
// form is on screen, mirroring pendingRun for the manifest deploy flow. A nil
// pendingWorkspace means no workspace action is awaiting input.
type pendingWorkspace struct{ kind workspaceKind }

// workspacePaletteItems returns the built-in workspace/template rows the TUI
// palette offers alongside the manifest-backed rows from pack.LoadPalette.
// buildPaletteLoader appends them on every refresh.
func workspacePaletteItems() []list.Item {
	return []list.Item{
		paletteItem{slash: slashNewProject, title: "Create a new project"},
		paletteItem{slash: slashNewEnv, title: "Create a new environment"},
		paletteItem{slash: slashSwitchProject, title: "Switch the active project / env"},
		paletteItem{slash: slashListProjects, title: "List projects and environments"},
		paletteItem{slash: slashCopyTemplate, title: "Fork a manifest into a draft"},
		paletteItem{slash: slashPromoteTemplate, title: "Promote a draft manifest"},
	}
}

// handleWorkspaceSlash routes a workspace/template palette pick. Input-taking
// slashes open a form (and stash a pendingWorkspace for handleFormSubmit);
// /list-projects needs no input and renders its notice immediately.
func (a app) handleWorkspaceSlash(slash string) (tea.Model, tea.Cmd) {
	switch slash {
	case slashListProjects:
		return a.pushNotice("Projects", renderProjectList())
	case slashNewProject:
		return a.openWorkspaceForm(wsNewProject, "New project", []imanifest.Field{
			{ID: "slug", Label: "Project slug", Required: true, Placeholder: "acme"},
			{ID: "name", Label: "Display name (optional)", Placeholder: "Acme Corp"},
		})
	case slashNewEnv:
		return a.openWorkspaceForm(wsNewEnv, "New environment", []imanifest.Field{
			{ID: "project", Label: "Project slug", Required: true, Placeholder: "acme"},
			{ID: "env", Label: "Env slug", Required: true, Placeholder: "dev"},
			{ID: "name", Label: "Display name (optional)", Placeholder: "Development"},
		})
	case slashSwitchProject:
		return a.openWorkspaceForm(wsSwitchProject, "Switch project", []imanifest.Field{
			{ID: "project", Label: "Project slug", Required: true, Placeholder: "acme"},
			{ID: "env", Label: "Env slug (optional)", Placeholder: "dev"},
		})
	case slashCopyTemplate:
		return a.openWorkspaceForm(wsCopyTemplate, "Copy template", []imanifest.Field{
			{ID: "src", Label: "Source manifest path", Required: true, Placeholder: "packs/aws/manifests/alb.yaml"},
			{ID: "dest", Label: "Destination scope dir", Required: true, Placeholder: "projects/acme/dev"},
			{ID: "slash", Label: "New slash command", Required: true, Placeholder: "/alb-shared"},
		})
	case slashPromoteTemplate:
		return a.openWorkspaceForm(wsPromoteTemplate, "Promote template", []imanifest.Field{
			{ID: "path", Label: "Draft manifest path", Required: true, Placeholder: "projects/acme/dev/drafts/alb-shared.yaml"},
		})
	}
	return a, nil
}

// openWorkspaceForm pushes an input form and stashes the pending action so
// handleFormSubmit can run it on submit.
func (a app) openWorkspaceForm(kind workspaceKind, title string, fields []imanifest.Field) (tea.Model, tea.Cmd) {
	a.pendingWorkspace = &pendingWorkspace{kind: kind}
	cmd := a.registry.Push(screens.NewForm(title, fields))
	a.focus = focusContent
	a.tree.Focus(false)
	a.applyContentSize()
	return a, cmd
}

// pushNotice pushes a read-only notice screen (used for /list-projects, which
// has no form step).
func (a app) pushNotice(title, body string) (tea.Model, tea.Cmd) {
	cmd := a.registry.Push(newNoticeScreen(title, body))
	a.focus = focusContent
	a.tree.Focus(false)
	a.applyContentSize()
	return a, cmd
}

// finishWorkspace runs the pending workspace action with the submitted form
// values and replaces the form with a notice showing the result. It replaces
// (rather than pushes) so Esc from the notice returns to the launcher, not to a
// stale form — matching handleFormSubmit's deploy path. A successful project /
// env / switch returns a fresh config, which is wired back in and the sidebar
// rebuilt so the change is visible without a relaunch.
func (a app) finishWorkspace(pw *pendingWorkspace, values map[string]string) (tea.Model, tea.Cmd) {
	title := workspaceTitle(pw.kind)
	msg, cfg, err := runWorkspace(pw.kind, values)
	body := msg
	if err != nil {
		body = updateErrStyle.Render("Error: " + err.Error())
	}
	if cfg != nil {
		a.cfg = cfg
		a.rebuildTree()
	}
	cmd := a.registry.Replace(newNoticeScreen(title, body))
	a.focus = focusContent
	a.tree.Focus(false)
	a.applyContentSize()
	return a, cmd
}

// runWorkspace performs the workspace/template operation for kind using the
// submitted form values. It is a faithful port of the cobra RunE handlers
// (cmd_project.go / cmd_copy.go / cmd_promote.go) minus the cobra plumbing, so
// the TUI and CLI share identical validation and on-disk effects. For the
// project / env / switch verbs it returns the reconciled-and-saved config so
// the caller can refresh in-memory state; copy / promote return a nil config
// (they do not mutate config.yaml).
func runWorkspace(kind workspaceKind, v map[string]string) (string, *config.Config, error) {
	switch kind {
	case wsNewProject:
		return doNewProject(v)
	case wsNewEnv:
		return doNewEnv(v)
	case wsSwitchProject:
		return doSwitchProject(v)
	case wsCopyTemplate:
		msg, err := doCopyTemplate(v)
		return msg, nil, err
	case wsPromoteTemplate:
		msg, err := doPromoteTemplate(v)
		return msg, nil, err
	}
	return "", nil, fmt.Errorf("tui: unknown workspace action")
}

func doNewProject(v map[string]string) (string, *config.Config, error) {
	slug := workspace.NormalizeSlug(v["slug"])
	if err := workspace.ValidateSlug(slug); err != nil {
		return "", nil, err
	}
	name := slug
	if n := strings.TrimSpace(v["name"]); n != "" {
		name = n
	}
	home, err := config.Home()
	if err != nil {
		return "", nil, err
	}
	created, err := workspace.CreateProject(home, workspace.Project{Slug: slug, Name: name})
	if err != nil {
		return "", nil, err
	}
	cfg, err := reconcileAndSave(home)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Created project %s", created.Slug), cfg, nil
}

func doNewEnv(v map[string]string) (string, *config.Config, error) {
	project := workspace.NormalizeSlug(v["project"])
	env := workspace.NormalizeSlug(v["env"])
	if err := workspace.ValidateSlug(project); err != nil {
		return "", nil, err
	}
	if err := workspace.ValidateSlug(env); err != nil {
		return "", nil, err
	}
	name := env
	if n := strings.TrimSpace(v["name"]); n != "" {
		name = n
	}
	home, err := config.Home()
	if err != nil {
		return "", nil, err
	}
	created, err := workspace.CreateEnv(home, project, workspace.Env{Slug: env, Name: name})
	if err != nil {
		return "", nil, err
	}
	cfg, err := reconcileAndSave(home)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Created env %s/%s", project, created.Slug), cfg, nil
}

func doSwitchProject(v map[string]string) (string, *config.Config, error) {
	project := workspace.NormalizeSlug(v["project"])
	env := workspace.NormalizeSlug(v["env"]) // "" when the field was left blank
	home, err := config.Home()
	if err != nil {
		return "", nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return "", nil, err
	}
	if _, err := cfg.Reconcile(home); err != nil {
		return "", nil, err
	}
	if err := cfg.SetActive(project, env); err != nil {
		return "", nil, err
	}
	if err := cfg.Save(); err != nil {
		return "", nil, err
	}
	if env == "" {
		return fmt.Sprintf("Switched to project %s", cfg.ActiveProject), cfg, nil
	}
	return fmt.Sprintf("Switched to %s/%s", cfg.ActiveProject, cfg.ActiveEnv), cfg, nil
}

func doCopyTemplate(v map[string]string) (string, error) {
	src := strings.TrimSpace(v["src"])
	dest := strings.TrimSpace(v["dest"])
	slash := strings.TrimSpace(v["slash"])
	if src == "" || dest == "" || slash == "" {
		return "", fmt.Errorf("copy-template: source, destination, and slash are all required")
	}
	dst := filepath.Join(dest, "drafts", slugForSlash(slash)+".yaml")
	if err := scaffold.CopyTemplate(src, dst, slash); err != nil {
		return "", err
	}
	return fmt.Sprintf("Wrote draft %s → %s\nEdit it, then run /promote-template on that path to deploy.", slash, dst), nil
}

func doPromoteTemplate(v map[string]string) (string, error) {
	path := strings.TrimSpace(v["path"])
	if path == "" {
		return "", fmt.Errorf("promote-template: a draft manifest path is required")
	}
	if err := scaffold.PromoteTemplate(path); err != nil {
		return "", err
	}
	return fmt.Sprintf("Promoted %s — _draft removed; it is now deployable.", path), nil
}

// reconcileAndSave reloads config, reconciles it against the on-disk workspace
// tree (so a freshly created project/env is mirrored in), and persists it.
func reconcileAndSave(home string) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if _, err := cfg.Reconcile(home); err != nil {
		return nil, err
	}
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// renderProjectList loads + reconciles config and renders the project/env tree
// as plain text for /list-projects. It mirrors cmd.writeProjectList's format;
// that helper is unexported in the cmd package, so the few lines are reproduced
// here rather than reaching across the package boundary.
func renderProjectList() string {
	home, err := config.Home()
	if err != nil {
		return "Error: " + err.Error()
	}
	cfg, err := config.Load()
	if err != nil {
		return "Error: " + err.Error()
	}
	if _, err := cfg.Reconcile(home); err != nil {
		return "Error: " + err.Error()
	}
	if len(cfg.Projects) == 0 {
		return "No projects yet — use /new-project to create one."
	}
	var b strings.Builder
	for _, p := range cfg.Projects {
		marker := " "
		if cfg.ActiveProject == p.Slug {
			marker = "*"
		}
		fmt.Fprintf(&b, "%s %s — %s\n", marker, p.Slug, p.Name)
		for _, e := range p.Envs {
			emark := " "
			if cfg.ActiveProject == p.Slug && cfg.ActiveEnv == e.Slug {
				emark = "*"
			}
			fmt.Fprintf(&b, "  %s %s — %s\n", emark, e.Slug, e.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// workspaceTitle is the notice-screen title for each workspace action.
func workspaceTitle(k workspaceKind) string {
	switch k {
	case wsNewProject:
		return "New project"
	case wsNewEnv:
		return "New environment"
	case wsSwitchProject:
		return "Switch project"
	case wsCopyTemplate:
		return "Copy template"
	case wsPromoteTemplate:
		return "Promote template"
	}
	return "Workspace"
}

// slugForSlash converts a slash command into a filesystem-safe slug: the
// leading slash is stripped and inner slashes become dashes. It matches
// cmd.slugForSlash / scaffold's slugFromSlash convention (kept in sync by hand)
// so a copy made from the TUI lands at the same path the CLI would use.
func slugForSlash(slash string) string {
	s := strings.TrimPrefix(slash, "/")
	return strings.ReplaceAll(s, "/", "-")
}
