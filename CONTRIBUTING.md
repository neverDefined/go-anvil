# Contributing to go-anvil

This library has one maintainer, so reviews may take a while.

## Setup

You need Go 1.25 or later, [Foundry](https://book.getfoundry.sh/getting-started/installation) and `make`. `make install-tools` installs golangci-lint and goimports.

## Checks

Run this before you push:

```
make check
```

It runs `make fmt`, `vet`, `lint` and `test`. `make test` runs `go test -race -shuffle=on ./...`. A failing run prints its seed, and `go test -shuffle=<seed> ./...` repeats that order.

The integration tests and examples start real anvil processes. Without Foundry they are skipped, unless `CI` is set, which makes a missing anvil a failure. `make check-foundry` tells you whether anvil is on your `PATH`.

Fork tests need the `fork` build tag and an RPC URL:

```
ETH_RPC_URL=https://... go test -tags=fork -run Fork ./...
```

## Branches and commits

Name branches with a type prefix, such as `feat/`, `fix/`, `chore/`, `docs/` or `ci/`.

Commits follow [Conventional Commits](https://www.conventionalcommits.org/). Mark breaking changes with `feat!:`, `fix!:` or a `BREAKING CHANGE:` footer.

## Pull requests

- Start the body with `Closes #N`. If there's no issue yet, file one first.
- Work through the checklist in the PR template.
- Keep each PR to one concern.
- Don't amend or force-push after review has started, unless asked.

Every exported identifier needs a godoc comment that starts with its name; `make lint` enforces this. User-visible changes go in `CHANGELOG.md` under `[Unreleased]`.
