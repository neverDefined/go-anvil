# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Comprehensive godoc comments for all exported functions and types
- Custom error types for better error handling (`ErrNotStarted`, `ErrConnectionFailed`, etc.)
- Thread-safe resource cleanup using `sync.Once` pattern
- Snapshot and Revert RPC methods for state management
- `ResetState()` method for fast state reset using RPC (replaces `Reset()`)
- Additional Anvil RPC methods:
  - `SetCode()` - Set bytecode at an address
  - `SetStorageAt()` - Set storage slot values
  - `SetNonce()` - Set account nonce
  - `Mine()` - Mine multiple blocks at once
  - `DropTransaction()` - Remove transaction from mempool
  - `SetAutomine()` - Enable/disable automatic mining
  - `SetIntervalMining()` - Set interval mining
  - `AutoImpersonate()` - Enable auto-impersonation
  - `ResetFork()` - Reset fork to a new state
- Development tooling:
  - `.golangci.yml` linter configuration
  - GitHub Actions CI/CD pipeline
  - Comprehensive Makefile with common commands
- Documentation improvements in README.md
- Shared Anvil instance pattern in tests for significantly faster test execution

### Changed
- Package name from `main` to `anvil` (proper library package)
- Module path to `github.com/neverDefined/go-anvil`
- README.md import paths and API examples to match actual implementation
- `Start()` method no longer takes timeout parameter (uses internal retry logic)
- **BREAKING**: `Reset()` method replaced with `ResetState()` that uses RPC instead of process restart
- Test suite now uses shared Anvil instance with `ResetState()` between tests (much faster)
- **BREAKING**: All mutating RPC methods now take `context.Context` as their first parameter.
  Callers gain cancellation and timeout support on every RPC call; a hung RPC no longer blocks
  the caller indefinitely. Affected methods: `MineBlock`, `SetNextBlockTimestamp`, `IncreaseTime`,
  `SetBalance`, `Impersonate`, `StopImpersonating`, `Snapshot`, `Revert`, `SetCode`,
  `SetStorageAt`, `SetNonce`, `Mine`, `DropTransaction`, `SetAutomine`, `SetIntervalMining`,
  `AutoImpersonate`, `ResetFork`, `ResetState`. The `EthereumTestEnvironment` interface is
  updated to match.

  Migration:
  ```go
  // before
  anvil.MineBlock()
  anvil.SetBalance(addr, bal)

  // after
  ctx := context.Background() // or t.Context() in tests, or a ctx with timeout
  anvil.MineBlock(ctx)
  anvil.SetBalance(ctx, addr, bal)
  ```

### Added
- `(*Anvil).WaitForMemPoolEmpty(ctx, timeout)` — replaces the free function `MemPoolEmpty`
  with a context-aware method that respects both the caller's ctx deadline and an explicit
  timeout.
- `AnvilBuilder.WithStartupTimeout(d)` builder option; `DefaultStartupTimeout` (5s) constant.
- Sentinel errors: `ErrAnvilNotFound` (binary not on PATH or at Foundry fallback),
  `ErrAnvilNotExecutable` (binary found but lacks execute bit), `ErrStartupTimeout`
  (anvil did not become ready within the configured ceiling).

### Changed
- `Start()` replaces the hard-coded 2-second pre-connect sleep with a readiness probe
  that polls the RPC endpoint every 50ms and returns on the first successful response,
  capped by the new `WithStartupTimeout` ceiling (default 5s). Typical startup is now
  measurably faster; slow CI runners get longer headroom by setting a higher timeout.
- Subprocess stdout/stderr are now routed through the instance's zerolog logger
  (stdout at DEBUG, stderr at WARN, both tagged `stream="..."`) instead of leaking to
  the parent's `os.Stdout`/`os.Stderr`. Cleaner test output; consumers can silence via
  `WithLogLevel(zerolog.Disabled)`.
- When `anvil` isn't on `PATH`, the Foundry fallback path (`$XDG_CONFIG_HOME/.foundry/bin/anvil`
  or `~/.foundry/bin/anvil`) is now stat'd and checked for the execute bit before launch.
  Callers get `ErrAnvilNotFound` or `ErrAnvilNotExecutable` instead of an opaque
  `exec.Cmd.Start` failure.

### Removed
- `EthereumTestEnvironment` interface — it was defined but never used as an injection
  point anywhere in the codebase. Consumers who want to mock `*Anvil` in their own tests
  should declare their own interfaces at the consumption site (accept interfaces, return
  concrete types).

### Deprecated
- Free function `MemPoolEmpty(ctx, client)` — use `(*Anvil).WaitForMemPoolEmpty` instead.
  Will be removed in a future release.

### Tests
- Added `anvil_unit_test.go`: pure-Go unit tests (no live anvil / no Foundry required) covering
  builder validation, `resolveAnvilPath` with temp-dir fixtures, the `retry` helper,
  `errors.Is` wrapping for every sentinel, and RPC method shape via `httptest`-backed JSON-RPC
  server. Contributors without Foundry can run `go test -run '^Test(AnvilBuilder|ResolveAnvilPath|Retry|SentinelErrors|RPC)' ./...`.
- Added integration subtests for the startup-timeout path (`ErrStartupTimeout`), ctx-cancelled
  RPC call, and 50-goroutine concurrent-stress test that exercises atomics and `-race`.
- Added `fork_test.go` behind a `//go:build fork` tag; reads `ETH_RPC_URL` and skips cleanly
  when unset. Exercises `WithFork` and `WithForkBlockNumber`.
- Added `anvil_bench_test.go` with `BenchmarkMineBlock`, `BenchmarkSetBalance`,
  `BenchmarkSnapshotRevertCycle`, and `BenchmarkResetState` for tracking RPC-latency regressions.

### Fixed
- Duplicate test function "Test Reset Functionality" removed
- Resource cleanup now thread-safe with `sync.Once`
- Documentation inconsistencies between README and actual API

### Removed
- `Reset()` method (replaced with `ResetState()` for better performance)

## [0.1.0] - Initial Release

### Added
- Basic Anvil instance management (Start, Stop, Close)
- Builder pattern for flexible configuration
- Core RPC methods:
  - Block mining (`MineBlock`)
  - Time manipulation (`IncreaseTime`, `SetNextBlockTimestamp`)
  - Balance management (`SetBalance`)
  - Account impersonation (`Impersonate`, `StopImpersonating`)
- Default test account support
- Metrics collection (startup time, blocks mined, RPC calls)
- Helper function for mempool monitoring
- Comprehensive test suite
- MIT License

[Unreleased]: https://github.com/neverDefined/go-anvil/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/neverDefined/go-anvil/releases/tag/v0.1.0

