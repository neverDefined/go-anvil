<!-- Link the issue this PR closes. Use `Closes #N` for each. -->
Closes #

## Summary

<!-- 1–3 bullets describing what changes and why. -->
-

## Test plan

<!-- Check off what you ran. `go test -race ./...` requires anvil on PATH. -->
- [ ] `go build ./...` clean
- [ ] `go vet ./...` clean
- [ ] `golangci-lint run` clean
- [ ] `go test -race ./...` green
- [ ] Godoc updated for any new or changed exported identifiers
- [ ] `CHANGELOG.md` entry added (if user-visible change)

## Notes for reviewer

<!-- Breaking changes, follow-ups, edge cases to look at. Remove if none. -->
