package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Run dispatches a `/packs <verb> [args...]` invocation against homeDir.
// It is the seam between the front-end command palette (TUI / GUI) and
// the install package's typed entry points — the palette wraps the raw
// argv string, Run parses it, and we keep the verb-routing logic in
// one auditable place rather than threading parsing through both UIs.
//
// Status output goes to stdout; the returned error is the operation
// error verbatim so callers may switch on errors.Is(err, ErrDenied) /
// errors.Is(err, ErrNotInstalled) without parsing strings.
//
// Recognised verbs:
//   - add <source>
//   - update <name> | --all
//   - remove <name>
//   - list
func Run(ctx context.Context, stdout io.Writer, homeDir string, args []string) error {
	if stdout == nil {
		return errors.New("install: Run: nil stdout")
	}
	if len(args) == 0 {
		return errors.New("install: usage: packs <add|update|remove|list> [args...]")
	}
	switch args[0] {
	case "add":
		return runAdd(ctx, stdout, homeDir, args[1:])
	case "update":
		return runUpdate(ctx, stdout, homeDir, args[1:])
	case "remove":
		return runRemove(stdout, homeDir, args[1:])
	case "list":
		return runList(stdout, homeDir, args[1:])
	default:
		return fmt.Errorf("install: unknown verb %q (want add|update|remove|list)", args[0])
	}
}

func runAdd(ctx context.Context, stdout io.Writer, homeDir string, args []string) error {
	if len(args) != 1 {
		return errors.New("install: usage: packs add <git-url|./path>")
	}
	meta, err := Add(ctx, homeDir, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "added pack %q (%s)\n", meta.Name, describeSource(*meta))
	return nil
}

func runUpdate(ctx context.Context, stdout io.Writer, homeDir string, args []string) error {
	if len(args) == 0 {
		return errors.New("install: usage: packs update <name>|--all")
	}
	var names []string
	switch args[0] {
	case "--all":
		if len(args) != 1 {
			return errors.New("install: usage: packs update --all (no further arguments)")
		}
		installed, err := List(homeDir)
		if err != nil {
			return err
		}
		for _, i := range installed {
			names = append(names, i.Name)
		}
	default:
		names = args
	}

	var firstErr error
	for _, name := range names {
		meta, err := Update(ctx, homeDir, name)
		if err != nil {
			fmt.Fprintf(stdout, "update %q: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if meta.UpdatedAt.IsZero() {
			fmt.Fprintf(stdout, "%q already up to date\n", name)
		} else {
			fmt.Fprintf(stdout, "updated %q to %s\n", name, shortHash(meta.TrustedHash))
		}
	}
	return firstErr
}

func runRemove(stdout io.Writer, homeDir string, args []string) error {
	if len(args) != 1 {
		return errors.New("install: usage: packs remove <name>")
	}
	if err := Remove(homeDir, args[0]); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed pack %q\n", args[0])
	return nil
}

func runList(stdout io.Writer, homeDir string, args []string) error {
	if len(args) != 0 {
		return errors.New("install: usage: packs list (no arguments)")
	}
	installed, err := List(homeDir)
	if err != nil {
		return err
	}
	if len(installed) == 0 {
		fmt.Fprintln(stdout, "no packs installed")
		return nil
	}
	// List already returns sorted; sort again defensively so the
	// CLI's contract is robust against an API change in List.
	sort.Slice(installed, func(i, j int) bool { return installed[i].Name < installed[j].Name })
	for _, i := range installed {
		fmt.Fprintf(stdout, "%s\t%s\n", i.Name, describeSource(i))
	}
	return nil
}

// describeSource renders a one-line summary of where a pack came
// from: the git URL (with ref if pinned) or the local source path.
// Used by both the `add` confirmation and the `list` output so the
// two surfaces format the same fact identically.
func describeSource(i Installed) string {
	if i.Local {
		return "local:" + i.LocalSource
	}
	if i.Ref != "" {
		return i.URL + "#" + i.Ref
	}
	return i.URL
}

// shortHash truncates a "sha256:<hex>" string to the 12-character
// commit-style prefix readers expect from CLI output. A hash that
// lacks the prefix (defensive: should never happen given pack.Hash's
// contract) is returned verbatim.
func shortHash(h string) string {
	const prefix = "sha256:"
	if !strings.HasPrefix(h, prefix) {
		return h
	}
	rest := h[len(prefix):]
	if len(rest) <= 12 {
		return h
	}
	return prefix + rest[:12]
}
