package anvil

import (
	"context"
	"errors"
	"math/big"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests. Each test starts its own anvil (about 50ms) on a port anvil
// picks, so tests run in parallel and never share chain state.

// requireAnvil skips the test when anvil isn't installed, so `go test ./...`
// works without Foundry. A broken install fails, and so does a missing anvil
// when CI is set, so the integration tests can't be skipped by accident.
func requireAnvil(tb testing.TB) {
	tb.Helper()
	_, err := resolveAnvilPath()
	if err == nil {
		return
	}
	if !errors.Is(err, ErrAnvilNotFound) || os.Getenv("CI") != "" {
		tb.Fatalf("anvil is unavailable: %v", err)
	}
	tb.Skipf("skipping integration test: %v", err)
}

// startAnvil builds and starts an instance with logging off, and closes it
// when the test ends.
func startAnvil(tb testing.TB, b *AnvilBuilder) *Anvil {
	tb.Helper()
	requireAnvil(tb)
	a, err := b.WithLogLevel(zerolog.Disabled).Build()
	require.NoError(tb, err)
	require.NoError(tb, a.Start())
	tb.Cleanup(func() { _ = a.Close() })
	return a
}

func head(t *testing.T, a *Anvil) *types.Header {
	t.Helper()
	h, err := a.Client().HeaderByNumber(t.Context(), nil)
	require.NoError(t, err)
	return h
}

func balanceOf(t *testing.T, a *Anvil, addr common.Address) *big.Int {
	t.Helper()
	b, err := a.Client().BalanceAt(t.Context(), addr, nil)
	require.NoError(t, err)
	return b
}

// sendTx sends wei from `from` with eth_sendTransaction. anvil signs it for
// its default accounts and for impersonated addresses, and rejects it otherwise.
func sendTx(ctx context.Context, a *Anvil, from, to common.Address, wei int64) (common.Hash, error) {
	var hash common.Hash
	err := a.RPCClient().CallContext(ctx, &hash, "eth_sendTransaction", map[string]any{
		"from":  from,
		"to":    to,
		"value": hexutil.EncodeBig(big.NewInt(wei)),
	})
	return hash, err
}

// receipt waits for hash to be mined. With automine on, anvil mines the
// transaction just after eth_sendTransaction returns, not before.
func receipt(t *testing.T, a *Anvil, hash common.Hash) *types.Receipt {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		r, err := a.Client().TransactionReceipt(ctx, hash)
		if err == nil {
			return r
		}
		if !errors.Is(err, ethereum.NotFound) {
			require.NoError(t, err, "transaction %s", hash)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("transaction %s was not mined: %v", hash, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

var (
	stranger  = common.HexToAddress("0x1234567890123456789012345678901234567890") // no key in anvil
	recipient = common.HexToAddress("0x000000000000000000000000000000000000bEEF")
)

func TestBuilderOptions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder().
		WithChainID("1337").
		WithGasLimit("12000000").
		WithGasPrice("20000000000"))

	chainID, err := a.Client().ChainID(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1337), chainID.Int64())

	assert.Equal(t, uint64(12_000_000), head(t, a).GasLimit)
	// WithGasPrice is only checked for being accepted: on anvil 1.9 with the
	// default hardfork, eth_gasPrice and default fees stay at 2 gwei either way.
}

func TestBlockTime(t *testing.T) {
	t.Parallel()
	a := startAnvil(t, NewAnvilBuilder().WithBlockTime("1"))

	// Blocks arrive without MineBlock.
	require.NoError(t, a.WaitForBlock(1, 5*time.Second))
}

func TestAccounts(t *testing.T) {
	t.Parallel()
	a := startAnvil(t, NewAnvilBuilder())

	_, addrs, err := a.Accounts()
	require.NoError(t, err)

	// anvil's first three default accounts. A wrong key derives a different address.
	want := []common.Address{
		common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"),
		common.HexToAddress("0x70997970C51812dc3A010C7d01b50e0d17dc79C8"),
		common.HexToAddress("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"),
	}
	assert.Equal(t, want, addrs[:])
	for _, addr := range addrs {
		assert.Positive(t, balanceOf(t, a, addr).Sign(), "%s should be funded", addr)
	}
}

func TestMineBlock(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())

	require.NoError(t, a.MineBlock(ctx))
	require.NoError(t, a.MineBlock(ctx))
	assert.Equal(t, uint64(2), head(t, a).Number.Uint64())
}

func TestMine(t *testing.T) {
	t.Parallel()

	t.Run("mines n blocks", func(t *testing.T) {
		t.Parallel()
		a := startAnvil(t, NewAnvilBuilder())

		require.NoError(t, a.Mine(t.Context(), 5, 0))
		assert.Equal(t, uint64(5), head(t, a).Number.Uint64())
	})

	// The second argument spaces blocks apart; it is not a timestamp (#88).
	t.Run("interval spaces block timestamps", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewAnvilBuilder())
		start := head(t, a)

		const interval = 100
		require.NoError(t, a.Mine(ctx, 3, interval))

		var times []uint64
		for i := uint64(1); i <= 3; i++ {
			h, err := a.Client().HeaderByNumber(ctx, new(big.Int).SetUint64(start.Number.Uint64()+i))
			require.NoError(t, err)
			times = append(times, h.Time)
		}
		// The first block is timed from the node's clock, the rest from each other.
		assert.GreaterOrEqual(t, times[0], start.Time+interval)
		assert.Less(t, times[0], start.Time+interval+60, "a timestamp, not an interval, would be decades ahead")
		// Timestamps follow the wall clock, so a second boundary can add 1.
		assert.InDelta(t, times[0]+interval, times[1], 2)
		assert.InDelta(t, times[1]+interval, times[2], 2)
	})
}

func TestTime(t *testing.T) {
	t.Parallel()

	t.Run("SetNextBlockTimestamp sets the next block's time", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewAnvilBuilder())

		next := head(t, a).Time + 1000
		require.NoError(t, a.SetNextBlockTimestamp(ctx, int64(next)))
		require.NoError(t, a.MineBlock(ctx))
		assert.Equal(t, next, head(t, a).Time)
	})

	t.Run("IncreaseTime moves the clock forward", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewAnvilBuilder())

		before := head(t, a).Time
		require.NoError(t, a.IncreaseTime(ctx, 3600))
		require.NoError(t, a.MineBlock(ctx))

		after := head(t, a).Time
		assert.GreaterOrEqual(t, after, before+3600)
		assert.Less(t, after, before+3600+60, "only wall-clock seconds since Start should be added")
	})
}

func TestSetBalance(t *testing.T) {
	t.Parallel()
	a := startAnvil(t, NewAnvilBuilder())

	require.NoError(t, a.SetBalance(t.Context(), stranger, big.NewInt(123456789)))
	assert.Equal(t, big.NewInt(123456789), balanceOf(t, a, stranger))
}

func TestSetNonce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())

	require.NoError(t, a.SetNonce(ctx, stranger, 42))
	nonce, err := a.Client().NonceAt(ctx, stranger, nil)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), nonce)
}

func TestSetCode(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())

	code := hexutil.MustDecode("0x6080604052348015600f57600080fd5b50603f80601d6000396000f3fe")
	require.NoError(t, a.SetCode(ctx, stranger, hexutil.Encode(code)))

	got, err := a.Client().CodeAt(ctx, stranger, nil)
	require.NoError(t, err)
	assert.Equal(t, code, got)
}

func TestSetStorageAt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())

	slot := common.HexToHash("0x1")
	value := common.HexToHash("0x2a")
	require.NoError(t, a.SetStorageAt(ctx, stranger, slot.Hex(), value.Hex()))

	got, err := a.Client().StorageAt(ctx, stranger, slot, nil)
	require.NoError(t, err)
	assert.Equal(t, value.Bytes(), got)
}

func TestImpersonate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())
	require.NoError(t, a.SetBalance(ctx, stranger, big.NewInt(1e18)))

	_, err := sendTx(ctx, a, stranger, recipient, 1000)
	require.Error(t, err, "anvil can't sign for an address it doesn't hold a key for")

	require.NoError(t, a.Impersonate(ctx, stranger))
	hash, err := sendTx(ctx, a, stranger, recipient, 1000)
	require.NoError(t, err)
	assert.Equal(t, types.ReceiptStatusSuccessful, receipt(t, a, hash).Status)
	assert.Equal(t, big.NewInt(1000), balanceOf(t, a, recipient))

	require.NoError(t, a.StopImpersonating(ctx, stranger))
	_, err = sendTx(ctx, a, stranger, recipient, 1000)
	require.Error(t, err)
}

func TestAutoImpersonate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())
	require.NoError(t, a.SetBalance(ctx, stranger, big.NewInt(1e18)))

	require.NoError(t, a.AutoImpersonate(ctx, true))
	hash, err := sendTx(ctx, a, stranger, recipient, 1000)
	require.NoError(t, err)
	assert.Equal(t, types.ReceiptStatusSuccessful, receipt(t, a, hash).Status)
	assert.Equal(t, big.NewInt(1000), balanceOf(t, a, recipient))

	require.NoError(t, a.AutoImpersonate(ctx, false))
	_, err = sendTx(ctx, a, stranger, recipient, 1000)
	require.Error(t, err)
}

func TestSetAutomine(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())
	_, addrs, err := a.Accounts()
	require.NoError(t, err)

	require.NoError(t, a.SetAutomine(ctx, false))
	hash, err := sendTx(ctx, a, addrs[0], addrs[1], 1)
	require.NoError(t, err)
	_, err = a.Client().TransactionReceipt(ctx, hash)
	require.ErrorIs(t, err, ethereum.NotFound, "with automine off the tx waits in the pool")

	require.NoError(t, a.MineBlock(ctx))
	mined, err := a.Client().TransactionReceipt(ctx, hash)
	require.NoError(t, err, "MineBlock includes the pending tx before it returns")
	assert.Equal(t, uint64(1), mined.BlockNumber.Uint64())

	require.NoError(t, a.SetAutomine(ctx, true))
	hash, err = sendTx(ctx, a, addrs[0], addrs[1], 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), receipt(t, a, hash).BlockNumber.Uint64(), "automine gives the tx its own block")
}

func TestSetIntervalMining(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())

	require.NoError(t, a.SetIntervalMining(ctx, 1))
	require.NoError(t, a.WaitForBlock(1, 5*time.Second))

	require.NoError(t, a.SetIntervalMining(ctx, 0))
	n := head(t, a).Number.Uint64()
	err := a.WaitForBlock(n+1, 1500*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded, "no block should arrive once interval mining is off")
}

func TestDropTransaction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())
	_, addrs, err := a.Accounts()
	require.NoError(t, err)

	require.NoError(t, a.SetAutomine(ctx, false))
	hash, err := sendTx(ctx, a, addrs[0], addrs[1], 1)
	require.NoError(t, err)

	require.NoError(t, a.DropTransaction(ctx, hash.Hex()))
	require.NoError(t, a.MineBlock(ctx))

	block, err := a.Client().BlockByNumber(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, block.Transactions())
	_, err = a.Client().TransactionReceipt(ctx, hash)
	require.ErrorIs(t, err, ethereum.NotFound)
}

func TestSnapshotRevert(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())

	id, err := a.Snapshot(ctx)
	require.NoError(t, err)
	require.NoError(t, a.Mine(ctx, 2, 0))

	ok, err := a.Revert(ctx, id)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(0), head(t, a).Number.Uint64())

	// Reverting consumes the snapshot.
	ok, err = a.Revert(ctx, id)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestResetState(t *testing.T) {
	t.Parallel()

	t.Run("first call returns to the state after Start", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewAnvilBuilder())
		_, addrs, err := a.Accounts()
		require.NoError(t, err)
		funded := balanceOf(t, a, addrs[0])

		require.NoError(t, a.SetBalance(ctx, addrs[0], big.NewInt(1)))
		require.NoError(t, a.Mine(ctx, 5, 0))
		require.NoError(t, a.ResetState(ctx))

		assert.Equal(t, uint64(0), head(t, a).Number.Uint64())
		assert.Equal(t, funded, balanceOf(t, a, addrs[0]))
	})

	t.Run("can be called repeatedly", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewAnvilBuilder())

		for range 3 {
			require.NoError(t, a.MineBlock(ctx))
			require.NoError(t, a.ResetState(ctx))
			assert.Equal(t, uint64(0), head(t, a).Number.Uint64())
		}
	})

	// Regression for #79: concurrent resets used to consume the same snapshot.
	t.Run("concurrent calls all succeed", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewAnvilBuilder())
		require.NoError(t, a.Mine(ctx, 3, 0))

		errs := make([]error, 8)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Go(func() { errs[i] = a.ResetState(ctx) })
		}
		wg.Wait()
		for i, err := range errs {
			require.NoError(t, err, "call %d", i)
		}
		assert.Equal(t, uint64(0), head(t, a).Number.Uint64())
	})

	t.Run("a baseline consumed by a direct Revert is reported", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewAnvilBuilder())

		ok, err := a.Revert(ctx, a.baseline)
		require.NoError(t, err)
		require.True(t, ok)

		require.ErrorContains(t, a.ResetState(ctx), "no longer exists")
	})
}

func TestGetProof(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())
	_, addrs, err := a.Accounts()
	require.NoError(t, err)

	slot := "0x0000000000000000000000000000000000000000000000000000000000000001"
	require.NoError(t, a.SetStorageAt(ctx, addrs[0], slot, common.HexToHash("0x2a").Hex()))
	// Mine so the head block's state root includes the storage write.
	require.NoError(t, a.MineBlock(ctx))

	// Pin the block so the proof and the header describe the same state.
	header := head(t, a)
	proof, err := a.GetProof(ctx, addrs[0], []string{slot}, header.Number)
	require.NoError(t, err)

	assert.Equal(t, addrs[0], proof.Address)
	assert.NotEmpty(t, proof.AccountProof)
	balance, err := a.Client().BalanceAt(ctx, addrs[0], header.Number)
	require.NoError(t, err)
	assert.Zero(t, balance.Cmp(proof.Balance.ToInt()))
	require.Len(t, proof.StorageProof, 1)
	assert.Equal(t, int64(42), proof.StorageProof[0].Value.ToInt().Int64())

	// The light-client property the API exists for.
	require.NoError(t, VerifyProof(header.Root, proof))

	tampered := *proof
	tampered.Balance = (*hexutil.Big)(new(big.Int).Add(proof.Balance.ToInt(), big.NewInt(1)))
	require.Error(t, VerifyProof(header.Root, &tampered))

	// Regression: a zero-valued slot on an account with an empty storage trie
	// (any EOA) must verify as an absence proof.
	emptyProof, err := a.GetProof(ctx, addrs[1], []string{slot}, header.Number)
	require.NoError(t, err)
	assert.Equal(t, types.EmptyRootHash, emptyProof.StorageHash)
	require.NoError(t, VerifyProof(header.Root, emptyProof))
}

func TestWaitForBlock(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())

	require.NoError(t, a.Mine(ctx, 3, 0))
	require.NoError(t, a.WaitForBlock(3, time.Second))
	require.ErrorIs(t, a.WaitForBlock(100, 200*time.Millisecond), context.DeadlineExceeded)
}

func TestWaitForMemPoolEmpty(t *testing.T) {
	t.Parallel()

	t.Run("returns for an idle pool", func(t *testing.T) {
		t.Parallel()
		a := startAnvil(t, NewAnvilBuilder())
		require.NoError(t, a.WaitForMemPoolEmpty(t.Context(), time.Second))
	})

	t.Run("returns ctx's error", func(t *testing.T) {
		t.Parallel()
		a := startAnvil(t, NewAnvilBuilder())
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, a.WaitForMemPoolEmpty(ctx, time.Second), context.Canceled)
	})

	t.Run("counts pending transactions", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewAnvilBuilder())
		require.NoError(t, a.SetAutomine(ctx, false))
		_, addrs, err := a.Accounts()
		require.NoError(t, err)

		_, err = sendTx(ctx, a, addrs[0], addrs[1], 1)
		require.NoError(t, err)

		err = a.WaitForMemPoolEmpty(ctx, 300*time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)

		require.NoError(t, a.MineBlock(ctx))
		require.NoError(t, a.WaitForMemPoolEmpty(ctx, time.Second))
	})

	t.Run("counts queued transactions", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewAnvilBuilder())
		require.NoError(t, a.SetAutomine(ctx, false))
		_, addrs, err := a.Accounts()
		require.NoError(t, err)

		send := func(nonce uint64) {
			var hash common.Hash
			require.NoError(t, a.RPCClient().CallContext(ctx, &hash, "eth_sendTransaction", map[string]any{
				"from":  addrs[0],
				"to":    addrs[1],
				"value": "0x1",
				"nonce": hexutil.Uint64(nonce),
			}))
		}

		// A nonce ahead of the account's next nonce leaves the transaction
		// queued, not pending, so a pending-only count would see an empty pool.
		send(5)
		err = a.WaitForMemPoolEmpty(ctx, 300*time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)

		// Filling the nonce gap makes it mineable. (DropTransaction can't clear
		// it: anvil 1.5.1 only drops pending transactions.)
		for nonce := range uint64(5) {
			send(nonce)
		}
		require.NoError(t, a.MineBlock(ctx))
		require.NoError(t, a.WaitForMemPoolEmpty(ctx, time.Second))
	})
}

// TestConcurrentCalls runs mixed calls from many goroutines so -race can see
// the shared counters and client access.
func TestConcurrentCalls(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewAnvilBuilder())
	_, addrs, err := a.Accounts()
	require.NoError(t, err)

	before := a.Metrics()
	deadline := time.Now().Add(300 * time.Millisecond)
	errs := make([]error, 50)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			for time.Now().Before(deadline) && errs[i] == nil {
				switch i % 3 {
				case 0:
					errs[i] = a.MineBlock(ctx)
				case 1:
					errs[i] = a.SetBalance(ctx, addrs[0], big.NewInt(int64(i+1)))
				case 2:
					_ = a.Metrics()
				}
			}
		})
	}
	wg.Wait()
	require.NoError(t, errors.Join(errs...))

	after := a.Metrics()
	assert.Greater(t, after.RPCCalls, before.RPCCalls)
	assert.Greater(t, after.BlocksMined, before.BlocksMined)
}
