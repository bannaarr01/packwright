package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/config"
	imanifest "github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/scaling"
	"github.com/bannaarr01/packwright/internal/update"
	amanifest "github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/render/cfn"
	"github.com/bannaarr01/packwright/tui/screens"
)

// This file wires the sidebar RecordScreen's Update / Scale actions to the
// real in-place change-set flow (internal/update.Stack, ADR-0048). Both
// actions run the same coordinator — they differ only in how the caller
// computed the parameter map (current values for update, scaling.BuildParams
// for scale) — so one updateScreen drives both. The safety-critical
// replacement consent (ADR-0036) is bridged into the Bubble Tea loop the same
// way the AI write-consent is (see installReplacementConsentBridge in
// launch.go): update.Stack calls the gate from the off-UI dispatch goroutine,
// which sends a replacementConsentMsg to the program and blocks on the reply
// the modal fulfils from the user's keypress.

// replacementConsentFn is installed by Launch to bridge update.Stack's
// replacement-consent gate into the UI. The default denies (fail-closed) so a
// run started without the bridge — e.g. in a unit test that never reaches a
// replacement — never silently approves a destructive change.
var replacementConsentFn = func(_ context.Context, _ update.ReplacementPayload) update.ConsentDecision {
	return update.ConsentDeny
}

// changeSetAPIFor builds the CFN change-set client for a profile/region pair.
// It mirrors cmd_update's changeSetAPIFromClient. It is a package var so tests
// can inject a fake cfn.ChangeSetAPI without an AWS account.
var changeSetAPIFor = func(ctx context.Context, profile, region string) (cfn.ChangeSetAPI, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("update: load aws config: %w", err)
	}
	return cloudformation.NewFromConfig(cfg), nil
}

// updateReadyMsg carries the outcome of the update.Stack call made off the UI
// goroutine.
type updateReadyMsg struct {
	result *update.StackResult
	err    error
}

// replacementConsentMsg bridges update.Stack's replacement-consent gate into
// the event loop: the coordinator goroutine blocks on reply while the user
// answers the modal. Delivered via (*tea.Program).Send from the bridge.
type replacementConsentMsg struct {
	payload update.ReplacementPayload
	reply   chan update.ConsentDecision
}

// pendingReplacement holds an in-flight replacement-consent request.
type pendingReplacement struct {
	payload update.ReplacementPayload
	reply   chan update.ConsentDecision
}

// updatePhase is the updateScreen's small state machine.
type updatePhase int

const (
	// phaseConfirmScale shows the ADR-0049 env-guard confirmation before any
	// AWS call. Only entered when scaleReason is set (a require_confirmation
	// guard fired); plain updates skip straight to phaseRunning.
	phaseConfirmScale updatePhase = iota
	// phaseRunning is the change-set lifecycle executing off the UI goroutine.
	phaseRunning
	// phaseFinished is the terminal state — the diff / notice is on screen.
	phaseFinished
)

// updateScreen runs the change-set update (or scale) flow for one stack and
// renders the resulting diff and outcome. It satisfies screens.Screen.
type updateScreen struct {
	keys   KeyMap
	logger *slog.Logger

	label       string // "update" | "scale" — drives titles and copy
	stackName   string
	profile     string
	region      string
	templateB   string // resolved template body (read in the constructor)
	templateErr error  // a read error deferred to the run so it renders inline
	params      map[string]string
	prevParams  map[string]string
	description string
	scaleReason string // non-empty → env-guard confirmation before running

	width, height  int
	vp             viewport.Model
	lines          []string
	phase          updatePhase
	pendingReplace *pendingReplacement
	result         *update.StackResult
	back           key.Binding
}

// newUpdateScreen builds the flow. The template body is read eagerly so a
// missing template fails before any AWS call; the error is surfaced inline
// rather than panicking. scaleReason, when set, gates the run behind an
// env-guard confirmation (ADR-0049); description annotates the change set.
func newUpdateScreen(keys KeyMap, logger *slog.Logger, w, h int, label, stackName, profile, region, templatePath string, params, prevParams map[string]string, description, scaleReason string) *updateScreen {
	s := &updateScreen{
		keys:        keys,
		logger:      logger,
		label:       label,
		stackName:   stackName,
		profile:     profile,
		region:      region,
		params:      params,
		prevParams:  prevParams,
		description: description,
		scaleReason: scaleReason,
		back:        key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
	if body, err := os.ReadFile(templatePath); err != nil {
		s.templateErr = fmt.Errorf("read template %s: %w", templatePath, err)
	} else {
		s.templateB = string(body)
	}
	s.vp = viewport.New(max(1, w), max(1, h-2))
	s.resetIntro()
	s.setSize(w, h)
	return s
}

// resetIntro seeds the transcript for the screen's initial phase.
func (s *updateScreen) resetIntro() {
	if s.scaleReason != "" {
		s.phase = phaseConfirmScale
		s.lines = []string{
			updateWarnStyle.Render("Confirm " + s.label + ": " + s.scaleReason),
			updateDimStyle.Render("[y] proceed   [n/esc] cancel"),
		}
		return
	}
	s.phase = phaseRunning
	s.lines = []string{updateDimStyle.Render(titleLabel(s.label) + " " + s.stackName + " — creating change set…")}
}

// Init starts the run immediately unless an env-guard confirmation is pending.
func (s *updateScreen) Init() tea.Cmd {
	if s.phase == phaseRunning {
		return s.startCmd()
	}
	return nil
}

// startCmd runs update.Stack off the UI goroutine. The replacement-consent
// gate is the bridged replacementConsentFn, so a replacement pauses here for
// the modal the user answers on the main loop.
func (s *updateScreen) startCmd() tea.Cmd {
	if s.templateErr != nil {
		err := s.templateErr
		return func() tea.Msg { return updateReadyMsg{err: err} }
	}
	in := update.StackInput{
		StackName:          s.stackName,
		TemplateBody:       s.templateB,
		Parameters:         s.params,
		PreviousParameters: s.prevParams,
		Description:        s.description,
	}
	profile, region := s.profile, s.region
	return func() tea.Msg {
		ctx := context.Background()
		api, err := changeSetAPIFor(ctx, profile, region)
		if err != nil {
			return updateReadyMsg{err: err}
		}
		res, err := update.Stack(ctx, in, update.StackOptions{
			API:     api,
			Consent: replacementConsentFn,
		})
		return updateReadyMsg{result: &res, err: err}
	}
}

// Update routes one message through the screen's state machine.
func (s *updateScreen) Update(msg tea.Msg) (screens.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case updateReadyMsg:
		s.phase = phaseFinished
		if msg.err != nil {
			s.appendLine(updateErrStyle.Render("✗ " + s.label + " failed: " + oneLine(msg.err.Error())))
			s.refresh()
			return s, nil
		}
		s.result = msg.result
		s.renderResult(msg.result)
		return s, nil

	case replacementConsentMsg:
		s.pendingReplace = &pendingReplacement{payload: msg.payload, reply: msg.reply}
		s.refresh()
		return s, nil

	case tea.WindowSizeMsg:
		s.setSize(msg.Width, msg.Height)
		return s, nil

	case tea.KeyMsg:
		return s.handleKey(msg)
	}

	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return s, cmd
}

// handleKey resolves the active modal (scale confirmation or replacement
// consent) or, when none is open, the navigation keys.
func (s *updateScreen) handleKey(msg tea.KeyMsg) (screens.Screen, tea.Cmd) {
	answer := strings.ToLower(msg.String())

	if s.pendingReplace != nil {
		switch answer {
		case "y":
			s.pendingReplace.reply <- update.ConsentApprove
			s.pendingReplace = nil
			s.appendLine(updateDimStyle.Render("replacement approved — executing…"))
			s.refresh()
		case "n", "esc":
			s.pendingReplace.reply <- update.ConsentDeny
			s.pendingReplace = nil
			s.appendLine(updateDimStyle.Render("replacement denied — change set will be discarded"))
			s.refresh()
		}
		return s, nil
	}

	if s.phase == phaseConfirmScale {
		switch answer {
		case "y":
			s.phase = phaseRunning
			s.lines = []string{updateDimStyle.Render(titleLabel(s.label) + " " + s.stackName + " — creating change set…")}
			s.refresh()
			return s, s.startCmd()
		case "n", "esc":
			s.phase = phaseFinished
			s.appendLine(updateDimStyle.Render(s.label + " cancelled."))
			s.refresh()
			return s, nil
		}
		return s, nil
	}

	if key.Matches(msg, s.back) && s.phase == phaseFinished {
		return s, func() tea.Msg { return screens.PopMsg{} }
	}

	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return s, cmd
}

// renderResult writes the diff buckets and the outcome notice once the run
// finishes.
func (s *updateScreen) renderResult(r *update.StackResult) {
	s.lines = nil
	a, m, rp, dl := r.Diff.Counts()
	s.appendLine(updateTitleStyle.Render(fmt.Sprintf("%s · %s", titleLabel(s.label), s.stackName)))
	s.appendLine(updateDimStyle.Render(fmt.Sprintf("%d add · %d modify · %d replace · %d delete", a, m, rp, dl)))
	s.appendBucket("Add", r.Diff.Adds, updateAddStyle)
	s.appendBucket("Modify", r.Diff.Modifies, updateModStyle)
	s.appendBucket("Replace", r.Diff.Replaces, updateRepStyle)
	s.appendBucket("Remove", r.Diff.Deletes, updateErrStyle)
	if len(r.Diff.ParameterDeltas) > 0 {
		s.appendLine("")
		s.appendLine(updateTitleStyle.Render("parameters"))
		for _, p := range r.Diff.ParameterDeltas {
			marker := "  "
			if p.CausedReplacement {
				marker = updateRepStyle.Render(" !")
			}
			s.appendLine(fmt.Sprintf("%s %s: %s → %s", marker, p.Key, updateDimStyle.Render(p.Old), p.New))
		}
	}
	s.appendLine("")
	switch r.Outcome {
	case update.OutcomeExecuted:
		s.appendLine(updateOKStyle.Render("✓ " + r.Notice))
	case update.OutcomeNoChanges, update.OutcomeConsentDenied:
		s.appendLine(updateDimStyle.Render(r.Notice))
	default:
		s.appendLine(updateDimStyle.Render(r.Notice))
	}
	s.refresh()
}

// appendBucket renders one diff bucket (skipped when empty).
func (s *updateScreen) appendBucket(label string, rows []update.ResourceDelta, style lipgloss.Style) {
	if len(rows) == 0 {
		return
	}
	s.appendLine(style.Render(fmt.Sprintf("%s (%d)", label, len(rows))))
	for _, r := range rows {
		line := "  " + r.LogicalID
		if r.ResourceType != "" {
			line += updateDimStyle.Render("  " + r.ResourceType)
		}
		s.appendLine(line)
	}
}

func (s *updateScreen) appendLine(l string) { s.lines = append(s.lines, l) }

func (s *updateScreen) setSize(w, h int) {
	s.width, s.height = w, h
	s.vp.Width = max(1, w)
	s.vp.Height = max(1, h-2)
	s.refresh()
}

func (s *updateScreen) refresh() {
	body := strings.Join(s.lines, "\n")
	if s.pendingReplace != nil {
		body += "\n\n" + s.renderReplacementModal()
	}
	s.vp.SetContent(lipgloss.NewStyle().Width(max(1, s.width)).Render(body))
	s.vp.GotoBottom()
}

// renderReplacementModal renders the ADR-0036 replacement-consent prompt: the
// resources CFN would destroy and recreate, and the y/n bindings.
func (s *updateScreen) renderReplacementModal() string {
	p := s.pendingReplace.payload
	var b strings.Builder
	b.WriteString(updateErrStyle.Render(fmt.Sprintf("⚠ This update REPLACES %d resource(s)", p.Count)) + "\n")
	for _, r := range p.Rows {
		b.WriteString("  - " + r.LogicalID + " (" + r.ResourceType + ")")
		if len(r.PropertyCauses) > 0 {
			b.WriteString(updateDimStyle.Render(" — " + strings.Join(r.PropertyCauses, ", ") + " changed"))
		}
		b.WriteString("\n")
	}
	b.WriteString(updateDimStyle.Render("A replacement destroys and recreates the resource; data is not migrated.\n"))
	b.WriteString(updateDimStyle.Render("[y] approve   [n/esc] deny"))
	return updateModalStyle.Render(b.String())
}

// View renders the transcript plus a footer hint reflecting the phase.
func (s *updateScreen) View() string {
	footer := updateDimStyle.Render("working…")
	switch {
	case s.pendingReplace != nil:
		footer = updateDimStyle.Render("answer the replacement prompt above")
	case s.phase == phaseConfirmScale:
		footer = updateDimStyle.Render("[y] proceed   [n/esc] cancel")
	case s.phase == phaseFinished:
		footer = updateDimStyle.Render("esc to return")
	}
	return lipgloss.JoinVertical(lipgloss.Left, s.vp.View(), footer)
}

// SetSize satisfies screens.Screen.
func (s *updateScreen) SetSize(w, h int) { s.setSize(w, h) }

// KeyMap returns the screen-local binding rendered on the footer.
func (s *updateScreen) KeyMap() []key.Binding { return []key.Binding{s.back} }

// Title is the label rendered above the content pane.
func (s *updateScreen) Title() string {
	return titleLabel(s.label) + " · " + s.stackName
}

// handleRecordAction resolves the stack the user acted on (loading its record
// + manifest) and pushes the matching flow. A missing store / record / manifest
// is logged and ignored so a stray keypress never crashes the shell.
func (a app) handleRecordAction(m screens.RecordActionMsg) (tea.Model, tea.Cmd) {
	if a.store == nil {
		return a, nil
	}
	rec, err := a.store.Read(m.Project, m.Env, m.Stack)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("tui: record action: read record", slog.String("stack", m.Stack), slog.Any("err", err))
		}
		return a, nil
	}
	mf, err := amanifest.Load(rec.Manifest.Source)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("tui: record action: load manifest", slog.String("source", rec.Manifest.Source), slog.Any("err", err))
		}
		return a, nil
	}
	switch m.Action {
	case screens.RecordActionUpdate:
		return a.startStackUpdate(m.Stack, rec, mf)
	case screens.RecordActionScale:
		return a.startStackScale(m.Stack, rec, mf)
	case screens.RecordActionDelete:
		return a.startStackDelete(m.Stack, rec, mf)
	}
	return a, nil
}

// startStackUpdate pushes the in-place update flow (ADR-0048). No form is
// shown: the update applies the manifest's current template to the deployed
// stack using its current parameters, and the change-set diff shows what that
// implies before any write (replacements gated by the consent modal).
func (a app) startStackUpdate(stack string, rec *record.StackRecord, mf *amanifest.Manifest) (tea.Model, tea.Cmd) {
	templatePath := resolveManifestTemplatePath(rec.Manifest.Source, mf)
	if templatePath == "" {
		if a.logger != nil {
			a.logger.Warn("tui: update: manifest has no template path", slog.String("stack", stack))
		}
		return a, nil
	}
	current := map[string]string(rec.Parameters)
	w, h := a.contentSize()
	us := newUpdateScreen(a.keys, a.logger, w, h, "update", stack,
		profileOf(rec, a.cfg), regionOf(rec, a.cfg), templatePath,
		current, current, "packwright /update", "")
	cmd := a.registry.Push(us)
	a.focus = focusContent
	a.tree.Focus(false)
	a.applyContentSize()
	return a, cmd
}

// startStackScale opens the scaling form (pre-filled with current values) for a
// manifest that declares a scaling block; the submit handler runs
// scaling.BuildParams and pushes the update flow with HistoryKind=scale.
func (a app) startStackScale(stack string, rec *record.StackRecord, mf *amanifest.Manifest) (tea.Model, tea.Cmd) {
	if len(mf.Scaling) == 0 {
		if a.logger != nil {
			a.logger.Info("tui: scale: manifest declares no scaling block", slog.String("stack", stack))
		}
		return a, nil
	}
	templatePath := resolveManifestTemplatePath(rec.Manifest.Source, mf)
	if templatePath == "" {
		return a, nil
	}
	specs := manifestScalingSpecs(mf.Scaling)
	current := map[string]string(rec.Parameters)
	form := scaling.BuildForm(stack, rec.Env, current, specs)

	fields := make([]imanifest.Field, 0, len(form.Targets))
	initial := make(map[string]string, len(form.Targets))
	for _, t := range form.Targets {
		label := t.Spec.Label
		if label == "" {
			label = t.Spec.Param
		}
		fields = append(fields, imanifest.Field{
			ID:    t.Spec.Param,
			Label: label,
			Type:  imanifest.FieldType(t.Spec.Kind),
		})
		initial[t.Spec.Param] = t.Current
	}

	a.pendingRun = &pendingRun{
		kind:         pendingScale,
		stack:        stack,
		rec:          rec,
		specs:        specs,
		templatePath: templatePath,
	}
	cmd := a.registry.Push(screens.NewFormWithValues("Scale "+stack, fields, initial))
	a.focus = focusContent
	a.tree.Focus(false)
	a.applyContentSize()
	return a, cmd
}

// finishScale runs scaling.BuildParams against the submitted deltas, logs any
// env-guard clamps (ADR-0049 forbids silent clamps), and pushes the update
// flow with the post-clamp parameters and any required env-guard consent.
func (a app) finishScale(pr *pendingRun, values map[string]string) (tea.Model, tea.Cmd) {
	current := map[string]string(pr.rec.Parameters)
	res, err := scaling.BuildParams(current, values, pr.rec.Env, pr.specs)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("tui: scale: build params", slog.String("stack", pr.stack), slog.Any("err", err))
		}
		return a, nil
	}
	for _, c := range res.Clamps {
		if a.logger != nil {
			a.logger.Warn("scaling clamp",
				slog.String("param", c.Param), slog.String("env", c.Env),
				slog.String("requested", c.Requested), slog.String("effective", c.Effective))
		}
	}
	scaleReason := ""
	if res.RequireConsent {
		scaleReason = res.ConsentReason
	}
	w, h := a.contentSize()
	us := newUpdateScreen(a.keys, a.logger, w, h, "scale", pr.stack,
		profileOf(pr.rec, a.cfg), regionOf(pr.rec, a.cfg), pr.templatePath,
		res.Params, current, "packwright /scale on "+pr.rec.Env+" env", scaleReason)
	cmd := a.registry.Replace(us)
	a.focus = focusContent
	a.tree.Focus(false)
	a.applyContentSize()
	return a, cmd
}

// startStackDelete is the cascading-delete entry point (ADR-0053). The
// cascading-delete engine (internal/delete) and the MVP-6 deletion tray
// (reachable via /audit) own the destructive machinery; rather than duplicate
// a second destructive path here, this surfaces where deletion lives.
func (a app) startStackDelete(stack string, _ *record.StackRecord, _ *amanifest.Manifest) (tea.Model, tea.Cmd) {
	if a.logger != nil {
		a.logger.Info("tui: delete requested from record screen", slog.String("stack", stack))
	}
	cmd := a.registry.Push(newNoticeScreen("Delete "+stack,
		"Stack deletion runs through the audit deletion tray.\n\n"+
			"Open the command palette (ctrl+p) and pick /audit to stage this\n"+
			"stack for deletion with dependency-aware batch consent, or run\n"+
			"`packwright delete-resource` for the headless cascading-delete flow."))
	a.focus = focusContent
	a.tree.Focus(false)
	a.applyContentSize()
	return a, cmd
}

// resolveManifestTemplatePath resolves the manifest's template path against the
// manifest file's own directory (mirroring the engine's WithBaseDir contract).
func resolveManifestTemplatePath(manifestSource string, mf *amanifest.Manifest) string {
	if mf.Template == nil || mf.Template.Path == "" {
		return ""
	}
	if filepath.IsAbs(mf.Template.Path) {
		return mf.Template.Path
	}
	return filepath.Join(filepath.Dir(manifestSource), mf.Template.Path)
}

// profileOf / regionOf prefer the stack record's recorded profile/region (the
// account the stack actually lives in) and fall back to the active config.
func profileOf(rec *record.StackRecord, cfg *config.Config) string {
	if rec != nil && rec.Profile != "" {
		return rec.Profile
	}
	if cfg != nil {
		return cfg.Profile
	}
	return ""
}

func regionOf(rec *record.StackRecord, cfg *config.Config) string {
	if rec != nil && rec.Region != "" {
		return rec.Region
	}
	if cfg != nil {
		return cfg.Region
	}
	return ""
}

// manifestScalingSpecs converts the manifest's scaling entries into the runtime
// scaling.Spec the scaling package operates on (mirrors cmd_scale's converter
// for the root manifest type).
func manifestScalingSpecs(in []amanifest.ScalingSpec) []scaling.Spec {
	out := make([]scaling.Spec, 0, len(in))
	for _, s := range in {
		spec := scaling.Spec{
			Param:  s.Param,
			Label:  s.Label,
			Kind:   scaling.Kind(s.Kind),
			Min:    s.Min,
			Max:    s.Max,
			Step:   s.Step,
			Values: s.Values,
		}
		if len(s.EnvGuards) > 0 {
			spec.EnvGuards = make(map[string]scaling.EnvGuard, len(s.EnvGuards))
			for env, g := range s.EnvGuards {
				spec.EnvGuards[env] = scaling.EnvGuard{
					Min:                 g.Min,
					Max:                 g.Max,
					RequireConfirmation: g.RequireConfirmation,
				}
			}
		}
		out = append(out, spec)
	}
	return out
}

// titleLabel upper-cases the first byte of an ASCII label ("update" →
// "Update"). The labels are fixed lowercase literals, so this avoids pulling
// in golang.org/x/text just to capitalize a known word.
func titleLabel(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// noticeScreen is a minimal read-only screen that shows a title and a body
// paragraph with an esc-to-return binding. Used for short informational
// hand-offs (e.g. pointing the user at the deletion tray).
type noticeScreen struct {
	title         string
	body          string
	width, height int
	back          key.Binding
}

func newNoticeScreen(title, body string) *noticeScreen {
	return &noticeScreen{
		title: title,
		body:  body,
		back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
}

func (s *noticeScreen) Init() tea.Cmd { return nil }

func (s *noticeScreen) Update(msg tea.Msg) (screens.Screen, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && key.Matches(km, s.back) {
		return s, func() tea.Msg { return screens.PopMsg{} }
	}
	return s, nil
}

func (s *noticeScreen) View() string {
	return lipgloss.NewStyle().Padding(1, 2).Render(updateTitleStyle.Render(s.title) + "\n\n" + s.body)
}

func (s *noticeScreen) SetSize(w, h int) { s.width, s.height = w, h }

func (s *noticeScreen) KeyMap() []key.Binding { return []key.Binding{s.back} }

func (s *noticeScreen) Title() string { return s.title }

// Update-screen styles, built once.
var (
	updateDimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	updateTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	updateOKStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("70"))
	updateWarnStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("178"))
	updateErrStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	updateAddStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("70"))
	updateModStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))
	updateRepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	updateModalStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)
