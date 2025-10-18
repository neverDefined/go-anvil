package anvil

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testPort = 8545

// TODO(PE-1450) - remove once done
func getTestPort() string {
	testPort++
	return fmt.Sprintf("%d", testPort)
}

func setupTestAnvil(t *testing.T, opts ...func(*AnvilBuilder)) *Anvil {
	builder := NewAnvilBuilder().
		WithLogLevel(zerolog.Disabled).
		WithPort(getTestPort())

	for _, opt := range opts {
		opt(builder)
	}

	anvil, err := builder.Build()
	require.NoError(t, err)

	err = anvil.Start()
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = anvil.Close()
		time.Sleep(time.Second)
	})

	return anvil
}

func TestAnvil(t *testing.T) {
	t.Run("Basic Anvil Operations", func(t *testing.T) {
		anvil := setupTestAnvil(t, func(b *AnvilBuilder) {
			b.WithBlockTime("1")
		})

		client := anvil.Client()
		require.NotNil(t, client)

		// Get initial block number
		initialBlockNum, err := client.BlockNumber(anvil.context)
		require.NoError(t, err)

		// Mine exactly one block
		err = anvil.MineBlock()
		require.NoError(t, err)

		// Wait for block to be mined
		err = retry(5, time.Second, func() error {
			currentBlock, err := client.BlockNumber(anvil.context)
			if err != nil {
				return err
			}
			if currentBlock != initialBlockNum+1 {
				return fmt.Errorf("expected block %d, got %d", initialBlockNum+1, currentBlock)
			}
			return nil
		})
		require.NoError(t, err)

		metrics := anvil.Metrics()
		assert.Equal(t, uint64(1), metrics.BlocksMined)
	})

	t.Run("Test Account Management", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		// Get accounts
		keys, addresses, err := anvil.Accounts()
		require.NoError(t, err)
		assert.Len(t, addresses, len(AnvilPrivateKeys))
		assert.Len(t, keys, len(AnvilPrivateKeys))

		// Check balance of first account
		balance, err := anvil.Client().BalanceAt(anvil.context, addresses[0], nil)
		require.NoError(t, err)
		assert.True(t, balance.Cmp(big.NewInt(0)) > 0)
	})

	t.Run("Test Time Manipulation", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		// Get current block
		block, err := anvil.Client().BlockByNumber(anvil.context, nil)
		require.NoError(t, err)
		initialTime := block.Time()

		// Increase time by 3600 seconds (1 hour)
		err = anvil.IncreaseTime(3600)
		require.NoError(t, err)

		// Mine a new block to see the time change
		err = anvil.MineBlock()
		require.NoError(t, err)

		time.Sleep(time.Second * 2) // Wait for block to be mined

		newBlock, err := anvil.Client().BlockByNumber(anvil.context, nil)
		require.NoError(t, err)
		newTime := newBlock.Time()

		assert.Greater(t, newTime, initialTime)

		metrics := anvil.Metrics()
		assert.GreaterOrEqual(t, metrics.RPCCalls, int64(2))
	})

	t.Run("Test Balance Manipulation", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		_, addresses, err := anvil.Accounts()
		require.NoError(t, err)
		testAddr := addresses[0]

		newBalance := big.NewInt(123456789)
		err = anvil.SetBalance(testAddr, newBalance)
		require.NoError(t, err)

		err = anvil.MineBlock()
		require.NoError(t, err)

		time.Sleep(time.Second * 2) // Wait for block to be mined

		balance, err := anvil.Client().BalanceAt(anvil.context, testAddr, nil)
		require.NoError(t, err)
		assert.Equal(t, newBalance.String(), balance.String())
	})

	t.Run("Test Account Impersonation", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		testAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")

		err := anvil.SetBalance(testAddr, big.NewInt(1000000000000000000))
		require.NoError(t, err)

		err = anvil.Impersonate(testAddr)
		require.NoError(t, err)

		err = anvil.StopImpersonating(testAddr)
		require.NoError(t, err)

		metrics := anvil.Metrics()
		assert.GreaterOrEqual(t, metrics.RPCCalls, int64(3))
	})

	t.Run("Test Builder Options", func(t *testing.T) {
		anvil := setupTestAnvil(t, func(b *AnvilBuilder) {
			b.WithBlockTime("2").
				WithChainID("1337").
				WithGasLimit("12000000").
				WithGasPrice("20000000000")
		})

		client := anvil.Client()
		require.NotNil(t, client)

		_, err := client.BlockNumber(anvil.context)
		require.NoError(t, err)
	})

	t.Run("Test Reset Functionality", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		// Get initial block number
		initialBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)

		// Mine blocks
		for i := 0; i < 3; i++ {
			err = anvil.MineBlock()
			require.NoError(t, err)
			time.Sleep(time.Second)
		}

		// Create new context for reset operation
		newAnvil, err := NewAnvilBuilder().
			WithLogLevel(zerolog.Disabled).
			WithPort(getTestPort()).
			Build()
		require.NoError(t, err)

		err = newAnvil.Start()
		require.NoError(t, err)
		defer func() { _ = newAnvil.Close() }()

		time.Sleep(time.Second * 2)

		newBlock, err := newAnvil.Client().BlockNumber(newAnvil.context)
		require.NoError(t, err)
		assert.Equal(t, initialBlock, newBlock, "Block number should be at initial state")
	})

	t.Run("Test Snapshot and Revert", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		// Get initial state
		initialBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)

		// Create snapshot
		snapshotID, err := anvil.Snapshot()
		require.NoError(t, err)
		assert.NotEmpty(t, snapshotID)

		// Make changes
		err = anvil.MineBlock()
		require.NoError(t, err)
		err = anvil.MineBlock()
		require.NoError(t, err)

		time.Sleep(time.Second)

		// Verify changes
		changedBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)
		assert.Greater(t, changedBlock, initialBlock)

		// Revert to snapshot
		success, err := anvil.Revert(snapshotID)
		require.NoError(t, err)
		assert.True(t, success)

		time.Sleep(time.Second)

		// Verify revert
		revertedBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)
		assert.Equal(t, initialBlock, revertedBlock)
	})

	t.Run("Test SetNonce", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		_, addresses, err := anvil.Accounts()
		require.NoError(t, err)
		testAddr := addresses[0]

		// Set nonce
		err = anvil.SetNonce(testAddr, 42)
		require.NoError(t, err)

		// Verify nonce
		nonce, err := anvil.Client().NonceAt(anvil.context, testAddr, nil)
		require.NoError(t, err)
		assert.Equal(t, uint64(42), nonce)
	})

	t.Run("Test Mine Multiple Blocks", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		initialBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)

		// Mine 5 blocks
		err = anvil.Mine(5, nil)
		require.NoError(t, err)

		time.Sleep(time.Second)

		newBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)
		assert.Equal(t, initialBlock+5, newBlock)

		metrics := anvil.Metrics()
		assert.GreaterOrEqual(t, metrics.BlocksMined, uint64(5))
	})

	t.Run("Test Mine With Timestamp", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		futureTimestamp := uint64(time.Now().Unix() + 3600) //nolint:gosec // Unix timestamp is always positive
		err := anvil.Mine(1, &futureTimestamp)
		require.NoError(t, err)

		time.Sleep(time.Second)

		block, err := anvil.Client().BlockByNumber(anvil.context, nil)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, block.Time(), futureTimestamp)
	})

	t.Run("Test SetAutomine", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		// Disable automine
		err := anvil.SetAutomine(false)
		require.NoError(t, err)

		// Re-enable automine
		err = anvil.SetAutomine(true)
		require.NoError(t, err)
	})

	t.Run("Test SetIntervalMining", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		// Set interval mining to 1 second
		err := anvil.SetIntervalMining(1)
		require.NoError(t, err)

		// Disable interval mining
		err = anvil.SetIntervalMining(0)
		require.NoError(t, err)
	})

	t.Run("Test SetCode", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		_, addresses, err := anvil.Accounts()
		require.NoError(t, err)
		testAddr := addresses[0]

		// Set some bytecode (simple contract bytecode)
		bytecode := "0x6080604052348015600f57600080fd5b50603f80601d6000396000f3fe6080604052600080fdfea264697066735822122012345678901234567890123456789012345678901234567890123456789012345678901264736f6c63430008130033"
		err = anvil.SetCode(testAddr, bytecode)
		require.NoError(t, err)

		// Verify code is set
		code, err := anvil.Client().CodeAt(anvil.context, testAddr, nil)
		require.NoError(t, err)
		assert.NotEmpty(t, code)
	})

	t.Run("Test SetStorageAt", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		_, addresses, err := anvil.Accounts()
		require.NoError(t, err)
		testAddr := addresses[0]

		// Set storage at slot 0
		slot := "0x0000000000000000000000000000000000000000000000000000000000000000"
		value := "0x0000000000000000000000000000000000000000000000000000000000000001"

		err = anvil.SetStorageAt(testAddr, slot, value)
		require.NoError(t, err)

		// Note: Verifying storage requires the address to have code (be a contract)
		// This test just ensures the RPC call doesn't error
	})

	t.Run("Test AutoImpersonate", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		// Enable auto impersonate
		err := anvil.AutoImpersonate(true)
		require.NoError(t, err)

		// Disable auto impersonate
		err = anvil.AutoImpersonate(false)
		require.NoError(t, err)
	})

	t.Run("Test Metrics Tracking", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		// Perform several operations
		err := anvil.MineBlock()
		require.NoError(t, err)

		_, addresses, _ := anvil.Accounts()
		err = anvil.SetBalance(addresses[0], big.NewInt(1000))
		require.NoError(t, err)

		// Check metrics
		metrics := anvil.Metrics()
		assert.Greater(t, metrics.RPCCalls, int64(0))
		assert.Greater(t, metrics.BlocksMined, uint64(0))
		assert.Greater(t, metrics.StartupTime, time.Duration(0))
	})

	t.Run("Test Close Idempotence", func(t *testing.T) {
		anvil := setupTestAnvil(t)

		// Close should be safe to call multiple times
		err := anvil.Close()
		require.NoError(t, err)

		err = anvil.Close()
		require.NoError(t, err)

		err = anvil.Close()
		require.NoError(t, err)
	})

	t.Run("Test Stop Idempotence", func(t *testing.T) {
		builder := NewAnvilBuilder().
			WithLogLevel(zerolog.Disabled).
			WithPort(getTestPort())

		anvil, err := builder.Build()
		require.NoError(t, err)

		err = anvil.Start()
		require.NoError(t, err)

		// Stop should be safe to call multiple times
		err = anvil.Stop()
		require.NoError(t, err)

		err = anvil.Stop()
		require.NoError(t, err)

		err = anvil.Stop()
		require.NoError(t, err)
	})
}
