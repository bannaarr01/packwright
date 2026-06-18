package pack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bannaarr01/packwright/internal/scaffold"
	"github.com/bannaarr01/packwright/manifest"
)

// PaletteEntry is one row in the command-palette UI. Both surfaces (TUI and
// GUI) consume it: the TUI maps each entry into a paletteItem; the GUI
// returns it through the ListSlashCommands Wails binding.
//
// Title is the human-readable label the row renders. When the same Slash is
// registered by more than one pack the title is suffixed with the source so
// the user can disambiguate visually — that suffix is the data side of
// ADR-0023's resolution UX.
type PaletteEntry struct {
	// Slash is the leading-slash invocation (e.g. "/alb"). Unique per Source
	// but may repeat across Sources when packs collide.
	Slash string
	// Title is the display label. Either the manifest's Title verbatim
	// (unique slash) or "Title (source)" when the slash is in conflict.
	Title string
	// Source is the row's provenance: "user" for the user scope, "builtin"
	// for the /new-command / /new-pack wizards, or the pack name otherwise.
	Source string
	// Scope is ScopeUser for user-scope rows (including builtins) and
	// ScopePack for pack-scope rows. Front-ends can filter or group on it.
	Scope Scope
	// Pinned is true for the row promoted to first by a Config.PinnedDefaults
	// entry. Useful for the palette to render a small marker next to the
	// default row of a conflicting slash.
	Pinned bool
}

// builtinSource identifies the scaffold wizard rows (`/new-command`,
// `/new-pack`). It is distinct from the user-scope Source ("user") because
// the wizards are not on-disk manifests — they cannot be edited or pinned —
// and the palette may want to render them with a different glyph.
const builtinSource = "builtin"

// LoadPalette discovers every command surfaced in homeDir and returns the
// rows the command palette should render.
//
// The data sources are, in order:
//
//  1. User scope — manifests under <homeDir>/commands and <homeDir>/monitors
//     (see LoadUserScope).
//  2. Installed packs — every directory under <homeDir>/packs (see Discover).
//  3. Built-in wizards — `/new-command` and `/new-pack` from
//     internal/scaffold (always last so collisions never hide them).
//
// For each slash present in more than one source the rows are ordered by
// Resolve, so a pin in defaults promotes its source to the top of the group.
// Conflicting rows are suffixed with their source for visual disambiguation.
//
// Partial failure is tolerated: a malformed pack does not prevent the
// healthy packs and the user scope from appearing. The returned error
// aggregates every failure encountered via errors.Join. Callers should
// display the rows regardless (they are non-nil even on partial failure)
// and surface the error as a non-fatal warning.
//
// defaults is the value of Config.PinnedDefaults; pass nil when no config
// has been loaded.
func LoadPalette(homeDir string, defaults map[string]string) ([]PaletteEntry, error) {
	var errs []error

	user, err := LoadUserScope(homeDir)
	if err != nil {
		errs = append(errs, fmt.Errorf("pack: palette: user scope: %w", err))
	}
	packs, err := Discover(homeDir)
	if err != nil {
		errs = append(errs, fmt.Errorf("pack: palette: discover: %w", err))
	}

	all := make([]*Pack, 0, len(packs)+1)
	if user != nil {
		all = append(all, user)
	}
	all = append(all, packs...)

	tagged := Tag(all)

	// Group tagged entries by slash so we can apply conflict ordering once
	// per slash group rather than per row.
	bySlash := make(map[string][]Tagged)
	var slashOrder []string
	for _, t := range tagged {
		s := t.Manifest.Slash
		if s == "" {
			continue
		}
		if _, seen := bySlash[s]; !seen {
			slashOrder = append(slashOrder, s)
		}
		bySlash[s] = append(bySlash[s], t)
	}

	out := make([]PaletteEntry, 0, len(tagged)+2)
	for _, slash := range slashOrder {
		group := bySlash[slash]
		conflicts := len(group) > 1
		pin := defaults[slash]
		if conflicts || pin != "" {
			group = orderByResolve(all, group, slash, pin)
		}
		for i, t := range group {
			source := sourceLabel(t)
			out = append(out, PaletteEntry{
				Slash:  slash,
				Title:  paletteTitle(t.Manifest.Title, source, conflicts),
				Source: source,
				Scope:  t.Scope,
				Pinned: i == 0 && pin != "" && conflicts,
			})
		}
	}

	// Wizards are appended last so they never displace a user-authored row
	// with the same slash. Their slash is always shown bare — they are
	// synthetic and cannot be pinned or replaced.
	for _, m := range scaffold.WizardManifests() {
		if m == nil || m.Slash == "" {
			continue
		}
		out = append(out, PaletteEntry{
			Slash:  m.Slash,
			Title:  m.Title,
			Source: builtinSource,
			Scope:  ScopeUser,
		})
	}

	return out, errors.Join(errs...)
}

// ResolveRunnable maps a palette slash to the manifest that runs for the bare
// invocation, plus the base directory its relative template / script paths
// resolve against (filepath.Dir of the manifest source; empty for the built-in
// wizards, which write to a form-supplied directory). It mirrors LoadPalette's
// data sources — user scope, then discovered packs, then the /new-command and
// /new-pack wizards — and applies the same pinned-default ordering, so the
// manifest returned here is the row the palette displayed. A slash with no
// backing manifest returns ok=false. Both front-ends call this so the TUI
// palette and the GUI palette route a pick to the same manifest.
func ResolveRunnable(homeDir string, pinned map[string]string, slash string) (*manifest.Manifest, string, bool) {
	user, _ := LoadUserScope(homeDir)
	packs, _ := Discover(homeDir)
	all := make([]*Pack, 0, len(packs)+1)
	if user != nil {
		all = append(all, user)
	}
	all = append(all, packs...)

	if res := Resolve(all, Qualified{Slash: slash}, pinned[slash]); len(res) > 0 && res[0].Manifest != nil {
		m := res[0].Manifest
		baseDir := ""
		if m.Source != "" {
			baseDir = filepath.Dir(m.Source)
		}
		return m, baseDir, true
	}

	for _, m := range scaffold.WizardManifests() {
		if m != nil && m.Slash == slash {
			return m, "", true
		}
	}
	return nil, "", false
}

// WatchRoots returns the directories the manifest watcher should subscribe
// to so that edits propagate into the palette. Roots that do not exist on
// disk are skipped silently — they may be created later, but fsnotify
// rejects missing paths at Add time.
//
// Callers pass each returned root to manifest.Watcher.Add. The returned
// slice is freshly allocated; callers may mutate it.
func WatchRoots(homeDir string) []string {
	candidates := []string{
		filepath.Join(homeDir, "packs"),
		filepath.Join(homeDir, "commands"),
		filepath.Join(homeDir, "monitors"),
	}
	roots := make([]string, 0, len(candidates))
	for _, r := range candidates {
		if _, err := os.Stat(r); err == nil {
			roots = append(roots, r)
		}
	}
	return roots
}

// sourceLabel maps a Tagged row to its palette source label. ScopeUser rows
// render as "user"; ScopePack rows render as their pack name.
func sourceLabel(t Tagged) string {
	if t.Scope == ScopeUser {
		return string(ScopeUser)
	}
	return t.SourcePack
}

// paletteTitle appends the source suffix to a title when the slash is in
// conflict. A unique slash uses the manifest title verbatim so the common
// case stays clean.
func paletteTitle(title, source string, conflict bool) string {
	if !conflict {
		return title
	}
	if title == "" {
		return "(" + source + ")"
	}
	return title + " (" + source + ")"
}

// orderByResolve rearranges a slash group to match Resolve's priority order.
// The group is restricted to the same slash already, so Resolve operates on
// the full pack list once and the result is filtered back to the group.
func orderByResolve(packs []*Pack, group []Tagged, slash, pin string) []Tagged {
	resolved := Resolve(packs, Qualified{Slash: slash}, pin)
	if len(resolved) == 0 {
		return group
	}
	// Build a stable key per manifest pointer so resolved order can be
	// projected back onto the group without losing items.
	indexOf := make(map[*Tagged]int, len(group))
	for i := range group {
		indexOf[&group[i]] = i
	}
	used := make([]bool, len(group))
	out := make([]Tagged, 0, len(group))
	for _, r := range resolved {
		for i := range group {
			if used[i] {
				continue
			}
			if group[i].Manifest == r.Manifest {
				used[i] = true
				out = append(out, group[i])
				break
			}
		}
	}
	// Defensive: include anything Resolve dropped so the palette never silently
	// loses a row (Resolve is total over the input today; this guards future
	// changes).
	for i, t := range group {
		if !used[i] {
			out = append(out, t)
		}
	}
	return out
}
