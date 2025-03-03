package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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

// AnvilMetrics contains runtime metrics for the Anvil instance
type AnvilMetrics struct {
	StartupTime   time.Duration
	BlocksMined   int64
	RPCCalls      int64
	LastError     error
	LastErrorTime time.Time
}

// AnvilPrivateKey represents the default test account private keys
type AnvilPrivateKey string

// Default Anvil private keys for testing
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
	blocksMined atomic.Int64
	rpcCalls    atomic.Int64
}

// NewAnvil creates a new Anvil instance with default configuration
func NewAnvil() (*Anvil, error) {
	return NewAnvilBuilder().Build()
}

// Start initializes and starts the Anvil process
func (a *Anvil) Start() error {
	startTime := time.Now()

	// the foundry use the `XDG_CONFIG_HOME` as base dir if it's set, or user's home
	homeDir := os.Getenv("XDG_CONFIG_HOME")
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
	}

	anvilPath := filepath.Join(homeDir, ".foundry", "bin", "anvil")
	a.cmd = exec.CommandContext(a.context, anvilPath, a.args...)

	// Capture stdout and stderr
	a.cmd.Stdout = os.Stdout
	a.cmd.Stderr = os.Stderr

	if err := a.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start anvil: %w", err)
	}

	// Give Anvil time to initialize
	time.Sleep(2 * time.Second)

	// Try to establish connection with retries
	err := retry(5, time.Second, func() error {
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

// Client returns the Ethereum client
func (a *Anvil) Client() *ethclient.Client {
	return a.client
}

// RPCClient returns the RPC client
func (a *Anvil) RPCClient() *rpc.Client {
	return a.rpcClient
}

// Accounts returns the default test accounts with their private keys and addresses
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

// MineBlock triggers the mining of a new block
func (a *Anvil) MineBlock() error {
	a.blocksMined.Add(1)
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "evm_mine")
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to mine block")
	}
	return err
}

// SetNextBlockTimestamp sets the timestamp for the next block
func (a *Anvil) SetNextBlockTimestamp(timestamp int64) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "evm_setNextBlockTimestamp", timestamp)
	if err != nil {
		a.logger.Error().Err(err).Int64("timestamp", timestamp).Msg("Failed to set next block timestamp")
	}
	return err
}

// IncreaseTime increases the current block time
func (a *Anvil) IncreaseTime(seconds int64) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "evm_increaseTime", seconds)
	if err != nil {
		a.logger.Error().Err(err).Int64("seconds", seconds).Msg("Failed to increase time")
	}
	return err
}

// SetBalance sets the balance of an account
func (a *Anvil) SetBalance(address common.Address, balance *big.Int) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_setBalance", address, balance.String())
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Str("balance", balance.String()).Msg("Failed to set balance")
	}
	return err
}

// Impersonate enables impersonating an account
func (a *Anvil) Impersonate(address common.Address) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_impersonateAccount", address)
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Msg("Failed to impersonate account")
	}
	return err
}

// StopImpersonating stops impersonating an account
func (a *Anvil) StopImpersonating(address common.Address) error {
	a.rpcCalls.Add(1)
	err := a.rpcClient.Call(nil, "anvil_stopImpersonatingAccount", address)
	if err != nil {
		a.logger.Error().Err(err).Str("address", address.Hex()).Msg("Failed to stop impersonating account")
	}
	return err
}

// Close performs a clean shutdown of all resources
func (a *Anvil) Close() error {
	a.logger.Debug().Msg("Shutting down Anvil instance")

	if a.client != nil {
		a.client.Close()
	}

	if a.rpcClient != nil {
		a.rpcClient.Close()
	}

	if err := a.Stop(); err != nil {
		a.logger.Error().Err(err).Msg("Error stopping Anvil")
		return err
	}

	// Wait for process to fully terminate
	time.Sleep(time.Second)
	return nil
}

// Stop terminates the Anvil process
func (a *Anvil) Stop() error {
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}

	if a.rpcClient != nil {
		a.rpcClient.Close()
		a.rpcClient = nil
	}

	if a.cmd != nil && a.cmd.Process != nil {
		if err := a.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("failed to kill anvil process: %w", err)
		}
		_ = a.cmd.Wait()
		a.cmd = nil
	}

	return nil
}

// Metrics returns current metrics for the Anvil instance
func (a *Anvil) Metrics() AnvilMetrics {
	return AnvilMetrics{
		StartupTime:   a.metrics.StartupTime,
		BlocksMined:   a.blocksMined.Load(),
		RPCCalls:      a.rpcCalls.Load(),
		LastError:     a.metrics.LastError,
		LastErrorTime: a.metrics.LastErrorTime,
	}
}

// Reset restarts the Anvil instance
func (a *Anvil) Reset() error {
	// Stop the current instance
	if err := a.Stop(); err != nil {
		return fmt.Errorf("failed to stop anvil: %w", err)
	}

	// Wait for cleanup
	time.Sleep(time.Second * 2)

	// Start a new instance
	return a.Start()
}

// WaitForBlock waits for a specific block number
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

// AnvilBuilder helps construct Anvil instances
type AnvilBuilder struct {
	args     []string
	rpcURL   string
	logLevel zerolog.Level
}

// NewAnvilBuilder creates a new AnvilBuilder with default configuration
func NewAnvilBuilder() *AnvilBuilder {
	return &AnvilBuilder{
		args:     make([]string, 0),
		rpcURL:   DefaultConfig.DefaultRPCURL,
		logLevel: DefaultConfig.LogLevel,
	}
}

// Builder methods
func (b *AnvilBuilder) WithBlockTime(blockTime string) *AnvilBuilder {
	b.args = append(b.args, "--block-time", blockTime)
	return b
}

func (b *AnvilBuilder) WithFork(fork string) *AnvilBuilder {
	b.args = append(b.args, "--fork-url", fork)
	return b
}

func (b *AnvilBuilder) WithForkBlockNumber(blockNumber string) *AnvilBuilder {
	b.args = append(b.args, "--fork-block-number", blockNumber)
	return b
}

// WithPort sets the RPC port
func (b *AnvilBuilder) WithPort(port string) *AnvilBuilder {
	b.args = append(b.args, "--port", port)
	b.rpcURL = fmt.Sprintf("http://127.0.0.1:%s", port)
	return b
}

func (b *AnvilBuilder) WithChainId(chainId string) *AnvilBuilder {
	b.args = append(b.args, "--chain-id", chainId)
	return b
}

func (b *AnvilBuilder) WithGasLimit(limit string) *AnvilBuilder {
	b.args = append(b.args, "--gas-limit", limit)
	return b
}

func (b *AnvilBuilder) WithGasPrice(price string) *AnvilBuilder {
	b.args = append(b.args, "--gas-price", price)
	return b
}

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

// Build creates an Anvil instance
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
