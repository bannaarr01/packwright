# Security Policy

## Supported versions

Packwright is pre-1.0 and ships in MVP-tagged releases. Only the most
recent MVP tag receives security fixes.

| Version          | Supported          |
| ---------------- | ------------------ |
| `v0.3.x-mvp3`    | :white_check_mark: |
| `v0.2.x-mvp2`    | :x:                |
| `v0.1.x-mvp1`    | :x:                |

## Reporting a vulnerability

If you discover a security vulnerability in Packwright, please report it
responsibly.

**Do not open a public GitHub issue.**

Instead, email **tbannaarr@gmail.com** with:

- A description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

You can expect an initial response within 48 hours. We will work with you
to understand and address the issue before any public disclosure.

## Scope

Packwright is a local CLI that scaffolds and runs AWS infrastructure
templates. The security-sensitive surfaces are:

- **Manifest execution.** `kind: shell` and `kind: composite` run host
  commands. The pack trust contract (`internal/pack/consent.go`) gates
  invocation; bypasses are in scope.
- **Pack installation** (MVP-4). Once `packs add <git-url>` lands, any
  path that fetches and renders a third-party pack without going through
  the trust contract is in scope.
- **Manifest template DSL** (`internal/manifest/template.go`). The
  template renderer ships a curated function set and a whitelisted env
  allow-list (`USER`, `HOME`, `AWS_PROFILE`, `AWS_REGION`). Escapes —
  reading arbitrary env, executing host commands, reading arbitrary
  files — are in scope.
- **AWS credential handling.** Packwright reads `$AWS_PROFILE` /
  `$AWS_REGION` and never serialises credentials to disk. Any path that
  logs, echoes, or persists access keys, session tokens, or assumed-role
  output is in scope.
- **TUI/GUI input.** Slash-command rendering, palette filter, manifest
  field rendering — anything that could cause an injection (terminal
  escape sequences in the TUI, XSS in the GUI webview, command
  substitution in shell-kind manifests) is in scope.

Out of scope:

- Vulnerabilities in third-party dependencies that do not affect a
  Packwright code path. Report those upstream and open a tracking issue
  here if you'd like a version bump prioritised.
- Issues that require an attacker to already control the user's
  `~/.config/packwright/` directory.
