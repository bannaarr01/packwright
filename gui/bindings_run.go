package gui

import (
	"context"
	"fmt"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/action/dispatch"
	"github.com/bannaarr01/packwright/action/resource"
	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/pack"
)

// This file gives the GUI the ability to actually run a manifest command.
// Before it, SelectSlashCommand only logged the pick (the palette was a
// "log the selection" stub); these bindings mirror the TUI's palette → engine
// path: SlashCommandForm returns the inputs to collect, and RunSlashCommand
// resolves the slash to a manifest and runs it through action/dispatch.Dispatch.
//
// v1 drains the engine's output to completion server-side and returns it; live
// token-by-token streaming over Wails events is a follow-up. The frontend
// shows a "running…" state for the duration of the RPC and renders the
// collected output and outcome when it resolves.

// FormField is one input the GUI renders before running a manifest command.
// It mirrors the subset of manifest.Field the form UI needs.
type FormField struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Placeholder string `json:"placeholder"`
	Required    bool   `json:"required"`
}

// FormPayload is SlashCommandForm's response. Resolved is false when the slash
// has no backing manifest (the frontend then declines to open a run panel).
type FormPayload struct {
	Slash    string      `json:"slash"`
	Title    string      `json:"title"`
	Resolved bool        `json:"resolved"`
	Fields   []FormField `json:"fields"`
}

// RunResult is RunSlashCommand's response: the collected output lines plus the
// outcome. OK is false (and Error set) when the engine returned an error or the
// run finished with a non-nil exit status.
type RunResult struct {
	OK     bool     `json:"ok"`
	Output []string `json:"output"`
	Error  string   `json:"error"`
}

// resolveRunnable is the package-level seam tests stub to avoid touching the
// real home directory. Production resolves the Packwright home + pinned
// defaults and delegates to pack.ResolveRunnable (the same resolver the TUI
// palette uses), so both front-ends route a slash to the same manifest.
var resolveRunnable = func(slash string) (*manifest.Manifest, string, bool) {
	home, err := config.Home()
	if err != nil {
		return nil, "", false
	}
	var pinned map[string]string
	if cfg, err := config.Load(); err == nil {
		pinned = cfg.PinnedDefaults
	}
	return pack.ResolveRunnable(home, pinned, slash)
}

// SlashCommandForm returns the form schema for a manifest-backed slash so the
// frontend can collect inputs before RunSlashCommand. A slash with no backing
// manifest returns Resolved=false.
func (a *App) SlashCommandForm(slash string) FormPayload {
	m, _, ok := resolveRunnable(slash)
	if !ok {
		return FormPayload{Slash: slash, Resolved: false}
	}
	fields := make([]FormField, 0, len(m.Form))
	for _, f := range m.Form {
		fields = append(fields, FormField{
			ID:          f.ID,
			Label:       f.Label,
			Type:        string(f.Type),
			Placeholder: f.Placeholder,
			Required:    f.Required,
		})
	}
	return FormPayload{Slash: slash, Title: m.Title, Resolved: true, Fields: fields}
}

// RunSlashCommand resolves the slash to a manifest and runs it through the
// action engine, returning the collected output and outcome. Resource and
// composite kinds get an AWS client; other kinds (shell, the scaffold wizards)
// run without one. The surface label is left to bootstrap's
// SetDefaultSurface("gui") so usage events stay attributed.
func (a *App) RunSlashCommand(slash string, inputs map[string]string) RunResult {
	m, baseDir, ok := resolveRunnable(slash)
	if !ok {
		return RunResult{Error: fmt.Sprintf("no command found for %q", slash)}
	}

	in := make(action.Inputs, len(inputs))
	for k, v := range inputs {
		in[k] = v
	}

	ctx := context.Background()
	var client *awsx.Client
	if m.Kind == manifest.KindResource || m.Kind == manifest.KindComposite {
		home, _ := config.Home()
		profile, region := "", ""
		if cfg, err := config.Load(); err == nil {
			profile, region = cfg.Profile, cfg.Region
		}
		if c, err := awsx.New(ctx, profile, region, home, a.logger); err == nil {
			client = c
		} else {
			a.logger.Warn("gui: run: build aws client", "err", err)
		}
	}
	ctx = dispatch.WithAWSClient(ctx, client)
	ctx = dispatch.WithBaseDir(ctx, baseDir)

	res, err := dispatch.Dispatch(ctx, m, in)
	if err != nil {
		return RunResult{Error: err.Error()}
	}

	// Resource runs are asynchronous: drain the engine's event channel to
	// completion, then Wait for the deploy's exit status. Other kinds return
	// synchronously with a non-resource Value.
	if rr, ok := res.Value.(*resource.Result); ok {
		var out []string
		for ev := range rr.Events {
			if line := formatRunLine(ev); line != "" {
				out = append(out, line)
			}
		}
		if werr := rr.Wait(); werr != nil {
			return RunResult{OK: false, Output: out, Error: werr.Error()}
		}
		return RunResult{OK: true, Output: out}
	}
	return RunResult{OK: true, Output: []string{slash + " completed"}}
}

// formatRunLine renders one engine event as a plain text line for the GUI
// output panel.
func formatRunLine(ev resource.Event) string {
	if ev.Source == resource.SourceCFN {
		if ev.Stack == nil {
			return ""
		}
		return fmt.Sprintf("[cfn] %s %s %s", ev.Stack.ResourceStatus, ev.Stack.ResourceType, ev.Stack.LogicalResourceID)
	}
	return ev.Line
}
