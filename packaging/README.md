# Packaging — release runbook

This directory holds the assets used by `.github/workflows/release.yml`
to cut a Packwright release: signed installers for each OS, a Homebrew
formula template, and the license-scan configuration. See ADR-0029
(Packaging) and ADR-0032 (License) for the architectural decisions
that gate this layer.

## What a release produces

A `v*` tag push triggers the release workflow and produces these
artifacts on the GitHub Release:

| Artifact                                            | OS              | Distribution channel                |
| --------------------------------------------------- | --------------- | ----------------------------------- |
| `packwright-<v>-darwin-universal.dmg`               | macOS arm64+x64 | Direct download, signed + notarized |
| `packwright-<v>-windows-amd64-setup.exe`            | Windows x64     | Direct download (Inno Setup)        |
| `packwright-<v>-linux-amd64.AppImage`               | Linux x64       | Direct download (TUI front-end, v1) |
| `packwright-<v>-linux-arm64.AppImage`               | Linux arm64     | Direct download (TUI front-end, v1) |
| `packwright-<v>-darwin-{arm64,amd64}.tar.gz`        | macOS           | Homebrew tap + raw download         |
| `packwright-<v>-linux-{amd64,arm64}.tar.gz`         | Linux           | Raw download / scripted installs    |
| `packwright-<v>-windows-amd64.zip`                  | Windows         | Raw download                        |
| `SHA256SUMS`                                        | —               | Checksum verification               |

In parallel the workflow opens a PR against the Homebrew tap repo
(`bannaarr01/homebrew-packwright`) bumping `Formula/packwright.rb` to
the new version + sha256. A human merges the PR once they've verified
the artifacts.

## Trade-off: Inno Setup vs GoReleaser

PR-03 had a choice between an **Inno Setup** script and a single
**GoReleaser** config for the Windows side. We picked Inno Setup; the
reasoning is:

* The ADR commits to shipping a `.exe` installer on Windows — a polished
  first-launch experience to keep SmartScreen friction low. GoReleaser
  produces archives and checksums, not installers; for an installer
  you'd end up wiring NSIS or Inno Setup *anyway*.
* Wails has its own build orchestration (`wails build -platform ...`).
  GoReleaser's archive/build phases can't drive a Wails build directly,
  so a GoReleaser-only setup would still need a custom wrapper around
  Wails — at which point the simplification claim disappears.
* Inno Setup ships pre-installed on GitHub's `windows-latest` runner,
  so the workflow has no extra install step under normal conditions
  (it falls back to a `choco install innosetup` if the path moves).
* Inno Setup gives us first-class Add/Remove-Programs entries, an
  optional desktop shortcut, and PATH integration — features GoReleaser
  archives don't address at all.

There is no `.goreleaser.yaml` in the repo. Adding one later is an
independent decision.

## Trade-off: Linux ships TUI-only via AppImage for v1

The macOS `.dmg` and Windows installer bundle the full Wails GUI (the
embedded WebKit/WebView2 webview). The Linux AppImage in v1 bundles
**only** the TUI front-end — `CGO_ENABLED=0` with no GTK / WebKit
dependency. Reasons:

* Wails GUI on Linux requires `libwebkit2gtk-4.x-dev` and `libgtk-3-dev`
  at build time. `webkit2gtk-4.0` (Wails default) was retired from
  Ubuntu 24.04; `webkit2gtk-4.1` is its replacement, and Wails v2.12 is
  mid-transition. Pinning either today produces churn.
* Cross-compiling a CGO-linked arm64 Wails binary from an amd64 runner
  needs multiarch apt sources + arm64 cross-toolchain — non-trivial.
* The TUI front-end is the primary Linux UX anyway (the ADR notes
  "ssh users, CI environments, terminal-only setups" as the main Linux
  audience).

Linux GUI parity tracks as a follow-up PR — likely under MVP-5 once
Wails settles on `webkit2gtk-4.1`. The AppImage / `.tar.gz` artifacts
themselves do not change shape when that lands; only the binary inside
gains the GUI launcher.

## Required GitHub secrets

The workflow is **safe to run with no secrets** — forks and dry-run
contributors get unsigned artifacts and skipped tap PRs, never a hard
failure. Add the secrets below in `Settings → Secrets and variables →
Actions` to upgrade to the full signed release.

### macOS — signing + notarization

The presence of `MACOS_CERTIFICATE` is the toggle. If it's empty, the
`.app` / `.dmg` are emitted unsigned and the workflow logs a warning;
all macOS-specific signing steps are skipped.

| Secret                       | What it is                                                                          |
| ---------------------------- | ----------------------------------------------------------------------------------- |
| `MACOS_CERTIFICATE`          | Base64-encoded `.p12` containing the Developer ID Application certificate + key.    |
| `MACOS_CERTIFICATE_PASSWORD` | Password for the `.p12`.                                                            |
| `MACOS_KEYCHAIN_PASSWORD`    | Throwaway password used to lock the temporary CI keychain. Generate a random value. |
| `MACOS_SIGNING_IDENTITY`     | The full identity string used by `codesign --sign`, e.g. `Developer ID Application: Packwright (TEAMID12)`. |
| `MACOS_NOTARY_APPLE_ID`      | Apple ID email used to submit to `notarytool`.                                      |
| `MACOS_NOTARY_PASSWORD`      | App-specific password from appleid.apple.com (not the iCloud password).             |
| `MACOS_NOTARY_TEAM_ID`       | Developer team ID (10-character alphanumeric).                                      |

The presence of `MACOS_NOTARY_APPLE_ID` separately gates the notarize +
staple step, so a project that codesigns but isn't yet enrolled for
notarization can still produce a signed-but-not-notarized `.dmg`.

### Windows — Authenticode signing

The presence of `WIN_CERT_P12` is the toggle. If it's empty the `.exe`
and installer are emitted unsigned and the workflow logs a SmartScreen
warning.

| Secret              | What it is                                                              |
| ------------------- | ----------------------------------------------------------------------- |
| `WIN_CERT_P12`      | Base64-encoded `.p12` / `.pfx` containing the code-signing certificate. |
| `WIN_CERT_PASSWORD` | Password for the `.p12`.                                                |

### Homebrew tap — automated PR

The presence of `HOMEBREW_TAP_TOKEN` is the toggle. Without it the tap
PR step is skipped and the workflow logs a notice.

| Secret               | What it is                                                                       |
| -------------------- | -------------------------------------------------------------------------------- |
| `HOMEBREW_TAP_TOKEN` | Personal access token (or fine-grained PAT) with `contents:write` + `pull_requests:write` on `bannaarr01/homebrew-packwright`. The default `GITHUB_TOKEN` cannot push to a different repo, so a PAT (or a GitHub App token) is required. |

## Cutting a release

1. Land your last commit on `master`.
2. Verify the license-scan workflow is green on `master`. If it has
   ever flagged a non-allowlisted dependency, fix that *before* tagging
   — the release workflow does not re-run the scan; the assumption is
   that `master` is always policy-clean.
3. Tag and push:

   ```bash
   git tag v1.2.3 -m "Packwright 1.2.3"
   git push origin v1.2.3
   ```
4. Watch the `Release` workflow in `Actions`. On success it:
   - Uploads all artifacts to a new GitHub Release named `Packwright 1.2.3`.
   - Opens (or force-updates) a PR against the tap repo bumping the
     formula. Review and merge that PR by hand.

### Dry-run a release without publishing

Push a tag matching `v*-test-*` (for example `v0.0.0-test-rc1`). The
workflow runs end-to-end — building, signing, packaging — but skips
the GitHub Release upload and the tap PR. Use this to validate signing
secrets, packaging scripts, and the Inno Setup / AppImage / DMG steps
without polluting the public release feed.

## Verifying the license scan locally

```bash
go install github.com/google/go-licenses@v1.6.0
cat <<'EOF' > /tmp/lic.tpl
{{range .}}{{.Name}}	{{.LicenseName}}
{{end}}
EOF
go-licenses report ./... --template /tmp/lic.tpl | \
  awk -F'\t' 'NF==2 && $2 !~ /^(Apache-2\.0|MIT|BSD-2-Clause|BSD-3-Clause|ISC|MPL-2\.0)$/ { print }'
```

If `awk` prints nothing, the project is policy-clean. Anything that
prints is a license outside the ADR-0032 allowlist and either needs an
upstream replacement or an explicit ADR.
