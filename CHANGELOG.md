# Changelog

All notable changes to Packwright are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project follows MVP-tagged pre-1.0 versioning (`v0.<mvp>.<patch>-mvp<N>`).

## [Unreleased]

### Added
- Browseable collapsible sidebar in the GUI: grouped Commands / Packs /
  Wizards, inline filter, quick-action tiles, AWS-context footer pill.
  Toggle with <kbd>⌘B</kbd>; collapsed state persisted via `localStorage`.
- Window-chrome drag rails (`-webkit-app-region: drag`) so the GUI window
  can be moved under `mac.TitleBarHiddenInset`; minimum size 640×480.
- Geist Variable + Geist Mono Variable typefaces via `@fontsource-variable`.
- Husky-managed git hooks at `.husky/`: `pre-commit` (`gofmt`, `go vet`)
  and `pre-push` (`go test ./...`, `npm run check`).
- `assets/brand/` — tracked canonical brand pack (wordmark, icon, raster
  fallbacks) so the project never loses its visual identity to a
  gitignored design directory.
- `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md`,
  `.github/PULL_REQUEST_TEMPLATE.md`, `.github/ISSUE_TEMPLATE/*`,
  `.github/dependabot.yml`, `.github/FUNDING.yml`.

### Changed
- `gui.SlashCommand` JSON now exposes `source`, `scope`, and `pinned` so
  the GUI sidebar can group rows by provenance and surface the pin
  promotion.
- Unified `Window.go.gui.App` typing in a single `gui/web/src/lib/wails-app.d.ts`
  so multiple modules calling into the same App object stop conflicting at
  type-merge time.

### Fixed
- Svelte 5 keyed-each collision when two palette rows shared the same
  slash (`/alb` from two packs) caused the list to render as "No matches".
  Key now combines slash + title.

## [v0.3.0-mvp3] — palette wiring, hot-reload, conflict resolution, sidebar

### Added
- `pack.LoadPalette` composes Discover + LoadUserScope + Tag + Resolve +
  scaffold wizards; replaces `/example/*` placeholders in both fronts.
- `internal/manifest.Watcher` — fsnotify-backed watcher with 150ms
  debounce; TUI consumes via `refreshPaletteMsg`, GUI via Wails event
  `packwright:palette-changed`.
- Pack conflict resolution — qualified IDs (`pack:name/slash`), resolver,
  and pinned defaults via `config.PinnedDefaults`.
- Composite action runner (`kind: composite`) with confirmation gating.
- Pack trust primitives — content hash, surface scan, consent contract.
- Manifest template DSL — curated function set, env whitelist
  (`USER`, `HOME`, `AWS_PROFILE`, `AWS_REGION`), parse-only validator.
- Scaffold wizards — `/new-command`, `/new-pack` materialise template
  manifests; appended last in `LoadPalette` output.
- `.mcp.json` registering the Playwright MCP server for AI-assisted GUI
  testing.

## [v0.2.0-mvp2] — scaffolders, action runners, forms

### Added
- Action runners for `kind: resource`, `kind: shell`, `kind: monitor`.
- Form layer for collecting manifest inputs before run.
- CloudFormation rendering pipeline (`render/cfn`).
- Stream / monitor abstractions for long-running actions.

## [v0.1.0-mvp1] — bootstrap

### Added
- Cobra CLI with `packwright` / `packwright --gui` / `--version` / `--help`.
- Front-end registry pattern (`cmd/registry.go`) — `TUILauncher` /
  `GUILauncher` package vars overridden at `init()` time.
- Bubble Tea TUI scaffold.
- Wails + Svelte 5 GUI scaffold sharing theme tokens via `//go:embed`.
- CI workflow enforcing `gofmt`, `go vet`, `go build`, `go test`.

[Unreleased]: https://github.com/bannaarr01/packwright/compare/v0.3.0-mvp3...HEAD
[v0.3.0-mvp3]: https://github.com/bannaarr01/packwright/compare/v0.2.0-mvp2...v0.3.0-mvp3
[v0.2.0-mvp2]: https://github.com/bannaarr01/packwright/compare/v0.1.0-mvp1...v0.2.0-mvp2
[v0.1.0-mvp1]: https://github.com/bannaarr01/packwright/releases/tag/v0.1.0-mvp1
