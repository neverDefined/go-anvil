// Package anvil provides a Go interface for programmatic control of Anvil,
// Foundry's local Ethereum test node. It enables management of test environments,
// blockchain state manipulation, and control over time and accounts during development.
package anvil

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Common errors
var (
	// ErrNotStarted indicates the Anvil instance has not been started yet
	ErrNotStarted = fmt.Errorf("anvil: instance not started")

	// ErrAlreadyStarted indicates the Anvil instance is already running
	ErrAlreadyStarted = fmt.Errorf("anvil: instance already started")

	// ErrConnectionFailed indicates the connection to Anvil failed
	ErrConnectionFailed = fmt.Errorf("anvil: connection failed")

	// ErrProcessNotFound indicates the Anvil process could not be found
	ErrProcessNotFound = fmt.Errorf("anvil: process not found")

	// ErrInvalidConfig indicates the configuration is invalid
	ErrInvalidConfig = fmt.Errorf("anvil: invalid configuration")

	// ErrRPCCallFailed indicates an RPC call to Anvil failed
	ErrRPCCallFailed = fmt.Errorf("anvil: RPC call failed")

	// ErrAnvilNotFound indicates the anvil binary could not be located on PATH or at the Foundry fallback path
	ErrAnvilNotFound = fmt.Errorf("anvil: binary not found")

	// ErrAnvilNotExecutable indicates the anvil binary was located but is not executable
	ErrAnvilNotExecutable = fmt.Errorf("anvil: binary not executable")

	// ErrStartupTimeout indicates anvil did not become ready within the configured startup timeout
	ErrStartupTimeout = fmt.Errorf("anvil: startup timeout exceeded")

	// ErrProcessExited indicates the anvil process exited before it became ready,
	// for example because of an invalid flag or a port that is already in use.
	// The error message includes the exit status and anvil's last stderr lines.
	ErrProcessExited = fmt.Errorf("anvil: process exited during startup")
)

// AnvilConfig holds the configuration for Anvil client
type AnvilConfig struct {
	DefaultTimeout time.Duration
	StartupSleep   time.Duration
	DefaultRPCURL  string
	DefaultWSURL   string
	MaxRetries     int
	RetryInterval  time.Duration
	LogLevel       zerolog.Level
}

// DefaultConfig provides default configuration values for Anvil client
var DefaultConfig = AnvilConfig{
	DefaultTimeout: 10 * time.Second,
	StartupSleep:   time.Second / 3,
	DefaultRPCURL:  "http://127.0.0.1:8545",
	DefaultWSURL:   "ws://127.0.0.1:8545",
	MaxRetries:     5,
	RetryInterval:  100 * time.Millisecond,
	LogLevel:       zerolog.InfoLevel,
}

// DefaultStartupTimeout is the default ceiling for waiting on anvil to become ready.
// The readiness probe polls on a short interval and returns on the first successful RPC,
// so typical startup completes well before this deadline.
const DefaultStartupTimeout = 5 * time.Second

// AnvilMetrics contains runtime metrics for the Anvil instance
type AnvilMetrics struct {
	StartupTime   time.Duration
	BlocksMined   uint64
	RPCCalls      int64
	LastError     error
	LastErrorTime time.Time
}

// AnvilPrivateKey represents the default test account private keys
type AnvilPrivateKey string

// AnvilPrivateKeys are the default Anvil private keys for testing.
var AnvilPrivateKeys = [...]AnvilPrivateKey{
	"ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
	"59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
	"5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a",
}

// lifecycle is the state of an Anvil instance. Instances are single-use:
// new -> running -> stopped.
type lifecycle int

const (
	stateNew lifecycle = iota
	stateRunning
	stateStopped
)

// Anvil represents a local Ethereum test environment
type Anvil struct {
	context        context.Context
	cancel         context.CancelFunc
	args           []string
	startupTimeout time.Duration
	logger         zerolog.Logger

	// mu guards the lifecycle fields below. RPC calls hold it only long enough
	// to read the client pointers.
	mu          sync.RWMutex
	state       lifecycle
	cmd         *exec.Cmd
	exited      chan struct{} // closed once the process has exited and been reaped
	rpcURL      string
	client      *ethclient.Client
	rpcClient   *rpc.Client
	startupTime time.Duration

	blocksMined atomic.Uint64
	rpcCalls    atomic.Int64

	// baseline is the snapshot ResetState reverts to: the state right after
	// Start, or after the last ResetFork. Empty if a reset failed partway.
	// Start sets it under mu before the instance is running; after that it is
	// guarded by baselineMu.
	baselineMu sync.Mutex
	baseline   string
}

// NewAnvil creates a new Anvil instance with default configuration.
// Anvil picks a free port when started; URL reports the address.
// Returns an error if the configuration is invalid.
func NewAnvil() (*Anvil, error) {
	return NewAnvilBuilder().Build()
}

// Start launches the anvil process and waits until its RPC endpoint answers.
//
// Unless WithPort was used, anvil picks a free port itself, so instances in
// parallel tests or test binaries never collide; URL reports the address.
// Subprocess stdout and stderr go to the instance logger.
//
// If the process exits during startup (an invalid flag, a port already in use),
// Start returns ErrProcessExited right away, with anvil's stderr in the message.
// If anvil is not ready within the startup timeout, Start returns
// ErrStartupTimeout. In both cases the process is stopped.
//
// An instance can be started only once; a second call returns ErrAlreadyStarted.
func (a *Anvil) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch a.state {
	case stateRunning:
		return ErrAlreadyStarted
	case stateStopped:
		return fmt.Errorf("%w: the instance was stopped and cannot be restarted", ErrAlreadyStarted)
	}

	startTime := time.Now()

	anvilPath, err := resolveAnvilPath()
	if err != nil {
		return err
	}

	listening := make(chan string, 1)
	stderrTail := &tailBuffer{max: 10}

	stdout := &lineWriter{fn: func(line string) {
		a.logger.Debug().Str("stream", "stdout").Msg(line)
		if addr, ok := strings.CutPrefix(line, "Listening on "); ok {
			select {
			case listening <- strings.TrimSpace(addr):
			default:
			}
		}
	}}
	stderr := &lineWriter{fn: func(line string) {
		a.logger.Warn().Str("stream", "stderr").Msg(line)
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
		if stopErr := a.shutdownLocked(); stopErr != nil {
			a.logger.Error().Err(stopErr).Msg("Failed to stop anvil after failed startup")
		}
		return err
	}

	a.state = stateRunning
	a.startupTime = time.Since(startTime)
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

// clients returns the RPC and Ethereum clients if the instance is running,
// and ErrNotStarted otherwise.
func (a *Anvil) clients() (*rpc.Client, *ethclient.Client, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch a.state {
	case stateRunning:
		return a.rpcClient, a.client, nil
	case stateStopped:
		return nil, nil, fmt.Errorf("%w: the instance is stopped", ErrNotStarted)
	default:
		return nil, nil, ErrNotStarted
	}
}

// call forwards a JSON-RPC call to anvil. It returns ErrNotStarted if the
// instance is not running.
func (a *Anvil) call(ctx context.Context, result any, method string, args ...any) error {
	c, _, err := a.clients()
	if err != nil {
		return err
	}
	a.rpcCalls.Add(1)
	return c.CallContext(ctx, result, method, args...)
}

// retry attempts an operation with exponential backoff
func retry(attempts int, sleep time.Duration, f func() error) error {
	if err := f(); err != nil {
		if attempts--; attempts > 0 {
			time.Sleep(sleep)
			return retry(attempts, sleep*2, f)
		}
		return err
	}
	return nil
}

// Client returns the Ethereum client for interacting with the blockchain.
// The client can be used to query blocks, send transactions, and interact with contracts.
// It is nil before Start and after Stop.
func (a *Anvil) Client() *ethclient.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

// RPCClient returns the raw RPC client for making direct JSON-RPC calls.
// This is useful for calling Anvil-specific methods that aren't available in the standard Ethereum client.
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

// Accounts returns the default test accounts with their private keys and addresses.
// Anvil provides pre-funded test accounts that can be used for testing.
// Returns an error if any private key cannot be parsed.
func (a *Anvil) Accounts() ([len(AnvilPrivateKeys)]*ecdsa.PrivateKey, [len(AnvilPrivateKeys)]common.Address, error) {
	var (
		keys     [len(AnvilPrivateKeys)]*ecdsa.PrivateKey
		accounts [len(AnvilPrivateKeys)]common.Address
	)

	for i, keyStr := range AnvilPrivateKeys {
		key, err := crypto.HexToECDSA(string(keyStr))
		if err != nil {
			a.logger.Error().Err(err).Int("index", i).Msg("Failed to convert private key")
			return keys, accounts, fmt.Errorf("failed to convert private key at index %d: %w", i, err)
		}
		keys[i] = key
		accounts[i] = crypto.PubkeyToAddress(key.PublicKey)
	}

	return keys, accounts, nil
}

// MineBlock triggers the immediate mining of a new block.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) MineBlock(ctx context.Context) error {
	a.blocksMined.Add(1)
	err := a.call(ctx, nil, "evm_mine")
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to mine block")
	}
	return err
}

// SetNextBlockTimestamp sets the timestamp for the next block to be mined.
// This only affects the next block; subsequent blocks will use normal timestamps.
// The timestamp is in Unix seconds.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) SetNextBlockTimestamp(ctx context.Context, timestamp int64) error {
	err := a.call(ctx, nil, "evm_setNextBlockTimestamp", timestamp)
	if err != nil {
		a.logger.Error().Err(err).Int64("timestamp", timestamp).Msg("Failed to set next block timestamp")
	}
	return err
}

// IncreaseTime increases the current block time by the specified number of seconds.
// This affects all future blocks and is useful for testing time-dependent contracts.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) IncreaseTime(ctx context.Context, seconds int64) error {
	err := a.call(ctx, nil, "evm_increaseTime", seconds)
	if err != nil {
		a.logger.Error().Err(err).Int64("seconds", seconds).Msg("Failed to increase time")
	}
	return err
}

// SetBalance sets the balance of an account to the specified amount in Wei.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) SetBalance(ctx context.Context, address common.Address, balance *big.Int) error {
	err := a.call(ctx, nil, "anvil_setBalance", address, balance.String())
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Str("balance", balance.String()).Msg("Failed to set balance")
	}
	return err
}

// Impersonate enables impersonating an account, allowing transactions to be sent
// from that address without needing the private key.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) Impersonate(ctx context.Context, address common.Address) error {
	err := a.call(ctx, nil, "anvil_impersonateAccount", address)
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Msg("Failed to impersonate account")
	}
	return err
}

// StopImpersonating stops impersonating an account that was previously impersonated.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) StopImpersonating(ctx context.Context, address common.Address) error {
	err := a.call(ctx, nil, "anvil_stopImpersonatingAccount", address)
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Msg("Failed to stop impersonating account")
	}
	return err
}

// Snapshot creates a snapshot of the current blockchain state and returns a snapshot ID.
// The snapshot can be reverted to using the Revert method.
// Returns the snapshot ID string and an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) Snapshot(ctx context.Context) (string, error) {
	var snapshotID string
	err := a.call(ctx, &snapshotID, "evm_snapshot")
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to create snapshot")
	}
	return snapshotID, err
}

// Revert reverts the blockchain state to a previously created snapshot.
// The snapshot ID should be obtained from a previous Snapshot() call.
// Returns true if the revert was successful and an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) Revert(ctx context.Context, snapshotID string) (bool, error) {
	var success bool
	err := a.call(ctx, &success, "evm_revert", snapshotID)
	if err != nil {
		a.logger.Error().Err(err).Str("snapshotID", snapshotID).Msg("Failed to revert to snapshot")
	}
	return success, err
}

// SetCode sets the bytecode at a given address.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) SetCode(ctx context.Context, address common.Address, code string) error {
	err := a.call(ctx, nil, "anvil_setCode", address, code)
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Msg("Failed to set code")
	}
	return err
}

// SetStorageAt sets the storage value at a specific slot for an address.
// The slot and value should be 32-byte hex strings with 0x prefix.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) SetStorageAt(ctx context.Context, address common.Address, slot string, value string) error {
	err := a.call(ctx, nil, "anvil_setStorageAt", address, slot, value)
	if err != nil {
		a.logger.Error().Err(err).
			Str("address", address.Hex()).
			Str("slot", slot).
			Str("value", value).
			Msg("Failed to set storage")
	}
	return err
}

// SetNonce sets the nonce for a given address.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) SetNonce(ctx context.Context, address common.Address, nonce uint64) error {
	err := a.call(ctx, nil, "anvil_setNonce", address, fmt.Sprintf("0x%x", nonce))
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Uint64("nonce", nonce).Msg("Failed to set nonce")
	}
	return err
}

// AccountResult is an EIP-1186 account proof with its storage proofs, as
// returned by eth_getProof.
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

// GetProof returns the EIP-1186 account and storage proofs for the given address.
// The storageKeys are hex-encoded storage slot keys; pass nil for an account-only proof.
// A nil blockNumber means the latest block; pin a specific block when the proof must
// be checked against that block's state root. Use VerifyProof to check the result
// against a header's state root.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) GetProof(ctx context.Context, address common.Address, storageKeys []string, blockNumber *big.Int) (*AccountResult, error) {
	if storageKeys == nil {
		storageKeys = []string{}
	}
	var result AccountResult
	if err := a.call(ctx, &result, "eth_getProof", address, storageKeys, toBlockNumArg(blockNumber)); err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Msg("Failed to get proof")
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

// Mine mines numBlocks blocks at once.
// interval is the number of seconds between block timestamps: the first block is
// interval seconds after the node's current time and each later block interval
// seconds after the one before it. With 0, anvil uses its normal timestamps.
// It is not an absolute time; to mine at a specific timestamp, call
// SetNextBlockTimestamp and then MineBlock.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) Mine(ctx context.Context, numBlocks uint64, interval uint64) error {
	a.blocksMined.Add(numBlocks)

	var err error
	if interval != 0 {
		err = a.call(ctx, nil, "anvil_mine", fmt.Sprintf("0x%x", numBlocks), fmt.Sprintf("0x%x", interval))
	} else {
		err = a.call(ctx, nil, "anvil_mine", fmt.Sprintf("0x%x", numBlocks))
	}

	if err != nil {
		a.logger.Error().Err(err).Uint64("numBlocks", numBlocks).Msg("Failed to mine blocks")
	}
	return err
}

// DropTransaction removes a transaction from the memory pool.
// The transaction hash should be a hex string with 0x prefix.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) DropTransaction(ctx context.Context, txHash string) error {
	err := a.call(ctx, nil, "anvil_dropTransaction", txHash)
	if err != nil {
		a.logger.Error().Err(err).Str("txHash", txHash).Msg("Failed to drop transaction")
	}
	return err
}

// SetAutomine enables or disables automatic mining of blocks.
// When disabled, blocks must be mined manually using MineBlock or Mine.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) SetAutomine(ctx context.Context, enabled bool) error {
	err := a.call(ctx, nil, "evm_setAutomine", enabled)
	if err != nil {
		a.logger.Error().Err(err).Bool("enabled", enabled).Msg("Failed to set automine")
	}
	return err
}

// SetIntervalMining sets the mining behavior to interval mining with the specified interval in seconds.
// Set to 0 to disable interval mining.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) SetIntervalMining(ctx context.Context, seconds uint64) error {
	err := a.call(ctx, nil, "evm_setIntervalMining", seconds)
	if err != nil {
		a.logger.Error().Err(err).Uint64("seconds", seconds).Msg("Failed to set interval mining")
	}
	return err
}

// AutoImpersonate enables or disables automatic impersonation for all transactions.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) AutoImpersonate(ctx context.Context, enabled bool) error {
	err := a.call(ctx, nil, "anvil_autoImpersonateAccount", enabled)
	if err != nil {
		a.logger.Error().Err(err).Bool("enabled", enabled).Msg("Failed to set auto impersonate")
	}
	return err
}

// ResetFork resets the fork to a fresh state, optionally at a new block number.
// If blockNumber is 0, resets to the original fork block or latest block.
// anvil discards every snapshot on reset, so afterwards ResetState returns to
// the freshly reset state rather than the state after Start.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) ResetFork(ctx context.Context, forkURL string, blockNumber uint64) error {
	var err error
	if blockNumber != 0 {
		err = a.call(ctx, nil, "anvil_reset", map[string]any{
			"forking": map[string]any{
				"jsonRpcUrl":  forkURL,
				"blockNumber": fmt.Sprintf("0x%x", blockNumber),
			},
		})
	} else {
		err = a.call(ctx, nil, "anvil_reset", map[string]any{
			"forking": map[string]any{
				"jsonRpcUrl": forkURL,
			},
		})
	}

	if err != nil {
		a.logger.Error().Err(err).Str("forkURL", forkURL).Msg("Failed to reset fork")
		return err
	}

	a.baselineMu.Lock()
	defer a.baselineMu.Unlock()
	return a.retakeBaselineLocked(ctx)
}

// Close stops the anvil process and closes the RPC clients. It is the same as
// Stop: safe to call more than once, and on an instance that was never started.
// Typically deferred or registered with t.Cleanup.
func (a *Anvil) Close() error {
	return a.Stop()
}

// Stop kills the anvil process, waits for it to exit and closes the RPC clients.
// Later calls on the instance return ErrNotStarted. It is safe to call more than
// once. Returns an error only if the process could not be killed.
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

// Metrics returns current metrics for the Anvil instance including startup time,
// blocks mined, RPC calls made, and any errors encountered. The metrics are thread-safe
// and can be queried at any time during the instance's lifetime.
func (a *Anvil) Metrics() AnvilMetrics {
	a.mu.RLock()
	startupTime := a.startupTime
	a.mu.RUnlock()
	return AnvilMetrics{
		StartupTime: startupTime,
		BlocksMined: a.blocksMined.Load(),
		RPCCalls:    a.rpcCalls.Load(),
	}
}

// ResetState reverts the chain to the state it had right after Start (or after
// the last ResetFork), discarding every block, transaction and state change since.
// It uses evm_snapshot/evm_revert, which is much faster than restarting the process.
// Node settings that are not chain state, such as automine or impersonation,
// are not reset.
//
// Concurrent calls are serialized. Resetting does not isolate tests that run in
// parallel against the same instance; give those their own instances.
// Returns an error if the RPC call fails or if ctx is canceled.
func (a *Anvil) ResetState(ctx context.Context) error {
	// Checking the state first also orders this read of baseline after Start's write.
	if _, _, err := a.clients(); err != nil {
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

// WaitForBlock waits for the blockchain to reach a specific block number.
// It polls every 100ms until the target block is reached or the timeout expires.
// Returns an error if the timeout is exceeded or if querying the block number fails.
func (a *Anvil) WaitForBlock(number uint64, timeout time.Duration) error {
	_, client, err := a.clients()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(a.context, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			current, err := client.BlockNumber(ctx)
			if err != nil {
				a.logger.Error().Err(err).Msg("Failed to get block number")
				return err
			}
			if current >= number {
				return nil
			}
			a.logger.Debug().
				Uint64("current", current).
				Uint64("target", number).
				Msg("Waiting for block")
		}
	}
}

// AnvilBuilder helps construct Anvil instances using the builder pattern.
// It allows for flexible configuration of Anvil options before creating the instance.
type AnvilBuilder struct {
	args           []string
	port           string // empty: anvil picks a free port
	startupTimeout time.Duration
	logLevel       zerolog.Level
}

// NewAnvilBuilder creates a new AnvilBuilder with default configuration.
// Use the With* methods to customize the configuration before calling Build().
func NewAnvilBuilder() *AnvilBuilder {
	return &AnvilBuilder{
		startupTimeout: DefaultStartupTimeout,
		logLevel:       DefaultConfig.LogLevel,
	}
}

// WithBlockTime sets the block time in seconds for automatic block mining.
// Set to "0" to disable automatic mining. Returns the builder for method chaining.
func (b *AnvilBuilder) WithBlockTime(blockTime string) *AnvilBuilder {
	b.args = append(b.args, "--block-time", blockTime)
	return b
}

// WithFork configures Anvil to fork from a remote Ethereum node at the latest block.
// Provide the RPC URL of the node to fork from. Returns the builder for method chaining.
func (b *AnvilBuilder) WithFork(fork string) *AnvilBuilder {
	b.args = append(b.args, "--fork-url", fork)
	return b
}

// WithForkBlockNumber sets the block number to fork from when using WithFork.
// If not specified, Anvil will fork from the latest block. Returns the builder for method chaining.
func (b *AnvilBuilder) WithForkBlockNumber(blockNumber string) *AnvilBuilder {
	b.args = append(b.args, "--fork-block-number", blockNumber)
	return b
}

// WithPort sets a fixed RPC port for the Anvil instance. By default anvil picks
// a free port, which keeps parallel tests and test binaries from colliding; use
// URL to find it. With a fixed port, Start returns ErrProcessExited if the port
// is already in use. Returns the builder for method chaining.
func (b *AnvilBuilder) WithPort(port string) *AnvilBuilder {
	b.port = port
	return b
}

// WithChainID sets the chain ID for the Anvil instance.
// Default is 31337. Returns the builder for method chaining.
func (b *AnvilBuilder) WithChainID(chainID string) *AnvilBuilder {
	b.args = append(b.args, "--chain-id", chainID)
	return b
}

// WithGasLimit sets the block gas limit for the Anvil instance.
// Returns the builder for method chaining.
func (b *AnvilBuilder) WithGasLimit(limit string) *AnvilBuilder {
	b.args = append(b.args, "--gas-limit", limit)
	return b
}

// WithGasPrice sets the gas price for the Anvil instance in Wei.
// Returns the builder for method chaining.
func (b *AnvilBuilder) WithGasPrice(price string) *AnvilBuilder {
	b.args = append(b.args, "--gas-price", price)
	return b
}

// WithLogLevel sets the logging level for the Anvil client.
// Use zerolog.Disabled to silence all logs. Returns the builder for method chaining.
func (b *AnvilBuilder) WithLogLevel(level zerolog.Level) *AnvilBuilder {
	b.logLevel = level
	return b
}

// WithStartupTimeout sets the ceiling for waiting on anvil to become ready.
// Start returns as soon as anvil is listening and answers RPC, which typically
// takes tens of milliseconds, so the timeout only matters when something is wrong.
// A zero or negative value leaves the default (DefaultStartupTimeout).
// Returns the builder for method chaining.
func (b *AnvilBuilder) WithStartupTimeout(d time.Duration) *AnvilBuilder {
	if d > 0 {
		b.startupTimeout = d
	}
	return b
}

// validate checks the builder configuration
func (b *AnvilBuilder) validate() error {
	if b.port == "" {
		return nil
	}
	if n, err := strconv.Atoi(b.port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("port %q is not a number from 0 to 65535", b.port)
	}
	return nil
}

// Build creates an Anvil instance with the configured options.
// Returns an error wrapping ErrInvalidConfig if the configuration is invalid.
// The instance must be started with Start() before it can be used.
func (b *AnvilBuilder) Build() (*Anvil, error) {
	if err := b.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	port := b.port
	if port == "" {
		port = "0" // anvil binds a free port and prints it on startup
	}
	timeout := b.startupTimeout
	if timeout <= 0 {
		timeout = DefaultStartupTimeout // e.g. a zero-value AnvilBuilder
	}
	args := append(slices.Clone(b.args), "--port", port)

	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored on Anvil.cancel and invoked in Stop()
	logger := log.With().Str("component", "anvil").Logger().Level(b.logLevel)

	return &Anvil{
		context:        ctx,
		cancel:         cancel,
		args:           args,
		startupTimeout: timeout,
		logger:         logger,
	}, nil
}
