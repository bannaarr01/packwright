package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai/chat"
	"github.com/bannaarr01/packwright/internal/ai/consent"
	"github.com/bannaarr01/packwright/internal/ai/cost"
)

// chatModel is the bubbletea sub-screen for the AI assistant (MVP-5 exit
// criterion 1). It owns a scrollback viewport, a prompt input, the live cost
// meter, and the write-consent modal. The heavy lifting — provider streaming,
// tool execution, redaction, metering — lives in the UI-agnostic
// [chat.Session]; this model only renders its [chat.Event] stream and brokers
// the consent decision back to the engine.
//
// When AI is disabled the model renders a hint pointing at `packwright ai
// setup` rather than a live session: per ADR-0033 the panel must open in both
// surfaces, but it cannot stream until the user has opted in.
type chatModel struct {
	keys   KeyMap
	logger *slog.Logger
	width  int
	height int

	cfg  *config.Config
	home string

	vp    viewport.Model
	input textinput.Model

	enabled  bool
	building bool
	session  *chat.Session

	lines []string        // committed transcript lines
	cur   strings.Builder // in-progress (streaming) assistant text

	snapshot cost.Snapshot
	haveSnap bool

	pending *pendingConsent
}

// pendingConsent holds an in-flight write-consent request: the engine's tool
// goroutine is blocked in [consent.ShowModal] waiting on reply while the user
// answers in the UI.
type pendingConsent struct {
	req   consent.Request
	reply chan consent.Decision
}

// newChatModel builds the panel sized to w×h. When enabled is true the caller
// follows up with buildSessionCmd to construct the live session asynchronously;
// otherwise the panel shows the setup hint.
func newChatModel(keys KeyMap, logger *slog.Logger, w, h int, cfg *config.Config, home string, enabled bool) chatModel {
	ti := textinput.New()
	ti.Placeholder = "Ask about a failure, or type a question…"
	ti.Prompt = "› "
	ti.CharLimit = 4000
	ti.Focus()

	m := chatModel{
		keys:    keys,
		logger:  logger,
		cfg:     cfg,
		home:    home,
		enabled: enabled,
		input:   ti,
		vp:      viewport.New(max(1, w), max(1, h-4)),
	}
	if enabled {
		m.building = true
		m.lines = []string{chatDimStyle.Render("Starting AI…")}
	} else {
		m.lines = disabledHint()
	}
	m.setSize(w, h)
	return m
}

// disabledHint is the transcript shown when AI is off: what the panel is and
// how to turn it on. The actual enabling happens in `packwright ai setup`.
func disabledHint() []string {
	return []string{
		chatTitleStyle.Render("AI assistant — disabled"),
		"",
		"Packwright's AI assistant is opt-in and off by default.",
		"It can explain failures, search logs, and propose fixes using",
		"read-only AWS tools; any change requires your explicit consent.",
		"",
		"To enable it, run:",
		chatToolStyle.Render("    packwright ai setup"),
		"",
		"That picks a provider/model, stores your API key in the OS keychain,",
		"and flips the gate. Then reopen /ai here.",
		"",
		chatDimStyle.Render("Press Esc to return."),
	}
}

// initCmd returns the command to run when the panel opens: build the session
// when enabled, nothing when disabled.
func (m chatModel) initCmd() tea.Cmd {
	if !m.enabled {
		return nil
	}
	return buildSessionCmd(m.cfg, m.home, m.logger)
}

// Update handles one message for the chat panel and returns the updated model.
// The root app routes chat-relevant messages here while in chat mode.
func (m chatModel) Update(msg tea.Msg) (chatModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)
		return m, nil

	case chatReadyMsg:
		m.building = false
		if msg.err != nil {
			m.lines = []string{chatErrStyle.Render("AI unavailable: " + oneLine(msg.err.Error()))}
			m.refreshViewport()
			return m, nil
		}
		m.session = msg.session
		m.snapshot = m.session.Snapshot()
		m.haveSnap = true
		m.lines = []string{chatDimStyle.Render(fmt.Sprintf(
			"AI ready — %s / %s. Type a message and press Enter; Esc to leave.",
			m.session.Provider(), m.session.Model()))}
		m.refreshViewport()
		return m, nil

	case aiStreamMsg:
		return m.handleStream(msg)

	case consentRequestMsg:
		m.pending = &pendingConsent{req: msg.req, reply: msg.reply}
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		if m.pending != nil {
			return m.handleConsentKey(msg)
		}
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// handleKey processes a keypress when no consent modal is open.
func (m chatModel) handleKey(msg tea.KeyMsg) (chatModel, tea.Cmd) {
	if key.Matches(msg, m.keys.ClosePalette) { // Esc leaves the panel
		return m, func() tea.Msg { return leaveChatMsg{} }
	}
	if msg.Type == tea.KeyEnter {
		if m.session == nil || m.building {
			return m, nil
		}
		if m.pending != nil {
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		m.appendLine(chatUserStyle.Render("you ") + text)
		m.cur.Reset()
		ch := m.session.Send(context.Background(), text)
		m.refreshViewport()
		return m, readChatCmd(ch)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleConsentKey maps a keypress to a consent decision while a write-tool
// modal is open, replies to the blocked engine goroutine, and dismisses it.
func (m chatModel) handleConsentKey(msg tea.KeyMsg) (chatModel, tea.Cmd) {
	var d consent.Decision
	switch strings.ToLower(msg.String()) {
	case "y":
		d = consent.ApproveOnce
	case "s":
		d = consent.ApproveSession
	case "n", "esc":
		d = consent.Deny
	default:
		return m, nil // ignore other keys while the modal is up
	}
	m.pending.reply <- d
	m.appendLine(chatToolStyle.Render(fmt.Sprintf("consent: %s → %s", m.pending.req.Tool, decisionWord(d))))
	m.pending = nil
	m.refreshViewport()
	return m, nil
}

// handleStream folds one streamed engine event into the transcript and either
// re-arms the reader (more events coming) or finalizes the turn (channel
// closed).
func (m chatModel) handleStream(msg aiStreamMsg) (chatModel, tea.Cmd) {
	if !msg.ok {
		m.commitAssistant()
		if m.session != nil {
			m.snapshot = m.session.Snapshot()
			m.haveSnap = true
		}
		m.refreshViewport()
		return m, nil
	}
	switch ev := msg.ev.(type) {
	case chat.TextEvent:
		m.cur.WriteString(ev.Text)
	case chat.ToolStartEvent:
		m.commitAssistant()
		m.appendLine(chatToolStyle.Render("⚙ " + ev.Name + " …"))
	case chat.ToolEndEvent:
		if ev.IsError {
			m.appendLine(chatErrStyle.Render("✗ " + ev.Name + ": " + oneLine(ev.Result)))
		} else {
			m.appendLine(chatToolStyle.Render("✓ " + ev.Name))
		}
	case chat.CapEvent:
		m.commitAssistant()
		m.appendLine(chatErrStyle.Render(fmt.Sprintf(
			"Budget cap reached (%s): $%.4f spent of $%.4f. Raise the cap in config.yaml and retry.",
			ev.Cap.Kind, ev.Cap.SpentUSD, ev.Cap.LimitUSD)))
	case chat.DoneEvent:
		m.commitAssistant()
	case chat.ErrorEvent:
		m.commitAssistant()
		m.appendLine(chatErrStyle.Render("Error: " + oneLine(ev.Err.Error())))
	}
	m.refreshViewport()
	return m, readChatCmd(msg.ch)
}

// commitAssistant flushes any buffered streaming text into a committed line.
func (m *chatModel) commitAssistant() {
	if m.cur.Len() == 0 {
		return
	}
	m.appendLine(chatAsstStyle.Render("ai  ") + m.cur.String())
	m.cur.Reset()
}

// appendLine adds a finished transcript line.
func (m *chatModel) appendLine(s string) { m.lines = append(m.lines, s) }

// setSize lays out the viewport and input for the given terminal dimensions.
// The header takes one line and the footer (input or modal) up to three.
func (m *chatModel) setSize(w, h int) {
	m.width, m.height = w, h
	bodyH := h - 5
	if bodyH < 1 {
		bodyH = 1
	}
	m.vp.Width = max(1, w)
	m.vp.Height = bodyH
	m.input.Width = max(1, w-4)
	m.refreshViewport()
}

// refreshViewport re-renders the transcript (committed lines plus any
// in-progress assistant text) and pins the view to the bottom.
func (m *chatModel) refreshViewport() {
	body := strings.Join(m.lines, "\n")
	if m.cur.Len() > 0 {
		body += "\n" + chatAsstStyle.Render("ai  ") + m.cur.String()
	}
	m.vp.SetContent(lipgloss.NewStyle().Width(max(1, m.width)).Render(body))
	m.vp.GotoBottom()
}

// View renders the header (provider/model + live cost meter), the transcript,
// and the footer (consent modal, prompt input, or a building indicator).
func (m chatModel) View() string {
	header := m.renderHeader()
	footer := ""
	switch {
	case m.pending != nil:
		footer = m.renderConsentModal()
	case m.building:
		footer = chatDimStyle.Render("starting…")
	case m.enabled && m.session != nil:
		footer = m.input.View()
	default:
		footer = chatDimStyle.Render("Esc to return")
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, m.vp.View(), footer)
}

// renderHeader shows the active provider/model and the always-visible cost
// meter (ADR-0039 / exit criterion 4).
func (m chatModel) renderHeader() string {
	title := "AI assistant"
	if m.session != nil {
		title = m.session.Provider() + " / " + m.session.Model()
	}
	meter := ""
	if m.haveSnap {
		meter = fmt.Sprintf("  •  session $%.4f  today $%.4f", m.snapshot.SessionUSD, m.snapshot.TodayUSD)
	}
	return chatHeaderStyle.Render("✦ "+title) + chatDimStyle.Render(meter)
}

// renderConsentModal renders the write-consent prompt (ADR-0036): the tool, the
// target, the AI's stated reason, and the key bindings.
func (m chatModel) renderConsentModal() string {
	r := m.pending.req
	var b strings.Builder
	b.WriteString(chatErrStyle.Render("⚠ AI requests a WRITE action") + "\n")
	b.WriteString("tool:   " + r.Tool + "\n")
	if r.Resource != "" {
		b.WriteString("target: " + r.Resource + "\n")
	}
	if r.Region != "" || r.Profile != "" {
		b.WriteString(fmt.Sprintf("where:  profile=%s region=%s\n", emptyDash(r.Profile), emptyDash(r.Region)))
	}
	if r.Reason != "" {
		b.WriteString("reason: " + r.Reason + "\n")
	}
	b.WriteString(chatDimStyle.Render("[y] approve once   [s] approve session   [n/Esc] deny"))
	return chatModalStyle.Render(b.String())
}

// decisionWord renders a consent decision for the transcript.
func decisionWord(d consent.Decision) string {
	switch d {
	case consent.ApproveOnce:
		return "approved (once)"
	case consent.ApproveSession:
		return "approved (session)"
	default:
		return "denied"
	}
}

// oneLine collapses whitespace/newlines so a multi-line error or tool result
// fits a single transcript line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// emptyDash renders an empty string as a dash for the consent modal.
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// buildSessionCmd constructs the AI session off the UI goroutine. It builds an
// AWS client best-effort (so the read/write AWS tools work); a failure there is
// non-fatal — the session still runs, just without AWS-backed tools.
func buildSessionCmd(cfg *config.Config, home string, logger *slog.Logger) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		var awsClient *awsx.Client
		if c, err := awsx.New(ctx, cfg.Profile, cfg.Region, home, logger); err == nil {
			awsClient = c
		}
		s, err := chat.New(ctx, chat.Options{Config: cfg, Home: home, AWS: awsClient})
		return chatReadyMsg{session: s, err: err}
	}
}

// readChatCmd reads one event from a turn's channel. Update re-arms it after
// each event until the channel closes (ok == false), which finalizes the turn.
func readChatCmd(ch <-chan chat.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		return aiStreamMsg{ev: ev, ch: ch, ok: ok}
	}
}

// Chat panel styles. Kept as package-level vars so they are built once.
var (
	chatHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	chatTitleStyle  = lipgloss.NewStyle().Bold(true)
	chatDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	chatUserStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	chatAsstStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("70"))
	chatToolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))
	chatErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	chatModalStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)
