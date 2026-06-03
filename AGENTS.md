# AGENTS.md

## Project Purpose
Packwright — a Go-based **hybrid TUI + GUI** tool that scaffolds and manages AWS
infrastructure templates. It ships as a single `packwright` binary that runs as
an interactive terminal UI (TUI) by default and a graphical UI (GUI) with
`--gui`.

Module path: `github.com/bannaarr01/packwright`

## Core Stack
- Go 1.23+ (minimum declared in `go.mod`; developed/built with Go 1.26.x)
- Cobra (`github.com/spf13/cobra`) — CLI framework and command tree
- Pluggable front-ends (TUI + GUI) wired through the `cmd` registry; the
  concrete front-end frameworks are introduced in later PRs
- Standard-library `testing` for unit tests
- License: Apache-2.0 (`LICENSE`, with `NOTICE`)

## Core Principles
- **Simplicity First**: Make every change as simple as possible. Touch minimal code.
- **No Laziness**: Find root causes. No temporary fixes. Senior-engineer standards.
- **Minimal Impact**: Changes should only touch what's necessary. Avoid introducing bugs.
- **Minimal Dependencies**: Cobra is currently the *only* third-party dependency.
  Adding a new module needs a real justification.
- **No `internal/*` packages**: Every package is importable so later PRs can wire
  in front-ends via the registry.

## Project Layout & Conventions
```
.
├── main.go                   # entry point — delegates to cmd.Execute()
├── cmd/
│   ├── root.go               # cobra root command (--version, --gui); Execute()
│   ├── registry.go           # Launcher type + TUILauncher/GUILauncher vars
│   └── root_test.go          # contract tests for the registry + flags
├── .github/workflows/ci.yml  # CI: fmt / vet / build / test
├── go.mod / go.sum
├── LICENSE / NOTICE          # Apache-2.0
├── AGENTS.md / CLAUDE.md
└── README.md
```

### Front-end registry pattern (read before adding a TUI/GUI)
- `cmd/registry.go` declares `type Launcher func() error` and two package-level
  vars, `cmd.TUILauncher` and `cmd.GUILauncher`, defaulting to stubs that return
  `"<TUI|GUI> not linked into this build"`.
- A front-end package registers itself from an `init()`:
  ```go
  // in the TUI package
  func init() { cmd.TUILauncher = run }
  ```
- For that `init` to fire, the front-end package must be in the build graph (e.g.
  a blank import somewhere reachable from `main`). Keep this in mind when adding
  front-ends — it may mean touching `main.go`.
- The root command reads these vars at *run time* (inside `RunE`), so a launcher
  registered via `init` is always observed instead of the stub.

## Workflow Orchestration

### 1. Plan Mode Default
- Enter plan mode for ANY non-trivial task (3+ steps or architectural decisions).
- If something goes sideways, STOP and re-plan immediately — don't keep pushing.
- Use plan mode for verification steps, not just building.
- Write detailed specs upfront to reduce ambiguity.

### 2. Subagent Strategy
- Use subagents liberally to keep the main context window clean.
- Offload research, exploration, and parallel analysis to subagents.
- For complex problems, throw more compute at it via subagents.
- One task per subagent for focused execution.

### 3. Self-Improvement Loop
- After ANY correction from the user: update `tasks/lessons.md` with the pattern.
- Write rules for yourself that prevent the same mistake.
- Ruthlessly iterate on these lessons until the mistake rate drops.
- Review lessons at session start.

### 4. Verification Before Done
- Never mark a task complete without proving it works.
- Diff behaviour between `master` and your changes when relevant.
- Ask yourself: "Would a staff Go engineer approve this?"
- Run the verification commands, check output, demonstrate correctness.

### 5. Demand Elegance (Balanced)
- For non-trivial changes: pause and ask "is there a more idiomatic Go way?"
- If a fix feels hacky: "Knowing everything I know now, implement the elegant solution."
- Skip this for simple, obvious fixes — don't over-engineer.
- Challenge your own work before presenting it.

### 6. Autonomous Bug Fixing
- When given a bug report: just fix it. Don't ask for hand-holding.
- Point at logs, errors, failing tests — then resolve them.
- Zero context switching required from the user.
- Go fix failing CI checks without being told how.

## Task Management
1. **Plan First**: Write the plan to `tasks/todo.md` with checkable items.
2. **Verify Plan**: Check in before starting implementation.
3. **Track Progress**: Mark items complete as you go.
4. **Explain Changes**: High-level summary at each step.
5. **Document Results**: Add a review section to `tasks/todo.md`.
6. **Capture Lessons**: Update `tasks/lessons.md` after corrections.

## Git Operation Rules
IMPORTANT:
- Do not run `git commit` or `git push` unless explicitly asked.
- Always branch off `master` for changes; never commit directly to `master`.
- Allowed Git operations:
    - read-only: `git status`, `git diff`, `git log`, `git show`
    - branch creation/switch: `git branch`, `git checkout -b`
    - temporary stashing: `git stash`
- The developer reviews diffs and performs commit/push manually unless instructed otherwise.

## CLI Configuration
- Binary name: `packwright`
- Default action (no args): launch the TUI front-end.
- `--gui`: launch the GUI front-end instead of the TUI.
- `--version` / `-v`: print the version (defaults to `dev`; override at build time
  via `-ldflags "-X github.com/bannaarr01/packwright/cmd.version=<v>"`).
- `--help` / `-h`: usage. Unexpected positional args are rejected (`cobra.NoArgs`).
- Exit codes: `0` on success; `1` when a command returns an error (e.g. a
  front-end that isn't linked into the build).

## Commands
```bash
go run .                    # build & run the CLI (no args -> TUI)
go run . --version          # print the version
go build ./...              # compile all packages
go build -o packwright .    # build the binary at the repo root
go test ./...               # run unit tests
go test -race ./...         # run tests with the race detector
go vet ./...                # static analysis
gofmt -l .                  # list files needing formatting (MUST be empty)
gofmt -w .                  # format files in place
go mod tidy                 # sync go.mod / go.sum with imports
go mod verify               # verify module checksums

# Release build with an embedded version:
go build -ldflags "-X github.com/bannaarr01/packwright/cmd.version=v1.2.3" -o packwright .
```

## Verification Policy
After each implementation task, run and confirm all of these are clean — they
mirror what CI (`.github/workflows/ci.yml`) enforces:
- `gofmt -l .` — returns empty (no unformatted files)
- `go vet ./...` — no findings
- `go build ./...` — succeeds
- `go test ./...` — passes (use `-race` for anything touching concurrency)

Then provide a short manual test checklist for developer-side verification.

## Code Style
- **Formatting is non-negotiable**: `gofmt` is the law (tabs for indentation,
  gofmt's canonical layout). `gofmt -l .` must return empty. Configure your editor
  to run gofmt/goimports on save.
- **`go vet ./...` must pass** with zero findings.
- **Naming**: standard Go conventions — `MixedCaps`, exported identifiers
  capitalized, short receiver names, short lowercase package names, no stutter
  (`cmd.Execute`, not `cmd.CmdExecute`).
- **Errors**: return `error` values; wrap with `fmt.Errorf("doing X: %w", err)`;
  do not `panic` outside `main`/unrecoverable init; use `errors.Is`/`errors.As`
  for sentinel/typed errors.
- **Doc comments**: every exported identifier has a doc comment that starts with
  the identifier's name.
- **Testability**: prefer constructors (e.g. `newRootCmd()`) over package-global
  singletons where it aids isolation; restore any mutated package-level state in
  tests (`t.Cleanup`).
- **No `internal/*` packages** (project rule — see Core Principles).
- **Dependencies**: keep them minimal; prefer the standard library.
