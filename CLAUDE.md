# CLAUDE.md

Guidance for Claude Code when working in this repo. Read this first — it captures non-obvious conventions that aren't derivable from the code alone.

## What this is

`go-anvil` is a Go library that wraps Foundry's `anvil` local EVM node so Go tests can spawn one programmatically. Flat single-package layout:

- `anvil.go` — all public API, lifecycle (spawn subprocess + RPC client), builder.
- `anvil_test.go` — all tests. Integration-only; require `anvil` on `PATH`.
- `helpers.go` — small utilities (e.g. `(*Anvil).WaitForMemPoolEmpty`).

Module path: `github.com/neverDefined/go-anvil`.

## Running and verifying

```
make check    # fmt + vet + golangci-lint + go test -race
make test     # go test -race ./... (requires anvil on PATH)
make lint     # golangci-lint run
govulncheck ./...
```

If `anvil` isn't on `PATH`, `make check-foundry` tells you.

## Non-obvious conventions

**Shared-anvil test pattern.** Tests reuse a single `*Anvil` instance via `setupSharedAnvil(t, sharedAnvil)` (see `anvil_test.go`). `ResetState()` between subtests uses snapshot/revert rather than process restart — ~10× faster than spawning a new anvil per test. Only drop to `setupTestAnvil(t, cfg)` when a test truly needs custom config (e.g. forking from a specific URL). Do not introduce new top-level `anvil.NewAnvil()` calls inside test subtests.

**RPC methods are context-first.** Every mutating RPC method takes `ctx context.Context` as its first parameter and forwards to `a.rpcClient.CallContext(ctx, ...)`. Do not regress to bare `Call(nil, ...)` — it removes cancellation and timeout support for callers. When adding a new RPC wrapper, follow this shape:

```go
func (a *Anvil) SetFoo(ctx context.Context, arg T) error {
    a.rpcCalls.Add(1)
    if err := a.rpcClient.CallContext(ctx, nil, "anvil_setFoo", arg); err != nil {
        a.logger.Error().Err(err).Str("arg", fmt.Sprint(arg)).Msg("Failed to set foo")
        return err
    }
    return nil
}
```

**Builder pattern.** All `With*` methods are setters that return `*AnvilBuilder` for chaining; they don't validate. Validation lives in `Build()` (`validate()` is called at the top). When adding a new option, follow this split.

**Error wrapping.** Always `fmt.Errorf("...: %w", err)`. Sentinel errors are defined at `anvil.go` top; prefer them where they make sense instead of ad-hoc messages.

**Godoc on exports is enforced.** `revive` is in `.golangci.yml`; every exported identifier needs a godoc comment starting with the identifier name. Don't ship PRs that regress this.

**Context lifecycle.** `Build()` creates `context.WithCancel(context.Background())`; the cancel is stored on `Anvil.cancel` and invoked inside `Stop()` (which `Close()` delegates to). The `//nolint:gosec` at the `context.WithCancel` line is intentional — gosec can't follow the cross-function flow. Keep that comment if you move the code.

## Workflow

**Roadmap and tracking.** The full maintainer roadmap lives in the pinned tracking issue — see the `Issues` tab, filter by `phase:*` labels. Every improvement lands as a tracked issue. If you're doing non-trivial work without an open issue, file one first.

**PRs.**
- Target `main`.
- Body opens with `Closes #N` for each issue the PR resolves.
- Fill the template checklist honestly (tests run, lint run, godoc, CHANGELOG if user-visible).
- One concern per PR. Drive-by cleanups go in a separate PR.

**Commits.** Conventional Commits (`feat:`, `fix:`, `chore:`, `docs:`, `ci:`, `refactor:`, `test:`). Scope optional. Breaking changes: `feat!:` or a `BREAKING CHANGE:` footer.

**CI structure.** Three parallel jobs on push/PR: `test` (with `-race`, requires Foundry), `vuln` (`govulncheck`), `lint` (`golangci-lint`). All three must pass.

**CHANGELOG.** User-visible changes go under `## [Unreleased]` in `CHANGELOG.md`, Keep-a-Changelog format.

## Out of scope

- Adding a non-anvil execution client, node type, or unrelated blockchain tooling — this library is anvil-only.
- Refactors larger than the issue asked for — propose first, don't do it under a bug fix.
