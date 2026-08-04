package anvil

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

// VerifyProof verifies an EIP-1186 proof (as returned by GetProof) against a
// state root — typically the Root of the block header the proof was taken at.
// It checks that the account proof binds the claimed nonce, balance, storage
// hash, and code hash, and that every storage proof is consistent with the
// proven storage hash. The account must exist in the state trie; account
// exclusion proofs are not supported. Storage slots with a zero value are
// verified as absence proofs against the storage trie.
// Returns nil if the proof is valid.
func VerifyProof(stateRoot common.Hash, proof *AccountResult) error {
	if proof == nil {
		return fmt.Errorf("verify proof: proof is nil")
	}

	leaf, err := verifyTrieProof(stateRoot, crypto.Keccak256(proof.Address.Bytes()), proof.AccountProof)
	if err != nil {
		return fmt.Errorf("account proof for %s: %w", proof.Address.Hex(), err)
	}
	if len(leaf) == 0 {
		return fmt.Errorf("account proof for %s: account not found in state trie", proof.Address.Hex())
	}

	var account struct {
		Nonce    uint64
		Balance  *big.Int
		Root     common.Hash
		CodeHash common.Hash
	}
	if err := rlp.DecodeBytes(leaf, &account); err != nil {
		return fmt.Errorf("account proof for %s: decode account leaf: %w", proof.Address.Hex(), err)
	}
	if account.Nonce != uint64(proof.Nonce) {
		return fmt.Errorf("account proof for %s: nonce mismatch: proved %d, claimed %d", proof.Address.Hex(), account.Nonce, uint64(proof.Nonce))
	}
	if proof.Balance == nil || account.Balance.Cmp(proof.Balance.ToInt()) != 0 {
		return fmt.Errorf("account proof for %s: balance mismatch: proved %s, claimed %s", proof.Address.Hex(), account.Balance, proof.Balance)
	}
	if account.Root != proof.StorageHash {
		return fmt.Errorf("account proof for %s: storage hash mismatch: proved %s, claimed %s", proof.Address.Hex(), account.Root.Hex(), proof.StorageHash.Hex())
	}
	if account.CodeHash != proof.CodeHash {
		return fmt.Errorf("account proof for %s: code hash mismatch: proved %s, claimed %s", proof.Address.Hex(), account.CodeHash.Hex(), proof.CodeHash.Hex())
	}

	for _, sp := range proof.StorageProof {
		if err := verifyStorageProof(account.Root, sp); err != nil {
			return fmt.Errorf("storage proof for %s slot %s: %w", proof.Address.Hex(), sp.Key, err)
		}
	}
	return nil
}

// verifyStorageProof checks a single EIP-1186 storage proof against the
// account's proven storage root.
func verifyStorageProof(storageRoot common.Hash, sp StorageResult) error {
	key, err := parseStorageKey(sp.Key)
	if err != nil {
		return err
	}
	leaf, err := verifyTrieProof(storageRoot, crypto.Keccak256(key), sp.Proof)
	if err != nil {
		return err
	}
	if sp.Value == nil || sp.Value.ToInt().Sign() == 0 {
		if len(leaf) != 0 {
			return fmt.Errorf("claimed zero value but trie holds %s", hexutil.Encode(leaf))
		}
		return nil
	}
	expected, err := rlp.EncodeToBytes(sp.Value.ToInt())
	if err != nil {
		return fmt.Errorf("encode claimed value: %w", err)
	}
	if !bytes.Equal(leaf, expected) {
		return fmt.Errorf("value mismatch: proved %s, claimed %s", hexutil.Encode(leaf), sp.Value)
	}
	return nil
}

// verifyTrieProof replays a Merkle-Patricia proof against root and returns the
// leaf value for key, or nil if the proof shows the key is absent.
func verifyTrieProof(root common.Hash, key []byte, proofNodes []string) ([]byte, error) {
	db := memorydb.New()
	for i, node := range proofNodes {
		blob, err := hexutil.Decode(node)
		if err != nil {
			return nil, fmt.Errorf("decode proof node %d: %w", i, err)
		}
		if err := db.Put(crypto.Keccak256(blob), blob); err != nil {
			return nil, fmt.Errorf("store proof node %d: %w", i, err)
		}
	}
	value, err := trie.VerifyProof(root, key, db)
	if err != nil {
		return nil, fmt.Errorf("verify merkle proof: %w", err)
	}
	return value, nil
}

// parseStorageKey normalizes a hex storage key (padded or not) to 32 bytes.
func parseStorageKey(key string) ([]byte, error) {
	n, ok := new(big.Int).SetString(strings.TrimPrefix(key, "0x"), 16)
	if !ok || n.Sign() < 0 {
		return nil, fmt.Errorf("invalid storage key %q", key)
	}
	if n.BitLen() > 256 {
		return nil, fmt.Errorf("storage key %q exceeds 32 bytes", key)
	}
	return n.FillBytes(make([]byte, 32)), nil
}

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
