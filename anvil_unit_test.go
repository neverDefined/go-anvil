package anvil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise pure-Go logic and use httptest for RPC-shape assertions.
// They do not spawn anvil and do not require Foundry on PATH, so they run on any machine.

// -----------------------------------------------------------------------------
// Builder validation
// -----------------------------------------------------------------------------

func TestAnvilBuilder_validate(t *testing.T) {
	cases := []struct {
		name    string
		builder *AnvilBuilder
		wantErr bool
	}{
		{
			name:    "default builder is valid",
			builder: NewAnvilBuilder(),
			wantErr: false,
		},
		{
			name:    "empty rpcURL fails",
			builder: &AnvilBuilder{rpcURL: ""},
			wantErr: true,
		},
		{
			name:    "non-empty rpcURL is valid",
			builder: &AnvilBuilder{rpcURL: "http://127.0.0.1:8545"},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.builder.validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestAnvilBuilder_WithStartupTimeout(t *testing.T) {
	t.Run("positive duration wins", func(t *testing.T) {
		b := NewAnvilBuilder().WithStartupTimeout(7 * time.Second)
		assert.Equal(t, 7*time.Second, b.startupTimeout)
	})
	t.Run("zero is ignored (keeps default)", func(t *testing.T) {
		b := NewAnvilBuilder().WithStartupTimeout(0)
		assert.Equal(t, DefaultStartupTimeout, b.startupTimeout)
	})
	t.Run("negative is ignored (keeps default)", func(t *testing.T) {
		b := NewAnvilBuilder().WithStartupTimeout(-time.Second)
		assert.Equal(t, DefaultStartupTimeout, b.startupTimeout)
	})
}

func TestAnvilBuilder_BuildPropagatesStartupTimeout(t *testing.T) {
	a, err := NewAnvilBuilder().WithStartupTimeout(123 * time.Millisecond).Build()
	require.NoError(t, err)
	assert.Equal(t, 123*time.Millisecond, a.startupTimeout)
}

// -----------------------------------------------------------------------------
// resolveAnvilPath — fallback discovery and executability check
// -----------------------------------------------------------------------------

func TestResolveAnvilPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit check is POSIX-only")
	}

	// Save and restore env; isolate from host machine where anvil may be on PATH.
	t.Setenv("PATH", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	t.Run("fallback missing returns ErrAnvilNotFound", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		_, err := resolveAnvilPath()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrAnvilNotFound), "got %v", err)
	})

	t.Run("fallback not executable returns ErrAnvilNotExecutable", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		binDir := filepath.Join(home, ".foundry", "bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))
		anvilPath := filepath.Join(binDir, "anvil")
		require.NoError(t, os.WriteFile(anvilPath, []byte("#!/bin/sh\n"), 0o644)) // no exec bit

		_, err := resolveAnvilPath()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrAnvilNotExecutable), "got %v", err)
	})

	t.Run("fallback is a directory returns ErrAnvilNotExecutable", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		anvilPath := filepath.Join(home, ".foundry", "bin", "anvil")
		require.NoError(t, os.MkdirAll(anvilPath, 0o755)) // directory at the expected file path

		_, err := resolveAnvilPath()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrAnvilNotExecutable), "got %v", err)
	})

	t.Run("fallback executable is returned", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		binDir := filepath.Join(home, ".foundry", "bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))
		anvilPath := filepath.Join(binDir, "anvil")
		require.NoError(t, os.WriteFile(anvilPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

		got, err := resolveAnvilPath()
		require.NoError(t, err)
		assert.Equal(t, anvilPath, got)
	})

	t.Run("XDG_CONFIG_HOME wins over HOME", func(t *testing.T) {
		home := t.TempDir()
		xdg := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", xdg)

		// Executable under XDG path only.
		binDir := filepath.Join(xdg, ".foundry", "bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))
		anvilPath := filepath.Join(binDir, "anvil")
		require.NoError(t, os.WriteFile(anvilPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

		got, err := resolveAnvilPath()
		require.NoError(t, err)
		assert.Equal(t, anvilPath, got)
	})
}

// -----------------------------------------------------------------------------
// retry helper
// -----------------------------------------------------------------------------

func TestRetry(t *testing.T) {
	t.Run("success on first attempt", func(t *testing.T) {
		var calls int32
		err := retry(5, time.Millisecond, func() error {
			atomic.AddInt32(&calls, 1)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
	})

	t.Run("success after N retries", func(t *testing.T) {
		var calls int32
		err := retry(5, time.Millisecond, func() error {
			n := atomic.AddInt32(&calls, 1)
			if n < 3 {
				return errors.New("not yet")
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
	})

	t.Run("attempts exhausted returns last error", func(t *testing.T) {
		var calls int32
		sentinel := errors.New("always fail")
		err := retry(3, time.Millisecond, func() error {
			atomic.AddInt32(&calls, 1)
			return sentinel
		})
		require.Error(t, err)
		assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
		assert.Equal(t, sentinel, err)
	})
}

// -----------------------------------------------------------------------------
// Error sentinels — errors.Is propagation
// -----------------------------------------------------------------------------

func TestSentinelErrorsAreWrappable(t *testing.T) {
	sentinels := []error{
		ErrNotStarted,
		ErrAlreadyStarted,
		ErrConnectionFailed,
		ErrProcessNotFound,
		ErrInvalidConfig,
		ErrRPCCallFailed,
		ErrAnvilNotFound,
		ErrAnvilNotExecutable,
		ErrStartupTimeout,
	}
	for _, s := range sentinels {
		wrapped := fmt.Errorf("wrapped: %w", s)
		assert.True(t, errors.Is(wrapped, s), "errors.Is failed for %v", s)
	}
}

// -----------------------------------------------------------------------------
// RPC method shape — httptest-backed
// -----------------------------------------------------------------------------

// rpcServer is a test JSON-RPC server that records calls and responds with canned results.
type rpcServer struct {
	t      *testing.T
	calls  []rpcCall
	server *httptest.Server
}

type rpcCall struct {
	Method string
	Params []json.RawMessage
}

type rpcReq struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

func newRPCServer(t *testing.T, results map[string]any) *rpcServer {
	t.Helper()
	rs := &rpcServer{t: t}
	rs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req rpcReq
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rs.calls = append(rs.calls, rpcCall{Method: req.Method, Params: req.Params})

		w.Header().Set("Content-Type", "application/json")
		id := string(req.ID)
		if id == "" {
			id = "null"
		}
		if result, ok := results[req.Method]; ok {
			// Always emit the "result" key, even when nil — go-ethereum rejects responses without it.
			resultJSON, mErr := json.Marshal(result)
			if mErr != nil {
				http.Error(w, mErr.Error(), http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, id, resultJSON)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":%q}}`, id, "method not found: "+req.Method)
	}))
	t.Cleanup(rs.server.Close)
	return rs
}

func (rs *rpcServer) lastCall() rpcCall {
	rs.t.Helper()
	require.NotEmpty(rs.t, rs.calls, "expected at least one RPC call")
	return rs.calls[len(rs.calls)-1]
}

// newTestAnvil constructs an *Anvil wired up to a test RPC server — no subprocess, no ethclient.
func newTestAnvil(t *testing.T, server *httptest.Server) *Anvil {
	t.Helper()
	rpcClient, err := rpc.Dial(server.URL)
	require.NoError(t, err)
	t.Cleanup(rpcClient.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return &Anvil{
		context:   ctx,
		cancel:    cancel,
		rpcClient: rpcClient,
		logger:    zerolog.New(io.Discard),
	}
}

func TestRPCMethods_sendCorrectMethod(t *testing.T) {
	rs := newRPCServer(t, map[string]any{
		"evm_mine":                       nil,
		"evm_setNextBlockTimestamp":      nil,
		"evm_increaseTime":               nil,
		"anvil_setBalance":               nil,
		"anvil_impersonateAccount":       nil,
		"anvil_stopImpersonatingAccount": nil,
		"evm_snapshot":                   "0xabc",
		"evm_revert":                     true,
		"anvil_setCode":                  nil,
		"anvil_setStorageAt":             nil,
		"anvil_setNonce":                 nil,
		"anvil_mine":                     nil,
		"anvil_dropTransaction":          nil,
		"evm_setAutomine":                nil,
		"evm_setIntervalMining":          nil,
		"anvil_autoImpersonateAccount":   nil,
		"anvil_reset":                    nil,
		"eth_getProof": map[string]any{
			"address":      "0x1234567890123456789012345678901234567890",
			"accountProof": []string{},
			"balance":      "0x0",
			"codeHash":     "0x0000000000000000000000000000000000000000000000000000000000000000",
			"nonce":        "0x0",
			"storageHash":  "0x0000000000000000000000000000000000000000000000000000000000000000",
			"storageProof": []any{},
		},
	})
	a := newTestAnvil(t, rs.server)
	ctx := context.Background()
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")

	t.Run("MineBlock", func(t *testing.T) {
		require.NoError(t, a.MineBlock(ctx))
		assert.Equal(t, "evm_mine", rs.lastCall().Method)
	})
	t.Run("SetNextBlockTimestamp", func(t *testing.T) {
		require.NoError(t, a.SetNextBlockTimestamp(ctx, 12345))
		assert.Equal(t, "evm_setNextBlockTimestamp", rs.lastCall().Method)
	})
	t.Run("IncreaseTime", func(t *testing.T) {
		require.NoError(t, a.IncreaseTime(ctx, 3600))
		assert.Equal(t, "evm_increaseTime", rs.lastCall().Method)
	})
	t.Run("SetBalance", func(t *testing.T) {
		require.NoError(t, a.SetBalance(ctx, addr, big.NewInt(100)))
		assert.Equal(t, "anvil_setBalance", rs.lastCall().Method)
	})
	t.Run("Impersonate", func(t *testing.T) {
		require.NoError(t, a.Impersonate(ctx, addr))
		assert.Equal(t, "anvil_impersonateAccount", rs.lastCall().Method)
	})
	t.Run("StopImpersonating", func(t *testing.T) {
		require.NoError(t, a.StopImpersonating(ctx, addr))
		assert.Equal(t, "anvil_stopImpersonatingAccount", rs.lastCall().Method)
	})
	t.Run("Snapshot returns result", func(t *testing.T) {
		id, err := a.Snapshot(ctx)
		require.NoError(t, err)
		assert.Equal(t, "0xabc", id)
	})
	t.Run("Revert returns bool", func(t *testing.T) {
		ok, err := a.Revert(ctx, "0xabc")
		require.NoError(t, err)
		assert.True(t, ok)
	})
	t.Run("SetCode", func(t *testing.T) {
		require.NoError(t, a.SetCode(ctx, addr, "0x00"))
		assert.Equal(t, "anvil_setCode", rs.lastCall().Method)
	})
	t.Run("SetStorageAt", func(t *testing.T) {
		require.NoError(t, a.SetStorageAt(ctx, addr, "0x0", "0x1"))
		assert.Equal(t, "anvil_setStorageAt", rs.lastCall().Method)
	})
	t.Run("SetNonce", func(t *testing.T) {
		require.NoError(t, a.SetNonce(ctx, addr, 42))
		assert.Equal(t, "anvil_setNonce", rs.lastCall().Method)
	})
	t.Run("Mine", func(t *testing.T) {
		require.NoError(t, a.Mine(ctx, 5, 0))
		assert.Equal(t, "anvil_mine", rs.lastCall().Method)
	})
	t.Run("DropTransaction", func(t *testing.T) {
		require.NoError(t, a.DropTransaction(ctx, "0xdeadbeef"))
		assert.Equal(t, "anvil_dropTransaction", rs.lastCall().Method)
	})
	t.Run("SetAutomine", func(t *testing.T) {
		require.NoError(t, a.SetAutomine(ctx, true))
		assert.Equal(t, "evm_setAutomine", rs.lastCall().Method)
	})
	t.Run("SetIntervalMining", func(t *testing.T) {
		require.NoError(t, a.SetIntervalMining(ctx, 5))
		assert.Equal(t, "evm_setIntervalMining", rs.lastCall().Method)
	})
	t.Run("AutoImpersonate", func(t *testing.T) {
		require.NoError(t, a.AutoImpersonate(ctx, true))
		assert.Equal(t, "anvil_autoImpersonateAccount", rs.lastCall().Method)
	})
	t.Run("ResetFork", func(t *testing.T) {
		require.NoError(t, a.ResetFork(ctx, "https://example.org", 0))
		assert.Equal(t, "anvil_reset", rs.lastCall().Method)
	})
	t.Run("GetProof", func(t *testing.T) {
		result, err := a.GetProof(ctx, addr, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "eth_getProof", rs.lastCall().Method)
		assert.Equal(t, addr, result.Address)
	})
}

func TestRPCMethods_serverError(t *testing.T) {
	rs := newRPCServer(t, map[string]any{}) // empty map → server returns method-not-found
	a := newTestAnvil(t, rs.server)
	ctx := context.Background()

	err := a.MineBlock(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "method not found")
}

func TestRPCMethods_ctxCancelled(t *testing.T) {
	// Server sleeps long enough for ctx to cancel mid-call.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}))
	defer slow.Close()

	a := newTestAnvil(t, slow)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := a.MineBlock(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled),
		"expected ctx error, got %v", err)
}

func TestRPCMetricsIncrement(t *testing.T) {
	rs := newRPCServer(t, map[string]any{
		"evm_mine":     nil,
		"evm_snapshot": "0xa",
	})
	a := newTestAnvil(t, rs.server)
	ctx := context.Background()

	require.NoError(t, a.MineBlock(ctx))
	require.NoError(t, a.MineBlock(ctx))
	_, err := a.Snapshot(ctx)
	require.NoError(t, err)

	m := a.Metrics()
	assert.Equal(t, uint64(2), m.BlocksMined)
	assert.Equal(t, int64(3), m.RPCCalls)
}

// -----------------------------------------------------------------------------
// VerifyProof — hermetic tamper matrix over real merkle tries (no anvil binary)
// -----------------------------------------------------------------------------

// proofList collects trie proof nodes, implementing ethdb.KeyValueWriter.
type proofList [][]byte

func (p *proofList) Put(_ []byte, value []byte) error {
	*p = append(*p, value)
	return nil
}

func (p *proofList) Delete([]byte) error {
	return errors.New("delete not supported")
}

func (p proofList) hex() []string {
	out := make([]string, len(p))
	for i, node := range p {
		out[i] = hexutil.Encode(node)
	}
	return out
}

// proofFixture holds a state root and EIP-1186 results built from real merkle
// tries, mirroring what eth_getProof returns.
type proofFixture struct {
	stateRoot    common.Hash
	withStorage  *AccountResult // account whose storage trie has slot 1 = 42
	emptyStorage *AccountResult // account with an empty storage trie
	excluded     *AccountResult // address absent from the state trie
}

func buildProofFixture(t *testing.T) proofFixture {
	t.Helper()

	// Same field order as geth's StateAccount, and the struct VerifyProof decodes.
	type account struct {
		Nonce    uint64
		Balance  *big.Int
		Root     common.Hash
		CodeHash common.Hash
	}

	db := triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil)

	// Storage trie: slot 1 = 42.
	slotKey := crypto.Keccak256(common.HexToHash("0x01").Bytes())
	slotValue, err := rlp.EncodeToBytes(big.NewInt(42))
	require.NoError(t, err)
	storageTrie := trie.NewEmpty(db)
	require.NoError(t, storageTrie.Update(slotKey, slotValue))

	addrWithStorage := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addrEmpty := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addrExcluded := common.HexToAddress("0x3333333333333333333333333333333333333333")

	accWithStorage := account{Nonce: 5, Balance: big.NewInt(1000), Root: storageTrie.Hash(), CodeHash: types.EmptyCodeHash}
	accEmpty := account{Nonce: 0, Balance: big.NewInt(7), Root: types.EmptyRootHash, CodeHash: types.EmptyCodeHash}

	stateTrie := trie.NewEmpty(db)
	for _, entry := range []struct {
		addr common.Address
		data account
	}{{addrWithStorage, accWithStorage}, {addrEmpty, accEmpty}} {
		blob, err := rlp.EncodeToBytes(entry.data)
		require.NoError(t, err)
		require.NoError(t, stateTrie.Update(crypto.Keccak256(entry.addr.Bytes()), blob))
	}

	prove := func(tr *trie.Trie, key []byte) []string {
		var nodes proofList
		require.NoError(t, tr.Prove(key, &nodes))
		return nodes.hex()
	}

	return proofFixture{
		stateRoot: stateTrie.Hash(),
		withStorage: &AccountResult{
			Address:      addrWithStorage,
			AccountProof: prove(stateTrie, crypto.Keccak256(addrWithStorage.Bytes())),
			Balance:      (*hexutil.Big)(accWithStorage.Balance),
			CodeHash:     accWithStorage.CodeHash,
			Nonce:        hexutil.Uint64(accWithStorage.Nonce),
			StorageHash:  accWithStorage.Root,
			StorageProof: []StorageResult{
				// Unpadded key on purpose: exercises parseStorageKey normalization.
				{Key: "0x1", Value: (*hexutil.Big)(big.NewInt(42)), Proof: prove(storageTrie, slotKey)},
				{Key: "0x2", Value: (*hexutil.Big)(big.NewInt(0)), Proof: prove(storageTrie, crypto.Keccak256(common.HexToHash("0x02").Bytes()))},
			},
		},
		emptyStorage: &AccountResult{
			Address:      addrEmpty,
			AccountProof: prove(stateTrie, crypto.Keccak256(addrEmpty.Bytes())),
			Balance:      (*hexutil.Big)(accEmpty.Balance),
			CodeHash:     accEmpty.CodeHash,
			Nonce:        hexutil.Uint64(accEmpty.Nonce),
			StorageHash:  types.EmptyRootHash,
			// The empty-trie root node, RLP 0x80 — what anvil sends for EOAs.
			StorageProof: []StorageResult{{Key: "0x1", Value: (*hexutil.Big)(big.NewInt(0)), Proof: []string{"0x80"}}},
		},
		excluded: &AccountResult{
			Address:      addrExcluded,
			AccountProof: prove(stateTrie, crypto.Keccak256(addrExcluded.Bytes())),
			Balance:      (*hexutil.Big)(big.NewInt(0)),
			CodeHash:     types.EmptyCodeHash,
			Nonce:        0,
			StorageHash:  types.EmptyRootHash,
		},
	}
}

func TestVerifyProof_validProofs(t *testing.T) {
	fx := buildProofFixture(t)
	require.NoError(t, VerifyProof(fx.stateRoot, fx.withStorage))
	require.NoError(t, VerifyProof(fx.stateRoot, fx.emptyStorage))
}

func TestVerifyProof_tamperMatrix(t *testing.T) {
	fx := buildProofFixture(t)

	clone := func(p *AccountResult) *AccountResult {
		c := *p
		c.StorageProof = make([]StorageResult, len(p.StorageProof))
		copy(c.StorageProof, p.StorageProof)
		for i := range c.StorageProof {
			c.StorageProof[i].Proof = append([]string(nil), p.StorageProof[i].Proof...)
		}
		return &c
	}

	// corrupt flips a byte so the node no longer matches its referenced hash.
	corrupt := func(node string) string {
		raw, err := hexutil.Decode(node)
		require.NoError(t, err)
		raw[len(raw)-1] ^= 0xff
		return hexutil.Encode(raw)
	}

	cases := []struct {
		name   string
		mutate func(p *AccountResult)
	}{
		{"tampered balance", func(p *AccountResult) { p.Balance = (*hexutil.Big)(big.NewInt(999)) }},
		{"nil balance", func(p *AccountResult) { p.Balance = nil }},
		{"tampered nonce", func(p *AccountResult) { p.Nonce = 6 }},
		{"tampered code hash", func(p *AccountResult) { p.CodeHash = common.Hash{0x01} }},
		{"faked empty storage hash", func(p *AccountResult) { p.StorageHash = types.EmptyRootHash }},
		{"wrong storage value", func(p *AccountResult) { p.StorageProof[0].Value = (*hexutil.Big)(big.NewInt(43)) }},
		{"zero claim for a set slot", func(p *AccountResult) { p.StorageProof[0].Value = (*hexutil.Big)(big.NewInt(0)) }},
		{"missing storage value", func(p *AccountResult) { p.StorageProof[0].Value = nil }},
		{"nonzero claim for an absent slot", func(p *AccountResult) { p.StorageProof[1].Value = (*hexutil.Big)(big.NewInt(1)) }},
		{"value claimed under the wrong slot", func(p *AccountResult) { p.StorageProof[0].Key = "0x3" }},
		{"invalid storage key", func(p *AccountResult) { p.StorageProof[0].Key = "-0x1" }},
		{"corrupted account proof node", func(p *AccountResult) {
			p.AccountProof[len(p.AccountProof)-1] = corrupt(p.AccountProof[len(p.AccountProof)-1])
		}},
		{"corrupted storage proof node", func(p *AccountResult) {
			p.StorageProof[0].Proof[0] = corrupt(p.StorageProof[0].Proof[0])
		}},
		{"truncated account proof", func(p *AccountResult) { p.AccountProof = p.AccountProof[:len(p.AccountProof)-1] }},
		{"truncated storage proof", func(p *AccountResult) { p.StorageProof[0].Proof = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := clone(fx.withStorage)
			tc.mutate(p)
			require.Error(t, VerifyProof(fx.stateRoot, p))
		})
	}

	t.Run("wrong state root", func(t *testing.T) {
		require.Error(t, VerifyProof(common.Hash{0xde, 0xad}, fx.withStorage))
	})
	t.Run("nonzero claim against empty storage trie", func(t *testing.T) {
		p := clone(fx.emptyStorage)
		p.StorageProof[0].Value = (*hexutil.Big)(big.NewInt(1))
		require.Error(t, VerifyProof(fx.stateRoot, p))
	})
	t.Run("account exclusion is rejected", func(t *testing.T) {
		require.Error(t, VerifyProof(fx.stateRoot, fx.excluded))
	})
	t.Run("nil proof", func(t *testing.T) {
		require.Error(t, VerifyProof(fx.stateRoot, nil))
	})
}
