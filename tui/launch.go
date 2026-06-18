package tui

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/bootstrap"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai/consent"
	"github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/pack"
)

// Launch starts the Packwright TUI and blocks until the user quits or the
// program errors. It is the entry point invoked by both the `tui` subcommand
// and the default no-args command (via cmd.TUILauncher).
//
// Cancelling ctx causes the underlying Bubble Tea program to exit.
//
// Behaviour beyond starting Bubble Tea:
//
//   - The palette is sourced from pack.LoadPalette over <config.Home>, so
//     packs under <home>/packs, user-scope manifests under <home>/commands
//     and <home>/monitors, and the built-in `/new-command` / `/new-pack`
//     wizards all show up alongside each other.
//   - A manifest watcher (internal/manifest.Watcher) subscribes to the same
//     roots and sends refreshPaletteMsg whenever a manifest file changes,
//     so edits propagate without restarting the TUI.
//
// A failure to resolve the home directory or to start the watcher is
// non-fatal: the TUI still launches, just without live palette data.
func Launch(ctx context.Context) error {
	logger := bootstrap.Init("tui")

	loader := buildPaletteLoader(logger)

	// Resolve the home + config once so the /ai dispatch can gate on
	// ai.Enabled and the chat panel can build a session. Failures are
	// non-fatal: the TUI still runs, just with AI defaulting to off.
	a := newApp(logger, loader)
	if home, err := config.Home(); err == nil {
		a.home = home
		a.store = record.NewStore(home)
	} else {
		logger.Warn("tui: resolve home", slog.Any("err", err))
	}
	if cfg, err := config.Load(); err == nil {
		a.cfg = cfg
	} else {
		logger.Warn("tui: load config", slog.Any("err", err))
	}
	a.rebuildTree()

	p := tea.NewProgram(a, tea.WithContext(ctx), tea.WithAltScreen())

	// Bridge the engine's synchronous write-consent prompt into the bubbletea
	// loop: ShowModal hands the request to the program and blocks on a reply
	// the chat panel fulfils from the user's keypress (ADR-0036). Restored on
	// exit so a later run (or a test) starts from the deny-all default.
	restoreModal := installConsentBridge(p)
	defer restoreModal()

	stopWatcher := startManifestWatcher(ctx, logger, p)
	defer stopWatcher()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("running tui program: %w", err)
	}
	return nil
}

// installConsentBridge points consent.ShowModal at the running program so the
// chat panel can render the modal and return the user's decision to the
// blocked engine goroutine. It returns a function that restores the previous
// modal func.
func installConsentBridge(p *tea.Program) func() {
	prev := consent.ShowModal
	consent.ShowModal = func(req consent.Request) consent.Decision {
		reply := make(chan consent.Decision, 1)
		p.Send(consentRequestMsg{req: req, reply: reply})
		return <-reply
	}
	return func() { consent.ShowModal = prev }
}

// buildPaletteLoader returns the closure the root model calls on every
// refreshPaletteMsg. It resolves the Packwright home once per invocation so
// a config relocation between reloads is honoured. On error it logs and
// returns nil so the palette renders empty rather than crashing.
func buildPaletteLoader(logger *slog.Logger) paletteLoader {
	return func() []list.Item {
		home, err := config.Home()
		if err != nil {
			logger.Warn("tui: palette: resolve home", slog.Any("err", err))
			return nil
		}
		cfg, err := config.Load()
		if err != nil {
			logger.Warn("tui: palette: load config", slog.Any("err", err))
			cfg = &config.Config{}
		}
		entries, err := pack.LoadPalette(home, cfg.PinnedDefaults)
		if err != nil {
			// Non-fatal: LoadPalette returns the entries that did parse.
			logger.Warn("tui: palette: partial load", slog.Any("err", err))
		}
		items := make([]list.Item, 0, len(entries))
		for _, e := range entries {
			items = append(items, paletteItem{slash: e.Slash, title: e.Title})
		}
		return items
	}
}

// startManifestWatcher spawns a goroutine that subscribes a manifest watcher
// to the Packwright home's manifest roots and forwards every debounced
// change as a refreshPaletteMsg on the program's input loop. The returned
// stop function closes the watcher and joins the goroutine.
//
// Any setup failure is logged and the function returns a no-op stop: the
// TUI still runs, just without live palette updates.
func startManifestWatcher(ctx context.Context, logger *slog.Logger, p *tea.Program) func() {
	home, err := config.Home()
	if err != nil {
		logger.Warn("tui: watcher: resolve home", slog.Any("err", err))
		return func() {}
	}
	w, err := manifest.NewWatcher(0)
	if err != nil {
		logger.Warn("tui: watcher: new", slog.Any("err", err))
		return func() {}
	}
	for _, root := range pack.WatchRoots(home) {
		if err := w.Add(root); err != nil {
			logger.Warn("tui: watcher: add root", slog.String("root", root), slog.Any("err", err))
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-w.Events():
				if !ok {
					return
				}
				p.Send(refreshPaletteMsg{})
			}
		}
	}()
	return func() {
		_ = w.Close()
		<-done
	}
}
