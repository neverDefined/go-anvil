package anvil

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// Errors returned by this package. Check them with errors.Is.
var (
	// ErrNotStarted means the instance isn't running: Start wasn't called, or Stop was.
	ErrNotStarted = fmt.Errorf("anvil: instance not started")

	// ErrAlreadyStarted means Start was called twice. Instances are single-use.
	ErrAlreadyStarted = fmt.Errorf("anvil: instance already started")

	// ErrConnectionFailed means Start couldn't connect to anvil's RPC endpoint.
	ErrConnectionFailed = fmt.Errorf("anvil: connection failed")

	// ErrInvalidConfig means Build rejected the builder's settings.
	ErrInvalidConfig = fmt.Errorf("anvil: invalid configuration")

	// ErrAnvilNotFound means anvil isn't on PATH or in the Foundry install directory.
	ErrAnvilNotFound = fmt.Errorf("anvil: binary not found")

	// ErrAnvilNotExecutable means the anvil binary was found but can't be run.
	ErrAnvilNotExecutable = fmt.Errorf("anvil: binary not executable")

	// ErrStartupTimeout means anvil wasn't ready within the startup timeout.
	ErrStartupTimeout = fmt.Errorf("anvil: startup timeout exceeded")

	// ErrProcessExited means anvil exited before it was ready, for example on an
	// invalid flag or a port in use. The message has the exit status and anvil's
	// last stderr lines.
	ErrProcessExited = fmt.Errorf("anvil: process exited during startup")
)

// DefaultStartupTimeout is how long Start waits for anvil unless
// WithStartupTimeout sets another limit.
const DefaultStartupTimeout = 5 * time.Second

// pollInterval is how often WaitForBlock and WaitForMemPoolEmpty check the node.
const pollInterval = 100 * time.Millisecond

// SnapshotID identifies a saved chain state. Get one from Snapshot and pass it
// to Revert.
type SnapshotID string

// Account is a private key and its address. Accounts returns anvil's pre-funded
// default accounts, and NewFundedAccount creates new ones.
type Account struct {
	Key     *ecdsa.PrivateKey
	Address common.Address
}

// defaultKeys are the private keys of the accounts anvil derives from its
// default mnemonic. They are public test keys.
var defaultKeys = [...]string{
	"ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
	"59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
	"5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a",
	"7c852118294e51e653712a81e05800f419141751be58f605c371e15141b007a6",
	"47e179ec197488593b187f80a00eb0da91f1b9d0b13f8733639f19c30a34926a",
	"8b3a350cf5c34c9194ca85829a2df0ec3153be0318b5e2d3348e872092edffba",
	"92db14e403b83dfe3df233f83dfa3a0d7096f21ca9b0d6d6b8d88b2b4ec1564e",
	"4bbbf85ce3377467afe5d46f804f221813b2bb87f24d81f60f1fcdbf7cbf4356",
	"dbda1821b80551c9d65939329250298aa3472ba22feea921c0cf5d620ea67b97",
	"2a871d0798f97d79848a013d4936a73bf4cc922c825d33c1cf7073dff6d409c6",
}

// defaultAccounts parses defaultKeys once.
var defaultAccounts = sync.OnceValue(func() []Account {
	accounts := make([]Account, len(defaultKeys))
	for i, hexKey := range defaultKeys {
		key, err := crypto.HexToECDSA(hexKey)
		if err != nil {
			panic(fmt.Errorf("anvil: default key %d is invalid: %w", i, err))
		}
		accounts[i] = Account{Key: key, Address: crypto.PubkeyToAddress(key.PublicKey)}
	}
	return accounts
})

// lifecycle is the state of an Anvil instance. Instances are single-use:
// new -> running -> stopped.
type lifecycle int

const (
	stateNew lifecycle = iota
	stateRunning
	stateStopped
)

// Anvil controls one anvil process. Create one with New, NewBuilder or NewT.
type Anvil struct {
	context        context.Context
	cancel         context.CancelFunc
	args           []string
	startupTimeout time.Duration
	logger         *slog.Logger

	// mu guards the lifecycle fields below. RPC calls hold it only long enough
	// to read the client pointers.
	mu        sync.RWMutex
	state     lifecycle
	cmd       *exec.Cmd
	exited    chan struct{} // closed once the process has exited and been reaped
	rpcURL    string
	client    *ethclient.Client
	rpcClient *rpc.Client

	// baseline is the snapshot ResetState reverts to: the state right after
	// Start, or after the last Reset. Empty if a reset failed partway.
	// Start sets it under mu before the instance is running; after that it is
	// guarded by baselineMu.
	baselineMu sync.Mutex
	baseline   SnapshotID
}

// New returns an instance with the default configuration. Call Start to run it.
func New() (*Anvil, error) {
	return NewBuilder().Build()
}

// Start launches anvil and returns once its RPC endpoint answers.
//
// Unless WithPort was used, anvil picks a free port, so instances in parallel
// tests never collide. URL reports the address.
//
// If anvil exits during startup, for example on an invalid flag or a port in
// use, Start returns ErrProcessExited with anvil's stderr in the message. If
// anvil isn't ready within the startup timeout, it returns ErrStartupTimeout.
// Either way the process is stopped.
//
// An instance can be started only once. A second call returns ErrAlreadyStarted.
func (a *Anvil) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch a.state {
	case stateRunning:
		return ErrAlreadyStarted
	case stateStopped:
		return fmt.Errorf("%w: the instance was stopped and cannot be restarted", ErrAlreadyStarted)
	}

	anvilPath, err := resolveAnvilPath()
	if err != nil {
		return err
	}

	listening := make(chan string, 1)
	stderrTail := &tailBuffer{max: 10}

	stdout := &lineWriter{fn: func(line string) {
		a.logger.Debug(line, "stream", "stdout")
		if addr, ok := strings.CutPrefix(line, "Listening on "); ok {
			select {
			case listening <- strings.TrimSpace(addr):
			default:
			}
		}
	}}
	stderr := &lineWriter{fn: func(line string) {
		a.logger.Warn(line, "stream", "stderr")
		stderrTail.add(line)
	}}

	cmd := exec.CommandContext(a.context, anvilPath, a.args...) //nolint:gosec // anvilPath is resolved via PATH lookup or verified executable at the Foundry fallback
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Bound how long Wait blocks on output copying after the process exits.
	cmd.WaitDelay = time.Second

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start anvil: %w", err)
	}

	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		// Wait has finished copying output, so the writers are idle.
		stdout.flush()
		stderr.flush()
		close(exited)
	}()
	a.cmd = cmd
	a.exited = exited

	ctx, cancel := context.WithTimeout(a.context, a.startupTimeout)
	defer cancel()

	err = a.awaitReady(ctx, listening, exited)
	if err == nil {
		// Record the freshly started state so ResetState can return to it.
		err = a.rpcClient.CallContext(ctx, &a.baseline, "evm_snapshot")
		if err != nil {
			err = fmt.Errorf("take baseline snapshot: %w", err)
		}
	}
	if err != nil {
		if errors.Is(err, ErrProcessExited) {
			// exited is closed, so waitErr and the stderr tail are final.
			if waitErr != nil {
				err = fmt.Errorf("%w (%w): %s", ErrProcessExited, waitErr, stderrTail)
			} else {
				err = fmt.Errorf("%w (exit status 0): %s", ErrProcessExited, stderrTail)
			}
		}
		stopErr := a.shutdownLocked()
		if errors.Is(err, ErrStartupTimeout) {
			// Output is discarded by default, so this may be the only trace of it.
			err = fmt.Errorf("%w: %s", err, stderrTail)
		}
		return errors.Join(err, stopErr)
	}

	a.state = stateRunning
	return nil
}

// awaitReady waits for anvil to print its listening address, connects to it and
// confirms it answers RPC. It returns ErrProcessExited if the process exits first
// and ErrStartupTimeout if ctx expires first. a.mu must be held.
func (a *Anvil) awaitReady(ctx context.Context, listening <-chan string, exited <-chan struct{}) error {
	timeout := fmt.Errorf("%w after %s", ErrStartupTimeout, a.startupTimeout)

	var addr string
	select {
	case addr = <-listening:
	case <-exited:
		return ErrProcessExited
	case <-ctx.Done():
		return timeout
	}

	url := "http://" + addr
	rpcClient, err := rpc.DialContext(ctx, url)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrConnectionFailed, url, err)
	}

	// Anvil prints its address once the socket is bound. Probe until it serves
	// requests, which is normally the first attempt.
	for {
		var block string
		if err = rpcClient.CallContext(ctx, &block, "eth_blockNumber"); err == nil {
			break
		}
		select {
		case <-exited:
			rpcClient.Close()
			return ErrProcessExited
		case <-ctx.Done():
			rpcClient.Close()
			return fmt.Errorf("%w: %w", timeout, err)
		case <-time.After(10 * time.Millisecond):
		}
	}

	a.rpcURL = url
	a.rpcClient = rpcClient
	a.client = ethclient.NewClient(rpcClient)
	return nil
}

// maxLineLen caps how much unterminated output lineWriter buffers.
const maxLineLen = 1 << 20

// lineWriter calls fn for each complete line written to it. exec.Cmd writes
// each stream from a single goroutine, so it needs no locking.
type lineWriter struct {
	buf []byte
	fn  func(line string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.fn(strings.TrimSuffix(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLineLen {
		w.flush()
	}
	return len(p), nil
}

// flush emits any buffered output that did not end in a newline.
func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.fn(strings.TrimSuffix(string(w.buf), "\r"))
		w.buf = w.buf[:0]
	}
}

// tailBuffer keeps the last max non-empty lines added to it.
type tailBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func (t *tailBuffer) add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		return "no stderr output"
	}
	return strings.Join(t.lines, "; ")
}

// resolveAnvilPath locates the anvil binary, preferring PATH and falling back to the
// standard Foundry install location. The fallback path is stat'd and checked for the
// executable bit so callers receive a clear error instead of an opaque exec failure.
func resolveAnvilPath() (string, error) {
	if path, err := exec.LookPath("anvil"); err == nil {
		return path, nil
	}

	homeDir := os.Getenv("XDG_CONFIG_HOME")
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
	}
	anvilPath := filepath.Join(homeDir, ".foundry", "bin", "anvil")

	info, err := os.Stat(anvilPath) //nolint:gosec // path is built from trusted components (home dir + fixed "foundry/bin/anvil")
	if err != nil {
		return "", fmt.Errorf("%w at %s: %w", ErrAnvilNotFound, anvilPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%w: %s is a directory", ErrAnvilNotExecutable, anvilPath)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%w: %s", ErrAnvilNotExecutable, anvilPath)
	}

	return anvilPath, nil
}

// running returns the RPC client if the instance is running, and ErrNotStarted
// otherwise.
func (a *Anvil) running() (*rpc.Client, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch a.state {
	case stateRunning:
		return a.rpcClient, nil
	case stateStopped:
		return nil, fmt.Errorf("%w: the instance is stopped", ErrNotStarted)
	default:
		return nil, ErrNotStarted
	}
}

// call forwards a JSON-RPC call to anvil. It returns ErrNotStarted if the
// instance is not running, and otherwise wraps any error with the method name.
func (a *Anvil) call(ctx context.Context, result any, method string, args ...any) error {
	c, err := a.running()
	if err != nil {
		return err
	}
	if err := c.CallContext(ctx, result, method, args...); err != nil {
		return fmt.Errorf("anvil: %s: %w", method, err)
	}
	return nil
}

// Client returns an ethclient connected to the node. It is nil before Start and
// after Stop.
func (a *Anvil) Client() *ethclient.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

// RPCClient returns the JSON-RPC client, for methods this package doesn't wrap.
// It is nil before Start and after Stop.
func (a *Anvil) RPCClient() *rpc.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.rpcClient
}

// URL returns the HTTP JSON-RPC endpoint of the node, for example
// "http://127.0.0.1:53817". It is empty before Start and after Stop.
func (a *Anvil) URL() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.rpcURL
}

// WSURL returns the WebSocket JSON-RPC endpoint of the node, for example
// "ws://127.0.0.1:53817". anvil serves HTTP and WebSocket on the same port.
// It is empty before Start and after Stop.
func (a *Anvil) WSURL() string {
	url := a.URL()
	if url == "" {
		return ""
	}
	return "ws" + strings.TrimPrefix(url, "http")
}

// DialWS opens a new WebSocket connection to the node. Subscriptions such as
// SubscribeNewHead and SubscribeFilterLogs need one, because the HTTP client
// from Client can't receive pushed events. The caller closes the returned
// client.
func (a *Anvil) DialWS(ctx context.Context) (*ethclient.Client, error) {
	url := a.WSURL()
	if url == "" {
		return nil, ErrNotStarted
	}
	client, err := ethclient.DialContext(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("anvil: dial websocket: %w", err)
	}
	return client, nil
}

// Accounts returns anvil's ten pre-funded default accounts. With a different
// --mnemonic or a smaller --accounts through WithArgs, some or all of them won't
// be funded.
func (a *Anvil) Accounts() []Account {
	return slices.Clone(defaultAccounts())
}

// MineBlock mines one block.
func (a *Anvil) MineBlock(ctx context.Context) error {
	return a.call(ctx, nil, "evm_mine")
}

// SetNextBlockTimestamp sets the timestamp of the next block, in Unix seconds.
func (a *Anvil) SetNextBlockTimestamp(ctx context.Context, timestamp int64) error {
	return a.call(ctx, nil, "evm_setNextBlockTimestamp", timestamp)
}

// IncreaseTime moves the chain's clock forward by seconds. The next mined block
// shows the change.
func (a *Anvil) IncreaseTime(ctx context.Context, seconds int64) error {
	return a.call(ctx, nil, "evm_increaseTime", seconds)
}

// SetBalance sets the balance of address, in wei.
func (a *Anvil) SetBalance(ctx context.Context, address common.Address, balance *big.Int) error {
	return a.call(ctx, nil, "anvil_setBalance", address, balance.String())
}

// Impersonate lets SendTransaction send from address without its private key.
func (a *Anvil) Impersonate(ctx context.Context, address common.Address) error {
	return a.call(ctx, nil, "anvil_impersonateAccount", address)
}

// StopImpersonating undoes Impersonate for address.
func (a *Anvil) StopImpersonating(ctx context.Context, address common.Address) error {
	return a.call(ctx, nil, "anvil_stopImpersonatingAccount", address)
}

// Snapshot saves the current chain state and returns an ID to pass to Revert.
func (a *Anvil) Snapshot(ctx context.Context) (SnapshotID, error) {
	var id SnapshotID
	err := a.call(ctx, &id, "evm_snapshot")
	return id, err
}

// Revert restores the state saved by Snapshot and reports whether the snapshot
// existed. A snapshot works once: reverting discards it and any taken after it.
func (a *Anvil) Revert(ctx context.Context, id SnapshotID) (bool, error) {
	var success bool
	err := a.call(ctx, &success, "evm_revert", id)
	return success, err
}

// SetCode sets the bytecode at address.
func (a *Anvil) SetCode(ctx context.Context, address common.Address, code []byte) error {
	return a.call(ctx, nil, "anvil_setCode", address, hexutil.Bytes(code))
}

// SetStorageAt sets one storage slot of address.
func (a *Anvil) SetStorageAt(ctx context.Context, address common.Address, slot, value common.Hash) error {
	return a.call(ctx, nil, "anvil_setStorageAt", address, slot, value)
}

// SetNonce sets the nonce of address.
func (a *Anvil) SetNonce(ctx context.Context, address common.Address, nonce uint64) error {
	return a.call(ctx, nil, "anvil_setNonce", address, hexutil.Uint64(nonce))
}

// AccountResult is an EIP-1186 account proof with its storage proofs, as
// returned by eth_getProof.
//
// It has the same field names and JSON tags as gethclient.AccountResult, with
// hexutil types for Balance and Nonce. go-anvil
// doesn't use that type because importing gethclient pulls go-ethereum's p2p
// stack into every build.
type AccountResult struct {
	Address      common.Address  `json:"address"`
	AccountProof []string        `json:"accountProof"`
	Balance      *hexutil.Big    `json:"balance"`
	CodeHash     common.Hash     `json:"codeHash"`
	Nonce        hexutil.Uint64  `json:"nonce"`
	StorageHash  common.Hash     `json:"storageHash"`
	StorageProof []StorageResult `json:"storageProof"`
}

// StorageResult is an EIP-1186 proof for a single storage slot.
type StorageResult struct {
	Key   string       `json:"key"`
	Value *hexutil.Big `json:"value"`
	Proof []string     `json:"proof"`
}

// GetProof returns the EIP-1186 account and storage proofs for address. Pass nil
// storageKeys for an account-only proof. A nil blockNumber means the latest
// block; pin a block when the proof must match that block's state root.
// VerifyProof checks the result against a header's state root.
func (a *Anvil) GetProof(ctx context.Context, address common.Address, storageKeys []common.Hash, blockNumber *big.Int) (*AccountResult, error) {
	if storageKeys == nil {
		storageKeys = []common.Hash{}
	}
	var result AccountResult
	if err := a.call(ctx, &result, "eth_getProof", address, storageKeys, toBlockNumArg(blockNumber)); err != nil {
		return nil, err
	}
	return &result, nil
}

// toBlockNumArg encodes a block number for JSON-RPC; nil means "latest".
// Negative values map through rpc.BlockNumber, matching ethclient's handling
// of the "pending"/"finalized"/"safe" sentinels.
func toBlockNumArg(number *big.Int) string {
	if number == nil {
		return "latest"
	}
	if number.Sign() >= 0 {
		return hexutil.EncodeBig(number)
	}
	return rpc.BlockNumber(number.Int64()).String()
}

// Mine mines numBlocks blocks. interval is the number of seconds between block
// timestamps: the first block is interval seconds after the node's current time
// and each later block interval seconds after the one before it. With 0, anvil
// uses its normal timestamps. To mine at an absolute time, call
// SetNextBlockTimestamp and then MineBlock.
func (a *Anvil) Mine(ctx context.Context, numBlocks uint64, interval uint64) error {
	args := []any{hexutil.Uint64(numBlocks)}
	if interval != 0 {
		args = append(args, hexutil.Uint64(interval))
	}
	return a.call(ctx, nil, "anvil_mine", args...)
}

// DropTransaction removes a transaction from the pool and reports whether it
// removed anything.
func (a *Anvil) DropTransaction(ctx context.Context, hash common.Hash) (bool, error) {
	var dropped *common.Hash
	err := a.call(ctx, &dropped, "anvil_dropTransaction", hash)
	return dropped != nil, err
}

// SetAutomine sets whether anvil mines a block for each transaction. With it
// off, mine with MineBlock or Mine.
func (a *Anvil) SetAutomine(ctx context.Context, enabled bool) error {
	return a.call(ctx, nil, "evm_setAutomine", enabled)
}

// SetIntervalMining makes anvil mine on a fixed interval, in seconds. Zero turns
// it off.
func (a *Anvil) SetIntervalMining(ctx context.Context, seconds uint64) error {
	return a.call(ctx, nil, "evm_setIntervalMining", seconds)
}

// AutoImpersonate lets SendTransaction send from any address while enabled.
func (a *Anvil) AutoImpersonate(ctx context.Context, enabled bool) error {
	return a.call(ctx, nil, "anvil_autoImpersonateAccount", enabled)
}

// TxArgs describes a transaction for SendTransaction and DebugTraceCall.
// Only From is required.
type TxArgs struct {
	From  common.Address
	To    *common.Address // nil creates a contract from Data
	Value *big.Int        // nil sends no ether
	Data  []byte
	Gas   uint64 // zero lets anvil estimate it
}

// params encodes the set fields as a JSON-RPC transaction object.
func (tx TxArgs) params() map[string]any {
	params := map[string]any{"from": tx.From}
	if tx.To != nil {
		params["to"] = tx.To
	}
	if tx.Value != nil {
		params["value"] = (*hexutil.Big)(tx.Value)
	}
	if len(tx.Data) > 0 {
		params["data"] = hexutil.Bytes(tx.Data)
	}
	if tx.Gas != 0 {
		params["gas"] = hexutil.Uint64(tx.Gas)
	}
	return params
}

// SendTransaction submits a transaction that anvil signs itself, which works for
// its default accounts and for impersonated addresses. To send from a key you
// hold, sign the transaction and use Client().SendTransaction.
//
// With automine on, this can return just before the transaction is mined. Call
// WaitForMemPoolEmpty or wait for the receipt before reading the result.
func (a *Anvil) SendTransaction(ctx context.Context, tx TxArgs) (common.Hash, error) {
	var hash common.Hash
	err := a.call(ctx, &hash, "eth_sendTransaction", tx.params())
	return hash, err
}

// TraceConfig chooses the tracer for DebugTraceTransaction and DebugTraceCall.
type TraceConfig struct {
	// Tracer names a built-in tracer such as "callTracer" or "prestateTracer".
	// Empty uses the opcode logger.
	Tracer string `json:"tracer,omitempty"`
	// TracerConfig holds options for the tracer, for example
	// map[string]any{"onlyTopCall": true}.
	TracerConfig any `json:"tracerConfig,omitempty"`
}

// DebugTraceTransaction returns the trace of a mined transaction as raw JSON,
// whose shape depends on the tracer. A nil config uses the opcode logger, whose
// log anvil leaves empty unless it was started with WithArgs("--steps-tracing").
func (a *Anvil) DebugTraceTransaction(ctx context.Context, hash common.Hash, config *TraceConfig) (json.RawMessage, error) {
	return a.trace(ctx, "debug_traceTransaction", config, hash)
}

// DebugTraceCall simulates tx at a block and returns its trace as raw JSON,
// without changing the chain. A nil blockNumber means the latest block, and a
// nil config uses the opcode logger.
func (a *Anvil) DebugTraceCall(ctx context.Context, tx TxArgs, blockNumber *big.Int, config *TraceConfig) (json.RawMessage, error) {
	return a.trace(ctx, "debug_traceCall", config, tx.params(), toBlockNumArg(blockNumber))
}

// trace calls a debug_trace method, leaving out a nil config because anvil
// rejects null there.
func (a *Anvil) trace(ctx context.Context, method string, config *TraceConfig, args ...any) (json.RawMessage, error) {
	if config != nil {
		args = append(args, config)
	}
	var result json.RawMessage
	err := a.call(ctx, &result, method, args...)
	return result, err
}

// DumpState returns the chain state as a blob for LoadState. Use it to keep a
// fixture, such as a set of deployed contracts, between test runs.
func (a *Anvil) DumpState(ctx context.Context) ([]byte, error) {
	var state hexutil.Bytes
	err := a.call(ctx, &state, "anvil_dumpState")
	return state, err
}

// LoadState applies a blob from DumpState, which may come from another instance.
// It merges: accounts in the blob are overwritten and other accounts are left as
// they are. On a node that isn't forked, the head moves to the block number in
// the blob.
func (a *Anvil) LoadState(ctx context.Context, state []byte) error {
	return a.call(ctx, nil, "anvil_loadState", hexutil.Bytes(state))
}

// DealERC20 sets the recipient's token balance to amount. anvil finds the balance slot by
// simulating balanceOf, so it returns an error for a token that doesn't keep
// balances in its own storage. The total supply doesn't change.
func (a *Anvil) DealERC20(ctx context.Context, token, to common.Address, amount *big.Int) error {
	return a.call(ctx, nil, "anvil_dealERC20", to, token, (*hexutil.Big)(amount))
}

// SetERC20Allowance sets how much of owner's token balance spender may use.
func (a *Anvil) SetERC20Allowance(ctx context.Context, token, owner, spender common.Address, amount *big.Int) error {
	return a.call(ctx, nil, "anvil_setERC20Allowance", owner, spender, token, (*hexutil.Big)(amount))
}

// SetNextBlockBaseFee sets the base fee of the next block, in wei. Later blocks
// adjust from it as usual.
func (a *Anvil) SetNextBlockBaseFee(ctx context.Context, fee uint64) error {
	return a.call(ctx, nil, "anvil_setNextBlockBaseFeePerGas", hexutil.Uint64(fee))
}

// SetCoinbase sets the miner address of future blocks.
func (a *Anvil) SetCoinbase(ctx context.Context, address common.Address) error {
	return a.call(ctx, nil, "anvil_setCoinbase", address)
}

// SetBlockGasLimit sets the gas limit of future blocks.
func (a *Anvil) SetBlockGasLimit(ctx context.Context, limit uint64) error {
	return a.call(ctx, nil, "evm_setBlockGasLimit", hexutil.Uint64(limit))
}

// ForkOptions says what Reset should fork.
type ForkOptions struct {
	// URL is the node to fork from. Empty keeps the current fork URL, which
	// fails on a node that isn't forked.
	URL string
	// BlockNumber is the block to fork at. Zero means the upstream's latest
	// block, not the block the node first forked at.
	BlockNumber uint64
}

// Reset discards the chain and starts over. With nil, the node becomes a fresh
// local chain with its default accounts funded, and a forked node stops forking.
// With options, the node forks again as they describe.
//
// To undo changes and keep the node as it started, use ResetState. anvil discards
// every snapshot on reset, so afterwards ResetState returns to the freshly reset
// state rather than the state after Start.
func (a *Anvil) Reset(ctx context.Context, fork *ForkOptions) error {
	var args []any
	if fork != nil {
		forking := map[string]any{}
		if fork.URL != "" {
			forking["jsonRpcUrl"] = fork.URL
		}
		if fork.BlockNumber != 0 {
			forking["blockNumber"] = hexutil.Uint64(fork.BlockNumber)
		}
		args = append(args, map[string]any{"forking": forking})
	}
	if err := a.call(ctx, nil, "anvil_reset", args...); err != nil {
		return err
	}

	a.baselineMu.Lock()
	defer a.baselineMu.Unlock()
	return a.retakeBaselineLocked(ctx)
}

// Close stops the node. It is the same as Stop, and is safe to call more than
// once or on an instance that was never started.
func (a *Anvil) Close() error {
	return a.Stop()
}

// Stop kills the anvil process, waits for it to exit and closes the RPC clients.
// Later RPC calls on the instance return ErrNotStarted. It is safe to call more than
// once. It returns an error only if the process could not be killed.
func (a *Anvil) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shutdownLocked()
}

// shutdownLocked closes the clients, kills the process and waits until it has
// been reaped. It is idempotent. a.mu must be held.
func (a *Anvil) shutdownLocked() error {
	if a.state == stateStopped {
		return nil
	}
	a.state = stateStopped

	// Drop the clients so Client/RPCClient return nil, as before Start. Closing an
	// HTTP rpc.Client doesn't stop it from sending requests, and with --port 0 the
	// OS may hand this port to another node later.
	if a.rpcClient != nil {
		a.rpcClient.Close() // a.client wraps the same connection
	}
	a.rpcClient, a.client, a.rpcURL = nil, nil, ""

	var err error
	if a.cmd != nil {
		killErr := a.cmd.Process.Kill()
		switch {
		case killErr == nil, errors.Is(killErr, os.ErrProcessDone):
			<-a.exited
		default:
			err = fmt.Errorf("failed to kill anvil process: %w", killErr)
		}
	}

	if a.cancel != nil {
		a.cancel()
	}
	return err
}

// ResetState reverts the chain to the state right after Start or the last Reset.
// Settings that aren't chain state, such as automine, impersonation or the
// coinbase, stay as they are.
//
// Concurrent calls are serialized. Resetting doesn't isolate tests running in
// parallel on one instance; give those their own instances.
func (a *Anvil) ResetState(ctx context.Context) error {
	// Checking the state first also orders this read of baseline after Start's write.
	if _, err := a.running(); err != nil {
		return err
	}

	a.baselineMu.Lock()
	defer a.baselineMu.Unlock()

	if a.baseline == "" {
		return errors.New("anvil: reset state: no baseline snapshot (an earlier reset failed partway)")
	}

	ok, err := a.Revert(ctx, a.baseline)
	if err != nil {
		return fmt.Errorf("reset state: %w", err)
	}
	if !ok {
		a.baseline = ""
		return errors.New("anvil: reset state: the baseline snapshot no longer exists (was it reverted directly?)")
	}

	// evm_revert consumes the snapshot, so take a fresh one of the same state.
	return a.retakeBaselineLocked(ctx)
}

// retakeBaselineLocked snapshots the current state as the new ResetState
// baseline. a.baselineMu must be held.
func (a *Anvil) retakeBaselineLocked(ctx context.Context) error {
	id, err := a.Snapshot(ctx)
	if err != nil {
		a.baseline = ""
		return fmt.Errorf("take baseline snapshot: %w", err)
	}
	a.baseline = id
	return nil
}

// WaitForBlock waits until the chain reaches the given block. It checks immediately,
// then every 100ms, until the block is reached or ctx is done, so pass a ctx with
// a deadline to bound the wait.
func (a *Anvil) WaitForBlock(ctx context.Context, number uint64) error {
	// Check the lifecycle first so ErrNotStarted isn't masked by a finished ctx.
	if _, err := a.running(); err != nil {
		return err
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		var current hexutil.Uint64
		if err := a.call(ctx, &current, "eth_blockNumber"); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for block %d: %w", number, ctx.Err())
			}
			return err
		}
		if uint64(current) >= number {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for block %d: %w", number, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Builder configures an instance. Its With methods return the builder, so calls
// chain, and they take effect when Build runs.
type Builder struct {
	port           int
	blockTime      time.Duration
	chainID        *uint64
	gasLimit       *uint64
	forkURL        string
	forkBlock      *uint64
	extraArgs      []string
	startupTimeout time.Duration
	logger         *slog.Logger
}

// NewBuilder returns a Builder with the default configuration.
func NewBuilder() *Builder {
	return &Builder{startupTimeout: DefaultStartupTimeout}
}

// WithBlockTime makes anvil mine a block every d instead of one per transaction.
// Fractions of a second work. Zero, the default, leaves automine on.
func (b *Builder) WithBlockTime(d time.Duration) *Builder {
	b.blockTime = d
	return b
}

// WithFork forks the chain served at url, starting from its latest block.
func (b *Builder) WithFork(url string) *Builder {
	b.forkURL = url
	return b
}

// WithForkBlockNumber sets the block to fork at. It requires WithFork.
func (b *Builder) WithForkBlockNumber(number uint64) *Builder {
	b.forkBlock = &number
	return b
}

// WithPort sets a fixed RPC port. By default anvil picks a free port, which URL
// reports. With a fixed port, Start returns ErrProcessExited if it is in use.
func (b *Builder) WithPort(port int) *Builder {
	b.port = port
	return b
}

// WithChainID sets the chain ID. The default is 31337.
func (b *Builder) WithChainID(id uint64) *Builder {
	b.chainID = &id
	return b
}

// WithGasLimit sets the block gas limit.
func (b *Builder) WithGasLimit(limit uint64) *Builder {
	b.gasLimit = &limit
	return b
}

// WithArgs passes extra flags to anvil, after the ones the builder sets, for
// options with no method of their own. For example:
//
//	WithArgs("--hardfork", "cancun", "--code-size-limit", "50000")
//
// Build rejects --silent, --quiet and -q, which hide the output Start waits for.
// It also rejects --port and -p; use WithPort. Stop kills the process, so flags
// that act on a clean exit, such as --dump-state, don't take effect. Calling
// WithArgs again adds to the earlier flags.
func (b *Builder) WithArgs(args ...string) *Builder {
	b.extraArgs = append(b.extraArgs, args...)
	return b
}

// WithLogger sets the logger that receives anvil's output: stdout at Debug and
// stderr at Warn. A nil logger, the default, discards it. The handler runs on the
// goroutine that reads anvil's output, so it must not block, or Stop will hang.
func (b *Builder) WithLogger(logger *slog.Logger) *Builder {
	b.logger = logger
	return b
}

// WithStartupTimeout sets how long Start waits for anvil to become ready. A zero
// or negative value keeps DefaultStartupTimeout.
func (b *Builder) WithStartupTimeout(d time.Duration) *Builder {
	if d > 0 {
		b.startupTimeout = d
	}
	return b
}

// validate checks the builder configuration
func (b *Builder) validate() error {
	if b.port < 0 || b.port > 65535 {
		return fmt.Errorf("port %d is not in the range 0 to 65535", b.port)
	}
	if b.blockTime < 0 {
		return fmt.Errorf("block time %s is negative", b.blockTime)
	}
	if b.forkBlock != nil && b.forkURL == "" {
		return errors.New("WithForkBlockNumber requires WithFork")
	}
	for _, arg := range b.extraArgs {
		switch flag, _, _ := strings.Cut(arg, "="); flag {
		case "--silent", "--quiet", "-q":
			return fmt.Errorf("WithArgs: %s hides the output Start waits for", flag)
		case "--port", "-p":
			return fmt.Errorf("WithArgs: use WithPort instead of %s", flag)
		}
	}
	return nil
}

// renderArgs builds anvil's command line. Port 0 asks anvil for a free port.
func (b *Builder) renderArgs() []string {
	args := []string{"--port", strconv.Itoa(b.port)}
	if b.blockTime > 0 {
		args = append(args, "--block-time", strconv.FormatFloat(b.blockTime.Seconds(), 'f', -1, 64))
	}
	if b.chainID != nil {
		args = append(args, "--chain-id", strconv.FormatUint(*b.chainID, 10))
	}
	if b.gasLimit != nil {
		args = append(args, "--gas-limit", strconv.FormatUint(*b.gasLimit, 10))
	}
	if b.forkURL != "" {
		args = append(args, "--fork-url", b.forkURL)
	}
	if b.forkBlock != nil {
		args = append(args, "--fork-block-number", strconv.FormatUint(*b.forkBlock, 10))
	}
	return append(args, b.extraArgs...)
}

// Build returns an instance with the configured options, or an error wrapping
// ErrInvalidConfig. Call Start to run it.
func (b *Builder) Build() (*Anvil, error) {
	if err := b.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	timeout := b.startupTimeout
	if timeout <= 0 {
		timeout = DefaultStartupTimeout // e.g. a zero-value Builder
	}
	logger := b.logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored on Anvil.cancel and invoked in Stop()

	return &Anvil{
		context:        ctx,
		cancel:         cancel,
		args:           b.renderArgs(),
		startupTimeout: timeout,
		logger:         logger,
	}, nil
}
