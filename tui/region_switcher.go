package tui

import (
	"context"
	"log/slog"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/awsx"
)

// regionItem implements list.Item / list.DefaultItem for the bubbles/list
// delegate. The leading "→ " marker on the active region mirrors the profile
// switcher so the two surfaces feel identical.
type regionItem struct {
	name   string
	active bool
}

// Title renders the region name with an active-row marker.
func (r regionItem) Title() string {
	if r.active {
		return "→ " + r.name
	}
	return "  " + r.name
}

// Description is required by list.DefaultItem but unused: the region list is
// rendered single-line (the delegate has ShowDescription disabled).
func (r regionItem) Description() string { return "" }

// FilterValue feeds bubbles/list's fuzzy filter.
func (r regionItem) FilterValue() string { return r.name }

// RegionLister loads the AWS regions selectable for a profile. The TUI root
// wires the real implementation (build an awsx.Client, call
// awsx.ListRegionsOrFallback); tests inject a deterministic fake. The returned
// slice is always non-empty in practice because the production implementation
// falls back to awsx.FallbackRegions.
type RegionLister interface {
	ListRegions(ctx context.Context, profile, region string) []string
}

// regionsLoadedMsg carries the live region list back from the async loader so
// the switcher can replace the initial fallback list once DescribeRegions
// returns. It is routed to the screen via the root model's UpdateTop fallthrough.
type regionsLoadedMsg struct {
	regions []string
}

// RegionSwitcherMsg is emitted by the switcher when the user picks a region.
// Exactly one of Identity / Err is non-nil on a wired verifier; Region echoes
// the chosen pick so the root can persist it.
type RegionSwitcherMsg struct {
	Region   string
	Identity *awsx.Identity
	Err      error
}

// RegionSwitcher is the /region screen: a filtered list of AWS regions enabled
// for the current profile (seeded from a static fallback, then replaced by the
// live DescribeRegions result). The active region is highlighted; Enter
// verifies the (profile, region) pair via the injected Verifier — the profile
// is held fixed — and emits a RegionSwitcherMsg with the result.
type RegionSwitcher struct {
	list     list.Model
	keys     KeyMap
	profile  string // current profile; region picks verify against it
	region   string // current region; seeds the discovery client endpoint
	active   string // currently-active region, marked in the list
	verifier Verifier
	lister   RegionLister
	logger   *slog.Logger
}

// NewRegionSwitcher constructs the screen seeded with an initial region list
// (typically awsx.FallbackRegions so the list is never empty) and the
// currently-active region. profile/region identify the context the discovery
// client and STS verification run against. A nil verifier is allowed — Enter
// then emits a RegionSwitcherMsg with Identity=nil; a nil lister disables the
// async refresh and the switcher just shows the seed list.
func NewRegionSwitcher(keys KeyMap, regions []string, profile, region, active string, v Verifier, l RegionLister, logger *slog.Logger) RegionSwitcher {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	lst := list.New(regionItems(regions, active), delegate, 0, 0)
	lst.Title = "Switch AWS region"
	lst.SetFilteringEnabled(true)
	lst.SetShowHelp(false)
	lst.SetShowStatusBar(false)
	// Mirror the profile switcher: the root model owns quit semantics, so strip
	// quit from the list keymap or typing 'q' to filter would exit the app.
	lst.KeyMap.Quit = key.Binding{}
	lst.KeyMap.ForceQuit = key.Binding{}
	return RegionSwitcher{
		list:     lst,
		keys:     keys,
		profile:  profile,
		region:   region,
		active:   active,
		verifier: v,
		lister:   l,
		logger:   logger,
	}
}

// regionItems builds the list items, flagging the active region.
func regionItems(regions []string, active string) []list.Item {
	items := make([]list.Item, 0, len(regions))
	for _, r := range regions {
		items = append(items, regionItem{name: r, active: r == active})
	}
	return items
}

// initCmd kicks off async region discovery. It is a no-op (nil) when no lister
// is wired — the switcher then just shows the seed list it was built with.
func (s RegionSwitcher) initCmd() tea.Cmd {
	if s.lister == nil {
		return nil
	}
	lister := s.lister
	profile, region := s.profile, s.region
	return func() tea.Msg {
		return regionsLoadedMsg{regions: lister.ListRegions(context.Background(), profile, region)}
	}
}

// SetSize forwards a window-resize to the underlying list.
func (s *RegionSwitcher) SetSize(w, h int) { s.list.SetSize(w, h) }

// Update routes a message to the switcher. regionsLoadedMsg swaps in the live
// region list; Esc with an empty filter emits closePaletteMsg; Enter kicks off
// verification asynchronously so the UI does not block on the STS round-trip.
func (s RegionSwitcher) Update(msg tea.Msg) (RegionSwitcher, tea.Cmd) {
	switch m := msg.(type) {
	case regionsLoadedMsg:
		if len(m.regions) > 0 {
			s.list.SetItems(regionItems(m.regions, s.active))
		}
		return s, nil
	case tea.KeyMsg:
		switch {
		case key.Matches(m, s.keys.ClosePalette):
			if s.list.FilterState() == list.Unfiltered {
				return s, func() tea.Msg { return closePaletteMsg{} }
			}
		case key.Matches(m, s.keys.Select):
			if s.list.FilterState() != list.Filtering {
				if it, ok := s.list.SelectedItem().(regionItem); ok {
					return s, s.verifyCmd(it.name)
				}
			}
		}
	}
	var cmd tea.Cmd
	s.list, cmd = s.list.Update(msg)
	return s, cmd
}

// verifyCmd is the tea.Cmd that performs the STS call for the chosen region off
// the UI goroutine, holding the current profile fixed.
func (s RegionSwitcher) verifyCmd(region string) tea.Cmd {
	v := s.verifier
	profile := s.profile
	logger := s.logger
	return func() tea.Msg {
		if v == nil {
			return RegionSwitcherMsg{Region: region}
		}
		id, err := v.Verify(context.Background(), profile, region)
		if logger != nil {
			if err != nil {
				logger.Warn("region switch verify failed",
					slog.String("region", region),
					slog.Any("err", err))
			} else if id != nil {
				logger.Info("region switched",
					slog.String("region", region),
					slog.String("account", id.Account))
			}
		}
		return RegionSwitcherMsg{Region: region, Identity: id, Err: err}
	}
}

// regionSwitcherStyle keeps the screen padding consistent with the palette and
// the profile switcher so the overlay UIs feel like one family.
var regionSwitcherStyle = lipgloss.NewStyle().Padding(1, 2)

// View renders the switcher.
func (s RegionSwitcher) View() string { return regionSwitcherStyle.Render(s.list.View()) }
