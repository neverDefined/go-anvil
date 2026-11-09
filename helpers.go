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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
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
//
// VerifyProof checks consistency, not completeness: only the storage entries
// present in proof.StorageProof are verified, so a response omitting a
// requested slot goes unnoticed. Callers proving a specific slot must confirm
// a StorageProof entry for it exists, comparing keys in normalized form —
// "0x1" and "0x01" name the same slot.
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
	if sp.Value == nil {
		return fmt.Errorf("missing value")
	}
	isZero := sp.Value.ToInt().Sign() == 0

	// An empty storage trie proves every slot absent. geth's trie.VerifyProof
	// can't replay this case: the root node is RLP 0x80 (the empty string),
	// which its node decoder rejects as a non-list.
	if storageRoot == types.EmptyRootHash {
		if !isZero {
			return fmt.Errorf("claimed nonzero value %s against empty storage trie", sp.Value)
		}
		return nil
	}

	leaf, err := verifyTrieProof(storageRoot, crypto.Keccak256(key), sp.Proof)
	if err != nil {
		return err
	}
	if isZero {
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

// WaitForMemPoolEmpty waits until the transaction pool holds no pending and no
// queued transactions (txpool_status). Queued transactions are ones that cannot
// be mined yet, for example because of a nonce gap. It checks immediately, then
// every 100ms, until the pool is empty or ctx is done, so pass a ctx with a
// deadline to bound the wait.
// Returns an error if ctx is done first or if the query fails.
func (a *Anvil) WaitForMemPoolEmpty(ctx context.Context) error {
	// Check the lifecycle first so ErrNotStarted isn't masked by a finished ctx.
	if _, err := a.running(); err != nil {
		return err
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		var status struct {
			Pending hexutil.Uint64 `json:"pending"`
			Queued  hexutil.Uint64 `json:"queued"`
		}
		if err := a.call(ctx, &status, "txpool_status"); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for empty mempool: %w", ctx.Err())
			}
			return err
		}
		if status.Pending == 0 && status.Queued == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for empty mempool: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
