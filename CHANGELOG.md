# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-11-09

### Added

- Package overview in `doc.go` and runnable godoc examples (#70).
- `GetProof(ctx, address, storageKeys, blockNumber)`, with storage keys as `[]common.Hash`, wraps EIP-1186 `eth_getProof` and returns `*AccountResult`, for light-client tests (#84).
- `VerifyProof(stateRoot, proof)` verifies an EIP-1186 account + storage proof against a state root, e.g. a block header's `Root` (#84).
- `URL()` returns the node's HTTP JSON-RPC endpoint.
- `ErrProcessExited`, returned by `Start()` when anvil exits before it is ready.
- `WithArgs(...string)` passes any other flag to anvil.
- `SendTransaction(ctx, TxArgs)` sends through `eth_sendTransaction`, so a test can act as an impersonated address without holding its key.
- `NewT(tb, builder)` starts a node for a test and stops it when the test ends.
- `WSURL()` and `DialWS(ctx)` give WebSocket access, which subscriptions need.
- `DumpState` and `LoadState` save and restore chain state (#60).
- `DealERC20` and `SetERC20Allowance` set token balances and allowances.
- `SetNextBlockBaseFee`, `SetCoinbase` and `SetBlockGasLimit` control future blocks.
- `FundAccounts` and `NewFundedAccount` set up funded addresses (#56).
- `DebugTraceTransaction` and `DebugTraceCall` return traces as raw JSON (#58).

### Changed

- Minimum `github.com/ethereum/go-ethereum` is now v1.17.10.
- **Behavior change:** without `WithPort`, anvil now picks a free port instead of using 8545. Use `URL()` to find it, or `WithPort(8545)` to keep the old behavior.
- `Start()` waits for anvil's `Listening on` line, then probes that address until it answers, instead of polling a fixed endpoint every 50ms.
- `Close()` and `Stop()` are now the same call.
- **Breaking:** builder options take typed values instead of strings: `WithPort(int)`, `WithChainID(uint64)`, `WithBlockTime(time.Duration)`, `WithForkBlockNumber(uint64)`, `WithGasLimit(uint64)`. Calling one twice keeps the last value instead of passing the flag twice.
- `Build()` returns `ErrInvalidConfig` for an out-of-range port, a negative block time, or a fork block without a fork URL. It also rejects `--silent`, `--quiet`, `-q`, `--port` and `-p` in `WithArgs`; the first three hide the output `Start()` waits for.
- **Breaking:** `SetCode` takes `[]byte`, and `SetStorageAt` takes a `common.Hash` slot and value.
- **Breaking:** `DropTransaction` takes a `common.Hash` and also returns whether anvil dropped anything.
- **Breaking:** `Snapshot` and `Revert` use a `SnapshotID` type (#63).
- **Breaking:** `New`, `NewBuilder` and `Builder` replace `NewAnvil`, `NewAnvilBuilder` and `AnvilBuilder`.
- **Breaking:** `WaitForMemPoolEmpty(ctx)` no longer takes a timeout. Put a deadline on `ctx`.
- **Breaking:** `WaitForBlock(ctx, number)` replaces `WaitForBlock(number, timeout)`. Put a deadline on `ctx`. It now checks before its first 100ms tick.
- **Breaking:** `Accounts()` returns all ten default accounts as `[]Account` and no longer returns an error (#62):

  ```go
  // before
  keys, addrs, err := a.Accounts()
  // after
  accounts := a.Accounts()
  key, addr := accounts[0].Key, accounts[0].Address
  ```

- **Breaking:** `WithLogger(*slog.Logger)` replaces `WithLogLevel`. Anvil's output is discarded by default; it used to go to zerolog's global logger. To see anvil's stdout, use a handler at `slog.LevelDebug`; stderr is logged at Warn. The zerolog dependency is gone.
- `ErrStartupTimeout` errors include anvil's last stderr lines.
- `Start()` takes one `evm_snapshot` as the `ResetState` baseline, so the first ID returned by `Snapshot()` is no longer `0x0`.
- **Breaking:** `Reset(ctx, *ForkOptions)` replaces `ResetFork(ctx, url, blockNumber)`. `Reset(ctx, nil)` starts a fresh local chain, on forked and non-forked nodes alike. With options and a block number of 0, it forks the upstream's latest block, not the original fork block as the old godoc said.
- `Reset()` takes a new `ResetState` baseline, because anvil discards all snapshots on reset.
- The library no longer logs errors it also returns, and `WaitForBlock` no longer logs at Debug on every poll. Errors from RPC methods start with the method name instead (`anvil: evm_mine: ...`).

### Removed

- `AnvilConfig` and `DefaultConfig`. In 0.1.0 only `LogLevel` and `DefaultRPCURL` were read. Use `WithLogger` instead of `LogLevel`, and `WithPort` plus `URL()` instead of `DefaultRPCURL`.
- `ErrProcessNotFound` and `ErrRPCCallFailed`, which nothing returned.
- `MemPoolEmpty(ctx, client)`, deprecated in 0.1.0. Use `(*Anvil).WaitForMemPoolEmpty(ctx)` (#61).
- `Metrics()` and `AnvilMetrics`. `LastError` was never set, and the block count included failed calls but not automined or interval blocks.
- `WithGasPrice`. It had no effect on anvil's default hardfork. Use `WithArgs("--gas-price", "...")` with a pre-London `--hardfork`.
- `AnvilPrivateKey` and `AnvilPrivateKeys`. Use `Accounts()`; `hex.EncodeToString(crypto.FromECDSA(account.Key))` gives the hex form.

### Fixed

- `Start()` no longer reports success while connected to another process when the port is already in use. It returns `ErrProcessExited` with anvil's error.
- `Start()` returns as soon as anvil exits during startup (for example, on an invalid flag), with anvil's stderr in the error, instead of waiting for the full startup timeout.
- RPC methods, `WaitForBlock` and `WaitForMemPoolEmpty` return `ErrNotStarted` before `Start()` and after `Close()` instead of panicking on a nil client. A second `Start()` returns `ErrAlreadyStarted` instead of leaking the first process. A `Start()` that fails after launching anvil counts as the first call, because instances are single-use. `Client()`, `RPCClient()` and `URL()` return nil/empty after `Close()`.
- `Close()` no longer sleeps for 1 second after the process has exited (#80).
- `Mine(ctx, n, x)` documented `x` as a timestamp, but anvil treats it as the number of seconds between blocks, so passing a Unix time moved the chain decades ahead. The parameter is now named `interval` and the README shows `SetNextBlockTimestamp` + `MineBlock` for an absolute time.
- `ResetState()` returns to the state right after `Start()`. Its first call used to snapshot whatever state existed at that moment. Concurrent calls are serialized instead of consuming the same snapshot (#79).
- `WaitForMemPoolEmpty()` counts queued transactions as well as pending ones (via `txpool_status`), and checks before its first 100ms tick.

## [0.1.0] - 2026-04-23

First tagged release. Captures the full feature set built over phases 0–3 of the maintainer roadmap (tracking issue #12), plus the original pre-roadmap work.

### Added

- Core Anvil instance management: `NewAnvil`, `NewAnvilBuilder`, `Start`, `Stop`, `Close`.
- Builder pattern with 9 options: `WithBlockTime`, `WithFork`, `WithForkBlockNumber`, `WithPort`, `WithChainID`, `WithGasLimit`, `WithGasPrice`, `WithLogLevel`, `WithStartupTimeout`.
- RPC wrappers (all context-first): `MineBlock`, `Mine`, `SetNextBlockTimestamp`, `IncreaseTime`, `SetBalance`, `Impersonate`, `StopImpersonating`, `AutoImpersonate`, `Snapshot`, `Revert`, `ResetState`, `SetCode`, `SetStorageAt`, `SetNonce`, `DropTransaction`, `SetAutomine`, `SetIntervalMining`, `ResetFork`.
- `WaitForBlock(number, timeout)` — polls the chain head with a context-backed deadline.
- `(*Anvil).WaitForMemPoolEmpty(ctx, timeout)` — method form of the old free helper; respects both ctx deadline and explicit timeout.
- `Accounts()` returns the default test keys and addresses.
- `Client()`, `RPCClient()`, `Metrics()` accessors. `AnvilMetrics` covers startup time, blocks mined, RPC calls.
- Sentinel errors: `ErrNotStarted`, `ErrAlreadyStarted`, `ErrConnectionFailed`, `ErrProcessNotFound`, `ErrInvalidConfig`, `ErrRPCCallFailed`, `ErrAnvilNotFound`, `ErrAnvilNotExecutable`, `ErrStartupTimeout`.
- `DefaultConfig` and `DefaultStartupTimeout` (5s) for tuning.
- Readiness probe in `Start()` polls the RPC endpoint every 50ms until it responds, capped by `WithStartupTimeout`. Replaces the original hard-coded 2-second sleep.
- Subprocess stdout/stderr routed through the instance's zerolog logger (DEBUG / WARN with `stream="..."` tags) instead of leaking to the parent process.
- Executable check on the Foundry fallback path (`$XDG_CONFIG_HOME/.foundry/bin/anvil` → `~/.foundry/bin/anvil`); returns `ErrAnvilNotFound` or `ErrAnvilNotExecutable` with context.
- Thread-safe metrics via `atomic.Uint64` / `atomic.Int64`; idempotent `Close()`/`Stop()` via `sync.Once`.
- Snapshot-based fast state reset: `ResetState(ctx)` takes a snapshot on first call, then reverts + re-snapshots on subsequent calls — ~10× faster than restarting the anvil process.

### Tests

- Integration suite (`anvil_test.go`) using the shared-anvil pattern: one `anvil` instance per `TestAnvil` run, `ResetState` between subtests. 24 subtests covering the full RPC surface, builder options, close/stop idempotence, startup timeout, ctx cancellation, and 50-goroutine concurrent stress.
- Unit tests (`anvil_unit_test.go`) requiring neither Foundry nor a live anvil: builder validation, `resolveAnvilPath` with temp-dir fixtures, `retry` helper, `errors.Is` propagation for every sentinel, and RPC method shape via an `httptest` JSON-RPC server.
- Fork-mode tests (`fork_test.go`) behind the `fork` build tag; skipped when `ETH_RPC_URL` is unset. Exercises `WithFork` and `WithForkBlockNumber`.
- Benchmarks (`anvil_bench_test.go`): `BenchmarkMineBlock`, `BenchmarkSetBalance`, `BenchmarkSnapshotRevertCycle`, `BenchmarkResetState`.

### CI / tooling

- Matrix test job across `(ubuntu-latest, macos-latest) × (go 1.25, 1.26)`; `fail-fast: false`. Coverage generated every slot; uploaded to Codecov from the primary slot.
- `govulncheck` and `golangci-lint` (v2) as separate jobs.
- Foundry pinned to `v1.5.1` via `foundry-rs/foundry-toolchain@v1`.
- `.github/dependabot.yml` — weekly gomod + github-actions updates; ignores go-ethereum major bumps.
- `.github/workflows/release.yml` — tag-triggered release workflow creates a GitHub release with auto-generated notes from merged PRs since the last tag.
- Structured `.github/ISSUE_TEMPLATE/` (bug + feature forms; `config.yml` disables blank issues); `.github/PULL_REQUEST_TEMPLATE.md` checklist; `.github/CODEOWNERS`.
- `CONTRIBUTING.md` with local workflow, branch + commit conventions (Conventional Commits), PR checklist, and unit-only / fork-mode test invocations.
- `CLAUDE.md` with non-obvious repo conventions for Claude Code sessions.
- Shared `.claude/settings.json` with a conservative read-only permission allowlist.
- Claude Code subagents: `.claude/agents/anvil-reviewer.md` (reviews PRs against repo conventions), `.claude/agents/release-drafter.md` (drafts CHANGELOG + release notes from git log).
- Claude Code slash commands: `.claude/commands/new-rpc.md` (scaffold new RPC wrapper + test stubs), `.claude/commands/bump-foundry.md` (check for newer stable Foundry and draft the ci.yml diff).

### Changed

- Package renamed from `main` to `anvil`. Module path: `github.com/neverDefined/go-anvil`.
- `Start()` no longer takes a timeout parameter; tuning is via `WithStartupTimeout` on the builder.
- **BREAKING vs pre-roadmap:** `Reset()` replaced by `ResetState(ctx)` which uses an RPC snapshot/revert loop instead of restarting the process.
- **BREAKING vs pre-roadmap:** every mutating RPC method now takes `ctx context.Context` as the first parameter and forwards via `a.rpcClient.CallContext(ctx, ...)`. Migration:

  ```go
  // before
  anvil.MineBlock()
  anvil.SetBalance(addr, bal)

  // after
  ctx := context.Background() // or t.Context() in tests, or a ctx with timeout
  anvil.MineBlock(ctx)
  anvil.SetBalance(ctx, addr, bal)
  ```

  The `EthereumTestEnvironment` interface was updated to match, then removed (see below).

### Deprecated

- Free function `MemPoolEmpty(ctx, client)` — use `(*Anvil).WaitForMemPoolEmpty(ctx, timeout)` instead. Will be removed in a future release.

### Removed

- `EthereumTestEnvironment` interface — was defined but never used as an injection point. Consumers who want to mock `*Anvil` in their own tests should declare their own narrow interfaces at the consumption site.
- `Reset()` — replaced by `ResetState(ctx)`.

### Fixed

- Leaked `context.CancelFunc` in `Build()` — was stored on `Anvil.cancel` but never invoked. `Stop()` now calls it (with `Close()` delegating to `Stop()`), so the context — and its bound anvil subprocess via `exec.CommandContext` — shuts down cleanly.

[Unreleased]: https://github.com/neverDefined/go-anvil/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/neverDefined/go-anvil/releases/tag/v0.2.0
[0.1.0]: https://github.com/neverDefined/go-anvil/releases/tag/v0.1.0
