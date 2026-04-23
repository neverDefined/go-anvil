# Security policy

## Supported versions

`go-anvil` is pre-1.0. Only the latest commit on `main` is supported — fixes are not backported to older tags.

## Reporting a vulnerability

Please do **not** file a public issue.

1. **Preferred:** open a private advisory via GitHub —
   <https://github.com/neverDefined/go-anvil/security/advisories/new>
2. **Fallback:** email <samsonm2050@gmail.com> with subject line `[go-anvil security]`.

Include:
- A minimal reproduction (Go snippet or repo link).
- Go version, Foundry/anvil version, OS.
- The earliest go-anvil commit or version you can confirm is affected.
- Any suggested fix or workaround, if you have one.

## What to expect

This is a solo-maintained project. Response timelines are best-effort:

- **Acknowledgement:** within 7 days.
- **Triage decision:** within 14 days.
- **Patch or disclosure plan:** communicated on the private advisory thread.

If you have not heard back in a week, a polite nudge on the same channel is welcome.

## Scope

go-anvil wraps Foundry's `anvil` binary. Vulnerabilities in `anvil` itself should be reported upstream to the Foundry project. Report here only issues in this Go library — the process wrapper, RPC client wiring, builder, or helpers.
