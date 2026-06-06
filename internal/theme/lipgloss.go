package theme

import "github.com/charmbracelet/lipgloss"

// Styles is the bundle of pre-built Lipgloss styles the TUI consumes. Each
// field has a defined semantic role; callers pick the role, not the colour.
// New constructs a Styles from a concrete mode's palette.
//
// The set is intentionally narrow: anything wider gets enumerated as needed.
type Styles struct {
	// Base sets the foreground and background of plain body text. All other
	// styles inherit from it implicitly by being rendered on the same surface.
	Base lipgloss.Style

	// Header is the top-level heading style: accent-coloured, bold.
	Header lipgloss.Style

	// Subheader is a secondary heading: accent_alt, bold, not as loud as
	// Header.
	Subheader lipgloss.Style

	// Muted is for de-emphasised text — hints, timestamps, footers.
	Muted lipgloss.Style

	// Accent is the primary call-to-action / brand accent (typically green).
	Accent lipgloss.Style

	// Warn is for non-fatal warnings (yellow/orange).
	Warn lipgloss.Style

	// Error is for failures (red).
	Error lipgloss.Style

	// Success confirms a completed action (green, distinct from Accent so the
	// two can be combined on the same screen).
	Success lipgloss.Style

	// Card is a bordered container — used for panels, prompts, summaries.
	Card lipgloss.Style

	// Selection is the inverse style used for the highlighted row in a list.
	Selection lipgloss.Style

	// Border exposes the raw border colour for callers that need to render
	// their own frames (e.g. a custom divider).
	Border lipgloss.Style
}

// New builds a Styles for the given concrete mode. It returns an error if m is
// not ModeDark or ModeLight, or if the embedded palette fails to load (which
// is also caught at package init, so this branch is defence-in-depth).
func New(m Mode) (Styles, error) {
	if !m.IsConcrete() {
		return Styles{}, errUnknownMode(m)
	}
	t, err := Load(m)
	if err != nil {
		return Styles{}, err
	}
	return stylesFor(t), nil
}

// stylesFor maps a palette onto the Styles bundle. Kept separate from New so
// tests can exercise it with hand-built palettes.
func stylesFor(t Tokens) Styles {
	var (
		fg          = lipgloss.Color(t.FG)
		bg          = lipgloss.Color(t.BG)
		muted       = lipgloss.Color(t.Muted)
		accent      = lipgloss.Color(t.Accent)
		accentAlt   = lipgloss.Color(t.AccentAlt)
		warn        = lipgloss.Color(t.Warn)
		errColor    = lipgloss.Color(t.Error)
		success     = lipgloss.Color(t.Success)
		border      = lipgloss.Color(t.Border)
		selectionBG = lipgloss.Color(t.SelectionBG)
		selectionFG = lipgloss.Color(t.SelectionFG)
	)

	base := lipgloss.NewStyle().Foreground(fg).Background(bg)

	return Styles{
		Base:      base,
		Header:    base.Foreground(accent).Bold(true),
		Subheader: base.Foreground(accentAlt).Bold(true),
		Muted:     base.Foreground(muted),
		Accent:    base.Foreground(accent),
		Warn:      base.Foreground(warn),
		Error:     base.Foreground(errColor).Bold(true),
		Success:   base.Foreground(success),
		Card: base.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Padding(0, 1),
		Selection: lipgloss.NewStyle().
			Foreground(selectionFG).
			Background(selectionBG).
			Bold(true),
		Border: lipgloss.NewStyle().Foreground(border),
	}
}
