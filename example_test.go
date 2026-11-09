package anvil_test

import (
	"context"
	"fmt"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/neverDefined/go-anvil"
)

func ExampleNewBuilder() {
	node, err := anvil.NewBuilder().WithChainID(1337).Build()
	if err != nil {
		log.Fatal(err)
	}
	if err := node.Start(); err != nil {
		log.Fatal(err)
	}
	defer node.Close()

	chainID, err := node.Client().ChainID(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(chainID)
	// Output: 1337
}

func ExampleAnvil_MineBlock() {
	ctx := context.Background()
	node, err := anvil.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := node.Start(); err != nil {
		log.Fatal(err)
	}
	defer node.Close()

	if err := node.MineBlock(ctx); err != nil {
		log.Fatal(err)
	}
	block, err := node.Client().BlockNumber(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(block)
	// Output: 1
}

func ExampleAnvil_SetBalance() {
	ctx := context.Background()
	node, err := anvil.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := node.Start(); err != nil {
		log.Fatal(err)
	}
	defer node.Close()

	address := common.HexToAddress("0xa11ce")
	if err := node.SetBalance(ctx, address, big.NewInt(500)); err != nil {
		log.Fatal(err)
	}
	balance, err := node.Client().BalanceAt(ctx, address, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(balance)
	// Output: 500
}

func ExampleAnvil_Snapshot() {
	ctx := context.Background()
	node, err := anvil.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := node.Start(); err != nil {
		log.Fatal(err)
	}
	defer node.Close()

	id, err := node.Snapshot(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if err := node.Mine(ctx, 3, 0); err != nil {
		log.Fatal(err)
	}
	found, err := node.Revert(ctx, id)
	if err != nil {
		log.Fatal(err)
	}
	block, err := node.Client().BlockNumber(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(found, block)
	// Output: true 0
}

func ExampleAnvil_ResetState() {
	ctx := context.Background()
	node, err := anvil.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := node.Start(); err != nil {
		log.Fatal(err)
	}
	defer node.Close()

	if err := node.Mine(ctx, 5, 0); err != nil {
		log.Fatal(err)
	}
	if err := node.ResetState(ctx); err != nil {
		log.Fatal(err)
	}
	block, err := node.Client().BlockNumber(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(block)
	// Output: 0
}

func ExampleAnvil_Impersonate() {
	ctx := context.Background()
	node, err := anvil.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := node.Start(); err != nil {
		log.Fatal(err)
	}
	defer node.Close()

	whale := common.HexToAddress("0xa11ce")
	to := common.HexToAddress("0xb0b")
	if err := node.SetBalance(ctx, whale, big.NewInt(1e18)); err != nil {
		log.Fatal(err)
	}
	if err := node.Impersonate(ctx, whale); err != nil {
		log.Fatal(err)
	}

	if _, err := node.SendTransaction(ctx, anvil.TxArgs{From: whale, To: &to, Value: big.NewInt(1000)}); err != nil {
		log.Fatal(err)
	}
	if err := node.WaitForMemPoolEmpty(ctx); err != nil {
		log.Fatal(err)
	}

	balance, err := node.Client().BalanceAt(ctx, to, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(balance)
	// Output: 1000
}
