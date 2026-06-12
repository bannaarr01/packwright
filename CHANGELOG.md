# Changelog

All notable changes to Packwright are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project follows MVP-tagged pre-1.0 versioning (`v0.<mvp>.<patch>-mvp<N>`).

## [Unreleased]

_No unreleased changes._

## [v0.4.0-mvp4] — ecosystem + release: distribution, versioning, packaging, auto-update, usage log

### Added
- `internal/pack/install` — git-based pack distribution. `packwright packs
  {add,update,remove,list}` clones a remote repo (with optional `#<ref>`
  pin) or symlinks a local directory into `<home>/packs/<name>`. Pin
  storage at `<home>/packs/pins.json`. `--end-of-options` argv defence
  on every git invocation.
- `internal/version` and `internal/pack.Check` — three-stream SemVer
  (app / manifest-schema / pack) plus a `requires:` gate at pack load.
  Incompatible packs are rejected with a typed `*RequiresError` carrying
  the pack name, failing module, constraint, and running version. Dev
  builds (`meta.Version == "dev"`) bypass the gate.
- `cmd/cmd_migrate.go` — `packwright migrate-manifests` sweeps
  `<home>/packs/**` and rewrites old `schema_version` tokens to the
  current one, preserving a `.bak` next to each touched file.
- `internal/update` — launch-time GitHub Releases probe with 24h
  per-channel cache, channel-aware (`stable` / `prerelease`), stderr
  `Banner`. Opt-out via `PACKWRIGHT_NO_UPDATE_CHECK=1` or
  `disable_update_check: true` in `<home>/config.yaml`. `update_channel`
  YAML key selects the stream.
- `internal/usage` — local-only JSONL events at
  `<home>/logs/usage.jsonl` with `lumberjack.v2` rotation (5 MB,
  3 backups, no compression). Recorded fields: timestamp, slash, kind,
  duration, outcome, surface, app version. Never records AWS
  identifiers, region, stack names, or form values.
- `cmd/cmd_packs.go` — cobra subcommand tree dispatching to
  `install.Run` via the shared `registerSubcommand` registry.
- `action/dispatch.SetDefaultSurface` / `surfaceFromContext` fallback —
  `bootstrap.Init` records the front-end's surface label so usage events
  carry `tui` / `gui` even without per-call `WithSurface`.
- `bootstrap.runUpdateCheck` — 5-second-deadline goroutine spawned from
  `Init`. Tests via the `var checkForUpdate = runUpdateCheck` seam.
- `.github/workflows/release.yml` — six-platform release matrix (darwin
  universal, windows amd64, linux amd64/arm64), macOS codesign +
  notarize, Windows signtool, Linux AppImage, GitHub Release publish
  with `SHA256SUMS`, Homebrew tap PR. `v*-test-*` tags dry-run.
- `.github/workflows/license-scan.yml` — `go-licenses` against
  Apache-2.0 / MIT / BSD-2-3-Clause / ISC / MPL-2.0 allowlist.
- `.github/workflows/dco.yml` — Signed-off-by check over
  `merge-base..HEAD` for every non-merge commit in a PR.
- `CODE_OF_CONDUCT.md` (Contributor Covenant 1.4) split out of
  `CONTRIBUTING.md`.
- `packaging/homebrew/packwright.rb`, `packaging/README.md`,
  `build/{darwin,linux,macos,windows}/**` — installer + tap artefacts.
- `meta/meta.go` — leaf package exporting `Version` (overridden by
  release ldflags), readable by every other package without pulling in
  heavy deps.
- New `config.yaml` keys: `disable_update_check` (bool),
  `update_channel` (string; empty == `stable`).

### Changed
- `pack/discover.go::loadPack` — runs the requires gate
  (`pkgcheck.Check`) between meta parse and manifest load. Incompatible
  packs short-circuit with a `*RequiresError`.
- `bootstrap.Init` — opens log + usage destinations, then spawns the
  update probe and registers the surface fallback. Failures still
  warn-and-continue per ADR-0018.
- `tui/launch.go` and `gui/launch.go` — pass their surface label
  (`"tui"` / `"gui"`) into `bootstrap.Init`.
- `CONTRIBUTING.md` — drops the duplicated Code of Conduct preamble
  (now in its own file), adds the "Developer Certificate of Origin"
  section explaining `git commit -s` and pointing at `dco.yml`.

### Fixed
- Release workflow ldflags. MVP-3 set
  `-X github.com/bannaarr01/packwright/cmd.version=${tag}`, but the
  `cmd` package never declared a `version` variable, so every shipped
  binary reported `dev` from `meta.Version`. Now sets both
  `-X meta.Version=${tag}` and `-X internal/version.Version=${tag}`.

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
- `.mcp.json` registering the Playwright MCP server for AI-assisted GUI
  testing.

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

[Unreleased]: https://github.com/bannaarr01/packwright/compare/v0.4.0-mvp4...HEAD
[v0.4.0-mvp4]: https://github.com/bannaarr01/packwright/compare/v0.3.0-mvp3...v0.4.0-mvp4
[v0.3.0-mvp3]: https://github.com/bannaarr01/packwright/compare/v0.2.0-mvp2...v0.3.0-mvp3
[v0.2.0-mvp2]: https://github.com/bannaarr01/packwright/compare/v0.1.0-mvp1...v0.2.0-mvp2
[v0.1.0-mvp1]: https://github.com/bannaarr01/packwright/releases/tag/v0.1.0-mvp1
