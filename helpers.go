package anvil

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

// WaitForMemPoolEmpty waits for the memory pool to be empty (no pending transactions).
// It polls every 100ms until the pool is empty, ctx is canceled, or the timeout expires —
// whichever comes first. A zero or negative timeout means "use ctx's deadline only."
// Returns an error if the timeout is exceeded, ctx is canceled, or the query fails.
func (a *Anvil) WaitForMemPoolEmpty(ctx context.Context, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for empty mempool: %w", ctx.Err())
		case <-ticker.C:
			pending, err := a.client.PendingTransactionCount(ctx)
			if err != nil {
				return fmt.Errorf("query pending transaction count: %w", err)
			}
			if pending == 0 {
				return nil
			}
		}
	}
}

// MemPoolEmpty waits for the memory pool to be empty (no pending transactions).
// It polls every 100ms for up to 5 seconds.
//
// Deprecated: Use (*Anvil).WaitForMemPoolEmpty instead. The method honors the
// ctx deadline directly, accepts a configurable timeout, and uses the instance's
// own client rather than a passed-in one.
func MemPoolEmpty(ctx context.Context, client *ethclient.Client) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(5 * time.Second)

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for pending transactions")
		case <-ticker.C:
			pending, err := client.PendingTransactionCount(ctx)
			if err != nil {
				return err
			}
			if pending == 0 {
				return nil
			}
		}
	}
}
