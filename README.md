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
- Thread-safe metrics collection
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
anvil, err := anvil.NewAnvilBuilder().
    WithBlockTime("1").              // Block time in seconds
    WithChainID("1337").              // Custom chain ID
    WithGasLimit("12000000").         // Block gas limit
    WithPort("8545").                 // RPC port
    WithLogLevel(zerolog.InfoLevel).  // Logging level
    WithFork("https://eth-mainnet.alchemyapi.io/v2/YOUR_KEY").
    WithForkBlockNumber("15000000").  // Fork from specific block
    Build()
```

### Available Options

- `WithBlockTime(seconds)` - Set automatic block mining interval
- `WithChainID(id)` - Set the chain ID
- `WithGasLimit(limit)` - Set the block gas limit
- `WithGasPrice(price)` - Set the gas price
- `WithPort(port)` - Set the RPC port
- `WithFork(url)` - Fork from a remote network
- `WithForkBlockNumber(number)` - Fork from a specific block
- `WithLogLevel(level)` - Set logging verbosity

## Usage Examples

Every mutating RPC method takes a `context.Context` as its first argument. Tests can use `t.Context()`; production callers should use their request context or `context.Background()` with a deadline.

### Block Mining

```go
ctx := context.Background()

// Mine a single block
err := anvil.MineBlock(ctx)

// Mine multiple blocks
err = anvil.Mine(ctx, 5, 0)  // Mine 5 blocks with default timestamps

// Mine with specific timestamp
timestamp := uint64(time.Now().Unix())
err = anvil.Mine(ctx, 1, timestamp)

// Wait for a specific block
err = anvil.WaitForBlock(10, 5*time.Second)
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
// Get default test accounts
keys, addresses, err := anvil.Accounts()

// Set account balance
address := common.HexToAddress("0x...")
err = anvil.SetBalance(ctx, address, big.NewInt(1e18))  // 1 ETH

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
err := anvil.SetCode(ctx, address, "0x6080604052...")

// Modify storage slot
err = anvil.SetStorageAt(ctx, address, slot, value)

// Control mining
err = anvil.SetAutomine(ctx, false)  // Disable automatic mining
err = anvil.SetIntervalMining(ctx, 5)  // Mine every 5 seconds

// Enable auto-impersonation
err = anvil.AutoImpersonate(ctx, true)

// Drop pending transaction
err = anvil.DropTransaction(ctx, txHash)
```

### Metrics

```go
metrics := anvil.Metrics()
fmt.Printf("Startup time: %v\n", metrics.StartupTime)
fmt.Printf("Blocks mined: %d\n", metrics.BlocksMined)
fmt.Printf("RPC calls: %d\n", metrics.RPCCalls)
```

## Testing

The library includes comprehensive tests. Use the shared instance pattern for faster test execution:

```go
func TestMyContract(t *testing.T) {
    // Create shared instance once
    anvil, _ := anvil.NewAnvil()
    anvil.Start()
    defer anvil.Close()
    
    t.Run("test case 1", func(t *testing.T) {
        // Reset state before each test
        anvil.ResetState(t.Context())
        // ... your test
    })
    
    t.Run("test case 2", func(t *testing.T) {
        anvil.ResetState(t.Context())
        // ... your test
    })
}
```

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
