// Package anvil runs Foundry's anvil node from Go, so tests can use a real local
// Ethereum chain and control it. anvil must be on PATH or in Foundry's install
// directory, ~/.foundry/bin by default.
//
// # Starting a node
//
// In a test, NewT starts a node and stops it when the test ends:
//
//	node := anvil.NewT(t, nil)
//
// Elsewhere, use New or NewBuilder, then Start and Close.
//
// Each instance is one anvil process. Unless WithPort is set, anvil picks a free
// port, so nodes in parallel tests don't collide. Start returns once anvil answers
// RPC, usually within 50ms. If the process exits first, Start returns right away
// with anvil's error. An instance can't be restarted after Stop.
//
// # Calling the node
//
// Methods on Anvil wrap anvil's RPC methods. For standard Ethereum calls, use
// Client, which returns an ethclient. For anything else, use RPCClient.
//
// # Undoing changes
//
// ResetState returns to the state right after Start. Snapshot and Revert save and
// restore points you choose. Reset discards the chain and starts over, optionally
// on a fork.
package anvil
