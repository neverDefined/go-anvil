package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

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
