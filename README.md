# Anvil Test Client

A robust Go package for managing Anvil, a blazing-fast local Ethereum development environment. This package provides a powerful interface for programmatic control of Anvil nodes during testing and development.

## Overview

Anvil is Foundry's local testnet node implementation, similar to Ganache. This package provides:
- Full programmatic control over local Ethereum networks
- Comprehensive testing utilities
- Thread-safe metrics collection
- Configurable logging and error handling
- Builder pattern for flexible configuration

## Prerequisites

- [Foundry](https://book.getfoundry.sh/) installed on your system
- Go 1.19 or later

## Installation

1. Install Foundry (which includes Anvil):
```bash
curl -L https://foundry.paradigm.xyz | bash
foundryup
```

2. Import the package:
```go
import "your-project/backend/test/client"
```

## Core Features

### 1. Basic Node Management
```go
// Create and start a node with default configuration
anvil, err := client.NewAnvil()
if err != nil {
    log.Fatal(err)
}
err = anvil.Start(10 * time.Second)
defer anvil.Close()

// Access clients
ethClient := anvil.Client()
rpcClient := anvil.RPCClient()
```

### 2. Builder Pattern Configuration
```go
anvil, err := client.NewAnvilBuilder().
    WithBlockTime("1").
    WithChainId("1337").
    WithGasLimit("12000000").
    WithPort("8545").
    WithLogLevel(zerolog.InfoLevel).
    WithFork("https://eth-mainnet.alchemyapi.io/v2/KEY").
    WithForkBlockNumber("15000000").
    Build()
```

### 3. Chain Manipulation
```go
// Block operations
anvil.MineBlock()
anvil.WaitForBlock(10, 5*time.Second)

// Time manipulation
anvil.IncreaseTime(3600)
anvil.SetNextBlockTimestamp(time.Now().Unix())

// Account operations
keys, addresses, _ := anvil.Accounts()
anvil.SetBalance(address, big.NewInt(100))
anvil.Impersonate(address)
anvil.StopImpersonating(address)
```

### 4. Metrics Collection
```go
metrics := anvil.Metrics()
fmt.Printf("Blocks mined: %d\n", metrics.BlocksMined)
fmt.Printf("RPC calls: %d\n", metrics.RPCCalls)
fmt.Printf("Startup time: %v\n", metrics.StartupTime)
```

## Default Test Accounts

The package provides predefined test accounts:

```go
var AnvilPrivateKeys = [...]AnvilPrivateKey{
    "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
    "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
    "5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a",
}

// Access accounts
keys, addresses, err := anvil.Accounts()
```

## Advanced Features

### State Management
```go
// Reset chain state
err := anvil.Reset()

// Wait for specific block
err = anvil.WaitForBlock(targetBlock, 30*time.Second)
```

### Configuration Management
```go
// Custom configuration
config := client.AnvilConfig{
    DefaultTimeout: 15 * time.Second,
    MaxRetries:     10,
    RetryInterval:  200 * time.Millisecond,
    LogLevel:       zerolog.DebugLevel,
}
```

## Best Practices

1. Use defer for cleanup:
```go
anvil, _ := client.NewAnvil()
err := anvil.Start(10 * time.Second)
defer anvil.Close()
```

2. Handle errors with retries:
```go
err := retry(5, time.Second, func() error {
    return someOperation()
})
```

3. Configure logging appropriately:
```go
anvil, _ := client.NewAnvilBuilder().
    WithLogLevel(zerolog.DebugLevel).
    Build()
```

4. Monitor metrics:
```go
metrics := anvil.Metrics()
if metrics.LastError != nil {
    log.Printf("Last error: %v at %v", metrics.LastError, metrics.LastErrorTime)
}
```

## Testing

The package includes comprehensive tests covering:
- Basic node operations
- Account management
- Time manipulation
- Balance modification
- Account impersonation
- Metrics collection
- Error handling
- Reset functionality

See `anvil_test.go` for detailed testing examples.
