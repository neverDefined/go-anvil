---
description: Scaffold a new RPC wrapper method on *Anvil, plus a minimal test stub.
---

Scaffold a new RPC method following the go-anvil repo convention. The shape is documented in `CLAUDE.md`: context-first signature, forward through `a.call` and return its error, godoc starting with the method name.

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
   func (a *Anvil) <MethodName>(ctx context.Context, <params>) error {
       return a.call(ctx, nil, "<rpc_method_name>", <param-names>)
   }
   ```

4. **Methods that return a result** (like `Snapshot` returning a snapshot ID, `Revert` returning a bool): adjust signature to `(ctx, ...) (T, error)`, bind the result to a local variable, and pass `&result` to `a.call`.

5. Add a top-level integration test in `anvil_test.go` on its own instance. Assert what the call changed on the chain, not just that it returned nil:

   ```go
   func Test<MethodName>(t *testing.T) {
       t.Parallel()
       ctx := t.Context()
       a := startAnvil(t, NewBuilder())

       require.NoError(t, a.<MethodName>(ctx, <test-args>))
       // read the state back through a.Client() and assert on it
   }
   ```

6. In `anvil_unit_test.go`, add `"<rpc_method_name>": <result-or-nil>,` to `cannedResults` and a row to the `TestRPCWrappers` table with the exact params JSON the method sends.

7. Run the checks before handing back:
   ```
   go build ./...
   golangci-lint run ./...
   go test -race -run 'Test<MethodName>|TestRPCWrappers' ./...
   ```

## Out of scope

- Don't invent parameter types you can't verify. If the RPC method isn't documented in [the Foundry book](https://book.getfoundry.sh/reference/anvil/) or a known header comment, ask the user for the signature.
- Don't commit. Hand the diff back; user will review before committing.
