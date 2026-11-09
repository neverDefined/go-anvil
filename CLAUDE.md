# CLAUDE.md

Conventions for this repo that aren't obvious from the code.

## Layout

`go-anvil` wraps Foundry's `anvil` node so Go tests can start one. It is a single package, `github.com/neverDefined/go-anvil`:

- `doc.go`: the package overview.
- `anvil.go`: the public API, process lifecycle and builder.
- `helpers.go`: `NewT`, the funding helpers, `VerifyProof` and `WaitForMemPoolEmpty`.
- `example_test.go`: godoc examples. They start real anvil processes, and `go test` checks their output.
- `anvil_test.go`, `lifecycle_test.go`: integration tests against real anvil processes. They skip without anvil and fail instead when `CI` is set.
- `anvil_unit_test.go`: unit tests against a fake RPC server.
- `fork_test.go`: tests behind the `fork` build tag.

## Commands

```
make check    # fmt, vet, golangci-lint, go test -race
make test     # go test -race -shuffle=on ./...
make lint     # golangci-lint run
govulncheck ./...
```

`make check-foundry` tells you whether anvil is on your `PATH`.

## Conventions

### One anvil per test

Integration tests call `startAnvil(t, NewBuilder()...)`, which starts an instance on a port anvil picks and closes it when the test ends. Each test has its own instance, so these tests call `t.Parallel()` and don't depend on order. Startup takes about 50 ms.

Besides checking the error, assert what a call changed on the chain, such as a balance, code or a block time. Each wrapper also has a row in `TestRPCWrappers` pinning its method and params.

Don't add `time.Sleep`. `evm_mine` and the setters take effect before they return, and `WaitForBlock` covers block production. With automine, `eth_sendTransaction` can return just before the transaction is mined, so wait with the `receipt` test helper.

### RPC methods take a context first

Every RPC method takes `ctx context.Context` first and forwards through `a.call(ctx, result, method, args...)`. `call` returns `ErrNotStarted` unless the instance is running, uses `CallContext`, and wraps errors with the method name (`anvil: anvil_setFoo: ...`). Don't use `a.rpcClient` directly, because that loses the lifecycle check, and don't log errors you return. A new wrapper looks like this:

```go
func (a *Anvil) SetFoo(ctx context.Context, arg T) error {
    return a.call(ctx, nil, "anvil_setFoo", arg)
}
```

### Builder

`With*` methods store a typed field and return `*Builder`. They don't validate. `Build()` calls `validate()`, and `renderArgs()` turns the fields into anvil flags. Flags without a method go through `WithArgs`.

### Errors

Wrap with `fmt.Errorf("...: %w", err)`. Sentinel errors are at the top of `anvil.go`; use them where they fit.

### Docs

`revive` requires a godoc comment that starts with the identifier's name on every export. Say what the method does and anything surprising about it, and skip lines that are true of every method, such as "returns an error if the call fails".

Keep the README and other docs short and plain: no marketing words, and no sentence that only repeats a heading.

### Process lifecycle

An instance is single-use: new, then running, then stopped, guarded by `a.mu`. By default `Build()` passes `--port 0`, and `Start()` reads the bound address from anvil's `Listening on ...` stdout line, then probes `eth_blockNumber` until the node answers. If the process exits first, `Start()` returns `ErrProcessExited`. `Close()` and `Stop()` share one shutdown path that kills and reaps the process.

`Build()` creates `context.WithCancel(context.Background())`, and `Stop()` calls the cancel. Keep the `//nolint:gosec` on that line, because gosec can't follow the flow across functions.

## Workflow

Every improvement has an issue. File one before starting non-trivial work.

- PRs target `main`, open with `Closes #N`, cover one concern and fill in the template checklist honestly.
- Commits use Conventional Commits (`feat:`, `fix:`, `chore:`, `docs:`, `ci:`, `refactor:`, `test:`). Mark breaking changes with `feat!:` or a `BREAKING CHANGE:` footer.
- User-visible changes go in `CHANGELOG.md` under `## [Unreleased]`, in Keep a Changelog format.
- CI has three jobs: `test` (with `-race`, needs Foundry), `vuln` (`govulncheck`) and `lint` (`golangci-lint`). All three must pass.

## Claude Code tools

`.claude/` holds the project's subagents and slash commands:

- `@anvil-reviewer` reviews a PR or working-tree diff against this page. Run it before merging.
- `@release-drafter` drafts the CHANGELOG entry and release notes from `git log` since the last tag.
- `/new-rpc <MethodName> <rpc_method> [<params>]` scaffolds a new RPC wrapper and test stubs.
- `/bump-foundry` checks for a newer stable Foundry than the version in `ci.yml` and drafts the change.

Shared permissions live in `.claude/settings.json`. Per-machine overrides go in `.claude/settings.local.json`, which is gitignored.

## Out of scope

- Execution clients other than anvil, and blockchain tooling unrelated to it. Helpers that check what anvil returns in tests, like `VerifyProof`, are in scope.
- Refactors larger than the issue asked for. Propose those first.
