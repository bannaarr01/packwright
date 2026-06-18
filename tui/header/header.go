// Package header renders the top strip of the TUI shell: an embedded
// ASCII logo at wide terminals, or a single-line "packwright" wordmark
// when the logo would overflow.
//
// The header is intentionally dumb — it owns only the logo bytes and the
// styles. The root model passes the current terminal width to View() on
// every layout pass.
package header

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/tui/assets"
)

// wordmarkBreakpoint is the minimum terminal width (in columns) at which
// the multi-line ASCII logo renders. Below this, View() falls back to a
// single-line wordmark so the header doesn't wrap or clip.
const wordmarkBreakpoint = 80

// wordmark is the single-line label used at narrow widths and in
// fallback paths (e.g. when the embedded logo is empty in a test build).
const wordmark = "packwright"

// logoSource is the embedded ASCII art surfaced by the assets package.
// Pulled at init so the header doesn't pay the embed cost on every
// render.
var logoSource = assets.Logo()

// Header is the root-model-owned widget that renders the top strip.
// Construction is cheap; the same Header can be reused across renders.
type Header struct {
	style    lipgloss.Style
	wordmark lipgloss.Style
}

// New returns a Header ready to render. The dim style mirrors the
// chat/audit panels' header treatment so the shell feels cohesive.
func New() Header {
	return Header{
		style:    lipgloss.NewStyle().Foreground(lipgloss.Color("63")),
		wordmark: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")),
	}
}

// View renders the header at the given terminal width.
//
//   - width >= wordmarkBreakpoint and logoSource is non-empty: the
//     embedded multi-line logo, horizontally centred on the strip.
//   - otherwise: the single-line "packwright" wordmark, centred.
//
// View always returns a non-empty string so JoinVertical layout calls in
// the root model see a stable header height of 1 (wordmark) or
// len(logoSource) lines (logo).
func (h Header) View(width int) string {
	if width < wordmarkBreakpoint || strings.TrimSpace(logoSource) == "" {
		return centerLine(h.wordmark.Render(wordmark), width)
	}
	lines := strings.Split(strings.TrimRight(logoSource, "\n"), "\n")
	maxLine := 0
	for _, l := range lines {
		if w := lipgloss.Width(l); w > maxLine {
			maxLine = w
		}
	}
	if maxLine > width {
		// The embedded logo doesn't fit either — fall back to the
		// wordmark so we never clip mid-letter.
		return centerLine(h.wordmark.Render(wordmark), width)
	}
	for i, l := range lines {
		lines[i] = centerLine(h.style.Render(l), width)
	}
	return strings.Join(lines, "\n")
}

// Height returns the number of rows View(width) would emit. The root
// model uses it to subtract the header height from the available space
// before sizing the content pane and footer.
func (h Header) Height(width int) int {
	if width < wordmarkBreakpoint || strings.TrimSpace(logoSource) == "" {
		return 1
	}
	lines := strings.Split(strings.TrimRight(logoSource, "\n"), "\n")
	maxLine := 0
	for _, l := range lines {
		if w := lipgloss.Width(l); w > maxLine {
			maxLine = w
		}
	}
	if maxLine > width {
		return 1
	}
	return len(lines)
}

// centerLine pads s on the left so its content sits centred within
// width columns. Lines longer than width are returned unchanged — the
// caller already vetted them.
func centerLine(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	pad := (width - w) / 2
	return strings.Repeat(" ", pad) + s
}
