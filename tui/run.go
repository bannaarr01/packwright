package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/action/dispatch"
	"github.com/bannaarr01/packwright/action/resource"
	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	imanifest "github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/scaling"
	amanifest "github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/pack"
	"github.com/bannaarr01/packwright/tui/screens"
)

// This file is the TUI's bridge from a palette pick to the action engine. The
// palette used to be a "log the selection" UX (handlePaletteSelection returned
// nil for every slash that was not /ai, /audit, or /profile); manifest-backed
// slashes — the reference /alb deploy and every pack command — therefore never
// ran. startManifestRun resolves the picked slash to a manifest, collects its
// form inputs through the (previously orphaned) screens.FormScreen, and runs it
// through action/dispatch.Dispatch, streaming the engine's output into a
// runScreen modelled on the chat panel's reader-rearm loop.

// pendingRunKind distinguishes what handleFormSubmit does with the submitted
// form values: launch a fresh deploy, or compute scaling params and update.
type pendingRunKind int

const (
	// pendingDeploy: the form collects a manifest's inputs for a fresh run.
	pendingDeploy pendingRunKind = iota
	// pendingScale: the form collects scaling deltas for an existing stack.
	pendingScale
)

// pendingRun records the action awaiting form input while its form is on
// screen. handleFormSubmit reads it back once the user submits. A nil
// pendingRun means no run is awaiting form input.
type pendingRun struct {
	kind pendingRunKind
	// Deploy fields.
	m       *amanifest.Manifest
	baseDir string
	// Scale fields.
	stack        string
	rec          *record.StackRecord
	specs        []scaling.Spec
	templatePath string
}

// runReadyMsg carries the outcome of starting the dispatch off the UI
// goroutine. For a resource manifest the engine is asynchronous, so result is
// non-nil and its Events channel is live; for a synchronous kind (shell /
// composite) result is nil and the runner has already finished. A non-nil err
// means the dispatch could not start (or the synchronous runner failed).
type runReadyMsg struct {
	result *resource.Result
	value  any
	err    error
}

// runOutputMsg delivers one streamed engine event. ok is false when the Events
// channel has closed; ch is carried so Update can re-arm the reader for the
// next event (the same pattern chatModel uses for chat.Event).
type runOutputMsg struct {
	ev resource.Event
	ch <-chan resource.Event
	ok bool
}

// runDoneMsg carries the deploy's final exit error, read from Result.Wait once
// the Events channel has drained.
type runDoneMsg struct{ err error }

// runScreen renders a manifest run: a scrolling transcript of the engine's
// stdout / stderr / CFN events plus a terminal status line. It satisfies
// screens.Screen directly (no wrapper) because, unlike chatModel, it has a
// pointer-receiver Update already shaped like the interface.
type runScreen struct {
	keys    KeyMap
	logger  *slog.Logger
	slash   string
	m       *amanifest.Manifest
	baseDir string
	inputs  action.Inputs
	cfg     *config.Config
	home    string

	width, height int
	vp            viewport.Model
	lines         []string
	result        *resource.Result
	done          bool
	runErr        error
	keymap        runKeyMap
}

// runKeyMap holds the screen-local bindings rendered on the footer.
type runKeyMap struct{ Back key.Binding }

// newRunScreen builds the screen sized to w×h. Init starts the dispatch.
func newRunScreen(keys KeyMap, logger *slog.Logger, w, h int, cfg *config.Config, home string, m *amanifest.Manifest, baseDir string, inputs action.Inputs) *runScreen {
	s := &runScreen{
		keys:    keys,
		logger:  logger,
		slash:   m.Slash,
		m:       m,
		baseDir: baseDir,
		inputs:  inputs,
		cfg:     cfg,
		home:    home,
		keymap:  runKeyMap{Back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))},
	}
	s.lines = []string{runDimStyle.Render("Starting " + m.Slash + " …")}
	s.vp = viewport.New(max(1, w), max(1, h-2))
	s.setSize(w, h)
	return s
}

// Init starts the dispatch off the UI goroutine.
func (s *runScreen) Init() tea.Cmd { return s.startCmd() }

// startCmd builds the AWS client, threads the manifest's base directory and the
// client onto the dispatch context, and calls dispatch.Dispatch. The surface
// label is left to bootstrap's SetDefaultSurface("tui"), so usage events are
// still tagged without an explicit WithSurface here.
func (s *runScreen) startCmd() tea.Cmd {
	m, baseDir, inputs := s.m, s.baseDir, s.inputs
	home, logger := s.home, s.logger
	profile, region := "", ""
	if s.cfg != nil {
		profile, region = s.cfg.Profile, s.cfg.Region
	}
	// Only resource and composite kinds touch AWS; building a client for a
	// scaffold wizard (/new-command, /new-pack) or a shell action would be a
	// wasted — and possibly interactive/slow — credential resolution.
	needsAWS := s.m.Kind == amanifest.KindResource || s.m.Kind == amanifest.KindComposite
	return func() tea.Msg {
		ctx := context.Background()
		var client *awsx.Client
		if needsAWS {
			// Best-effort: a resource manifest needs a client and the engine
			// surfaces its own "awsClient is nil" if this failed.
			c, err := awsx.New(ctx, profile, region, home, logger)
			if err != nil && logger != nil {
				logger.Warn("tui: run: build aws client", slog.Any("err", err))
			}
			client = c
		}
		ctx = dispatch.WithAWSClient(ctx, client)
		ctx = dispatch.WithBaseDir(ctx, baseDir)
		res, derr := dispatch.Dispatch(ctx, m, inputs)
		if derr != nil {
			return runReadyMsg{err: derr}
		}
		rr, _ := res.Value.(*resource.Result)
		return runReadyMsg{result: rr, value: res.Value}
	}
}

// Update routes one message. Streaming events re-arm the reader until the
// channel closes, then Wait yields the final status.
func (s *runScreen) Update(msg tea.Msg) (screens.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case runReadyMsg:
		if msg.err != nil {
			s.fail(msg.err)
			return s, nil
		}
		if msg.result != nil {
			s.result = msg.result
			s.lines = []string{runDimStyle.Render("Running " + s.slash + " — streaming output (esc to leave).")}
			s.refresh()
			return s, readRunCmd(msg.result.Events)
		}
		// Synchronous kind: the runner already returned. Rich per-kind output
		// rendering (e.g. shell stdout) is a follow-up; report completion.
		s.done = true
		s.appendLine(runOKStyle.Render("✓ " + s.slash + " completed"))
		s.refresh()
		return s, nil

	case runOutputMsg:
		if !msg.ok {
			if s.result != nil {
				return s, waitRunCmd(s.result)
			}
			s.done = true
			s.refresh()
			return s, nil
		}
		if line := formatRunEvent(msg.ev); line != "" {
			s.appendLine(line)
			s.refresh()
		}
		return s, readRunCmd(msg.ch)

	case runDoneMsg:
		s.done = true
		if msg.err != nil {
			s.runErr = msg.err
			s.appendLine(runErrStyle.Render("✗ " + s.slash + " failed: " + oneLine(msg.err.Error())))
		} else {
			s.appendLine(runOKStyle.Render("✓ " + s.slash + " finished"))
		}
		s.refresh()
		return s, nil

	case tea.WindowSizeMsg:
		s.setSize(msg.Width, msg.Height)
		return s, nil

	case tea.KeyMsg:
		if key.Matches(msg, s.keymap.Back) {
			return s, func() tea.Msg { return screens.PopMsg{} }
		}
		var cmd tea.Cmd
		s.vp, cmd = s.vp.Update(msg)
		return s, cmd
	}

	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return s, cmd
}

// fail records a terminal error onto the transcript.
func (s *runScreen) fail(err error) {
	s.done = true
	s.runErr = err
	s.appendLine(runErrStyle.Render("✗ " + oneLine(err.Error())))
	s.refresh()
}

// appendLine adds one finished transcript line.
func (s *runScreen) appendLine(l string) { s.lines = append(s.lines, l) }

// setSize lays out the viewport (one line reserved for the footer hint).
func (s *runScreen) setSize(w, h int) {
	s.width, s.height = w, h
	s.vp.Width = max(1, w)
	s.vp.Height = max(1, h-2)
	s.refresh()
}

// refresh re-renders the transcript and pins the view to the bottom.
func (s *runScreen) refresh() {
	s.vp.SetContent(lipgloss.NewStyle().Width(max(1, s.width)).Render(strings.Join(s.lines, "\n")))
	s.vp.GotoBottom()
}

// View renders the transcript plus a one-line footer hint.
func (s *runScreen) View() string {
	footer := runDimStyle.Render("esc to return")
	if !s.done && s.result != nil {
		footer = runDimStyle.Render("streaming… esc to leave (the deploy keeps running)")
	}
	return lipgloss.JoinVertical(lipgloss.Left, s.vp.View(), footer)
}

// SetSize satisfies screens.Screen.
func (s *runScreen) SetSize(w, h int) { s.setSize(w, h) }

// KeyMap returns the screen-local bindings the footer renders.
func (s *runScreen) KeyMap() []key.Binding { return []key.Binding{s.keymap.Back} }

// Title is the label rendered above the content pane.
func (s *runScreen) Title() string { return "Run · " + s.slash }

// readRunCmd reads one event from the engine's channel; Update re-arms it after
// each event until the channel closes (ok == false).
func readRunCmd(ch <-chan resource.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		return runOutputMsg{ev: ev, ch: ch, ok: ok}
	}
}

// waitRunCmd blocks on Result.Wait (off the UI goroutine) once the event stream
// has drained, surfacing the deploy's final exit status as a runDoneMsg.
func waitRunCmd(r *resource.Result) tea.Cmd {
	return func() tea.Msg { return runDoneMsg{err: r.Wait()} }
}

// formatRunEvent renders one engine event for the transcript. Stdout passes
// through; stderr is reddened; CFN events render a compact status line.
func formatRunEvent(ev resource.Event) string {
	switch ev.Source {
	case resource.SourceStderr:
		return runErrStyle.Render(strings.TrimRight(ev.Line, "\n"))
	case resource.SourceCFN:
		if ev.Stack == nil {
			return ""
		}
		return runDimStyle.Render(fmt.Sprintf("[cfn] %-22s %-28s %s",
			ev.Stack.ResourceStatus, ev.Stack.ResourceType, ev.Stack.LogicalResourceID))
	default:
		return strings.TrimRight(ev.Line, "\n")
	}
}

// resolveManifestForSlash maps a palette slash to the manifest that would run
// for the bare invocation, plus the base directory its relative template /
// script paths resolve against. It mirrors buildPaletteLoader's data sources
// (user scope + discovered packs) and applies the same pinned-default ordering
// as the palette, so the manifest chosen here matches the row the user saw.
// A slash with no backing manifest returns ok=false (the caller no-ops).
func resolveManifestForSlash(home string, cfg *config.Config, slash string) (*amanifest.Manifest, string, bool) {
	var pinned map[string]string
	if cfg != nil {
		pinned = cfg.PinnedDefaults
	}
	return pack.ResolveRunnable(home, pinned, slash)
}

// toScreenFields converts the engine-shim manifest fields into the canonical
// internal/manifest.Field type that screens.FormScreen and the hint resolver
// consume. The two structs are field-for-field identical apart from the named
// FieldType; the manifest-declared validators are dropped because the form
// screen renders neither them nor needs them to collect input.
func toScreenFields(in []amanifest.Field) []imanifest.Field {
	out := make([]imanifest.Field, len(in))
	for i, f := range in {
		out[i] = imanifest.Field{
			ID:          f.ID,
			Label:       f.Label,
			Type:        imanifest.FieldType(f.Type),
			Placeholder: f.Placeholder,
			Required:    f.Required,
			Default:     f.Default,
			Min:         f.Min,
			Max:         f.Max,
			Values:      f.Values,
			DependsOn:   f.DependsOn,
		}
	}
	return out
}

// startManifestRun is the palette's default handler: resolve the picked slash
// to a manifest and either collect its inputs through a form (when it declares
// one) or launch the run immediately. Unknown slashes are a no-op — the overlay
// has already closed — so a stray pick never disrupts the shell.
func (a app) startManifestRun(slash string) (tea.Model, tea.Cmd) {
	m, baseDir, ok := resolveManifestForSlash(a.home, a.cfg, slash)
	if !ok {
		if a.logger != nil {
			a.logger.Info("palette: no manifest for slash", slog.String("slash", slash))
		}
		return a, nil
	}

	w, h := a.contentSize()
	if len(m.Form) == 0 {
		cmd := a.registry.Push(newRunScreen(a.keys, a.logger, w, h, a.cfg, a.home, m, baseDir, action.Inputs{}))
		a.focus = focusContent
		a.tree.Focus(false)
		a.applyContentSize()
		return a, cmd
	}

	a.pendingRun = &pendingRun{kind: pendingDeploy, m: m, baseDir: baseDir}
	title := m.Title
	if title == "" {
		title = m.Slash
	}
	cmd := a.registry.Push(screens.NewForm(title, toScreenFields(m.Form)))
	a.focus = focusContent
	a.tree.Focus(false)
	a.applyContentSize()
	return a, cmd
}

// handleFormSubmit launches the pending run once the user submits the input
// form. It swaps the form for the run screen in place (registry.Replace) so Esc
// from the run returns to the launcher rather than back to a stale form.
func (a app) handleFormSubmit(msg screens.FormSubmitMsg) (tea.Model, tea.Cmd) {
	// A workspace/template action (new-project, copy-template, …) parks a
	// pendingWorkspace instead of a pendingRun; it takes priority and runs
	// through the workspace.go form → notice flow.
	if a.pendingWorkspace != nil {
		pw := a.pendingWorkspace
		a.pendingWorkspace = nil
		return a.finishWorkspace(pw, msg.Values)
	}
	if a.pendingRun == nil {
		return a, nil
	}
	pr := a.pendingRun
	a.pendingRun = nil

	if pr.kind == pendingScale {
		return a.finishScale(pr, msg.Values)
	}

	inputs := make(action.Inputs, len(msg.Values))
	for k, v := range msg.Values {
		inputs[k] = v
	}

	w, h := a.contentSize()
	cmd := a.registry.Replace(newRunScreen(a.keys, a.logger, w, h, a.cfg, a.home, pr.m, pr.baseDir, inputs))
	a.focus = focusContent
	a.tree.Focus(false)
	a.applyContentSize()
	return a, cmd
}

// Run-screen styles, built once.
var (
	runDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	runOKStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("70"))
	runErrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)
