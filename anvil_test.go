package main

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

// Global shared Anvil instance for tests that don't need custom options
var sharedAnvil *Anvil
var sharedAnvilInitialized bool

// setupSharedAnvil sets up or resets a shared Anvil instance
func setupSharedAnvil(t *testing.T) *Anvil {
	if sharedAnvil == nil {
		builder := NewAnvilBuilder().
			WithLogLevel(zerolog.Disabled).
			WithPort(getTestPort())

		var err error
		sharedAnvil, err = builder.Build()
		require.NoError(t, err)

		err = sharedAnvil.Start()
		require.NoError(t, err)

		sharedAnvilInitialized = true
	} else if sharedAnvilInitialized {
		// Reset the Anvil state using RPC (much faster than restarting)
		err := sharedAnvil.ResetState()
		require.NoError(t, err)
	}

	return sharedAnvil
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
		anvil.Close()
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
		assert.Equal(t, int64(1), metrics.BlocksMined)
	})

	t.Run("Test Account Management", func(t *testing.T) {
		anvil := setupSharedAnvil(t)

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
		anvil := setupSharedAnvil(t)

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

		assert.Greater(t, uint64(newTime), uint64(initialTime))

		metrics := anvil.Metrics()
		assert.GreaterOrEqual(t, metrics.RPCCalls, int64(2))
	})

	t.Run("Test Balance Manipulation", func(t *testing.T) {
		anvil := setupSharedAnvil(t)

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
		anvil := setupSharedAnvil(t)

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
				WithChainId("1337").
				WithGasLimit("12000000").
				WithGasPrice("20000000000")
		})

		client := anvil.Client()
		require.NotNil(t, client)

		_, err := client.BlockNumber(anvil.context)
		require.NoError(t, err)
	})

	t.Run("Test Reset Functionality", func(t *testing.T) {
		anvil := setupSharedAnvil(t)

		initialBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)

		for i := 0; i < 3; i++ {
			err = anvil.MineBlock()
			require.NoError(t, err)
			time.Sleep(time.Second) // Wait for block to be mined
		}

		err = anvil.Reset()
		require.NoError(t, err)

		time.Sleep(time.Second * 2) // Wait for reset to complete

		newBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)
		assert.Equal(t, initialBlock, newBlock, "Block number should reset to initial state")
	})

	t.Run("Test ResetState Functionality", func(t *testing.T) {
		anvil := setupSharedAnvil(t)

		// Get initial block number
		initialBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)

		// Mine blocks
		for i := 0; i < 3; i++ {
			err = anvil.MineBlock()
			require.NoError(t, err)
		}

		time.Sleep(time.Second) // Wait for blocks to be mined

		// Check that blocks were mined
		currentBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)
		assert.Greater(t, currentBlock, initialBlock, "Should have mined blocks")

		// Reset state using RPC
		err = anvil.ResetState()
		require.NoError(t, err)

		time.Sleep(time.Second) // Wait for reset to complete

		// Check that block number is back to initial
		newBlock, err := anvil.Client().BlockNumber(anvil.context)
		require.NoError(t, err)
		assert.Equal(t, initialBlock, newBlock, "Block number should reset to initial state")
	})

	// Cleanup shared anvil at the end
	t.Cleanup(func() {
		if sharedAnvil != nil {
			sharedAnvil.Close()
		}
	})
}
