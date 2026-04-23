# Contributing to go-anvil

Thanks for your interest. This is a solo-maintained library, so PRs and issues are reviewed on a best-effort basis.

## Prerequisites

- Go 1.25 or later (matches `go.mod`)
- [Foundry](https://book.getfoundry.sh/) — tests spawn real `anvil` processes
- `make`

Install the Go toolchain helpers once:

```
make install-tools
```

## Local workflow

The single command to run before pushing:

```
make check
```

That wraps `fmt`, `vet`, `lint` (golangci-lint against the repo's `.golangci.yml`), and `test` (with `-race`).

Individual targets if you want to run them in isolation:

```
make fmt          # gofmt / gofumpt
make vet          # go vet
make lint         # golangci-lint run
make test         # go test -race ./...
make test-coverage # HTML coverage report
```

Tests require `anvil` on `PATH`. If you don't have Foundry installed, `make check-foundry` will tell you so.

## Branch naming

Use a prefix that reflects the nature of the change. Examples:

- `feat/context-first-rpc-methods`
- `fix/startup-timeout-race`
- `chore/bump-foundry-1.6`
- `docs/examples-fork-scenario`
- `ci/add-govulncheck`

## Commit style

Use [Conventional Commits](https://www.conventionalcommits.org/). Examples:

```
feat(api): add SetCoinbase RPC wrapper
fix(startup): replace 2s sleep with readiness probe
chore(deps): bump go-ethereum to v1.18.0
docs(readme): document snapshot/revert pattern
```

The scope is optional but helpful. Breaking changes go under `feat!:` / `fix!:` or include a `BREAKING CHANGE:` footer.

## Pull requests

Every PR should:

1. Close an issue — put `Closes #N` at the top of the body. If an issue doesn't exist yet, file one first (the maintainer tracks all work via issues).
2. Pass the PR template checklist (tests, lint, godoc, CHANGELOG if user-visible).
3. Keep the diff focused. If you're also fixing an unrelated nit, file a separate PR.
4. Not amend or force-push after review has started, unless asked.

## Godoc

Every exported identifier must have a godoc comment starting with the identifier name. `make lint` (via `revive`) enforces this.

## Changelog

User-visible changes go in `CHANGELOG.md` under `## [Unreleased]` in Keep-a-Changelog form (`Added` / `Changed` / `Fixed` / `Removed`). Maintainer-only chores don't need an entry.
