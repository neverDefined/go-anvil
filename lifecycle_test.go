package anvil

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLifecycle covers Start/Stop behavior. Every subtest uses its own instance
// on a port picked by anvil, so they run in parallel.
func TestLifecycle(t *testing.T) {
	t.Parallel()
	requireAnvil(t)

	t.Run("default port is picked by anvil and reported by URL", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		a := startAnvil(t, NewBuilder())
		b := startAnvil(t, NewBuilder())
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
		owner := startAnvil(t, NewBuilder())
		u, err := url.Parse(owner.URL())
		require.NoError(t, err)
		port, err := strconv.Atoi(u.Port())
		require.NoError(t, err)

		a, err := NewBuilder().WithPort(port).Build()
		require.NoError(t, err)
		t.Cleanup(func() { _ = a.Close() })

		err = a.Start()
		require.ErrorIs(t, err, ErrProcessExited)
		assert.Contains(t, err.Error(), "already in use")
		assert.Empty(t, a.URL())
	})

	t.Run("invalid flag fails fast with anvil's own error", func(t *testing.T) {
		t.Parallel()
		a, err := NewBuilder().
			WithArgs("--block-time", "not-a-number").
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
		a, err := NewBuilder().Build()
		require.NoError(t, err)

		require.ErrorIs(t, a.MineBlock(ctx), ErrNotStarted)
		require.ErrorIs(t, a.WaitForBlock(ctx, 1), ErrNotStarted)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		require.ErrorIs(t, a.WaitForBlock(canceled, 1), ErrNotStarted, "a finished ctx must not mask it")
		require.ErrorIs(t, a.WaitForMemPoolEmpty(ctx), ErrNotStarted)

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
		a := startAnvil(t, NewBuilder())
		require.ErrorIs(t, a.Start(), ErrAlreadyStarted)
		require.NoError(t, a.Close())
		require.ErrorIs(t, a.Start(), ErrAlreadyStarted)
	})

	t.Run("Close returns promptly and is idempotent", func(t *testing.T) {
		t.Parallel()
		a := startAnvil(t, NewBuilder())

		start := time.Now()
		require.NoError(t, a.Close())
		assert.Less(t, time.Since(start), time.Second, "Close must not wait out a fixed sleep (#80)")

		require.NoError(t, a.Close())
		require.NoError(t, a.Stop())
	})

	t.Run("startup timeout returns ErrStartupTimeout", func(t *testing.T) {
		t.Parallel()
		a, err := NewBuilder().WithStartupTimeout(time.Millisecond).Build()
		require.NoError(t, err)
		t.Cleanup(func() { _ = a.Close() })

		require.ErrorIs(t, a.Start(), ErrStartupTimeout)
	})

	t.Run("WithLogger receives anvil's output", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))

		a := startAnvil(t, NewBuilder().WithLogger(logger))
		require.NoError(t, a.Close()) // the process has been reaped, so out is final
		assert.Contains(t, out.String(), "Listening on")
		assert.Contains(t, out.String(), "stream=stdout")

		out.Reset()
		a, err := NewBuilder().WithLogger(logger).WithArgs("--no-such-flag").Build()
		require.NoError(t, err)
		require.ErrorIs(t, a.Start(), ErrProcessExited)
		assert.Contains(t, out.String(), "level=WARN")
		assert.Contains(t, out.String(), "stream=stderr")
	})
}

// TestFixedPort is not parallel: between closing the probe listener and anvil
// binding the port, an instance started by a parallel test could take it.
func TestFixedPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	a := startAnvil(t, NewBuilder().WithPort(port))
	assert.Equal(t, "http://127.0.0.1:"+strconv.Itoa(port), a.URL())
	_, err = a.Client().BlockNumber(t.Context())
	require.NoError(t, err)
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
