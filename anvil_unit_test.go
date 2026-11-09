package anvil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise pure-Go logic and use httptest for RPC-shape assertions.
// They do not spawn anvil and do not require Foundry on PATH, so they run on any machine.

// -----------------------------------------------------------------------------
// Builder validation
// -----------------------------------------------------------------------------

func TestBuilder_validate(t *testing.T) {
	cases := []struct {
		name    string
		builder *Builder
		wantErr bool
	}{
		{"default builder is valid (anvil picks the port)", NewBuilder(), false},
		{"fixed port is valid", NewBuilder().WithPort(8545), false},
		{"negative port fails", NewBuilder().WithPort(-1), true},
		{"out-of-range port fails", NewBuilder().WithPort(70000), true},
		{"negative block time fails", NewBuilder().WithBlockTime(-time.Second), true},
		{"fork block without a fork URL fails", NewBuilder().WithForkBlockNumber(5), true},
		{"fork block with a fork URL is valid", NewBuilder().WithFork("https://example.org").WithForkBlockNumber(5), false},
		{"other extra flags are valid", NewBuilder().WithArgs("--hardfork", "cancun"), false},
		{"--silent would hide the listening line", NewBuilder().WithArgs("--silent"), true},
		{"--quiet would hide the listening line", NewBuilder().WithArgs("--quiet"), true},
		{"-q would hide the listening line", NewBuilder().WithArgs("-q"), true},
		{"--port conflicts with WithPort", NewBuilder().WithArgs("--port", "9000"), true},
		{"--port=N conflicts with WithPort", NewBuilder().WithArgs("--port=9000"), true},
		{"-p conflicts with WithPort", NewBuilder().WithArgs("-p", "9000"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.builder.Build()
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidConfig)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBuilder_args(t *testing.T) {
	a, err := NewBuilder().Build()
	require.NoError(t, err)
	assert.Equal(t, []string{"--port", "0"}, a.args, "anvil picks the port by default")

	a, err = NewBuilder().
		WithBlockTime(1500*time.Millisecond).
		WithChainID(1337).
		WithGasLimit(12_000_000).
		WithFork("https://example.org").
		WithForkBlockNumber(0).
		WithPort(9000).
		WithArgs("--hardfork", "cancun").
		Build()
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--port", "9000",
		"--block-time", "1.5",
		"--chain-id", "1337",
		"--gas-limit", "12000000",
		"--fork-url", "https://example.org",
		"--fork-block-number", "0",
		"--hardfork", "cancun",
	}, a.args)
}

func TestBuilder_settingTwiceKeepsTheLastValue(t *testing.T) {
	a, err := NewBuilder().WithChainID(1).WithChainID(2).Build()
	require.NoError(t, err)
	assert.Equal(t, []string{"--port", "0", "--chain-id", "2"}, a.args)
}

func TestBuilder_explicitZeroIsPassed(t *testing.T) {
	a, err := NewBuilder().WithChainID(0).WithGasLimit(0).Build()
	require.NoError(t, err)
	assert.Equal(t, []string{"--port", "0", "--chain-id", "0", "--gas-limit", "0"}, a.args)
}

func TestBuilder_WithArgsAccumulates(t *testing.T) {
	a, err := NewBuilder().WithArgs("--hardfork", "cancun").WithArgs("--no-mining").Build()
	require.NoError(t, err)
	assert.Equal(t, []string{"--port", "0", "--hardfork", "cancun", "--no-mining"}, a.args)
}

// TestStartupTimeoutIncludesStderr uses a stand-in binary that writes to stderr
// and never prints the listening line. It can't run in parallel: it sets PATH.
func TestStartupTimeoutIncludesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the anvil binary")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho boom >&2\nexec sleep 30\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "anvil"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	a, err := NewBuilder().WithStartupTimeout(500 * time.Millisecond).Build()
	require.NoError(t, err)

	err = a.Start()
	require.ErrorIs(t, err, ErrStartupTimeout)
	assert.Contains(t, err.Error(), "boom")
}

func TestAccounts_defaults(t *testing.T) {
	a := &Anvil{}
	accounts := a.Accounts()
	require.Len(t, accounts, 10)
	assert.Equal(t, common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"), accounts[0].Address)
	assert.Equal(t, accounts[0].Address, crypto.PubkeyToAddress(accounts[0].Key.PublicKey))

	accounts[0] = Account{}
	assert.NotEqual(t, Account{}, a.Accounts()[0], "callers get their own copy")
}

func TestBuilder_WithStartupTimeout(t *testing.T) {
	t.Run("positive duration wins", func(t *testing.T) {
		b := NewBuilder().WithStartupTimeout(7 * time.Second)
		assert.Equal(t, 7*time.Second, b.startupTimeout)
	})
	t.Run("zero is ignored (keeps default)", func(t *testing.T) {
		b := NewBuilder().WithStartupTimeout(0)
		assert.Equal(t, DefaultStartupTimeout, b.startupTimeout)
	})
	t.Run("negative is ignored (keeps default)", func(t *testing.T) {
		b := NewBuilder().WithStartupTimeout(-time.Second)
		assert.Equal(t, DefaultStartupTimeout, b.startupTimeout)
	})
}

func TestBuilder_ZeroValueUsesDefaultStartupTimeout(t *testing.T) {
	a, err := (&Builder{}).Build()
	require.NoError(t, err)
	assert.Equal(t, DefaultStartupTimeout, a.startupTimeout)
}

func TestBuilder_BuildPropagatesStartupTimeout(t *testing.T) {
	a, err := NewBuilder().WithStartupTimeout(123 * time.Millisecond).Build()
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
// RPC method shape — httptest-backed
// -----------------------------------------------------------------------------

// rpcServer is a test JSON-RPC server that records calls and responds with canned results.
type rpcServer struct {
	mu     sync.Mutex
	calls  []rpcCall
	server *httptest.Server
}

type rpcCall struct {
	Method string
	Params []json.RawMessage
}

// paramsJSON returns the call's params as one JSON array; no params is "[]".
func (c rpcCall) paramsJSON() string {
	if len(c.Params) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(c.Params)
	return string(b)
}

type rpcReq struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

func newRPCServer(t *testing.T, results map[string]any) *rpcServer {
	t.Helper()
	rs := &rpcServer{}
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
		rs.mu.Lock()
		rs.calls = append(rs.calls, rpcCall{Method: req.Method, Params: req.Params})
		rs.mu.Unlock()

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

func (rs *rpcServer) recorded() []rpcCall {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return slices.Clone(rs.calls)
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
		state:     stateRunning,
		rpcClient: rpcClient,
		logger:    slog.New(slog.DiscardHandler),
	}
}

// cannedResults answers every wrapped method.
var cannedResults = map[string]any{
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
	"txpool_status":                  map[string]any{"pending": "0x0", "queued": "0x0"},
	"eth_blockNumber":                "0x5",
	"eth_getProof": map[string]any{
		"address":      "0x1234567890123456789012345678901234567890",
		"accountProof": []string{},
		"balance":      "0x0",
		"codeHash":     "0x0000000000000000000000000000000000000000000000000000000000000000",
		"nonce":        "0x0",
		"storageHash":  "0x0000000000000000000000000000000000000000000000000000000000000000",
		"storageProof": []any{},
	},
}

// TestRPCWrappers pins the JSON-RPC method and params each wrapper sends.
func TestRPCWrappers(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	const a = `"0x1234567890123456789012345678901234567890"`

	cases := []struct {
		name string
		run  func(context.Context, *Anvil) error
		want []rpcCall // Params holds one JSON array, compared with JSONEq
	}{
		{"MineBlock", func(ctx context.Context, x *Anvil) error { return x.MineBlock(ctx) },
			calls("evm_mine", `[]`)},
		{"Mine", func(ctx context.Context, x *Anvil) error { return x.Mine(ctx, 5, 0) },
			calls("anvil_mine", `["0x5"]`)},
		{"Mine with interval", func(ctx context.Context, x *Anvil) error { return x.Mine(ctx, 5, 12) },
			calls("anvil_mine", `["0x5","0xc"]`)},
		{"SetNextBlockTimestamp", func(ctx context.Context, x *Anvil) error { return x.SetNextBlockTimestamp(ctx, 12345) },
			calls("evm_setNextBlockTimestamp", `[12345]`)},
		{"IncreaseTime", func(ctx context.Context, x *Anvil) error { return x.IncreaseTime(ctx, 3600) },
			calls("evm_increaseTime", `[3600]`)},
		{"SetBalance", func(ctx context.Context, x *Anvil) error { return x.SetBalance(ctx, addr, big.NewInt(100)) },
			calls("anvil_setBalance", `[`+a+`,"100"]`)},
		{"SetNonce", func(ctx context.Context, x *Anvil) error { return x.SetNonce(ctx, addr, 42) },
			calls("anvil_setNonce", `[`+a+`,"0x2a"]`)},
		{"SetCode", func(ctx context.Context, x *Anvil) error { return x.SetCode(ctx, addr, []byte{0x60, 0x80}) },
			calls("anvil_setCode", `[`+a+`,"0x6080"]`)},
		{"SetStorageAt", func(ctx context.Context, x *Anvil) error {
			return x.SetStorageAt(ctx, addr, common.HexToHash("0x1"), common.HexToHash("0x2a"))
		}, calls("anvil_setStorageAt", `[`+a+`,"`+common.HexToHash("0x1").Hex()+`","`+common.HexToHash("0x2a").Hex()+`"]`)},
		{"Impersonate", func(ctx context.Context, x *Anvil) error { return x.Impersonate(ctx, addr) },
			calls("anvil_impersonateAccount", `[`+a+`]`)},
		{"StopImpersonating", func(ctx context.Context, x *Anvil) error { return x.StopImpersonating(ctx, addr) },
			calls("anvil_stopImpersonatingAccount", `[`+a+`]`)},
		{"AutoImpersonate", func(ctx context.Context, x *Anvil) error { return x.AutoImpersonate(ctx, true) },
			calls("anvil_autoImpersonateAccount", `[true]`)},
		{"SetAutomine", func(ctx context.Context, x *Anvil) error { return x.SetAutomine(ctx, false) },
			calls("evm_setAutomine", `[false]`)},
		{"SetIntervalMining", func(ctx context.Context, x *Anvil) error { return x.SetIntervalMining(ctx, 5) },
			calls("evm_setIntervalMining", `[5]`)},
		{"DropTransaction", func(ctx context.Context, x *Anvil) error {
			_, err := x.DropTransaction(ctx, common.HexToHash("0xbeef"))
			return err
		}, calls("anvil_dropTransaction", `["`+common.HexToHash("0xbeef").Hex()+`"]`)},
		{"Snapshot", func(ctx context.Context, x *Anvil) error { _, err := x.Snapshot(ctx); return err },
			calls("evm_snapshot", `[]`)},
		{"Revert", func(ctx context.Context, x *Anvil) error { _, err := x.Revert(ctx, "0xabc"); return err },
			calls("evm_revert", `["0xabc"]`)},
		{"GetProof", func(ctx context.Context, x *Anvil) error { _, err := x.GetProof(ctx, addr, nil, nil); return err },
			calls("eth_getProof", `[`+a+`,[],"latest"]`)},
		{"GetProof at a block", func(ctx context.Context, x *Anvil) error {
			_, err := x.GetProof(ctx, addr, []common.Hash{common.HexToHash("0x1")}, big.NewInt(5))
			return err
		}, calls("eth_getProof", `[`+a+`,["`+common.HexToHash("0x1").Hex()+`"],"0x5"]`)},
		// evm_revert consumes the snapshot, so ResetState takes a new one.
		{"ResetState", func(ctx context.Context, x *Anvil) error { x.baseline = "0x1"; return x.ResetState(ctx) },
			calls("evm_revert", `["0x1"]`, "evm_snapshot", `[]`)},
		{"WaitForMemPoolEmpty", func(ctx context.Context, x *Anvil) error { return x.WaitForMemPoolEmpty(ctx) },
			calls("txpool_status", `[]`)},
		{"WaitForBlock", func(ctx context.Context, x *Anvil) error { return x.WaitForBlock(ctx, 5) },
			calls("eth_blockNumber", `[]`)},
		// anvil drops every snapshot on reset, so ResetFork takes a new baseline.
		{"ResetFork", func(ctx context.Context, x *Anvil) error { return x.ResetFork(ctx, "https://example.org", 0) },
			calls("anvil_reset", `[{"forking":{"jsonRpcUrl":"https://example.org"}}]`, "evm_snapshot", `[]`)},
		{"ResetFork at a block", func(ctx context.Context, x *Anvil) error { return x.ResetFork(ctx, "https://example.org", 100) },
			calls("anvil_reset", `[{"forking":{"jsonRpcUrl":"https://example.org","blockNumber":"0x64"}}]`, "evm_snapshot", `[]`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rs := newRPCServer(t, cannedResults)
			require.NoError(t, tc.run(t.Context(), newTestAnvil(t, rs.server)))

			got := rs.recorded()
			require.Len(t, got, len(tc.want))
			for i, want := range tc.want {
				assert.Equal(t, want.Method, got[i].Method)
				assert.JSONEq(t, string(want.Params[0]), got[i].paramsJSON(), want.Method)
			}
		})
	}
}

// calls builds expected calls from method/params pairs.
func calls(methodParams ...string) []rpcCall {
	var out []rpcCall
	for i := 0; i < len(methodParams); i += 2 {
		out = append(out, rpcCall{Method: methodParams[i], Params: []json.RawMessage{json.RawMessage(methodParams[i+1])}})
	}
	return out
}

func TestRPCResults(t *testing.T) {
	rs := newRPCServer(t, cannedResults)
	a := newTestAnvil(t, rs.server)
	ctx := t.Context()

	id, err := a.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, SnapshotID("0xabc"), id)

	ok, err := a.Revert(ctx, id)
	require.NoError(t, err)
	assert.True(t, ok)

	proof, err := a.GetProof(ctx, common.HexToAddress("0x1234567890123456789012345678901234567890"), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, common.HexToAddress("0x1234567890123456789012345678901234567890"), proof.Address)

	require.NoError(t, a.ResetFork(ctx, "https://example.org", 0))
	assert.Equal(t, SnapshotID("0xabc"), a.baseline)
}

func TestDropTransaction_reportsWhetherAnythingWasDropped(t *testing.T) {
	hash := common.HexToHash("0xbeef")

	a := newTestAnvil(t, newRPCServer(t, map[string]any{"anvil_dropTransaction": hash}).server)
	dropped, err := a.DropTransaction(t.Context(), hash)
	require.NoError(t, err)
	assert.True(t, dropped)

	a = newTestAnvil(t, newRPCServer(t, map[string]any{"anvil_dropTransaction": nil}).server)
	dropped, err = a.DropTransaction(t.Context(), hash)
	require.NoError(t, err)
	assert.False(t, dropped)
}

func TestRPCMethods_serverError(t *testing.T) {
	rs := newRPCServer(t, map[string]any{}) // empty map → server returns method-not-found
	a := newTestAnvil(t, rs.server)

	err := a.MineBlock(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "method not found")
	// call prefixes the method name and keeps the JSON-RPC error reachable.
	assert.Regexp(t, `^anvil: evm_mine: `, err.Error())
	var rpcErr rpc.Error
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, -32601, rpcErr.ErrorCode())
}

func TestRPCMethods_ctxCancelled(t *testing.T) {
	// The server never answers. Draining the body lets net/http notice the
	// client hanging up and cancel r.Context(); release covers the case where
	// it doesn't, so Close can't block.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer slow.Close()
	defer close(release)

	a := newTestAnvil(t, slow)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	require.ErrorIs(t, a.MineBlock(ctx), context.DeadlineExceeded)
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
