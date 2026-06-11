# Contributing

Thanks for considering a contribution to Packwright. The conventions below are
the short version; [AGENTS.md](AGENTS.md) is canonical and wins on any conflict.

By participating you agree to abide by the
[Code of Conduct](CODE_OF_CONDUCT.md).

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

## Developer Certificate of Origin (DCO)

Packwright uses the [Developer Certificate of Origin][dco] (DCO) instead of
a CLA. There is no legal paperwork — but every commit must carry a
`Signed-off-by:` trailer certifying that you wrote the change (or otherwise
have the right to contribute it under the project's Apache 2.0 license).

`git commit -s` adds the trailer automatically using the name + email from
your git config:

```bash
git commit -s -m "feat: add migrate-manifests --json output"
```

The resulting commit message ends with:

```
Signed-off-by: Your Name <you@example.com>
```

A GitHub Action verifies the trailer on every PR (see
`.github/workflows/dco.yml`). PRs whose commits lack a sign-off will not
merge; rewrite the offending commits with `git commit --amend -s` (single
commit) or `git rebase --signoff master` (multiple).

[dco]: https://developercertificate.org/

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
- Every commit must be `Signed-off-by:` per the DCO section above.
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
