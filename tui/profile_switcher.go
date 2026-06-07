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

// PR-07 ships the /profile screen as a stand-alone tea.Model so the bubble
// tea root can route to it without re-implementing list + filter machinery.
//
// Wiring note (TUI hook gap): PR-07's plan presumes app.go exposes a
// screen-registration hook PR-08 was meant to ship. It does not — tui/app.go
// uses a hardcoded mode enum (modeLauncher, modePalette) and no Register(...)
// entry point exists. Per PR-07's rules we must not edit app.go from this PR.
// The follow-up work to wire /profile into the palette and add a new
// mode value lives in PR-08-owned files (app.go, keymap.go) and should land
// once PR-08 ships its screen registry — at which point this file's
// NewProfileSwitcher is invoked from the palette's selection handler with no
// further changes to ProfileSwitcher itself.
//
// The Identity exchange between the TUI root and the rest of the app is
// modelled via ProfileSwitcherMsg so the wiring PR can dispatch it without
// editing this file.

// profileItem implements list.Item / list.DefaultItem for the bubbles/list
// delegate. The leading "→ " marker on the active profile mirrors the GUI
// switcher panel so the two surfaces feel identical.
type profileItem struct {
	name   string
	region string
	active bool
}

// Title renders the profile name with an active-row marker.
func (p profileItem) Title() string {
	if p.active {
		return "→ " + p.name
	}
	return "  " + p.name
}

// Description renders the profile's region (or a placeholder when none is
// declared, so the row is never blank).
func (p profileItem) Description() string {
	if p.region == "" {
		return "(no region set)"
	}
	return p.region
}

// FilterValue feeds bubbles/list's fuzzy filter; both fields participate so
// `eu-west` matches every European profile regardless of name.
func (p profileItem) FilterValue() string { return p.name + " " + p.region }

// Verifier is the dependency the switcher uses to re-init the awsx Client and
// run STS GetCallerIdentity on the user's pick. The TUI root wires in the real
// implementation (constructs an awsx.Client, calls awsx.Verify); tests inject
// a deterministic fake.
type Verifier interface {
	Verify(ctx context.Context, profile, region string) (*awsx.Identity, error)
}

// ProfileSwitcherMsg is emitted by the switcher when the user picks a profile.
// Exactly one of Identity / Err is non-nil; Profile/Region echo the chosen
// pick so the status bar can show "switching to ..." before verification
// returns.
type ProfileSwitcherMsg struct {
	Profile  string
	Region   string
	Identity *awsx.Identity
	Err      error
}

// ProfileSwitcher is the /profile screen: a filtered list of discovered AWS
// profiles. The active profile is highlighted; Enter triggers verification
// via the injected Verifier and emits a ProfileSwitcherMsg with the result.
type ProfileSwitcher struct {
	list     list.Model
	keys     KeyMap
	verifier Verifier
	logger   *slog.Logger
}

// NewProfileSwitcher constructs the screen from a list of discovered profiles
// and the currently-active profile name. A nil verifier is allowed — the
// switcher will still emit ProfileSwitcherMsg with Identity=nil, which the
// wiring PR can fill in once awsx is hooked up.
func NewProfileSwitcher(keys KeyMap, profiles []awsx.Profile, active string, v Verifier, logger *slog.Logger) ProfileSwitcher {
	items := make([]list.Item, 0, len(profiles))
	for _, p := range profiles {
		items = append(items, profileItem{name: p.Name, region: p.Region, active: p.Name == active})
	}
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Switch AWS profile"
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	// The palette already strips 'q' and Ctrl+C from the list keymap so the
	// root model owns quit semantics; the switcher must do the same to stay
	// consistent — otherwise typing 'q' to filter for an `qa-*` profile quits
	// the app.
	l.KeyMap.Quit = key.Binding{}
	l.KeyMap.ForceQuit = key.Binding{}
	return ProfileSwitcher{list: l, keys: keys, verifier: v, logger: logger}
}

// SetSize forwards a window-resize to the underlying list, mirroring how the
// palette is sized from the root model's tea.WindowSizeMsg handler.
func (s *ProfileSwitcher) SetSize(w, h int) { s.list.SetSize(w, h) }

// Update routes a message to the switcher. Esc with an empty filter emits
// closePaletteMsg (which the wiring PR maps back to the launcher); Enter
// kicks off verification asynchronously so the UI does not block on the
// STS round-trip.
func (s ProfileSwitcher) Update(msg tea.Msg) (ProfileSwitcher, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(km, s.keys.ClosePalette):
			if s.list.FilterState() == list.Unfiltered {
				return s, func() tea.Msg { return closePaletteMsg{} }
			}
		case key.Matches(km, s.keys.Select):
			if s.list.FilterState() != list.Filtering {
				if it, ok := s.list.SelectedItem().(profileItem); ok {
					return s, s.verifyCmd(it.name, it.region)
				}
			}
		}
	}
	var cmd tea.Cmd
	s.list, cmd = s.list.Update(msg)
	return s, cmd
}

// verifyCmd is the tea.Cmd that performs the actual STS call. It runs off the
// UI goroutine so the list remains responsive while AWS is reached.
func (s ProfileSwitcher) verifyCmd(name, region string) tea.Cmd {
	v := s.verifier
	logger := s.logger
	return func() tea.Msg {
		if v == nil {
			return ProfileSwitcherMsg{Profile: name, Region: region}
		}
		id, err := v.Verify(context.Background(), name, region)
		if logger != nil {
			if err != nil {
				logger.Warn("profile switch verify failed",
					slog.String("profile", name),
					slog.Any("err", err))
			} else if id != nil {
				logger.Info("profile switched",
					slog.String("profile", name),
					slog.String("account", id.Account),
					slog.String("arn", id.Arn))
			}
		}
		return ProfileSwitcherMsg{Profile: name, Region: region, Identity: id, Err: err}
	}
}

// profileSwitcherStyle keeps the screen padding consistent with the palette
// so both feel like the same family of overlay UIs.
var profileSwitcherStyle = lipgloss.NewStyle().Padding(1, 2)

// View renders the switcher.
func (s ProfileSwitcher) View() string { return profileSwitcherStyle.Render(s.list.View()) }
