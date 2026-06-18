package tui

import (
	"log/slog"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai"
	"github.com/bannaarr01/packwright/internal/ai/consent"
	"github.com/bannaarr01/packwright/internal/audit"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/update"
	"github.com/bannaarr01/packwright/internal/workspace"
	"github.com/bannaarr01/packwright/tui/footer"
	"github.com/bannaarr01/packwright/tui/header"
	"github.com/bannaarr01/packwright/tui/screens"
	"github.com/bannaarr01/packwright/tui/sidebar"
)

// focus identifies which surface owns the keyboard. The shell holds
// exactly one focus state at a time; Tab cycles between sidebar and
// content, Ctrl+P parks focus on the palette overlay.
type focus int

const (
	focusSidebar focus = iota
	focusContent
	focusPalette
)

// paletteLoader is the source of palette rows the root model consults at
// startup and whenever a refreshPaletteMsg arrives. Launch supplies a
// real loader that calls pack.LoadPalette; tests pass nil or a stub so
// the root model stays independent of the discovery side effects.
type paletteLoader func() []list.Item

// app is the root tea.Model. It owns the layout (header / sidebar /
// content / footer), the screen registry that drives the content pane,
// and the focus state that decides which sub-model receives keystrokes.
type app struct {
	keys     KeyMap
	logger   *slog.Logger
	header   header.Header
	footer   footer.Footer
	tree     sidebar.Tree
	registry *screens.Registry
	palette  palette
	loadPal  paletteLoader
	focus    focus
	// prevFocus remembers the pre-overlay focus so closing the palette
	// returns the user to where they came from.
	prevFocus focus
	width     int
	height    int
	// cfg and home are set by Launch so the /ai dispatch can gate on
	// ai.Enabled, the chat panel can construct a session, and /profile
	// can verify identities through awsx. They are also re-used to
	// build the sidebar tree and resolve stack records.
	cfg   *config.Config
	home  string
	store *record.Store
	// pendingRun holds the manifest picked from the palette while its input
	// form is on screen; handleFormSubmit consumes it to launch the run.
	pendingRun *pendingRun
}

// newApp constructs the root model. logger receives palette-selection
// events; nil disables those log lines but is otherwise harmless.
// loader is consulted on startup (via the initial Init command) and on
// every refreshPaletteMsg; pass nil to start with an empty palette (the
// posture used by unit tests that exercise only key handling).
func newApp(logger *slog.Logger, loader paletteLoader) app {
	keys := DefaultKeyMap()
	a := app{
		keys:     keys,
		logger:   logger,
		header:   header.New(),
		footer:   footer.New(),
		tree:     sidebar.New(nil, nil),
		registry: screens.New(newLauncherScreen()),
		palette:  newPalette(keys),
		loadPal:  loader,
		focus:    focusSidebar,
	}
	a.tree.Focus(true)
	return a
}

// rebuildTree refreshes the sidebar widget from the currently-wired
// config + store. Called after Launch sets cfg/home/store so the first
// frame already reflects the user's real workspace; also called on a
// future config-changed signal once one lands.
func (a *app) rebuildTree() {
	var projects []workspace.Project
	if a.cfg != nil {
		projects = a.cfg.Projects
	}
	a.tree = sidebar.New(projects, a.store)
	a.tree.Focus(a.focus == focusSidebar)
}

// Init satisfies tea.Model. When a loader is configured, the root
// issues a single refreshPaletteMsg so the first frame renders against
// the real registry instead of an empty palette.
func (a app) Init() tea.Cmd {
	if a.loadPal == nil {
		return nil
	}
	return func() tea.Msg { return refreshPaletteMsg{} }
}

// Update implements tea.Model.
func (a app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.resize(m.Width, m.Height)
		return a, nil

	case refreshPaletteMsg:
		if a.loadPal != nil {
			a.palette.SetItems(a.loadPal())
		}
		return a, nil

	case closePaletteMsg:
		a.closePalette()
		return a, nil

	case screens.PopMsg:
		if a.registry.Pop() {
			a.refocusAfterPop()
			a.applyContentSize()
		}
		return a, nil

	case screens.PushMsg:
		cmd := a.registry.Push(m.Screen)
		a.focus = focusContent
		a.tree.Focus(false)
		a.applyContentSize()
		return a, cmd

	case sidebar.OpenStackMsg:
		rec := screens.NewRecord(m.Project, m.Env, m.Stack, a.store)
		cmd := a.registry.Push(rec)
		a.focus = focusContent
		a.tree.Focus(false)
		a.applyContentSize()
		return a, cmd

	case paletteSelectedMsg:
		return a.handlePaletteSelection(m)

	case screens.FormSubmitMsg:
		return a.handleFormSubmit(m)

	case screens.RecordActionMsg:
		return a.handleRecordAction(m)

	case consentRequestMsg:
		// A write-consent request must always be answered so the
		// engine's blocked tool goroutine never leaks: route to the
		// chat screen when it's on top, otherwise deny (fail-closed).
		if _, ok := a.registry.Top().(*chatScreen); ok {
			cmd := a.registry.UpdateTop(msg)
			return a, cmd
		}
		m.reply <- consent.Deny
		return a, nil

	case replacementConsentMsg:
		// Route the update coordinator's replacement-consent request to the
		// update screen when it's on top; otherwise deny (fail-closed) so the
		// blocked coordinator goroutine never leaks.
		if _, ok := a.registry.Top().(*updateScreen); ok {
			cmd := a.registry.UpdateTop(msg)
			return a, cmd
		}
		m.reply <- update.ConsentDeny
		return a, nil

	case ProfileSwitcherMsg:
		// The switcher emitted its result. Log and pop back to the
		// previous screen — the actual identity is captured on the
		// caller side (today: launcher) once persistence lands.
		if a.logger != nil {
			if m.Err != nil {
				a.logger.Warn("tui profile switch failed",
					slog.String("profile", m.Profile),
					slog.Any("err", m.Err))
			} else if m.Identity != nil {
				a.logger.Info("tui profile switched",
					slog.String("profile", m.Profile),
					slog.String("account", m.Identity.Account))
			}
		}
		if a.registry.Pop() {
			a.refocusAfterPop()
			a.applyContentSize()
		}
		return a, nil

	case chatReadyMsg, aiStreamMsg, auditDoneMsg, auditDeleteDoneMsg:
		// Engine results matter while their owning screen is on top;
		// otherwise they're dropped so a backgrounded turn doesn't
		// stomp on the active screen.
		switch a.registry.Top().(type) {
		case *chatScreen, *auditScreen:
			cmd := a.registry.UpdateTop(msg)
			return a, cmd
		}
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(m)
	}

	// Fallthrough: forward to the top screen.
	cmd := a.registry.UpdateTop(msg)
	return a, cmd
}

// handleKey routes one key to the right sub-model.
func (a app) handleKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C is the unconditional escape hatch.
	if m.String() == "ctrl+c" {
		return a, tea.Quit
	}

	// Palette overlay owns every key while open — including 'q' and '/'
	// for the filter input — so the user can type any character into the
	// fuzzy filter without quitting or popping.
	if a.focus == focusPalette {
		var cmd tea.Cmd
		a.palette, cmd = a.palette.Update(m)
		return a, cmd
	}

	// Global bindings that work in both sidebar and content focus.
	switch {
	case key.Matches(m, a.keys.OpenPalette):
		a.openPalette()
		return a, nil
	case key.Matches(m, a.keys.FocusCycle):
		a.cycleFocus()
		return a, nil
	}

	// 'q' as a global quit only fires when we're not inside an editor
	// (chat input, form, palette filter). The chat and audit screens
	// keep 'q' for their own use; for them we forward straight to the
	// top screen below. The launcher has no input fields, so 'q' there
	// quits as the existing behaviour expects.
	if a.focus == focusSidebar {
		if key.Matches(m, a.keys.Quit) {
			return a, tea.Quit
		}
		var cmd tea.Cmd
		a.tree, cmd = a.tree.Update(m)
		return a, cmd
	}

	// Content focus: forward to the top screen. Whether 'q' quits is
	// the screen's choice — the launcher returns nil and we apply the
	// fallback below; chat/audit consume 'q' as part of their input.
	cmd := a.registry.UpdateTop(m)
	if cmd == nil && a.registry.Depth() == 1 && key.Matches(m, a.keys.Quit) {
		return a, tea.Quit
	}
	return a, cmd
}

// handlePaletteSelection acts on the slash the user picked. Recognised
// slashes push a corresponding screen; unknown slashes just close the
// overlay (logging the pick for now — a future PR routes them to pack
// runtime handlers).
func (a app) handlePaletteSelection(m paletteSelectedMsg) (tea.Model, tea.Cmd) {
	if a.logger != nil {
		a.logger.Info("palette selection",
			slog.String("slash", m.Slash),
			slog.String("title", m.Title))
	}
	a.closePalette()

	switch m.Slash {
	case ai.SlashCommand:
		enabled := ai.Enabled(a.cfg)
		w, h := a.contentSize()
		cm := newChatModel(a.keys, a.logger, w, h, a.cfg, a.home, enabled)
		screen := newChatScreen(cm)
		cmd := a.registry.Push(screen)
		a.focus = focusContent
		a.tree.Focus(false)
		a.applyContentSize()
		return a, cmd

	case audit.SlashCommand:
		w, h := a.contentSize()
		am := newAuditModel(a.keys, a.logger, w, h, a.cfg, a.home)
		screen := newAuditScreen(am)
		cmd := a.registry.Push(screen)
		a.focus = focusContent
		a.tree.Focus(false)
		a.applyContentSize()
		return a, cmd

	case "/profile":
		profiles, _ := awsx.ListProfiles()
		active := ""
		if a.cfg != nil {
			active = a.cfg.Profile
		}
		ps := NewProfileSwitcher(a.keys, profiles, active, newTUIVerifier(a.home, a.logger), a.logger)
		screen := newProfileScreen(ps)
		cmd := a.registry.Push(screen)
		a.focus = focusContent
		a.tree.Focus(false)
		a.applyContentSize()
		return a, cmd

	default:
		// Any other slash is a manifest-backed command (the reference /alb
		// deploy, a pack action, a user-scope command): resolve it and run it
		// through the action engine. This is the path that turned the palette
		// from a "log the selection" stub into a working launcher.
		return a.startManifestRun(m.Slash)
	}
}

// openPalette parks focus on the overlay and records the previous focus
// so closePalette can restore it.
func (a *app) openPalette() {
	a.prevFocus = a.focus
	a.focus = focusPalette
	a.tree.Focus(false)
	w, h := a.contentSize()
	a.palette.SetSize(w, max(1, h))
}

// closePalette restores the focus that was active before the overlay
// opened.
func (a *app) closePalette() {
	a.focus = a.prevFocus
	if a.focus == focusPalette {
		a.focus = focusSidebar
	}
	a.tree.Focus(a.focus == focusSidebar)
}

// cycleFocus toggles between sidebar and content. Tab is a no-op while
// the overlay is open (handleKey routes overlay keys to the palette
// first).
func (a *app) cycleFocus() {
	if a.focus == focusSidebar {
		a.focus = focusContent
		a.tree.Focus(false)
		return
	}
	a.focus = focusSidebar
	a.tree.Focus(true)
}

// refocusAfterPop restores focus after the registry shrinks. When the
// new top is the launcher (depth 1), focus returns to the sidebar so
// the user can keep navigating the project tree.
func (a *app) refocusAfterPop() {
	if a.registry.Depth() == 1 {
		a.focus = focusSidebar
		a.tree.Focus(true)
		return
	}
	a.focus = focusContent
	a.tree.Focus(false)
}

// resize records the latest terminal size and reflows every component.
func (a *app) resize(w, h int) {
	a.width, a.height = w, h
	a.applyContentSize()
}

// applyContentSize tells the sidebar, content screens, and palette
// overlay how much room they have after the header and footer.
func (a *app) applyContentSize() {
	contentW, contentH := a.contentSize()
	sbW := sidebarWidth(a.width)
	sbH := contentH
	a.tree.SetSize(sbW, sbH)
	a.registry.SetSize(contentW, contentH)
	a.palette.SetSize(contentW, max(1, contentH-1))
}

// contentSize returns the width/height available to the content pane
// (right of the sidebar, between header and footer).
func (a *app) contentSize() (int, int) {
	if a.width <= 0 || a.height <= 0 {
		return 0, 0
	}
	headerH := a.header.Height(a.width)
	footerH := a.footer.Height()
	bodyH := a.height - headerH - footerH
	if bodyH < 1 {
		bodyH = 1
	}
	sbW := sidebarWidth(a.width)
	contentW := a.width - sbW - 1 // -1 for the separator column
	if contentW < 1 {
		contentW = 1
	}
	return contentW, bodyH
}

// View renders the shell: header strip, sidebar | content body, footer.
// When the palette overlay is open it replaces the content pane while
// the sidebar stays visible.
func (a app) View() string {
	if a.width == 0 || a.height == 0 {
		return ""
	}
	head := a.header.View(a.width)

	contentW, contentH := a.contentSize()
	sbW := sidebarWidth(a.width)

	sidebarStyle := lipgloss.NewStyle().Width(sbW).Height(contentH)
	contentStyle := lipgloss.NewStyle().Width(contentW).Height(contentH)
	sep := lipgloss.NewStyle().
		Width(1).
		Height(contentH).
		Foreground(lipgloss.Color("237")).
		Render(separatorCol(contentH))

	sb := sidebarStyle.Render(a.tree.View())
	var body string
	if a.focus == focusPalette {
		body = contentStyle.Render(a.palette.View())
	} else {
		body = contentStyle.Render(a.renderContent(contentW, contentH))
	}

	row := lipgloss.JoinHorizontal(lipgloss.Top, sb, sep, body)
	foot := a.footer.View(a.localKeys(), a.keys.GlobalBindings(), a.width)
	return lipgloss.JoinVertical(lipgloss.Left, head, row, foot)
}

// renderContent renders the active screen with a one-line title prefix.
func (a app) renderContent(w, h int) string {
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("63")).
		Width(w).
		Render(" " + a.registry.Top().Title())
	body := a.registry.Top().View()
	return lipgloss.JoinVertical(lipgloss.Left, title, body)
}

// localKeys returns the bindings the footer renders on its top line.
// The active surface (palette / sidebar / content) decides; the
// keyboard owner gets first say on what to advertise.
func (a app) localKeys() []key.Binding {
	switch a.focus {
	case focusPalette:
		return []key.Binding{a.keys.Up, a.keys.Down, a.keys.Select, a.keys.ClosePalette}
	case focusSidebar:
		return a.tree.KeyMap()
	default:
		return a.registry.Top().KeyMap()
	}
}

// sidebarWidth derives the sidebar width from the total terminal width.
// Capped at 28 so the content pane stays usable on wide terminals;
// floored at 16 so the tree labels keep their badges on narrow ones.
func sidebarWidth(total int) int {
	w := total / 4
	if w < 16 {
		w = 16
	}
	if w > 28 {
		w = 28
	}
	if w > total-10 {
		// On a very narrow terminal, prefer giving the content pane
		// some breathing room over the sidebar.
		w = max(8, total-10)
	}
	return w
}

// separatorCol returns a column of vertical bars exactly h rows tall.
func separatorCol(h int) string {
	if h <= 0 {
		return ""
	}
	out := make([]byte, 0, h*2)
	for i := 0; i < h; i++ {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, []byte("│")...)
	}
	return string(out)
}
