# Contributor Covenant Code of Conduct

## Our Pledge

In the interest of fostering an open and welcoming environment, we as
contributors and maintainers pledge to make participation in our project and
our community a harassment-free experience for everyone, regardless of age, body
size, disability, ethnicity, sex characteristics, gender identity and expression,
level of experience, education, socio-economic status, nationality, personal
appearance, race, religion, or sexual identity and orientation.

## Our Standards

Examples of behavior that contributes to creating a positive environment
include:

- Using welcoming and inclusive language
- Being respectful of differing viewpoints and experiences
- Gracefully accepting constructive criticism
- Focusing on what is best for the community
- Showing empathy towards other community members

Examples of unacceptable behavior by participants include:

- The use of sexualized language or imagery and unwelcome sexual attention or
  advances
- Trolling, insulting/derogatory comments, and personal or political attacks
- Public or private harassment
- Publishing others' private information, such as a physical or electronic
  address, without explicit permission
- Other conduct which could reasonably be considered inappropriate in a
  professional setting

## Enforcement

Instances of abusive, harassing, or otherwise unacceptable behavior may be
reported by contacting the project team at **tbannaarr@gmail.com**. All
complaints will be reviewed and investigated and will result in a response that
is deemed necessary and appropriate to the circumstances. The project team is
obligated to maintain confidentiality with regard to the reporter of an incident.

## Attribution

This Code of Conduct is adapted from the [Contributor Covenant][homepage],
version 1.4, available at https://www.contributor-covenant.org/version/1/4/code-of-conduct.html

[homepage]: https://www.contributor-covenant.org

---

# Contributing

Thanks for considering a contribution to Packwright. The conventions below are
the short version; [AGENTS.md](AGENTS.md) is canonical and wins on any conflict.

## Getting started

1. Fork the repository.
2. Clone your fork:

   ```bash
   git clone https://github.com/YOUR_USERNAME/packwright.git
   cd packwright
   ```

3. Install tooling:

   ```bash
   npm install                       # repo-root Husky hooks
   (cd gui/web && npm install)       # frontend deps (only if you'll touch the GUI)
   ```

4. Branch off `master` — never commit directly to `master`:

   ```bash
   git checkout -b feature/my-change
   ```

## Development workflow

### Build

```bash
go build ./...                       # all Go packages
go build -o packwright .             # TUI-only binary
wails build -skipbindings            # full TUI + GUI app bundle
```

### Test

```bash
go test ./...                        # full suite
go test -race ./...                  # add -race for concurrency changes
(cd gui/web && npm run check)        # svelte-check (frontend types)
```

### Format & vet (blocking gates)

```bash
gofmt -l .                           # MUST print nothing
go vet ./...                         # MUST be clean
```

`gofmt -w .` auto-formats. Both checks also run on `pre-commit` via Husky
and again in CI.

### Working on the GUI

```bash
wails dev -skipbindings -appargs "--gui"
```

CDP endpoint at `http://localhost:34115`. `-skipbindings` is required
because the dev binding-generator runs the binary headless and would
otherwise default to TUI and fail on `/dev/tty`.

For AI-assisted GUI testing, the project ships a Playwright MCP server
(`.mcp.json`). See [AGENTS.md › AI-assisted GUI testing](AGENTS.md) for
the workflow.

## Pull request guidelines

- Keep PRs focused on a single change.
- All tests must pass; coverage of new logic is expected.
- Follow existing code style — `gofmt`, idiomatic Go, no `internal/*` if
  the package needs to be importable by a later front-end (see AGENTS.md
  for the registry pattern).
- Cobra is the **only** non-test third-party Go dependency. Adding a new
  one requires justification in the PR description.
- Use the [PR template](.github/PULL_REQUEST_TEMPLATE.md). Fill in *all*
  sections — risk level and rollback plan are not optional.
- Branch name: `{type}/{slug}` where type is one of
  `feature`, `bugfix`, `hotfix`, `refactor`.

## Reporting bugs

Use the [bug report template](https://github.com/bannaarr01/packwright/issues/new?template=bug_report.md).
Redact AWS account IDs, ARNs, and any secrets from logs.

## Reporting security vulnerabilities

**Do not open a public issue.** See [SECURITY.md](SECURITY.md) for
responsible-disclosure instructions.

## License

By contributing, you agree that your contributions will be licensed under
the [Apache License 2.0](LICENSE).
