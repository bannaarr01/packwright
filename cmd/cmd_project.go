package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/workspace"
)

// Slash labels for the future TUI palette routing. The palette in PR-09 /
// PR-10 will match against these strings; exporting them as constants here
// keeps the source of truth in one place.
const (
	SlashNewProject    = "/new-project"
	SlashNewEnv        = "/new-env"
	SlashSwitchProject = "/switch-project"
	SlashListProjects  = "/list-projects"
)

// projectCmd groups the workspace slash commands under one cobra parent so
// the `--help` output for `packwright` lists them cleanly. The verbs are
// also exposed as top-level cobra subcommands further down (new-project,
// new-env, switch-project, list-projects) to match the slash-name 1:1 —
// `packwright new-project acme` is the headless equivalent of `/new-project
// acme` in the palette.
var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage the local project / environment workspace (ADR-0045)",
	Long: `Manage the Packwright workspace data model.

Projects are folders under <Home>/projects/<slug>/ that group one or more
environments. Each env has its own manifests/, drafts/, and stacks/ subtree.
The verbs below are also exposed as top-level subcommands (new-project,
new-env, switch-project, list-projects) so they mirror the /-prefixed
palette labels.`,
}

// newProjectCmd backs `/new-project <slug> [name]`. It writes the on-disk
// tree atomically and mirrors the entry into config.yaml so a second
// launch re-loads the same state.
var newProjectCmd = &cobra.Command{
	Use:   "new-project <slug> [name]",
	Short: "Create a new project under <Home>/projects/",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runNewProject,
}

// newEnvCmd backs `/new-env <project> <env> [name]`. The project must
// already exist; the parent slug is normalized before lookup so case
// variations resolve cleanly.
var newEnvCmd = &cobra.Command{
	Use:   "new-env <project-slug> <env-slug> [name]",
	Short: "Create a new environment inside an existing project",
	Args:  cobra.RangeArgs(2, 3),
	RunE:  runNewEnv,
}

// switchProjectCmd backs `/switch-project <project> [env]`. It updates
// config.yaml's active selection but does not touch the on-disk tree.
var switchProjectCmd = &cobra.Command{
	Use:   "switch-project <project-slug> [env-slug]",
	Short: "Set the active project (and optionally env) in config.yaml",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runSwitchProject,
}

// listProjectsCmd backs `/list-projects`. It reconciles config.yaml with
// disk before printing so the listing is always honest about what exists.
var listProjectsCmd = &cobra.Command{
	Use:   "list-projects",
	Short: "List local projects and their environments",
	Args:  cobra.NoArgs,
	RunE:  runListProjects,
}

func init() {
	projectCmd.AddCommand(newProjectCmd, newEnvCmd, switchProjectCmd, listProjectsCmd)
	registerSubcommand(projectCmd)
	// Also expose the verbs as top-level subcommands so the CLI form
	// matches the slash-label 1:1 (e.g. `packwright new-project acme`).
	// Cobra commands can only have one parent, so the top-level
	// references are constructed fresh.
	registerSubcommand(&cobra.Command{
		Use:   newProjectCmd.Use,
		Short: newProjectCmd.Short,
		Args:  newProjectCmd.Args,
		RunE:  newProjectCmd.RunE,
	})
	registerSubcommand(&cobra.Command{
		Use:   newEnvCmd.Use,
		Short: newEnvCmd.Short,
		Args:  newEnvCmd.Args,
		RunE:  newEnvCmd.RunE,
	})
	registerSubcommand(&cobra.Command{
		Use:   switchProjectCmd.Use,
		Short: switchProjectCmd.Short,
		Args:  switchProjectCmd.Args,
		RunE:  switchProjectCmd.RunE,
	})
	registerSubcommand(&cobra.Command{
		Use:   listProjectsCmd.Use,
		Short: listProjectsCmd.Short,
		Args:  listProjectsCmd.Args,
		RunE:  listProjectsCmd.RunE,
	})
}

// runNewProject materializes a fresh project on disk and mirrors it into
// config.yaml. Both the workspace-layer create and the config-layer mirror
// reject duplicates case-insensitively, but the workspace layer is asked
// first so the user-facing error always comes from the disk authority
// rather than from a stale config mirror.
func runNewProject(cmd *cobra.Command, args []string) error {
	slug := workspace.NormalizeSlug(args[0])
	if err := workspace.ValidateSlug(slug); err != nil {
		return err
	}
	name := slug
	if len(args) == 2 {
		name = args[1]
	}

	home, err := config.Home()
	if err != nil {
		return err
	}
	created, err := workspace.CreateProject(home, workspace.Project{Slug: slug, Name: name})
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := cfg.Reconcile(home); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Created project %s\n", created.Slug)
	return nil
}

// runNewEnv materializes a fresh env on disk and reconciles config.yaml so
// the in-memory mirror picks up the new env. The disk write is atomic; if
// it fails (e.g. due to a duplicate slug) config.yaml stays unchanged.
func runNewEnv(cmd *cobra.Command, args []string) error {
	projectSlug := workspace.NormalizeSlug(args[0])
	envSlug := workspace.NormalizeSlug(args[1])
	if err := workspace.ValidateSlug(projectSlug); err != nil {
		return err
	}
	if err := workspace.ValidateSlug(envSlug); err != nil {
		return err
	}
	name := envSlug
	if len(args) == 3 {
		name = args[2]
	}

	home, err := config.Home()
	if err != nil {
		return err
	}
	created, err := workspace.CreateEnv(home, projectSlug, workspace.Env{Slug: envSlug, Name: name})
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := cfg.Reconcile(home); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Created env %s/%s\n", projectSlug, created.Slug)
	return nil
}

// runSwitchProject is the only verb that does not write to disk under
// projects/ — it only updates the active selection in config.yaml after
// validating the slugs against the reconciled tree.
func runSwitchProject(cmd *cobra.Command, args []string) error {
	projectSlug := workspace.NormalizeSlug(args[0])
	var envSlug string
	if len(args) == 2 {
		envSlug = workspace.NormalizeSlug(args[1])
	}

	home, err := config.Home()
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := cfg.Reconcile(home); err != nil {
		return err
	}
	if err := cfg.SetActive(projectSlug, envSlug); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if envSlug == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Switched to project %s\n", cfg.ActiveProject)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Switched to %s/%s\n", cfg.ActiveProject, cfg.ActiveEnv)
	}
	return nil
}

// runListProjects reconciles disk into config and prints the tree to
// stdout. The output format intentionally stays terse — the TUI sidebar
// (PR-09) and the GUI grouped sidebar (PR-10) consume the same underlying
// c.Projects slice and render the tree their own way.
func runListProjects(cmd *cobra.Command, _ []string) error {
	home, err := config.Home()
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := cfg.Reconcile(home); err != nil {
		return err
	}
	return writeProjectList(cmd.OutOrStdout(), cfg)
}

// writeProjectList renders the project tree. Pulled out so tests can drive
// it with a fixed config.
func writeProjectList(w io.Writer, cfg *config.Config) error {
	if len(cfg.Projects) == 0 {
		fmt.Fprintln(w, "No projects yet — try `new-project <slug>`.")
		return nil
	}
	for _, p := range cfg.Projects {
		marker := " "
		if cfg.ActiveProject == p.Slug {
			marker = "*"
		}
		fmt.Fprintf(w, "%s %s — %s\n", marker, p.Slug, p.Name)
		for _, e := range p.Envs {
			emark := " "
			if cfg.ActiveProject == p.Slug && cfg.ActiveEnv == e.Slug {
				emark = "*"
			}
			fmt.Fprintf(w, "  %s %s — %s\n", emark, e.Slug, e.Name)
		}
	}
	return nil
}
