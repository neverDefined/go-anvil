# go-anvil

[![CI](https://github.com/neverDefined/go-anvil/workflows/CI/badge.svg)](https://github.com/neverDefined/go-anvil/actions)
[![codecov](https://codecov.io/gh/neverDefined/go-anvil/branch/main/graph/badge.svg)](https://codecov.io/gh/neverDefined/go-anvil)
[![Go Reference](https://pkg.go.dev/badge/github.com/neverDefined/go-anvil.svg)](https://pkg.go.dev/github.com/neverDefined/go-anvil)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

go-anvil runs [anvil](https://book.getfoundry.sh/anvil/), Foundry's local Ethereum node, from Go. Tests get a real chain they can control: mine blocks, move the clock, set balances, impersonate accounts or fork a live network.

## Install

```bash
go get github.com/neverDefined/go-anvil
```

You need Go 1.25 or later and [Foundry](https://book.getfoundry.sh/getting-started/installation), with `anvil` on your `PATH` or in Foundry's install directory (`~/.foundry/bin` by default). CI runs against Foundry 1.9.0.

## Use it in a test

`NewT` starts a node and stops it when the test ends. Each node picks its own port, so tests can run in parallel. A node starts in about 50 ms.

```go
func TestTransfer(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	node := anvil.NewT(t, nil)

	from := node.Accounts()[0].Address
	to := common.HexToAddress("0xb0b")

	_, err := node.SendTransaction(ctx, anvil.TxArgs{From: from, To: &to, Value: big.NewInt(1000)})
	require.NoError(t, err)
	require.NoError(t, node.WaitForMemPoolEmpty(ctx))

	balance, err := node.Client().BalanceAt(ctx, to, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1000), balance.Int64())
}
```

`node.Client()` returns a go-ethereum `ethclient`. `node.RPCClient()` is there for RPC methods this package doesn't wrap.

Outside a test, start and stop the node yourself:

```go
node, err := anvil.New()
if err != nil {
	log.Fatal(err)
}
if err := node.Start(); err != nil {
	log.Fatal(err)
}
defer node.Close()
```

## Configure a node

Pass a builder to `NewT`, or call `Build` and `Start` on it.

```go
node := anvil.NewT(t, anvil.NewBuilder().
	WithChainID(1337).
	WithBlockTime(time.Second). // default: one block per transaction
	WithArgs("--hardfork", "cancun")) // any other anvil flag
```

The other options are `WithFork`, `WithForkBlockNumber`, `WithGasLimit`, `WithPort`, `WithStartupTimeout` and `WithLogger`. anvil's output is discarded unless you set a logger.

## Recipes

These run inside a test, with `ctx := t.Context()` and `node := anvil.NewT(t, nil)`.

### Send from an address without its key

```go
require.NoError(t, node.SetBalance(ctx, whale, big.NewInt(1e18)))
require.NoError(t, node.Impersonate(ctx, whale))

hash, err := node.SendTransaction(ctx, anvil.TxArgs{From: whale, To: &to, Value: big.NewInt(1)})
require.NoError(t, err)
```

### Move time

`IncreaseTime` takes effect in the next block.

```go
require.NoError(t, node.IncreaseTime(ctx, 3600))
require.NoError(t, node.MineBlock(ctx))
```

For an exact time, call `SetNextBlockTimestamp` and then `MineBlock`. `Mine(ctx, 5, 12)` mines five blocks 12 seconds apart.

### Undo changes

```go
id, err := node.Snapshot(ctx)
require.NoError(t, err)

// change the chain, then go back
ok, err := node.Revert(ctx, id)
require.NoError(t, err)
require.True(t, ok)
```

`ResetState(ctx)` goes back to the state right after `Start`, so it also discards anything deployed since. A snapshot works once, so take a new one after each `Revert`.

### Fork a network and give an address tokens

```go
node := anvil.NewT(t, anvil.NewBuilder().WithFork(rpcURL).WithForkBlockNumber(19_000_000))

require.NoError(t, node.DealERC20(ctx, token, holder, big.NewInt(1_000_000)))
require.NoError(t, node.SetERC20Allowance(ctx, token, holder, spender, big.NewInt(1_000_000)))
```

### Keep a fixture between runs

```go
state, err := node.DumpState(ctx)
require.NoError(t, err)

// later, on a new node
require.NoError(t, other.LoadState(ctx, state))
```

### Trace a transaction

```go
raw, err := node.DebugTraceTransaction(ctx, hash, &anvil.TraceConfig{Tracer: "callTracer"})
require.NoError(t, err)
t.Log(string(raw))
```

## Reference

[pkg.go.dev](https://pkg.go.dev/github.com/neverDefined/go-anvil) documents every method and has runnable examples. [CHANGELOG.md](CHANGELOG.md) lists what changed between releases.

## Contributing

`make check` runs formatting, vet, lint and tests. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT
