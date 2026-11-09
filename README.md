# go-anvil

[![CI](https://github.com/neverDefined/go-anvil/workflows/CI/badge.svg)](https://github.com/neverDefined/go-anvil/actions)
[![codecov](https://codecov.io/gh/neverDefined/go-anvil/branch/main/graph/badge.svg)](https://codecov.io/gh/neverDefined/go-anvil)
[![Go Report Card](https://goreportcard.com/badge/github.com/neverDefined/go-anvil)](https://goreportcard.com/report/github.com/neverDefined/go-anvil)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A Go client library for programmatic control of [Anvil](https://book.getfoundry.sh/anvil/), Foundry's local Ethereum test node. Provides a clean interface for managing test environments, manipulating blockchain state, and controlling time and accounts during development.

## Features

- Full programmatic control over Anvil nodes
- Builder pattern for flexible configuration
- Chain manipulation (mining, time travel, snapshots)
- Account management and impersonation
- Fast state reset using RPC calls
- Comprehensive test coverage

## Installation

```bash
go get github.com/neverDefined/go-anvil
```

**Prerequisites:**
- [Foundry](https://book.getfoundry.sh/) must be installed on your system
- Go 1.25 or later

## Quick Start

```go
package main

import (
    "context"
    "log"

    "github.com/neverDefined/go-anvil"
)

func main() {
    ctx := context.Background()

    // Create and start an Anvil instance
    anvil, err := anvil.NewAnvil()
    if err != nil {
        log.Fatal(err)
    }

    if err := anvil.Start(); err != nil {
        log.Fatal(err)
    }
    defer anvil.Close()

    // Query the chain via the Ethereum client
    blockNumber, err := anvil.Client().BlockNumber(ctx)
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("Current block: %d", blockNumber)

    // Drive Anvil's RPC with context-first methods
    if err := anvil.MineBlock(ctx); err != nil {
        log.Fatal(err)
    }
}
```

## Configuration

### Builder Pattern

Use the builder pattern for custom configuration:

```go
// anvil's stdout is logged at Debug, so use a handler that shows it
logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

anvil, err := anvil.NewAnvilBuilder().
    WithBlockTime(time.Second).      // Mine a block every second
    WithChainID(1337).               // Custom chain ID
    WithGasLimit(12_000_000).        // Block gas limit
    WithPort(8545).                  // Fixed RPC port (default: a free port)
    WithLogger(logger).              // Receive anvil's output (default: discarded)
    WithFork("https://eth-mainnet.alchemyapi.io/v2/YOUR_KEY").
    WithForkBlockNumber(15_000_000). // Fork from specific block
    WithArgs("--hardfork", "cancun"). // Any other anvil flag
    Build()
```

### Available Options

- `WithBlockTime(d)` - Mine a block every `d` instead of one per transaction
- `WithChainID(id)` - Set the chain ID
- `WithGasLimit(limit)` - Set the block gas limit
- `WithPort(port)` - Use a fixed RPC port. By default anvil picks a free port, so parallel tests never collide; `URL()` returns the endpoint.
- `WithFork(url)` - Fork from a remote network
- `WithForkBlockNumber(number)` - Fork from a specific block
- `WithLogger(logger)` - Receive anvil's stdout (Debug) and stderr (Warn) on a `*slog.Logger`
- `WithArgs(args...)` - Pass any other flag to anvil

## Usage Examples

Every mutating RPC method takes a `context.Context` as its first argument. Tests can use `t.Context()`; production callers should use their request context or `context.Background()` with a deadline.

### Block Mining

```go
ctx := context.Background()

// Mine a single block
err := anvil.MineBlock(ctx)

// Mine multiple blocks
err = anvil.Mine(ctx, 5, 0)   // 5 blocks, default timestamps
err = anvil.Mine(ctx, 5, 12)  // 5 blocks, 12 seconds apart

// Mine one block at a specific timestamp
err = anvil.SetNextBlockTimestamp(ctx, time.Now().Add(time.Hour).Unix())
err = anvil.MineBlock(ctx)

// Wait for a specific block
waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
err = anvil.WaitForBlock(waitCtx, 10)
```

### Time Manipulation

```go
// Increase time by 1 hour
err := anvil.IncreaseTime(ctx, 3600)

// Set timestamp for next block
err = anvil.SetNextBlockTimestamp(ctx, time.Now().Unix())
```

### Account Management

```go
// Get the ten default funded accounts
accounts := anvil.Accounts()
key, sender := accounts[0].Key, accounts[0].Address

// Set account balance
address := common.HexToAddress("0x...")
err := anvil.SetBalance(ctx, address, big.NewInt(1e18))  // 1 ETH

// Impersonate an account (send transactions without private key)
err = anvil.Impersonate(ctx, address)
// ... send transactions as this address
err = anvil.StopImpersonating(ctx, address)

// Set account nonce
err = anvil.SetNonce(ctx, address, 42)
```

### State Management

```go
// Reset to initial state (fast - uses RPC)
err := anvil.ResetState(ctx)

// Create a snapshot
snapshotID, err := anvil.Snapshot(ctx)

// Revert to snapshot
success, err := anvil.Revert(ctx, snapshotID)
```

### Advanced Operations

```go
// Set bytecode at address
err := anvil.SetCode(ctx, address, common.FromHex("0x6080604052..."))

// Modify storage slot
err = anvil.SetStorageAt(ctx, address, common.HexToHash("0x0"), common.HexToHash("0x2a"))

// Control mining
err = anvil.SetAutomine(ctx, false)  // Disable automatic mining
err = anvil.SetIntervalMining(ctx, 5)  // Mine every 5 seconds

// Enable auto-impersonation
err = anvil.AutoImpersonate(ctx, true)

// Drop a pending transaction; dropped is false if it wasn't in the pool
dropped, err := anvil.DropTransaction(ctx, txHash)
```

## Testing

Anvil picks its own port and starts in about 50 ms, so give each test its own instance and run the tests in parallel:

```go
func TestMyContract(t *testing.T) {
    t.Parallel()

    node, err := anvil.NewAnvil()
    require.NoError(t, err)
    require.NoError(t, node.Start())
    t.Cleanup(func() { _ = node.Close() })

    // ... your test
}
```

When setup is expensive (a fork, deployed contracts), share one instance across sequential subtests and call `ResetState(ctx)` between them to return to the state right after `Start`.

This repo's own tests run with `-shuffle=on`. A failing run prints its seed; `go test -shuffle=<seed> ./...` repeats that order.

## API Documentation

Full API documentation is available at [pkg.go.dev](https://pkg.go.dev/github.com/neverDefined/go-anvil).

## Development

```bash
# Run tests
make test

# Run linter
make lint

# Run all checks
make check
```

## License

MIT
