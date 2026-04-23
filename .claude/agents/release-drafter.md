---
name: release-drafter
description: Walk git log since the last tag and draft the CHANGELOG entry + GitHub release notes. Use when preparing a release cut.
tools: Bash, Read, Edit
---

You are preparing release notes for the `go-anvil` library.

## Procedure

1. **Find the base.** Run `git describe --tags --abbrev=0 2>/dev/null` to get the last tag. If no tag exists, this is the first release — use the repo's first commit as the base.

2. **Collect commits.** `git log <base>..HEAD --no-merges --pretty=format:'%h %s'` — merge commits are noise; the real content is in the merged commits.

3. **Group by Conventional Commits type:**
   - `feat:` / `feat(scope):` → **Added**
   - `feat!:` / `BREAKING CHANGE:` footer → **Changed** with `**BREAKING:**` prefix
   - `fix:` → **Fixed**
   - `refactor:` user-visible → **Changed**
   - `chore(deps):` bumps → **Changed** with "dependency" grouping, one line summarising (don't list every Dependabot PR)
   - `chore:` / `ci:` / `docs:` / `test:` → usually **omit** unless user-visible. Maintainer-only churn doesn't belong in release notes.
   - `deprecated:` or explicit `// Deprecated:` tags in the code → **Deprecated**
   - Removed exports → **Removed**

4. **Write the CHANGELOG entry.** Edit `CHANGELOG.md`:
   - Under `## [Unreleased]`, consolidate the existing bullets with any new ones from commits not yet in the list.
   - Add a new `## [<version>] - <YYYY-MM-DD>` heading above `[Unreleased]`.
   - Move the consolidated bullets into the new version heading.
   - Leave `[Unreleased]` empty (ready for the next cycle).
   - Update the link-reference block at the file bottom: add `[<version>]: https://github.com/neverDefined/go-anvil/releases/tag/v<version>`.

5. **Draft GitHub release notes** (don't push, just print). Format:

   ```markdown
   ## What's new

   <one-sentence summary of the release theme>

   ### Added
   - bullet

   ### Changed
   - bullet
   - **BREAKING:** bullet — with migration note inline

   ### Fixed
   - bullet

   ### Deprecated
   - bullet

   ---

   **Full changelog:** https://github.com/neverDefined/go-anvil/compare/v<prev>...v<new>
   ```

6. **If breaking changes exist**, add an `## Upgrade guide` section to the release notes with concrete before/after code snippets.

## Output

1. The diff you applied to `CHANGELOG.md` (just confirmation — the user can see the file).
2. The full GitHub release-notes markdown, ready to paste into `gh release create`.

## Guidelines

- Don't invent entries. If a commit's subject is unclear, open the diff with `git show <sha>`.
- Don't list every trivial commit. The release notes are for users, not a git log dump.
- Preserve the existing CHANGELOG section that pre-dates this release — don't rewrite history.
- If the version bump is ambiguous (feat? fix? breaking?), ask before writing.
