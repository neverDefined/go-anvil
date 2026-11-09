package anvil

import (
	"context"
	"encoding/json"
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

// startAnvil starts an instance for the test, or skips it without anvil.
func startAnvil(tb testing.TB, b *Builder) *Anvil {
	tb.Helper()
	requireAnvil(tb)
	return NewT(tb, b)
}

// waitForEmptyPool calls WaitForMemPoolEmpty with a deadline.
func waitForEmptyPool(t *testing.T, a *Anvil, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	return a.WaitForMemPoolEmpty(ctx)
}

func addresses(a *Anvil) []common.Address {
	accounts := a.Accounts()
	out := make([]common.Address, len(accounts))
	for i, account := range accounts {
		out[i] = account.Address
	}
	return out
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

// sendTx sends wei from `from`. anvil signs it for its default accounts and for
// impersonated addresses, and rejects it otherwise.
func sendTx(ctx context.Context, a *Anvil, from, to common.Address, wei int64) (common.Hash, error) {
	return a.SendTransaction(ctx, TxArgs{From: from, To: &to, Value: big.NewInt(wei)})
}

// waitForBlock calls WaitForBlock with a deadline.
func waitForBlock(t *testing.T, a *Anvil, number uint64, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	return a.WaitForBlock(ctx, number)
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
	a := startAnvil(t, NewBuilder().
		WithChainID(1337).
		WithGasLimit(12_000_000))

	chainID, err := a.Client().ChainID(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1337), chainID.Int64())

	assert.Equal(t, uint64(12_000_000), head(t, a).GasLimit)
}

func TestWithArgs(t *testing.T) {
	t.Parallel()
	a := startAnvil(t, NewBuilder().WithArgs("--hardfork", "berlin", "--gas-price", "20000000000"))

	// anvil only uses --gas-price before the London hardfork.
	gasPrice, err := a.Client().SuggestGasPrice(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(20_000_000_000), gasPrice.Int64())
}

func TestBlockTime(t *testing.T) {
	t.Parallel()
	a := startAnvil(t, NewBuilder().WithBlockTime(200*time.Millisecond))

	// Blocks arrive without MineBlock.
	require.NoError(t, waitForBlock(t, a, 2, 5*time.Second))
}

func TestAccounts(t *testing.T) {
	t.Parallel()
	a := startAnvil(t, NewBuilder())

	var want []common.Address
	require.NoError(t, a.RPCClient().CallContext(t.Context(), &want, "eth_accounts"))

	accounts := a.Accounts()
	require.Len(t, accounts, len(want))
	for i, account := range accounts {
		assert.Equal(t, want[i], account.Address, "account %d", i)
		assert.Positive(t, balanceOf(t, a, account.Address).Sign(), "account %d should be funded", i)
	}
}

func TestMineBlock(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())

	require.NoError(t, a.MineBlock(ctx))
	require.NoError(t, a.MineBlock(ctx))
	assert.Equal(t, uint64(2), head(t, a).Number.Uint64())
}

func TestMine(t *testing.T) {
	t.Parallel()

	t.Run("mines n blocks", func(t *testing.T) {
		t.Parallel()
		a := startAnvil(t, NewBuilder())

		require.NoError(t, a.Mine(t.Context(), 5, 0))
		assert.Equal(t, uint64(5), head(t, a).Number.Uint64())
	})

	// The second argument spaces blocks apart; it is not a timestamp (#88).
	t.Run("interval spaces block timestamps", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewBuilder())
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
		a := startAnvil(t, NewBuilder())

		next := head(t, a).Time + 1000
		require.NoError(t, a.SetNextBlockTimestamp(ctx, int64(next)))
		require.NoError(t, a.MineBlock(ctx))
		assert.Equal(t, next, head(t, a).Time)
	})

	t.Run("IncreaseTime moves the clock forward", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewBuilder())

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
	a := startAnvil(t, NewBuilder())

	require.NoError(t, a.SetBalance(t.Context(), stranger, big.NewInt(123456789)))
	assert.Equal(t, big.NewInt(123456789), balanceOf(t, a, stranger))
}

func TestSetNonce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())

	require.NoError(t, a.SetNonce(ctx, stranger, 42))
	nonce, err := a.Client().NonceAt(ctx, stranger, nil)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), nonce)
}

func TestSetCode(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())

	code := hexutil.MustDecode("0x6080604052348015600f57600080fd5b50603f80601d6000396000f3fe")
	require.NoError(t, a.SetCode(ctx, stranger, code))

	got, err := a.Client().CodeAt(ctx, stranger, nil)
	require.NoError(t, err)
	assert.Equal(t, code, got)
}

func TestSetStorageAt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())

	slot := common.HexToHash("0x1")
	value := common.HexToHash("0x2a")
	require.NoError(t, a.SetStorageAt(ctx, stranger, slot, value))

	got, err := a.Client().StorageAt(ctx, stranger, slot, nil)
	require.NoError(t, err)
	assert.Equal(t, value.Bytes(), got)
}

func TestImpersonate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())
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
	a := startAnvil(t, NewBuilder())
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
	a := startAnvil(t, NewBuilder())
	addrs := addresses(a)

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
	a := startAnvil(t, NewBuilder())

	require.NoError(t, a.SetIntervalMining(ctx, 1))
	require.NoError(t, waitForBlock(t, a, 1, 5*time.Second))

	require.NoError(t, a.SetIntervalMining(ctx, 0))
	n := head(t, a).Number.Uint64()
	err := waitForBlock(t, a, n+1, 1500*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded, "no block should arrive once interval mining is off")
}

func TestDropTransaction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())
	addrs := addresses(a)

	require.NoError(t, a.SetAutomine(ctx, false))
	hash, err := sendTx(ctx, a, addrs[0], addrs[1], 1)
	require.NoError(t, err)

	dropped, err := a.DropTransaction(ctx, hash)
	require.NoError(t, err)
	assert.True(t, dropped)

	dropped, err = a.DropTransaction(ctx, hash)
	require.NoError(t, err)
	assert.False(t, dropped, "the transaction is no longer in the pool")

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
	a := startAnvil(t, NewBuilder())

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

	// It also discards snapshots taken after it.
	first, err := a.Snapshot(ctx)
	require.NoError(t, err)
	second, err := a.Snapshot(ctx)
	require.NoError(t, err)
	ok, err = a.Revert(ctx, first)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = a.Revert(ctx, second)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestResetState(t *testing.T) {
	t.Parallel()

	t.Run("first call returns to the state after Start", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewBuilder())
		addrs := addresses(a)
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
		a := startAnvil(t, NewBuilder())

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
		a := startAnvil(t, NewBuilder())
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
		a := startAnvil(t, NewBuilder())

		ok, err := a.Revert(ctx, a.baseline)
		require.NoError(t, err)
		require.True(t, ok)

		require.ErrorContains(t, a.ResetState(ctx), "no longer exists")
	})
}

func TestGetProof(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())
	addrs := addresses(a)

	slot := common.HexToHash("0x1")
	require.NoError(t, a.SetStorageAt(ctx, addrs[0], slot, common.HexToHash("0x2a")))
	// Mine so the head block's state root includes the storage write.
	require.NoError(t, a.MineBlock(ctx))

	// Pin the block so the proof and the header describe the same state.
	header := head(t, a)
	proof, err := a.GetProof(ctx, addrs[0], []common.Hash{slot}, header.Number)
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
	emptyProof, err := a.GetProof(ctx, addrs[1], []common.Hash{slot}, header.Number)
	require.NoError(t, err)
	assert.Equal(t, types.EmptyRootHash, emptyProof.StorageHash)
	require.NoError(t, VerifyProof(header.Root, emptyProof))
}

func TestWaitForBlock(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())

	require.NoError(t, a.Mine(ctx, 3, 0))
	require.NoError(t, waitForBlock(t, a, 3, time.Second))
	require.ErrorIs(t, waitForBlock(t, a, 100, 200*time.Millisecond), context.DeadlineExceeded)
}

func TestWaitForMemPoolEmpty(t *testing.T) {
	t.Parallel()

	t.Run("returns for an idle pool", func(t *testing.T) {
		t.Parallel()
		a := startAnvil(t, NewBuilder())
		require.NoError(t, waitForEmptyPool(t, a, time.Second))
	})

	t.Run("returns ctx's error", func(t *testing.T) {
		t.Parallel()
		a := startAnvil(t, NewBuilder())
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, a.WaitForMemPoolEmpty(ctx), context.Canceled)
	})

	t.Run("counts pending transactions", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewBuilder())
		require.NoError(t, a.SetAutomine(ctx, false))
		addrs := addresses(a)

		_, err := sendTx(ctx, a, addrs[0], addrs[1], 1)
		require.NoError(t, err)

		err = waitForEmptyPool(t, a, 300*time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)

		require.NoError(t, a.MineBlock(ctx))
		require.NoError(t, waitForEmptyPool(t, a, time.Second))
	})

	t.Run("counts queued transactions", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewBuilder())
		require.NoError(t, a.SetAutomine(ctx, false))
		addrs := addresses(a)

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
		err := waitForEmptyPool(t, a, 300*time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)

		// Filling the nonce gap makes it mineable. (DropTransaction can't clear
		// it: anvil 1.5.1 only drops pending transactions.)
		for nonce := range uint64(5) {
			send(nonce)
		}
		require.NoError(t, a.MineBlock(ctx))
		require.NoError(t, waitForEmptyPool(t, a, time.Second))
	})
}

func TestNewT(t *testing.T) {
	t.Parallel()
	requireAnvil(t)

	var a *Anvil
	t.Run("the node runs during the test", func(t *testing.T) {
		a = NewT(t, nil)
		require.NoError(t, a.MineBlock(t.Context()))
	})
	assert.Empty(t, a.URL(), "the node stops when the test ends")

	chainID, err := NewT(t, NewBuilder().WithChainID(1337)).Client().ChainID(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(1337), chainID.Int64())
}

func TestSendTransaction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())
	from := a.Accounts()[0].Address
	to := recipient

	hash, err := a.SendTransaction(ctx, TxArgs{From: from, To: &to, Value: big.NewInt(1000), Data: []byte{0x01}, Gas: 100_000})
	require.NoError(t, err)
	assert.Equal(t, types.ReceiptStatusSuccessful, receipt(t, a, hash).Status)
	assert.Equal(t, int64(1000), balanceOf(t, a, to).Int64())

	tx, _, err := a.Client().TransactionByHash(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, []byte{0x01}, tx.Data())
	assert.Equal(t, uint64(100_000), tx.Gas())

	// Without To, Data is init code. This one deploys the single byte 0x00.
	initCode := hexutil.MustDecode("0x6001600c60003960016000f300")
	hash, err = a.SendTransaction(ctx, TxArgs{From: from, Data: initCode})
	require.NoError(t, err)
	code, err := a.Client().CodeAt(ctx, receipt(t, a, hash).ContractAddress, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte{0x00}, code)
}

func TestDialWS(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())
	assert.Equal(t, "ws"+a.URL()[len("http"):], a.WSURL())

	ws, err := a.DialWS(ctx)
	require.NoError(t, err)
	defer ws.Close()

	heads := make(chan *types.Header, 1)
	sub, err := ws.SubscribeNewHead(ctx, heads)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	require.NoError(t, a.MineBlock(ctx))
	select {
	case h := <-heads:
		assert.Equal(t, int64(1), h.Number.Int64())
	case err := <-sub.Err():
		t.Fatalf("subscription failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no new head arrived")
	}
}

func TestDumpAndLoadState(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	code := []byte{0x60, 0x00}

	source := startAnvil(t, NewBuilder())
	require.NoError(t, source.SetCode(ctx, recipient, code))
	require.NoError(t, source.SetBalance(ctx, stranger, big.NewInt(1234)))
	require.NoError(t, source.MineBlock(ctx))
	state, err := source.DumpState(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, state)

	target := startAnvil(t, NewBuilder())
	bystander := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	require.NoError(t, target.SetBalance(ctx, bystander, big.NewInt(7)))

	require.NoError(t, target.LoadState(ctx, state))
	assert.Equal(t, int64(1), head(t, target).Number.Int64(), "the head moves to the dumped block")
	assert.Equal(t, int64(1234), balanceOf(t, target, stranger).Int64())
	got, err := target.Client().CodeAt(ctx, recipient, nil)
	require.NoError(t, err)
	assert.Equal(t, code, got)
	assert.Equal(t, int64(7), balanceOf(t, target, bystander).Int64(), "LoadState merges, so other accounts stay")

	require.NoError(t, target.ResetState(ctx))
	assert.Zero(t, balanceOf(t, target, stranger).Sign(), "ResetState undoes a load")
}

// tokenRuntime is the deployed bytecode of a minimal ERC-20, compiled with
// `solc --optimize --no-cbor-metadata --bin-runtime`:
//
//	contract Token {
//	    mapping(address => uint256) public balanceOf;
//	    mapping(address => mapping(address => uint256)) public allowance;
//	}
const tokenRuntime = "0x6080604052348015600e575f5ffd5b50600436106030575f3560e01c806370a08231146034578063dd62ed3e146062575b5f5ffd5b6050603f36600460a3565b5f6020819052908152604090205481565b60405190815260200160405180910390f35b6050606d36600460c0565b600160209081525f928352604080842090915290825290205481565b80356001600160a01b0381168114609e575f5ffd5b919050565b5f6020828403121560b2575f5ffd5b60b9826089565b9392505050565b5f5f6040838503121560d0575f5ffd5b60d7836089565b915060e3602084016089565b9050925092905056"

// tokenView calls a view function that takes addresses and returns a uint256.
func tokenView(t *testing.T, a *Anvil, token common.Address, selector string, args ...common.Address) int64 {
	t.Helper()
	data := hexutil.MustDecode(selector)
	for _, arg := range args {
		data = append(data, common.LeftPadBytes(arg.Bytes(), 32)...)
	}
	out, err := a.Client().CallContract(t.Context(), ethereum.CallMsg{To: &token, Data: data}, nil)
	require.NoError(t, err)
	return new(big.Int).SetBytes(out).Int64()
}

func TestERC20(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())
	const balanceOfSelector, allowanceSelector = "0x70a08231", "0xdd62ed3e"
	token := common.HexToAddress("0x00000000000000000000000000000000000000dd")
	require.NoError(t, a.SetCode(ctx, token, hexutil.MustDecode(tokenRuntime)))

	require.NoError(t, a.DealERC20(ctx, token, stranger, big.NewInt(42)))
	assert.Equal(t, int64(42), tokenView(t, a, token, balanceOfSelector, stranger))
	assert.Zero(t, tokenView(t, a, token, balanceOfSelector, recipient))

	require.NoError(t, a.SetERC20Allowance(ctx, token, stranger, recipient, big.NewInt(7)))
	assert.Equal(t, int64(7), tokenView(t, a, token, allowanceSelector, stranger, recipient))
	assert.Zero(t, tokenView(t, a, token, allowanceSelector, recipient, stranger))

	require.Error(t, a.DealERC20(ctx, stranger, recipient, big.NewInt(1)), "an address with no code has no balance slot")
}

func TestBlockControls(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())

	require.NoError(t, a.SetNextBlockBaseFee(ctx, 1234))
	require.NoError(t, a.SetCoinbase(ctx, recipient))
	require.NoError(t, a.SetBlockGasLimit(ctx, 20_000_000))
	require.NoError(t, a.MineBlock(ctx))

	h := head(t, a)
	assert.Equal(t, int64(1234), h.BaseFee.Int64())
	assert.Equal(t, recipient, h.Coinbase)
	assert.Equal(t, uint64(20_000_000), h.GasLimit)
}

func TestReset(t *testing.T) {
	t.Parallel()

	t.Run("nil starts a fresh local chain", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewBuilder())
		from := a.Accounts()[0].Address

		require.NoError(t, a.SetBalance(ctx, from, big.NewInt(1)))
		require.NoError(t, a.Mine(ctx, 3, 0))
		id, err := a.Snapshot(ctx)
		require.NoError(t, err)

		require.NoError(t, a.Reset(ctx, nil))
		assert.Zero(t, head(t, a).Number.Sign())
		assert.Positive(t, balanceOf(t, a, from).Cmp(big.NewInt(1)), "default accounts are funded again")

		ok, err := a.Revert(ctx, id)
		require.NoError(t, err)
		assert.False(t, ok, "anvil discards snapshots on reset")

		require.NoError(t, a.MineBlock(ctx))
		require.NoError(t, a.ResetState(ctx))
		assert.Zero(t, head(t, a).Number.Sign(), "ResetState returns to the reset chain")
	})

	t.Run("options fork the upstream again", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		upstream := startAnvil(t, NewBuilder())
		require.NoError(t, upstream.SetBalance(ctx, stranger, big.NewInt(555)))
		require.NoError(t, upstream.Mine(ctx, 5, 0))
		a := startAnvil(t, NewBuilder().WithFork(upstream.URL()).WithForkBlockNumber(3))
		require.Equal(t, int64(3), head(t, a).Number.Int64())
		require.Equal(t, int64(555), balanceOf(t, a, stranger).Int64(), "the fork reads upstream state")
		require.NoError(t, a.MineBlock(ctx))

		require.NoError(t, a.Reset(ctx, &ForkOptions{BlockNumber: 2}))
		assert.Equal(t, int64(2), head(t, a).Number.Int64(), "the URL is kept")

		require.NoError(t, a.Reset(ctx, &ForkOptions{}))
		assert.Equal(t, int64(5), head(t, a).Number.Int64(), "zero means the upstream's latest, not block 3")

		require.NoError(t, a.Reset(ctx, &ForkOptions{URL: upstream.URL(), BlockNumber: 4}))
		assert.Equal(t, int64(4), head(t, a).Number.Int64())

		require.NoError(t, a.Reset(ctx, nil))
		assert.Zero(t, head(t, a).Number.Sign(), "nil stops forking")
		assert.Zero(t, balanceOf(t, a, stranger).Sign(), "upstream state is gone")
	})

	t.Run("a local node needs a URL to fork", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewBuilder())
		require.Error(t, a.Reset(ctx, &ForkOptions{}))

		upstream := startAnvil(t, NewBuilder())
		require.NoError(t, upstream.Mine(ctx, 2, 0))
		require.NoError(t, a.Reset(ctx, &ForkOptions{URL: upstream.URL()}))
		assert.Equal(t, int64(2), head(t, a).Number.Int64())
	})
}

func TestDebugTrace(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())
	to := recipient
	tx := TxArgs{From: a.Accounts()[0].Address, To: &to, Value: big.NewInt(1)}
	callTracer := &TraceConfig{Tracer: "callTracer", TracerConfig: map[string]any{"onlyTopCall": true}}

	hash, err := a.SendTransaction(ctx, tx)
	require.NoError(t, err)
	receipt(t, a, hash)

	assertCall := func(raw json.RawMessage) {
		t.Helper()
		var call struct {
			Type string         `json:"type"`
			To   common.Address `json:"to"`
		}
		require.NoError(t, json.Unmarshal(raw, &call), string(raw))
		assert.Equal(t, "CALL", call.Type)
		assert.Equal(t, to, call.To)
	}

	raw, err := a.DebugTraceTransaction(ctx, hash, callTracer)
	require.NoError(t, err)
	assertCall(raw)

	raw, err = a.DebugTraceCall(ctx, tx, nil, callTracer)
	require.NoError(t, err)
	assertCall(raw)

	raw, err = a.DebugTraceCall(ctx, tx, big.NewInt(0), nil)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "structLogs")
}

func TestDebugTraceTransaction_opcodeLogs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder().WithArgs("--steps-tracing"))
	contract := common.HexToAddress("0x00000000000000000000000000000000000000cc")
	require.NoError(t, a.SetCode(ctx, contract, []byte{0x60, 0x00, 0x00})) // PUSH1 0, STOP

	hash, err := a.SendTransaction(ctx, TxArgs{From: a.Accounts()[0].Address, To: &contract})
	require.NoError(t, err)
	receipt(t, a, hash)

	raw, err := a.DebugTraceTransaction(ctx, hash, nil)
	require.NoError(t, err)
	var trace struct {
		StructLogs []json.RawMessage `json:"structLogs"`
	}
	require.NoError(t, json.Unmarshal(raw, &trace), string(raw))
	assert.NotEmpty(t, trace.StructLogs)
}

func TestNewFundedAccount(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())
	wei := big.NewInt(1_000_000_000_000_000_000)

	account, err := a.NewFundedAccount(ctx, wei)
	require.NoError(t, err)
	assert.Zero(t, wei.Cmp(balanceOf(t, a, account.Address)))

	// The key really controls the address: it can sign a transfer.
	chainID, err := a.Client().ChainID(ctx)
	require.NoError(t, err)
	gasPrice, err := a.Client().SuggestGasPrice(ctx)
	require.NoError(t, err)
	to := recipient
	tx, err := types.SignNewTx(account.Key, types.LatestSignerForChainID(chainID), &types.LegacyTx{To: &to, Value: big.NewInt(1), Gas: 21_000, GasPrice: gasPrice})
	require.NoError(t, err)
	require.NoError(t, a.Client().SendTransaction(ctx, tx))
	assert.Equal(t, types.ReceiptStatusSuccessful, receipt(t, a, tx.Hash()).Status)
}

func TestFundAccounts(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())

	require.NoError(t, a.FundAccounts(ctx, []common.Address{stranger, recipient}, big.NewInt(99)))
	assert.Equal(t, int64(99), balanceOf(t, a, stranger).Int64())
	assert.Equal(t, int64(99), balanceOf(t, a, recipient).Int64())
}

// TestConcurrentCalls runs mixed calls from many goroutines so -race can see
// the client access.
func TestConcurrentCalls(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := startAnvil(t, NewBuilder())
	addrs := addresses(a)

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
					_ = a.URL()
				}
			}
		})
	}
	wg.Wait()
	require.NoError(t, errors.Join(errs...))
	assert.Positive(t, head(t, a).Number.Sign(), "blocks should have been mined")
}
