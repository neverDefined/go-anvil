---
description: Check for a newer stable Foundry release and draft the ci.yml bump (plus optional PR).
---

Check if there's a newer stable Foundry release than the version pinned in CI. If so, draft the one-line `ci.yml` change and optionally open a PR.

## Steps

1. **Find the pinned version:**
   ```
   grep -E 'foundry-toolchain' -A 2 .github/workflows/ci.yml | grep -E 'version:'
   ```
   Parse out the `v<semver>`.

2. **List recent stable tags from the Foundry repo** (filter out pre-releases like `-rc1`):
   ```
   gh api repos/foundry-rs/foundry/tags --paginate --jq '.[] | .name' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -10
   ```

3. **Compare.** If the top tag (lexically / semver-wise) is **greater** than the pinned version, propose the bump. If it's equal, say so and stop — don't open a no-op PR.

4. **Print the diff** to the user:
   ```
   -          version: v<old>
   +          version: v<new>
   ```

   Include a link to the Foundry release notes so the user can eyeball breaking changes:
   `https://github.com/foundry-rs/foundry/releases/tag/v<new>`

5. **Ask** whether to open a PR. If yes:
   - `git checkout -b chore/bump-foundry-<new>`
   - Edit `.github/workflows/ci.yml` with the single line change.
   - Commit:
     ```
     chore(ci): bump Foundry v<old> -> v<new>

     Release notes: https://github.com/foundry-rs/foundry/releases/tag/v<new>
     ```
   - `git push -u origin chore/bump-foundry-<new>`
   - `gh pr create` with a body that links to the release notes and confirms CI verified the bump is safe (it will, when it runs).

   Don't merge. The user reviews the PR.

## Guidelines

- Strict-semver filtering only — no `-rc`, `-beta`, `nightly-*` tags.
- If the gh API is rate-limited, fall back to `gh release list --repo foundry-rs/foundry --limit 20 --json tagName,isPrerelease`.
- If the Foundry release notes call out a breaking RPC change, **surface that prominently** in the PR body so the user can skim before merging.
- Don't open a PR if you can't verify the new version actually differs from what's pinned — show the comparison first.
