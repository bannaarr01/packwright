// Package sidebar renders the persistent left-hand tree of projects,
// environments, and stacks. The widget is rolled locally — bubbles/list
// is one-dimensional and its delegate machinery doesn't fit a
// Project → Env → Stack hierarchy well enough to justify the indirection.
//
// Data sources: workspace.Project trees (sourced from config.Config) and
// record.Store for the on-disk StackRecord lookups that supply the
// broad-status badges. The widget owns no state outside its own cursor
// and rendering; refresh is explicit (Refresh()) so the root model can
// re-run it on a config-changed signal.
package sidebar

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/workspace"
)

// nodeKind tags a flattened row by what it represents in the tree.
type nodeKind int

const (
	nodeProject nodeKind = iota
	nodeEnv
	nodeStack
	nodeEmpty
)

// row is one flattened, addressable line in the sidebar.
type row struct {
	kind    nodeKind
	depth   int
	label   string
	project string
	env     string
	stack   string
	status  record.BroadStatus
}

// OpenStackMsg is emitted when the user presses Enter on a Stack row.
// The root model consumes it and pushes the record screen for that
// stack onto the screen registry.
type OpenStackMsg struct {
	Project string
	Env     string
	Stack   string
}

// Tree is the sidebar widget. The root model holds one and forwards
// every key while focus is on the sidebar.
type Tree struct {
	projects []workspace.Project
	store    *record.Store
	rows     []row
	cursor   int
	width    int
	height   int
	focused  bool
	keymap   KeyMap
}

// KeyMap holds the sidebar-local bindings. Splitting them out lets the
// footer render the active screen's bindings without having to
// understand sidebar internals.
type KeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
}

// DefaultKeyMap returns the sidebar's stock bindings: j/k for movement
// (also arrow keys, supplied by the caller's wider keymap), Enter to
// activate the current row.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

// New constructs a tree against the given projects list and record
// store. Either source may be empty / nil — an empty workspace renders
// a one-row placeholder, and a nil store skips badge lookups (every
// stack row shows the "—" badge).
func New(projects []workspace.Project, store *record.Store) Tree {
	t := Tree{
		projects: projects,
		store:    store,
		keymap:   DefaultKeyMap(),
	}
	t.Refresh()
	return t
}

// Refresh rebuilds the flattened row list from the current projects +
// store contents. Cursor is clamped to the new length.
func (t *Tree) Refresh() {
	t.rows = flatten(t.projects, t.store)
	if t.cursor >= len(t.rows) {
		t.cursor = max(0, len(t.rows)-1)
	}
}

// SetSize records the latest sidebar dimensions. The widget renders
// against width/height at View time.
func (t *Tree) SetSize(w, h int) { t.width, t.height = w, h }

// Focus toggles whether the cursor row is highlighted. The root model
// flips it as Tab moves focus between sidebar and content.
func (t *Tree) Focus(b bool) { t.focused = b }

// KeyMap returns the sidebar-local bindings for the footer.
func (t Tree) KeyMap() []key.Binding {
	return []key.Binding{t.keymap.Up, t.keymap.Down, t.keymap.Select}
}

// Update handles one key while the sidebar has focus. Navigation keys
// move the cursor; Enter on a Stack row emits OpenStackMsg.
func (t Tree) Update(msg tea.Msg) (Tree, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return t, nil
	}
	switch {
	case key.Matches(km, t.keymap.Up):
		if t.cursor > 0 {
			t.cursor--
		}
	case key.Matches(km, t.keymap.Down):
		if t.cursor < len(t.rows)-1 {
			t.cursor++
		}
	case key.Matches(km, t.keymap.Select):
		if t.cursor < len(t.rows) && t.rows[t.cursor].kind == nodeStack {
			r := t.rows[t.cursor]
			return t, func() tea.Msg {
				return OpenStackMsg{Project: r.project, Env: r.env, Stack: r.stack}
			}
		}
	}
	return t, nil
}

// View renders the sidebar at the configured size. The active row gets a
// "▸" prefix and inverse styling when focused; otherwise it shows a
// fainter selection so the user can still see where they are.
func (t Tree) View() string {
	if len(t.rows) == 0 {
		return unfocusedSel.Render("  (no projects yet)")
	}
	var b strings.Builder
	for i, r := range t.rows {
		line := r.render()
		if i == t.cursor {
			if t.focused {
				line = focusedSel.Render("▸ " + strings.TrimPrefix(line, "  "))
			} else {
				line = unfocusedSel.Render("· " + strings.TrimPrefix(line, "  "))
			}
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// flatten walks the workspace tree into a single row list with indent
// depth pre-computed. Each env's stacks come from store.List(); when
// the store is nil or returns an error the env still renders, with an
// empty-state placeholder row underneath.
func flatten(ps []workspace.Project, store *record.Store) []row {
	out := make([]row, 0, 32)
	for _, p := range ps {
		out = append(out, row{kind: nodeProject, depth: 0, label: p.Name, project: p.Slug})
		for _, e := range p.Envs {
			out = append(out, row{kind: nodeEnv, depth: 1, label: e.Name, project: p.Slug, env: e.Slug})
			records := listStacks(store, p.Slug, e.Slug)
			if len(records) == 0 {
				out = append(out, row{kind: nodeEmpty, depth: 2, label: "(no stacks)"})
				continue
			}
			for _, rec := range records {
				out = append(out, row{
					kind:    nodeStack,
					depth:   2,
					label:   rec.StackName,
					project: p.Slug,
					env:     e.Slug,
					stack:   rec.StackName,
					status:  rec.Status.Broad,
				})
			}
		}
	}
	return out
}

// listStacks fetches the StackRecords for one (project, env) tuple. A
// nil store or any List() error yields an empty slice — the sidebar
// degrades gracefully rather than refusing to render the tree.
func listStacks(store *record.Store, project, env string) []*record.StackRecord {
	if store == nil {
		return nil
	}
	recs, err := store.List(project, env)
	if err != nil {
		return nil
	}
	return recs
}

// render formats a row's prefix (indent + glyph + label + badge) without
// any selection styling — the caller applies the cursor highlight.
func (r row) render() string {
	indent := strings.Repeat("  ", r.depth)
	switch r.kind {
	case nodeProject:
		return "  " + indent + projectStyle.Render("▾ "+r.label)
	case nodeEnv:
		return "  " + indent + envStyle.Render("▸ "+r.label)
	case nodeStack:
		badge := statusBadge(r.status)
		return "  " + indent + stackStyle.Render(r.label) + " " + badge
	case nodeEmpty:
		return "  " + indent + badgeDim.Render(r.label)
	}
	return "  " + r.label
}

// statusBadge maps a record.BroadStatus to a coloured one-word tag.
// Unknown values render as "—" so a missing or future status code
// doesn't blow up the layout.
func statusBadge(s record.BroadStatus) string {
	switch s {
	case record.BroadDeployed:
		return badgeOK.Render(string(s))
	case record.BroadDrifted:
		return badgeWarn.Render(string(s))
	case record.BroadFailed, record.BroadPartial:
		return badgeErr.Render(string(s))
	case record.BroadDeploying:
		return badgeBusy.Render(string(s))
	case record.BroadDraft:
		return badgeDim.Render(string(s))
	case record.BroadDeleted:
		return badgeDim.Render(string(s))
	case "":
		return badgeDim.Render("—")
	}
	return badgeDim.Render(string(s))
}

var (
	projectStyle = lipgloss.NewStyle().Bold(true)
	envStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	stackStyle   = lipgloss.NewStyle()
	focusedSel   = lipgloss.NewStyle().Background(lipgloss.Color("237")).Bold(true)
	unfocusedSel = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	badgeOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	badgeWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	badgeErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	badgeBusy    = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	badgeDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)
