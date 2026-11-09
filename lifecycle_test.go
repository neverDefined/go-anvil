package anvil

import (
	"net/url"
	"os/exec"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startQuiet builds and starts an instance and closes it when the test ends.
func startQuiet(t *testing.T, b *AnvilBuilder) *Anvil {
	t.Helper()
	a, err := b.WithLogLevel(zerolog.Disabled).Build()
	require.NoError(t, err)
	require.NoError(t, a.Start())
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// TestLifecycle covers Start/Stop behavior. Every subtest uses its own instance
// on a port picked by anvil, so they run in parallel.
func TestLifecycle(t *testing.T) {
	t.Run("default port is picked by anvil and reported by URL", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startQuiet(t, NewAnvilBuilder())
		b := startQuiet(t, NewAnvilBuilder())
		require.NotEqual(t, a.URL(), b.URL())

		require.NoError(t, a.MineBlock(ctx))
		blockA, err := a.Client().BlockNumber(ctx)
		require.NoError(t, err)
		blockB, err := b.Client().BlockNumber(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), blockA)
		assert.Equal(t, uint64(0), blockB, "instances must not share a node")
	})

	t.Run("fixed port already in use fails instead of attaching to the other node", func(t *testing.T) {
		t.Parallel()
		owner := startQuiet(t, NewAnvilBuilder())
		u, err := url.Parse(owner.URL())
		require.NoError(t, err)

		a, err := NewAnvilBuilder().WithLogLevel(zerolog.Disabled).WithPort(u.Port()).Build()
		require.NoError(t, err)
		t.Cleanup(func() { _ = a.Close() })

		err = a.Start()
		require.ErrorIs(t, err, ErrProcessExited)
		assert.Contains(t, err.Error(), "already in use")
		assert.Empty(t, a.URL())
	})

	t.Run("invalid flag fails fast with anvil's own error", func(t *testing.T) {
		t.Parallel()
		a, err := NewAnvilBuilder().
			WithLogLevel(zerolog.Disabled).
			WithBlockTime("not-a-number").
			WithStartupTimeout(30 * time.Second).
			Build()
		require.NoError(t, err)

		start := time.Now()
		err = a.Start()
		require.ErrorIs(t, err, ErrProcessExited)
		assert.Contains(t, err.Error(), "not-a-number")
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.NotZero(t, exitErr.ExitCode())
		assert.Less(t, time.Since(start), 10*time.Second,
			"Start must return when the process exits, not at the startup timeout")
	})

	t.Run("calls outside the started lifetime return ErrNotStarted", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a, err := NewAnvilBuilder().WithLogLevel(zerolog.Disabled).Build()
		require.NoError(t, err)

		require.ErrorIs(t, a.MineBlock(ctx), ErrNotStarted)
		require.ErrorIs(t, a.WaitForBlock(1, time.Millisecond), ErrNotStarted)
		require.ErrorIs(t, a.WaitForMemPoolEmpty(ctx, time.Millisecond), ErrNotStarted)

		require.NoError(t, a.Start())
		require.NoError(t, a.Close())

		require.ErrorIs(t, a.MineBlock(ctx), ErrNotStarted)
		_, err = a.Snapshot(ctx)
		require.ErrorIs(t, err, ErrNotStarted)
		assert.Nil(t, a.Client())
		assert.Nil(t, a.RPCClient())
		assert.Empty(t, a.URL())
	})

	t.Run("Start is single-use", func(t *testing.T) {
		t.Parallel()
		a := startQuiet(t, NewAnvilBuilder())
		require.ErrorIs(t, a.Start(), ErrAlreadyStarted)
		require.NoError(t, a.Close())
		require.ErrorIs(t, a.Start(), ErrAlreadyStarted)
	})

	t.Run("Close returns promptly and is idempotent", func(t *testing.T) {
		t.Parallel()
		a := startQuiet(t, NewAnvilBuilder())

		start := time.Now()
		require.NoError(t, a.Close())
		assert.Less(t, time.Since(start), 500*time.Millisecond)

		require.NoError(t, a.Close())
		require.NoError(t, a.Stop())
	})
}

func TestLineWriter(t *testing.T) {
	var got []string
	w := &lineWriter{fn: func(line string) { got = append(got, line) }}

	_, _ = w.Write([]byte("Listen"))
	_, _ = w.Write([]byte("ing on 127.0.0.1:1234\r\nsecond\npartial"))

	assert.Equal(t, []string{"Listening on 127.0.0.1:1234", "second"}, got)

	w.flush()
	assert.Equal(t, []string{"Listening on 127.0.0.1:1234", "second", "partial"}, got)
}
