# CLAUDE.md

Guidance for Claude Code when working in this repo. Read this first — it captures non-obvious conventions that aren't derivable from the code alone.

## What this is

`go-anvil` is a Go library that wraps Foundry's `anvil` local EVM node so Go tests can spawn one programmatically. Flat single-package layout:

- `anvil.go` — all public API, lifecycle (spawn subprocess + RPC client), builder.
- `*_test.go` — `anvil_test.go` and `lifecycle_test.go` start real anvil instances and skip when it isn't installed (they fail instead when `CI` is set); `anvil_unit_test.go` uses a fake RPC server; `fork_test.go` is behind the `fork` build tag.
- `helpers.go` — small utilities (e.g. `(*Anvil).WaitForMemPoolEmpty`).

Module path: `github.com/neverDefined/go-anvil`.

## Running and verifying

```
make check    # fmt + vet + golangci-lint + go test -race
make test     # go test -race -shuffle=on ./... (integration tests skip without anvil)
make lint     # golangci-lint run
govulncheck ./...
```

If `anvil` isn't on `PATH`, `make check-foundry` tells you.

## Non-obvious conventions

**One anvil per test.** Integration tests call `startAnvil(t, NewAnvilBuilder()...)`, which starts a fresh instance on a port anvil picks and closes it when the test ends. Startup takes about 50 ms, so integration tests call `t.Parallel()` and own their instance; nothing depends on test order. Assert what a call changed on the chain (a balance, code, a block time), not only that it returned nil. Don't add `time.Sleep`. `evm_mine` and the setters take effect before they return, and `WaitForBlock` covers block production. With automine, a transaction is mined just after `eth_sendTransaction` returns, so wait for it with the `receipt` test helper. Each wrapper also has a row in `TestRPCWrappers` pinning its method and params.

**RPC methods are context-first.** Every mutating RPC method takes `ctx context.Context` as its first parameter and forwards through `a.call(ctx, result, method, args...)`. `call` returns `ErrNotStarted` unless the instance is running, uses `CallContext`, and wraps errors with the method name (`anvil: anvil_setFoo: ...`). Don't use `a.rpcClient` directly: you lose the lifecycle check, and a bare `Call(nil, ...)` also loses cancellation and timeouts. Don't log errors you return; the caller decides. When adding a new RPC wrapper, follow this shape:

```go
func (a *Anvil) SetFoo(ctx context.Context, arg T) error {
    return a.call(ctx, nil, "anvil_setFoo", arg)
}
```

**Builder pattern.** All `With*` methods store a typed field and return `*AnvilBuilder` for chaining; they don't validate. Validation lives in `Build()` (`validate()` is called at the top), and `renderArgs()` turns the fields into anvil flags. When adding a new option, follow this split. Anvil flags without a method go through `WithArgs`.

**Error wrapping.** Always `fmt.Errorf("...: %w", err)`. Sentinel errors are defined at `anvil.go` top; prefer them where they make sense instead of ad-hoc messages.

**Godoc on exports is enforced.** `revive` is in `.golangci.yml`; every exported identifier needs a godoc comment starting with the identifier name. Don't ship PRs that regress this.

**Process lifecycle.** An instance is single-use: new → running → stopped, guarded by `a.mu`. By default `Build()` passes `--port 0`, and `Start()` reads the bound address from anvil's `Listening on ...` stdout line, which doubles as the readiness signal. If the process exits first, `Start()` returns `ErrProcessExited`. `Close()` and `Stop()` share one shutdown path that kills and reaps the process.

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

## Claude Code tools in this repo

Project-specific subagents and slash commands live in `.claude/`. Invoke them instead of reinventing the wheel.

- **`@anvil-reviewer`** (`.claude/agents/anvil-reviewer.md`) — reviews a PR or working-tree diff against the conventions on this page. Use before merging or as a self-check before committing.
- **`@release-drafter`** (`.claude/agents/release-drafter.md`) — walks `git log` since the last tag and drafts the CHANGELOG entry plus GitHub release notes. Use when preparing a release cut.
- **`/new-rpc <MethodName> <rpc_method> [<params>]`** (`.claude/commands/new-rpc.md`) — scaffolds a new RPC wrapper on `*Anvil` matching the shape above, plus integration and unit-test stubs.
- **`/bump-foundry`** (`.claude/commands/bump-foundry.md`) — checks for a newer stable Foundry release than the version pinned in `ci.yml`, drafts the one-line diff, and optionally opens a PR.

Shared permissions live in `.claude/settings.json` (conservative read-only allowlist). Per-machine overrides go in `.claude/settings.local.json` (globally gitignored).

## Out of scope

- Adding a non-anvil execution client, node type, or unrelated blockchain tooling — this library is anvil-only. Helpers that check what anvil returns in tests, like `VerifyProof`, are in scope.
- Refactors larger than the issue asked for — propose first, don't do it under a bug fix.
