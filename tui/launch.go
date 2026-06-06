package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// Launch starts the Packwright TUI and blocks until the user quits or the
// program errors. It is the entry point invoked by both the `tui` subcommand
// and the default no-args command (via cmd.TUILauncher).
//
// Cancelling ctx causes the underlying Bubble Tea program to exit.
//
// Wave-1 dependencies (config, pack registry, theme) are not yet wired in;
// once they land the Launch signature will grow a Deps argument carrying
// them. Today's TUI runs standalone with a stderr slog logger.
func Launch(ctx context.Context) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	p := tea.NewProgram(newApp(logger), tea.WithContext(ctx), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("running tui program: %w", err)
	}
	return nil
}
