package anvil

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

// MemPoolEmpty waits for the memory pool to be empty (no pending transactions).
// It polls every 100ms for up to 5 seconds. This is useful in tests when you need
// to ensure all transactions have been processed before proceeding.
// Returns an error if the timeout is exceeded or if querying pending transactions fails.
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
