package anvil

import (
	"context"
	"math/big"
	"testing"
)

func BenchmarkMineBlock(b *testing.B) {
	anvil := startAnvil(b, NewBuilder())
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := anvil.MineBlock(ctx); err != nil {
			b.Fatalf("mine: %v", err)
		}
	}
}

func BenchmarkSetBalance(b *testing.B) {
	anvil := startAnvil(b, NewBuilder())
	ctx := context.Background()
	addr := anvil.Accounts()[0].Address
	bal := big.NewInt(1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := anvil.SetBalance(ctx, addr, bal); err != nil {
			b.Fatalf("set balance: %v", err)
		}
	}
}

func BenchmarkSnapshotRevertCycle(b *testing.B) {
	anvil := startAnvil(b, NewBuilder())
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

// BenchmarkResetState measures one ResetState: a revert to the baseline taken
// in Start plus a fresh snapshot.
func BenchmarkResetState(b *testing.B) {
	anvil := startAnvil(b, NewBuilder())
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := anvil.ResetState(ctx); err != nil {
			b.Fatalf("reset state: %v", err)
		}
	}
}
