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

