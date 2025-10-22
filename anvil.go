// Package anvil provides a Go interface for programmatic control of Anvil,
// Foundry's local Ethereum test node. It enables management of test environments,
// blockchain state manipulation, and control over time and accounts during development.
package anvil

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
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
)

// AnvilConfig holds the configuration for Anvil client
//
//nolint:revive // Name is intentionally prefixed for clarity
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

// AnvilMetrics contains runtime metrics for the Anvil instance
//
//nolint:revive // Name is intentionally prefixed for clarity
type AnvilMetrics struct {
	StartupTime   time.Duration
	BlocksMined   uint64
	RPCCalls      int64
	LastError     error
	LastErrorTime time.Time
}

// AnvilPrivateKey represents the default test account private keys
//
//nolint:revive // Name is intentionally prefixed for clarity
type AnvilPrivateKey string

// AnvilPrivateKeys are the default Anvil private keys for testing.
var AnvilPrivateKeys = [...]AnvilPrivateKey{
	"ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
	"59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
	"5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a",
}

// EthereumTestEnvironment defines the interface for Ethereum test environments
type EthereumTestEnvironment interface {
	Start(timeout time.Duration) error
	Stop() error
	Client() *ethclient.Client
	RPCClient() *rpc.Client
	MineBlock() error
	SetNextBlockTimestamp(timestamp int64) error
	IncreaseTime(seconds int64) error
	SetBalance(address common.Address, balance *big.Int) error
	Impersonate(address common.Address) error
	StopImpersonating(address common.Address) error
}

// Anvil represents a local Ethereum test environment
type Anvil struct {
	context     context.Context
	client      *ethclient.Client
	rpcClient   *rpc.Client
	cmd         *exec.Cmd
	cancel      context.CancelFunc
	args        []string
	rpcURL      string
	logger      zerolog.Logger
	metrics     AnvilMetrics
	blocksMined atomic.Uint64
	rpcCalls    atomic.Int64
	closeOnce   sync.Once
	stopOnce    sync.Once
}

// NewAnvil creates a new Anvil instance with default configuration.
// It uses the default RPC URL (http://127.0.0.1:8545) and default settings.
// Returns an error if the configuration is invalid.
func NewAnvil() (*Anvil, error) {
	return NewAnvilBuilder().Build()
}

// Start initializes and starts the Anvil process.
// It locates the Anvil binary, starts the process, and establishes connections
// to both the RPC and Ethereum clients. The method will retry connection attempts
// up to 5 times with exponential backoff. If the connection fails after all retries,
// the Anvil process will be stopped automatically.
// Returns an error if the process fails to start or if connections cannot be established.
func (a *Anvil) Start() error {
	startTime := time.Now()

	// Look for anvil in PATH first, then fall back to standard Foundry location
	anvilPath, err := exec.LookPath("anvil")
	if err != nil {
		// Fallback: try the standard Foundry location
		homeDir := os.Getenv("XDG_CONFIG_HOME")
		if homeDir == "" {
			homeDir, err = os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
		}
		anvilPath = filepath.Join(homeDir, ".foundry", "bin", "anvil")
	}

	a.cmd = exec.CommandContext(a.context, anvilPath, a.args...) //nolint:gosec // anvilPath is validated

	// Capture stdout and stderr
	a.cmd.Stdout = os.Stdout
	a.cmd.Stderr = os.Stderr

	if err := a.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start anvil: %w", err)
	}

	// Give Anvil time to initialize
	time.Sleep(2 * time.Second)

	// Try to establish connection with retries
	err = retry(5, time.Second, func() error {
		if err := a.connect(); err != nil {
			a.logger.Debug().Err(err).Msg("Failed to connect, retrying...")
			return err
		}
		return nil
	})
	if err != nil {
		// Stop the Anvil process if connection failed.
		if err := a.Stop(); err != nil {
			return fmt.Errorf("failed to stop Anvil: %w", err)
		}

		// Return the error after stopping the process
		return fmt.Errorf("failed to establish connection: %w", err)
	}

	a.metrics.StartupTime = time.Since(startTime)
	return nil
}

// connect establishes connection to the RPC endpoint
func (a *Anvil) connect() error {
	var err error

	// Connect to RPC
	if a.rpcClient, err = rpc.Dial(a.rpcURL); err != nil {
		return fmt.Errorf("failed to connect to RPC: %w", err)
	}

	// Connect to eth client
	if a.client, err = ethclient.Dial(a.rpcURL); err != nil {
		a.rpcClient.Close()
		return fmt.Errorf("failed to connect to eth client: %w", err)
	}

	// Verify connection with a test call
	ctx, cancel := context.WithTimeout(a.context, time.Second*2)
	defer cancel()

	var block string
	if err := a.rpcClient.CallContext(ctx, &block, "eth_blockNumber"); err != nil {
		a.client.Close()
		a.rpcClient.Close()
		return fmt.Errorf("failed to get block number: %w", err)
	}

	return nil
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
func (a *Anvil) Client() *ethclient.Client {
	return a.client
}

// RPCClient returns the raw RPC client for making direct JSON-RPC calls.
// This is useful for calling Anvil-specific methods that aren't available in the standard Ethereum client.
func (a *Anvil) RPCClient() *rpc.Client {
	return a.rpcClient
}

// Accounts returns the default test accounts with their private keys and addresses.
// Anvil provides 10 pre-funded test accounts that can be used for testing.
// Each account starts with 10,000 ETH. Returns an error if any private key cannot be parsed.
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
// This is useful in tests when you need to advance the blockchain state.
// Returns an error if the RPC call fails.
func (a *Anvil) MineBlock() error {
	a.blocksMined.Add(1)
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "evm_mine")
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to mine block")
	}
	return err
}

// SetNextBlockTimestamp sets the timestamp for the next block to be mined.
// This only affects the next block; subsequent blocks will use normal timestamps.
// The timestamp is in Unix seconds. Returns an error if the RPC call fails.
func (a *Anvil) SetNextBlockTimestamp(timestamp int64) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "evm_setNextBlockTimestamp", timestamp)
	if err != nil {
		a.logger.Error().Err(err).Int64("timestamp", timestamp).Msg("Failed to set next block timestamp")
	}
	return err
}

// IncreaseTime increases the current block time by the specified number of seconds.
// This affects all future blocks and is useful for testing time-dependent contracts.
// Returns an error if the RPC call fails.
func (a *Anvil) IncreaseTime(seconds int64) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "evm_increaseTime", seconds)
	if err != nil {
		a.logger.Error().Err(err).Int64("seconds", seconds).Msg("Failed to increase time")
	}
	return err
}

// SetBalance sets the balance of an account to the specified amount in Wei.
// This is useful for testing scenarios that require specific account balances.
// Returns an error if the RPC call fails.
func (a *Anvil) SetBalance(address common.Address, balance *big.Int) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_setBalance", address, balance.String())
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Str("balance", balance.String()).Msg("Failed to set balance")
	}
	return err
}

// Impersonate enables impersonating an account, allowing transactions to be sent
// from that address without needing the private key. This is useful for testing
// interactions with existing contracts or accounts. Returns an error if the RPC call fails.
func (a *Anvil) Impersonate(address common.Address) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_impersonateAccount", address)
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Msg("Failed to impersonate account")
	}
	return err
}

// StopImpersonating stops impersonating an account that was previously impersonated.
// After calling this, transactions from the address will require a valid signature again.
// Returns an error if the RPC call fails.
func (a *Anvil) StopImpersonating(address common.Address) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_stopImpersonatingAccount", address)
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Msg("Failed to stop impersonating account")
	}
	return err
}

// Snapshot creates a snapshot of the current blockchain state and returns a snapshot ID.
// The snapshot can be reverted to using the Revert method. This is useful for testing
// scenarios where you need to test multiple paths from the same state.
// Returns the snapshot ID string and an error if the RPC call fails.
func (a *Anvil) Snapshot() (string, error) {
	a.rpcCalls.Add(1)
	var snapshotID string
	err := a.rpcClient.Call(&snapshotID, "evm_snapshot")
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to create snapshot")
	}
	return snapshotID, err
}

// Revert reverts the blockchain state to a previously created snapshot.
// The snapshot ID should be obtained from a previous Snapshot() call.
// Returns true if the revert was successful, false otherwise, and an error if the RPC call fails.
func (a *Anvil) Revert(snapshotID string) (bool, error) {
	a.rpcCalls.Add(1)
	var success bool
	err := a.rpcClient.Call(&success, "evm_revert", snapshotID)
	if err != nil {
		a.logger.Error().Err(err).Str("snapshotID", snapshotID).Msg("Failed to revert to snapshot")
	}
	return success, err
}

// SetCode sets the bytecode at a given address.
// This is useful for deploying contracts at specific addresses or modifying existing contract code.
// Returns an error if the RPC call fails.
func (a *Anvil) SetCode(address common.Address, code string) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_setCode", address, code)
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Msg("Failed to set code")
	}
	return err
}

// SetStorageAt sets the storage value at a specific slot for an address.
// The slot and value should be 32-byte hex strings with 0x prefix.
// Returns an error if the RPC call fails.
func (a *Anvil) SetStorageAt(address common.Address, slot string, value string) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_setStorageAt", address, slot, value)
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
// This is useful for testing scenarios that require specific nonce values.
// Returns an error if the RPC call fails.
func (a *Anvil) SetNonce(address common.Address, nonce uint64) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_setNonce", address, fmt.Sprintf("0x%x", nonce))
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Uint64("nonce", nonce).Msg("Failed to set nonce")
	}
	return err
}

// Mine mines multiple blocks at once.
// The numBlocks parameter specifies how many blocks to mine.
// If timestamp is 0, blocks will use incremental timestamps. Otherwise, the first block
// will use the specified timestamp and subsequent blocks will increment from there.
// Returns an error if the RPC call fails.
func (a *Anvil) Mine(numBlocks uint64, timestamp uint64) error {
	a.rpcCalls.Add(1)
	a.blocksMined.Add(numBlocks)

	var err error
	if timestamp != 0 {
		err = a.rpcClient.Call(nil, "anvil_mine", fmt.Sprintf("0x%x", numBlocks), fmt.Sprintf("0x%x", timestamp))
	} else {
		err = a.rpcClient.Call(nil, "anvil_mine", fmt.Sprintf("0x%x", numBlocks))
	}

	if err != nil {
		a.logger.Error().Err(err).Uint64("numBlocks", numBlocks).Msg("Failed to mine blocks")
	}
	return err
}

// DropTransaction removes a transaction from the memory pool.
// The transaction hash should be a hex string with 0x prefix.
// Returns an error if the RPC call fails or if the transaction is not found.
func (a *Anvil) DropTransaction(txHash string) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_dropTransaction", txHash)
	if err != nil {
		a.logger.Error().Err(err).Str("txHash", txHash).Msg("Failed to drop transaction")
	}
	return err
}

// SetAutomine enables or disables automatic mining of blocks.
// When disabled, blocks must be mined manually using MineBlock() or Mine().
// Returns an error if the RPC call fails.
func (a *Anvil) SetAutomine(enabled bool) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "evm_setAutomine", enabled)
	if err != nil {
		a.logger.Error().Err(err).Bool("enabled", enabled).Msg("Failed to set automine")
	}
	return err
}

// SetIntervalMining sets the mining behavior to interval mining with the specified interval in seconds.
// Set to 0 to disable interval mining. When enabled, blocks are mined automatically at the specified interval.
// Returns an error if the RPC call fails.
func (a *Anvil) SetIntervalMining(seconds uint64) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "evm_setIntervalMining", seconds)
	if err != nil {
		a.logger.Error().Err(err).Uint64("seconds", seconds).Msg("Failed to set interval mining")
	}
	return err
}

// AutoImpersonate enables or disables automatic impersonation for all transactions.
// When enabled, all transactions are automatically impersonated without needing to call Impersonate.
// Returns an error if the RPC call fails.
func (a *Anvil) AutoImpersonate(enabled bool) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_autoImpersonateAccount", enabled)
	if err != nil {
		a.logger.Error().Err(err).Bool("enabled", enabled).Msg("Failed to set auto impersonate")
	}
	return err
}

// ResetFork resets the fork to a fresh state, optionally at a new block number.
// If blockNumber is 0, resets to the original fork block or latest block.
// Returns an error if the RPC call fails or if not in forked mode.
func (a *Anvil) ResetFork(forkURL string, blockNumber uint64) error {
	a.rpcCalls.Add(1)

	var err error
	if blockNumber != 0 {
		err = a.rpcClient.Call(nil, "anvil_reset", map[string]any{
			"forking": map[string]any{
				"jsonRpcUrl":  forkURL,
				"blockNumber": fmt.Sprintf("0x%x", blockNumber),
			},
		})
	} else {
		err = a.rpcClient.Call(nil, "anvil_reset", map[string]any{
			"forking": map[string]any{
				"jsonRpcUrl": forkURL,
			},
		})
	}

	if err != nil {
		a.logger.Error().Err(err).Str("forkURL", forkURL).Msg("Failed to reset fork")
	}
	return err
}

// Close performs a clean shutdown of all resources including clients and the Anvil process.
// It's safe to call Close multiple times. Should be called when done with the Anvil instance,
// typically in a defer statement. Returns an error if the process cannot be stopped.
func (a *Anvil) Close() error {
	var err error
	a.closeOnce.Do(func() {
		a.logger.Debug().Msg("Shutting down Anvil instance")

		if a.client != nil {
			a.client.Close()
		}

		if a.rpcClient != nil {
			a.rpcClient.Close()
		}

		if stopErr := a.Stop(); stopErr != nil {
			a.logger.Error().Err(stopErr).Msg("Error stopping Anvil")
			err = stopErr
		}

		// Wait for process to fully terminate
		time.Sleep(time.Second)
	})
	return err
}

// Stop terminates the Anvil process and closes all client connections.
// This method forcefully kills the process. Use Close() for a cleaner shutdown.
// Returns an error if the process cannot be killed.
func (a *Anvil) Stop() error {
	var err error
	a.stopOnce.Do(func() {
		if a.client != nil {
			a.client.Close()
			a.client = nil
		}

		if a.rpcClient != nil {
			a.rpcClient.Close()
			a.rpcClient = nil
		}

		if a.cmd != nil && a.cmd.Process != nil {
			if killErr := a.cmd.Process.Kill(); killErr != nil {
				err = fmt.Errorf("failed to kill anvil process: %w", killErr)
				return
			}
			_ = a.cmd.Wait()
			a.cmd = nil
		}
	})
	return err
}

// Metrics returns current metrics for the Anvil instance including startup time,
// blocks mined, RPC calls made, and any errors encountered. The metrics are thread-safe
// and can be queried at any time during the instance's lifetime.
func (a *Anvil) Metrics() AnvilMetrics {
	return AnvilMetrics{
		StartupTime:   a.metrics.StartupTime,
		BlocksMined:   a.blocksMined.Load(),
		RPCCalls:      a.rpcCalls.Load(),
		LastError:     a.metrics.LastError,
		LastErrorTime: a.metrics.LastErrorTime,
	}
}

// ResetState resets the Anvil blockchain state to its initial values without restarting the process.
// This is much faster than stopping and starting the instance. All balances, contracts,
// and state will be reset to initial values, but the Anvil process continues running.
// Returns an error if the RPC call fails.
func (a *Anvil) ResetState() error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_reset")
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to reset state")
	}
	return err
}

// WaitForBlock waits for the blockchain to reach a specific block number.
// It polls every 100ms until the target block is reached or the timeout expires.
// Returns an error if the timeout is exceeded or if querying the block number fails.
func (a *Anvil) WaitForBlock(number uint64, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(a.context, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			current, err := a.client.BlockNumber(ctx)
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
//
//nolint:revive // Name is intentionally prefixed for clarity
type AnvilBuilder struct {
	args     []string
	rpcURL   string
	logLevel zerolog.Level
}

// NewAnvilBuilder creates a new AnvilBuilder with default configuration.
// Use the With* methods to customize the configuration before calling Build().
func NewAnvilBuilder() *AnvilBuilder {
	return &AnvilBuilder{
		args:     make([]string, 0),
		rpcURL:   DefaultConfig.DefaultRPCURL,
		logLevel: DefaultConfig.LogLevel,
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

// WithPort sets the RPC port for the Anvil instance.
// Default is 8545. Use different ports when running multiple instances.
// Returns the builder for method chaining.
func (b *AnvilBuilder) WithPort(port string) *AnvilBuilder {
	b.args = append(b.args, "--port", port)
	b.rpcURL = fmt.Sprintf("http://127.0.0.1:%s", port)
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

// validate checks the builder configuration
func (b *AnvilBuilder) validate() error {
	if b.rpcURL == "" {
		return fmt.Errorf("RPC URL is required")
	}
	return nil
}

// Build creates an Anvil instance with the configured options.
// Returns an error if the configuration is invalid. The instance must be started
// with Start() before it can be used.
func (b *AnvilBuilder) Build() (*Anvil, error) {
	if err := b.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	logger := log.With().Str("component", "anvil").Logger().Level(b.logLevel)

	// Ensure we have host in the URL
	if b.rpcURL == "" {
		b.rpcURL = DefaultConfig.DefaultRPCURL
	}

	return &Anvil{
		context: ctx,
		cancel:  cancel,
		args:    b.args,
		rpcURL:  b.rpcURL,
		logger:  logger,
		metrics: AnvilMetrics{},
	}, nil
}
