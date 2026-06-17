package tui

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/audit"
	"github.com/bannaarr01/packwright/internal/audit/cache"
	costpkg "github.com/bannaarr01/packwright/internal/audit/cost"
	auditdelete "github.com/bannaarr01/packwright/internal/audit/delete"
	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/postprocess"
	_ "github.com/bannaarr01/packwright/internal/audit/scanners"
)

// auditModel is the TUI sub-model that surfaces the /audit command:
// kicks off the read-only scan off the UI goroutine, streams per-
// scanner progress into a viewport, and after the run completes shows
// a sortable table where the user can stage rows into the deletion
// tray and trigger a typed-DELETE consent flow (ADR-0043). Every
// blocking AWS call happens inside a tea.Cmd so the bubbletea event
// loop stays responsive.
type auditModel struct {
	keys   KeyMap
	logger *slog.Logger
	cfg    *config.Config
	home   string

	width  int
	height int

	vp        viewport.Model
	lines     []string
	scanning  bool
	startedAt time.Time

	snapshot *cache.Snapshot
	cursor   int

	tray *auditdelete.Tray

	confirm    confirmState
	confirmBuf string

	status string
	errMsg string
}

// confirmState tracks the typed-DELETE batch consent modal lifecycle.
type confirmState int

const (
	confirmNone confirmState = iota
	confirmTyping
)

// newAuditModel builds an auditModel. The viewport is sized lazily
// once a WindowSizeMsg arrives so an unsized terminal (the case in
// tests) doesn't render a zero-height pane.
func newAuditModel(keys KeyMap, logger *slog.Logger, w, h int, cfg *config.Config, home string) auditModel {
	vp := viewport.New(w, max(1, h-6))
	return auditModel{
		keys:      keys,
		logger:    logger,
		cfg:       cfg,
		home:      home,
		width:     w,
		height:    h,
		vp:        vp,
		scanning:  true,
		startedAt: time.Now(),
	}
}

// initCmd kicks off the scan immediately on entry. The audit pipeline
// is read-only, so there is no consent gate — the user opted in by
// selecting /audit from the palette.
func (m auditModel) initCmd() tea.Cmd { return m.startScanCmd() }

// startScanCmd returns a tea.Cmd that runs the full audit pipeline
// (awsx → scanners → post-process → cache write) off the UI goroutine.
// The result lands as an auditDoneMsg.
func (m auditModel) startScanCmd() tea.Cmd {
	cfg, home := m.cfg, m.home
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		out := auditDoneMsg{startedAt: time.Now()}
		if cfg == nil {
			out.err = fmt.Errorf("audit: config not loaded")
			return out
		}
		client, err := awsx.New(ctx, cfg.Profile, cfg.Region, home, nil)
		if err != nil {
			out.err = fmt.Errorf("awsx: %w", err)
			return out
		}
		id, err := awsx.Verify(ctx, client)
		if err != nil {
			out.err = fmt.Errorf("sts verify: %w", err)
			return out
		}
		ac := audit.NewFromAWSX(client, id.Account, nil)
		scanners := audit.Default.All()

		events, result := audit.Run(ctx, scanners, ac, audit.RunOptions{})
		var progress []string
		for ev := range events {
			switch ev.Type {
			case audit.EventDone:
				progress = append(progress, fmt.Sprintf("  ✓ %s — %d", ev.Kind, ev.Count))
			case audit.EventError:
				progress = append(progress, fmt.Sprintf("  ✗ %s — %v", ev.Kind, ev.Err))
			case audit.EventWarn:
				progress = append(progress, fmt.Sprintf("  ! %s — %s", ev.Kind, ev.Msg))
			}
		}
		final := <-result

		postprocess.Apply(ctx, ac, final.Resources, postprocess.Options{Logger: nil})

		// Best-effort cache write.
		if store, serr := openTUICache(home); serr == nil && store != nil {
			lookback := postprocess.LookbackDays(postprocess.Options{})
			key := cache.Key{
				Profile:      profileSnapshotLabel(client.Profile()),
				Region:       client.Region(),
				LookbackDays: lookback,
			}
			_, _ = store.Refresh(ctx, key, func(context.Context) (cache.ScanResult, error) {
				return resourceSetToScanResult(final.Resources, final.Errors), nil
			}, cache.RefreshOptions{Force: true})
		}
		out.snapshot = buildSnapshot(client.Profile(), client.Region(), id.Account, final.Resources, final.Errors)
		out.progress = progress
		return out
	}
}

// Update implements tea.Model for the audit panel.
func (m auditModel) Update(msg tea.Msg) (auditModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.Width = msg.Width
		m.vp.Height = max(1, msg.Height-6)
		m.refreshViewport()
		return m, nil

	case auditDoneMsg:
		m.scanning = false
		m.lines = append(m.lines, msg.progress...)
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.refreshViewport()
			return m, nil
		}
		m.snapshot = msg.snapshot
		if tray, err := auditdelete.OpenTray(m.home); err == nil {
			m.tray = tray
		}
		m.refreshViewport()
		return m, nil

	case auditDeleteDoneMsg:
		m.confirm = confirmNone
		m.confirmBuf = ""
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		} else {
			m.status = fmt.Sprintf("Executed batch: %d deleted, %d failed", msg.deleted, msg.failed)
		}
		if tray, err := auditdelete.OpenTray(m.home); err == nil {
			m.tray = tray
		}
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		if m.confirm == confirmTyping {
			return m.handleConfirmKey(msg)
		}
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return leaveAuditMsg{} }
		case "r":
			if !m.scanning {
				m.scanning = true
				m.startedAt = time.Now()
				m.lines = nil
				m.errMsg = ""
				m.status = ""
				m.refreshViewport()
				return m, m.startScanCmd()
			}
			return m, nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.refreshViewport()
			}
			return m, nil
		case "down", "j":
			if m.snapshot != nil && m.cursor < len(m.snapshot.Resources)-1 {
				m.cursor++
				m.refreshViewport()
			}
			return m, nil
		case "a":
			if m.snapshot == nil || len(m.snapshot.Resources) == 0 {
				return m, nil
			}
			return m.handleAddToTray()
		case "d":
			if m.tray == nil || m.tray.Len() == 0 {
				m.status = "Tray is empty — press 'a' on a row to stage it first."
				m.refreshViewport()
				return m, nil
			}
			m.confirm = confirmTyping
			m.confirmBuf = ""
			m.errMsg = ""
			m.refreshViewport()
			return m, nil
		case "x":
			if m.tray != nil && m.tray.Len() > 0 {
				if err := m.tray.Clear(); err != nil {
					m.errMsg = err.Error()
				} else {
					m.status = "Tray cleared."
				}
				m.refreshViewport()
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// handleAddToTray adds the row under the cursor to the staging tray.
func (m auditModel) handleAddToTray() (auditModel, tea.Cmd) {
	if m.tray == nil {
		tray, err := auditdelete.OpenTray(m.home)
		if err != nil {
			m.errMsg = err.Error()
			m.refreshViewport()
			return m, nil
		}
		m.tray = tray
	}
	row := m.snapshot.Resources[m.cursor]
	res, ok := cacheResourceToDelete(row)
	if !ok {
		m.status = fmt.Sprintf("%s is not deletable from the audit tray (v1).", row.Kind)
		m.refreshViewport()
		return m, nil
	}
	if _, err := m.tray.Add(res, ""); err != nil {
		m.errMsg = err.Error()
	} else {
		m.status = fmt.Sprintf("Added %s %s to staging tray.", row.Kind, row.ID)
	}
	m.refreshViewport()
	return m, nil
}

// handleConfirmKey owns the keyboard while the typed-DELETE modal is
// open. Enter commits when the buffer equals "DELETE"; Esc cancels.
func (m auditModel) handleConfirmKey(msg tea.KeyMsg) (auditModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.confirm = confirmNone
		m.confirmBuf = ""
		m.refreshViewport()
		return m, nil
	case "backspace":
		if len(m.confirmBuf) > 0 {
			m.confirmBuf = m.confirmBuf[:len(m.confirmBuf)-1]
			m.refreshViewport()
		}
		return m, nil
	case "enter":
		if m.confirmBuf != auditdelete.ConfirmWord {
			m.errMsg = "Type DELETE exactly to confirm."
			m.refreshViewport()
			return m, nil
		}
		return m, m.executeBatchCmd()
	default:
		if len(msg.Runes) > 0 {
			m.confirmBuf += string(msg.Runes)
			m.refreshViewport()
		}
		return m, nil
	}
}

// executeBatchCmd runs the deletion executor off-UI and reports the
// totals via auditDeleteDoneMsg.
func (m auditModel) executeBatchCmd() tea.Cmd {
	cfg, home := m.cfg, m.home
	confirm := m.confirmBuf
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		tray, err := auditdelete.OpenTray(home)
		if err != nil {
			return auditDeleteDoneMsg{err: err}
		}
		rows := tray.List()
		decisions := make([]auditdelete.RowDecision, 0, len(rows))
		for _, r := range rows {
			decisions = append(decisions, auditdelete.RowDecision{RowID: r.ID, Selected: true})
		}
		batch := auditdelete.Batch{TypedConfirm: confirm, Decisions: decisions}

		client, err := awsx.New(ctx, cfg.Profile, cfg.Region, home, nil)
		if err != nil {
			return auditDeleteDoneMsg{err: err}
		}
		clients, err := newDeleteClients(ctx, client)
		if err != nil {
			return auditDeleteDoneMsg{err: err}
		}
		log, _ := auditdelete.OpenLog(home)
		ex := &auditdelete.Executor{Clients: clients, Log: log, RequestID: "tui"}
		deps := auditdelete.Probe(ctx, clients, rows)

		execErr := ex.Execute(ctx, rows, deps, batch)

		// Compute deleted/failed by re-reading the tray after Execute.
		out := auditDeleteDoneMsg{err: execErr}
		postTray, perr := auditdelete.OpenTray(home)
		if perr != nil {
			return out
		}
		post := postTray.List()
		for _, before := range rows {
			present := false
			for _, after := range post {
				if after.ID == before.ID {
					present = true
					break
				}
			}
			if present {
				out.failed++
			} else {
				out.deleted++
			}
		}
		return out
	}
}

// View renders the audit panel: a header, the viewport, and a footer.
func (m auditModel) View() string {
	header := auditHeaderStyle.Render("Audit")
	if m.cfg != nil {
		header += "  " + auditDimStyle.Render(fmt.Sprintf("profile=%s region=%s", m.cfg.Profile, m.cfg.Region))
	}
	footer := m.renderFooter()
	body := m.vp.View()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// renderFooter shows status / modal / hints depending on state.
func (m auditModel) renderFooter() string {
	if m.confirm == confirmTyping {
		modal := auditWarnStyle.Render(fmt.Sprintf("Type DELETE to execute batch (%d row[s]). Esc to cancel.", trayLen(m.tray)))
		return modal + "\n  > " + m.confirmBuf
	}
	hint := "esc back · r refresh · a stage · d execute batch · x clear tray · ↑/↓ select"
	out := auditDimStyle.Render(hint)
	if m.status != "" {
		out += "\n" + auditAccentStyle.Render(m.status)
	}
	if m.errMsg != "" {
		out += "\n" + auditErrorStyle.Render(m.errMsg)
	}
	return out
}

// refreshViewport rebuilds the viewport content from the model's
// current state.
func (m *auditModel) refreshViewport() {
	var b strings.Builder
	for _, l := range m.lines {
		b.WriteString(l + "\n")
	}
	if m.snapshot != nil {
		b.WriteString("\n")
		b.WriteString(renderResourceTable(m.snapshot.Resources, m.cursor, m.tray))
	}
	if m.scanning {
		b.WriteString(auditDimStyle.Render(fmt.Sprintf("\nScanning… (elapsed %s)", time.Since(m.startedAt).Round(time.Second))))
	}
	m.vp.SetContent(b.String())
}

// auditDoneMsg delivers the scan result back to the UI goroutine.
type auditDoneMsg struct {
	snapshot  *cache.Snapshot
	progress  []string
	err       error
	startedAt time.Time
}

// auditDeleteDoneMsg delivers the deletion-batch result back to the UI
// goroutine.
type auditDeleteDoneMsg struct {
	deleted int
	failed  int
	err     error
}

// leaveAuditMsg asks the root model to close the audit panel and
// return to the launcher.
type leaveAuditMsg struct{}

// ----------------- helpers -----------------

func openTUICache(home string) (*cache.Store, error) {
	dir := filepath.Join(home, "audit", "snapshots")
	return cache.NewStore(dir, cache.Config{})
}

func profileSnapshotLabel(p string) string {
	if p == "" {
		return "_default_"
	}
	return p
}

func resourceSetToScanResult(rs []audit.Resource, errs map[string]error) cache.ScanResult {
	out := cache.ScanResult{Resources: make([]cache.Resource, 0, len(rs))}
	seen := map[string]struct{}{}
	for i := range rs {
		out.Resources = append(out.Resources, cache.Resource{
			Kind:         rs[i].Kind,
			ID:           rs[i].ID,
			Region:       rs[i].Region,
			Account:      rs[i].Account,
			Name:         rs[i].Name,
			Tags:         rs[i].Tags,
			CreatedAt:    rs[i].CreatedAt,
			State:        rs[i].State,
			Raw:          rs[i].Raw,
			LastUsed:     rs[i].LastUsed,
			CostEstimate: rs[i].CostEstimate,
		})
		if _, ok := seen[rs[i].Kind]; !ok {
			seen[rs[i].Kind] = struct{}{}
			out.ScannersRun = append(out.ScannersRun, rs[i].Kind)
		}
	}
	sort.Strings(out.ScannersRun)
	for kind, err := range errs {
		out.ScannersSkipped = append(out.ScannersSkipped, cache.SkippedScanner{Kind: kind, Reason: err.Error()})
	}
	sort.Slice(out.ScannersSkipped, func(i, j int) bool {
		return out.ScannersSkipped[i].Kind < out.ScannersSkipped[j].Kind
	})
	return out
}

func buildSnapshot(profile, region, account string, rs []audit.Resource, errs map[string]error) *cache.Snapshot {
	scanRes := resourceSetToScanResult(rs, errs)
	return &cache.Snapshot{
		Version:         cache.SchemaVersion,
		ScannedAt:       time.Now().UTC(),
		Profile:         profileSnapshotLabel(profile),
		Account:         account,
		Region:          region,
		LookbackDays:    postprocess.LookbackDays(postprocess.Options{}),
		ScannersRun:     scanRes.ScannersRun,
		ScannersSkipped: scanRes.ScannersSkipped,
		Resources:       scanRes.Resources,
	}
}

func cacheResourceToDelete(r cache.Resource) (auditdelete.Resource, bool) {
	kind, ok := deletableKind(r.Kind)
	if !ok {
		return auditdelete.Resource{}, false
	}
	cost := 0.0
	if r.CostEstimate != nil {
		cost = r.CostEstimate.MonthlyUSD
	}
	return auditdelete.Resource{
		Kind:                kind,
		Identifier:          r.ID,
		Region:              r.Region,
		AccountID:           r.Account,
		Display:             r.Name,
		EstimatedMonthlyUSD: cost,
	}, true
}

func deletableKind(s string) (auditdelete.Kind, bool) {
	switch s {
	case "ec2/volume":
		return auditdelete.KindEC2Volume, true
	case "ec2/snapshot":
		return auditdelete.KindEC2Snapshot, true
	case "ec2/eip":
		return auditdelete.KindEC2EIP, true
	case "ec2/nat-gateway":
		return auditdelete.KindEC2NATGateway, true
	case "elbv2/target-group":
		return auditdelete.KindELBv2TargetGroup, true
	case "logs/log-group":
		return auditdelete.KindLogsLogGroup, true
	case "rds/db-snapshot":
		return auditdelete.KindRDSDBSnapshot, true
	case "ecr/repository":
		return auditdelete.KindECRImage, true
	}
	return "", false
}

func trayLen(t *auditdelete.Tray) int {
	if t == nil {
		return 0
	}
	return t.Len()
}

// renderResourceTable renders the inventory as a fixed-column ASCII
// table. The row under cursor is highlighted; tray-staged rows are
// prefixed with "*".
func renderResourceTable(rs []cache.Resource, cursor int, tray *auditdelete.Tray) string {
	if len(rs) == 0 {
		return auditDimStyle.Render("No resources discovered.")
	}
	staged := map[string]struct{}{}
	if tray != nil {
		for _, row := range tray.List() {
			staged[row.Resource.Identifier] = struct{}{}
		}
	}
	indexed := make([]int, len(rs))
	for i := range rs {
		indexed[i] = i
	}
	sort.Slice(indexed, func(i, j int) bool {
		ri, rj := rs[indexed[i]], rs[indexed[j]]
		ti, tj := lastUsedTime(ri), lastUsedTime(rj)
		switch {
		case ti.IsZero() && !tj.IsZero():
			return true
		case !ti.IsZero() && tj.IsZero():
			return false
		case ti.Before(tj):
			return true
		case tj.Before(ti):
			return false
		}
		return costOf(ri) > costOf(rj)
	})

	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %-24s %-32s %-14s %10s %s\n", "KIND", "NAME/ID", "LAST USED", "MONTHLY", "CONF"))
	for i, idx := range indexed {
		r := rs[idx]
		mark := "  "
		if i == cursor {
			mark = "> "
		} else if _, ok := staged[r.ID]; ok {
			mark = "* "
		}
		name := r.Name
		if name == "" {
			name = r.ID
		}
		if len(name) > 32 {
			name = name[:31] + "…"
		}
		lu := formatLastUsedTime(r.LastUsed)
		cost := formatCost(r.CostEstimate)
		conf := formatConf(r.LastUsed, r.CostEstimate)
		line := fmt.Sprintf("%s%-24s %-32s %-14s %10s %s\n", mark, r.Kind, name, lu, cost, conf)
		if i == cursor {
			b.WriteString(auditSelectionStyle.Render(line))
		} else {
			b.WriteString(line)
		}
	}
	return b.String()
}

func lastUsedTime(r cache.Resource) time.Time {
	if r.LastUsed == nil {
		return time.Time{}
	}
	return r.LastUsed.Best
}

func costOf(r cache.Resource) float64 {
	if r.CostEstimate == nil {
		return 0
	}
	return r.CostEstimate.MonthlyUSD
}

// formatLastUsedTime renders the LastUsed.Best timestamp as a relative
// age ("3d", "2mo", "—"). Falls back to the literal date when more
// than a year in the past.
func formatLastUsedTime(s *lastused.LastUsedSignal) string {
	if s == nil || s.Best.IsZero() {
		return "—"
	}
	d := time.Since(s.Best)
	switch {
	case d < 0:
		return s.Best.Format("2006-01-02")
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	default:
		return s.Best.Format("2006-01-02")
	}
}

// formatCost renders a CostEstimate as "$N.NN/mo" or "—" when unknown.
func formatCost(e *costpkg.CostEstimate) string {
	if e == nil || e.IsZero() {
		return "—"
	}
	return fmt.Sprintf("$%.2f", e.MonthlyUSD)
}

// formatConf renders the LastUsed + Cost confidences as a single
// short label, e.g. "L:med C:high".
func formatConf(s *lastused.LastUsedSignal, e *costpkg.CostEstimate) string {
	lc := "—"
	if s != nil {
		lc = string(s.Confidence.String()[0])
	}
	cc := "—"
	if e != nil {
		cc = string(e.Confidence.String()[0])
	}
	return fmt.Sprintf("L:%s C:%s", lc, cc)
}

// newDeleteClients constructs the AWS clients the deletion executor
// needs from the awsx.Client's underlying config.
func newDeleteClients(ctx context.Context, client *awsx.Client) (*auditdelete.Clients, error) {
	return auditdelete.NewClients(ctx, "tui", client)
}

// ----------------- styles -----------------

var (
	auditHeaderStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	auditDimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	auditAccentStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	auditWarnStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	auditErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	auditSelectionStyle = lipgloss.NewStyle().Background(lipgloss.Color("237"))
)
