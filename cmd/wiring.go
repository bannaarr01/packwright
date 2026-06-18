package cmd

// wiring.go pulls the self-registering engine packages into the build graph so
// their init() functions run. Each imported package registers itself with a
// process-global registry — action.Register for action runners, the provider
// and tool registries for AI — from an init function. Without a blank import
// somewhere reachable from main, that init never fires, so the feature is
// silently absent at runtime even though it compiles and its own tests pass.
//
// This mirrors the seams that already exist for the other registries:
//   - action/dispatch/resource_runner.go registers the resource runner and is
//     pulled into the graph because bootstrap imports action/dispatch;
//   - internal/audit/scanners is blank-imported by cmd_audit.go and
//     tui/audit.go so the audit scanners register.
//
// Keeping all of these in one cmd-level file (cmd is unconditionally linked
// into every build via main -> cmd.Execute) guarantees the registrations are
// present for the TUI, the GUI, and the headless subcommands alike.

import (
	// Action runners register their manifest kinds with action.Register so
	// dispatch.Dispatch can route resource | shell | composite manifests.
	// (resource is already wired via action/dispatch/resource_runner.go.)
	// internal/ai/tools/all transitively imports internal/action/shell via
	// the write/shell tool, but the import is kept explicit here so the shell
	// runner stays wired independently of whether the AI tool catalogue does.
	_ "github.com/bannaarr01/packwright/internal/action/composite"
	_ "github.com/bannaarr01/packwright/internal/action/shell"

	// AI providers register with the provider registry so provider.Known() is
	// non-empty and chat.New can construct a session. Without these the AI
	// chat panel opens but reports "AI unavailable: no provider registered".
	_ "github.com/bannaarr01/packwright/internal/ai/provider/anthropic"
	_ "github.com/bannaarr01/packwright/internal/ai/provider/bedrock"
	_ "github.com/bannaarr01/packwright/internal/ai/provider/ollama"
	_ "github.com/bannaarr01/packwright/internal/ai/provider/openai"

	// The AI tool catalogue (read + write tools) registers with tools.Default
	// so the assistant has a non-empty tool list.
	_ "github.com/bannaarr01/packwright/internal/ai/tools/all"
)
