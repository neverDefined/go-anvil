//go:build fork

// Fork-mode tests. Require a real RPC URL via the ETH_RPC_URL environment variable.
// Run locally with:  ETH_RPC_URL=https://... go test -tags=fork -run Fork ./...
// CI skips these by default (the tag is off).

package anvil

import (
	"os"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireForkURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("ETH_RPC_URL")
	if url == "" {
		t.Skip("ETH_RPC_URL not set; skipping fork tests")
	}
	return url
}

// TestForkLatest forks from the upstream chain head and asserts we can read a block.
func TestForkLatest(t *testing.T) {
	url := requireForkURL(t)

	anvil, err := NewAnvilBuilder().
		WithLogLevel(zerolog.Disabled).
		WithPort(getTestPort()).
		WithFork(url).
		Build()
	require.NoError(t, err)
	require.NoError(t, anvil.Start())
	t.Cleanup(func() { _ = anvil.Close() })

	ctx := t.Context()
	block, err := anvil.Client().BlockNumber(ctx)
	require.NoError(t, err)
	assert.Greater(t, block, uint64(0), "forked chain head should be > 0")

	chainID, err := anvil.Client().ChainID(ctx)
	require.NoError(t, err)
	assert.NotNil(t, chainID)
}

// TestForkAtBlock forks from a specific block and asserts we can read state from that block.
func TestForkAtBlock(t *testing.T) {
	url := requireForkURL(t)

	const forkBlock = "18000000" // a mainnet block known to have state

	anvil, err := NewAnvilBuilder().
		WithLogLevel(zerolog.Disabled).
		WithPort(getTestPort()).
		WithFork(url).
		WithForkBlockNumber(forkBlock).
		Build()
	require.NoError(t, err)
	require.NoError(t, anvil.Start())
	t.Cleanup(func() { _ = anvil.Close() })

	ctx := t.Context()
	block, err := anvil.Client().BlockNumber(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, block, uint64(18_000_000))
}
