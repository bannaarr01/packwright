# CLAUDE.md

> **Single source of truth: [AGENTS.md](./AGENTS.md).** Read it first and follow it.
> This file is a fast-start summary; AGENTS.md is canonical and wins on any conflict.

## What this is
Packwright — a Go **hybrid TUI + GUI** tool that scaffolds and manages AWS
infrastructure templates. One binary (`packwright`): TUI by default, GUI with
`--gui`. Module path: `github.com/bannaarr01/packwright`.

## Non-negotiables (never violate)
- **Format & vet**: `gofmt -l .` must be empty and `go vet ./...` clean before any
  task is "done".
- **Minimal dependencies**: cobra is the only third-party dep — justify any new one.
- **No `internal/*` packages.**
- **Git**: never `commit`/`push` unless explicitly asked. Branch off `master`;
  never commit to it directly. The developer reviews diffs and commits manually.

## Verify before "done"
```bash
gofmt -l .        # must print nothing
go vet ./...      # no findings
go build ./...    # succeeds
go test ./...     # passes  (add -race for concurrency changes)
```

## Where things live
- Entry point: `main.go` → `cmd.Execute()` (`cmd/root.go`).
- Front-end wiring: `cmd/registry.go` — `TUILauncher` / `GUILauncher` package vars
  overridden via `init()`. **Read that pattern in AGENTS.md before adding a TUI/GUI.**
- Conventions, full command list, code style, and workflow → **[AGENTS.md](./AGENTS.md)**.
