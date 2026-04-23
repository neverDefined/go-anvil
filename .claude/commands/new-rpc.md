---
description: Scaffold a new RPC wrapper method on *Anvil, plus a minimal test stub.
---

Scaffold a new RPC method following the go-anvil repo convention. The shape is documented in `CLAUDE.md`: context-first signature, `rpcCalls.Add(1)`, `CallContext`, error logged and returned, godoc starting with the method name.

## Arguments

`$ARGUMENTS` — expected format: `<MethodName> <rpc_method_name> [<signature-hint>]`

Examples:
- `/new-rpc SetCoinbase anvil_setCoinbase "address common.Address"`
- `/new-rpc GetAutomine evm_getAutomine` — zero-arg method
- `/new-rpc SetChainId anvil_setChainId "chainID uint64"`

If arguments are missing, ask the user for:
1. The Go method name (PascalCase)
2. The JSON-RPC method string (the exact name anvil/evm exposes)
3. Parameters — name and Go type for each
4. Return type — usually `error`; specify if the RPC has a meaningful result

## Steps

1. Parse `$ARGUMENTS` into method name, rpc name, and parameter list.
2. Read `anvil.go` and pick the insertion point — group with the closest existing method semantically (all the `Set*` methods are together, mining methods are together, etc.).
3. Write the method:

   ```go
   // <MethodName> <one-line purpose>.
   // Returns an error if the RPC call fails or if ctx is canceled.
   func (a *Anvil) <MethodName>(ctx context.Context, <params>) error {
       a.rpcCalls.Add(1)
       if err := a.rpcClient.CallContext(ctx, nil, "<rpc_method_name>", <param-names>); err != nil {
           a.logger.Error().Err(err).<field-methods>.Msg("Failed to <verb>")
           return err
       }
       return nil
   }
   ```

4. **Methods that return a result** (like `Snapshot` returning a snapshot ID, `Revert` returning a bool): adjust signature to `(ctx, ...) (T, error)`, bind the result to a local variable, and pass `&result` to `CallContext`.

5. Add a minimal subtest in `anvil_test.go` under `TestAnvil` following the shared-anvil pattern:

   ```go
   t.Run("Test <MethodName>", func(t *testing.T) {
       ctx := t.Context()
       anvil := setupSharedAnvil(t, sharedAnvil)

       err := anvil.<MethodName>(ctx, <test-args>)
       require.NoError(t, err)
   })
   ```

   If the method returns data, add a `require.NoError(t, err)` plus at least one assertion on the returned value.

6. Also add a subtest in `anvil_unit_test.go` under `TestRPCMethods_sendCorrectMethod`:
   - Add `"<rpc_method_name>": <result-or-nil>,` to the `results` map at the top of the function.
   - Add a `t.Run("<MethodName>", func(t *testing.T) { ... })` block that calls the method and asserts `rs.lastCall().Method` matches.

7. Run the checks before handing back:
   ```
   go build ./...
   golangci-lint run ./...
   go test -race -run 'TestRPCMethods_sendCorrectMethod/<MethodName>' ./...
   ```

## Out of scope

- Don't invent parameter types you can't verify. If the RPC method isn't documented in [the Foundry book](https://book.getfoundry.sh/reference/anvil/) or a known header comment, ask the user for the signature.
- Don't commit. Hand the diff back; user will review before committing.
