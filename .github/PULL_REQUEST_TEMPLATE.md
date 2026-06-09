## Summary

Brief description of the change and the user-visible behaviour it enables
or alters.

## Type of change

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] Breaking change (fix or feature that would cause existing behaviour to change)
- [ ] Refactor (no functional change)
- [ ] Documentation update

## Scope

- **In scope**:
- **Out of scope**:

## Risk level

- [ ] low — cosmetic, isolated module, docs
- [ ] medium — behaviour change, new integration, new dep
- [ ] high — touches credentials, manifest execution, or breaks an API

## Rollback plan

Concrete recovery steps (not just "revert"):

1.
2.

## Checklist

- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...` is clean
- [ ] `go build ./...` succeeds
- [ ] `go test ./...` passes (with `-race` if touching concurrency)
- [ ] `(cd gui/web && npm run check)` is clean (only if GUI files changed)
- [ ] PR title follows project convention (`feat:` / `fix:` / `chore:` / `docs:`)
- [ ] Branched off `master`, not committed directly to `master`
- [ ] No secrets, AWS account IDs, or `.env` files included
- [ ] CHANGELOG.md updated under `[Unreleased]` if user-visible

## Related issues

Closes #
