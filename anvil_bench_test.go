package anvil

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// setupBenchAnvil spins up a single anvil instance for a benchmark and returns it.
// Callers must call b.Cleanup for close; this helper registers that automatically.
func setupBenchAnvil(b *testing.B) *Anvil {
	b.Helper()
	anvil, err := NewAnvilBuilder().
		WithLogLevel(zerolog.Disabled).
		WithPort(getTestPort()).
		Build()
	if err != nil {
		b.Fatalf("build anvil: %v", err)
	}
	if err := anvil.Start(); err != nil {
		b.Fatalf("start anvil: %v", err)
	}
	b.Cleanup(func() {
		_ = anvil.Close()
		time.Sleep(time.Second)
	})
	return anvil
}

func BenchmarkMineBlock(b *testing.B) {
	anvil := setupBenchAnvil(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := anvil.MineBlock(ctx); err != nil {
			b.Fatalf("mine: %v", err)
		}
	}
}

func BenchmarkSetBalance(b *testing.B) {
	anvil := setupBenchAnvil(b)
	ctx := context.Background()
	_, addrs, err := anvil.Accounts()
	if err != nil {
		b.Fatal(err)
	}
	bal := big.NewInt(1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := anvil.SetBalance(ctx, addrs[0], bal); err != nil {
			b.Fatalf("set balance: %v", err)
		}
	}
}

func BenchmarkSnapshotRevertCycle(b *testing.B) {
	anvil := setupBenchAnvil(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, err := anvil.Snapshot(ctx)
		if err != nil {
			b.Fatalf("snapshot: %v", err)
		}
		if _, err := anvil.Revert(ctx, id); err != nil {
			b.Fatalf("revert: %v", err)
		}
	}
}

// BenchmarkResetState measures the fast snapshot-based reset used between tests.
// First iteration primes the initial snapshot; subsequent iterations revert to it.
func BenchmarkResetState(b *testing.B) {
	anvil := setupBenchAnvil(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := anvil.ResetState(ctx); err != nil {
			b.Fatalf("reset state: %v", err)
		}
	}
}
