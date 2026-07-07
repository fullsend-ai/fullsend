---
name: cutting-releases
description: >
  Use when the user wants to tag a release, cut a release candidate, or ship a
  new version. Also use when asking about release process, versioning, or how
  GoReleaser is configured.
allowed-tools: Read, Grep, Glob, AskUserQuestion, Bash(git tag:*), Bash(git log:*), Bash(git diff:*), Bash(git pull:*), Bash(git push:*), Bash(gh release:*), Bash(gh run:*), Bash(gh api:*), Bash(gh pr:*), Bash(git checkout:*), Bash(git fetch:*), Bash(bash skills/cutting-releases/scripts/install-binary.sh:*)
---

# Cutting Releases

Releases are driven by annotated git tags. When a tag matching `v*` is pushed,
`.github/workflows/release.yml` runs GoReleaser to build binaries, generate a
changelog, and create the GitHub release.

## Process

Before starting step 1, read
[pre-flight.md](pre-flight.md) in this skill's directory and complete
the pre-flight audit. Do not proceed until the user confirms GO.

Follow these steps in order.

### 1. Confirm the branch

Releases should be cut from `main`. Verify you are on `main` and up to date:

```
git checkout main && git pull --tags --force
```

### 2. Determine the version

Check the latest tag:

```
git tag --sort=-v:refname | head -5
```

Decide the next version following semver:

| Change type | Example bump |
|---|---|
| Breaking / major milestone | `v1.0.0` |
| New functionality (MVP, feature set) | `v0.X.0` |
| Bug fixes only | `v0.0.X` |
| Release candidate | `v0.X.0-rc.N` |

### 3. Confirm the version with the user

Use `AskUserQuestion` to present your proposed version tag and the rationale
for your choice. For example:

> I'd suggest `v0.2.0` — there are 5 new `feat:` commits since `v0.1.0` and
> no breaking changes. Does that look right, or would you prefer a different
> version?

Do not proceed until the user confirms.

### 4. Ask for a tag subject

Use `AskUserQuestion` to ask:

> Any special title for this release? (e.g. "MVP Release Candidate 1")
> Leave blank to use just the version tag.

The answer becomes the tag subject line. If blank, use the tag name itself as
the subject so that GoReleaser's `name_template` guard (`ne .TagSubject .Tag`)
suppresses it, producing a clean release title without duplication.

### 5. Gather changes since last tag

```
git log --oneline <previous-tag>..HEAD
```

Summarize changes into categories (features, fixes, refactors). Exclude
`docs:`, `test:`, `chore:`, `ci:`, `build:` commits — GoReleaser filters these anyway.

### 6. Create the annotated tag

Build the tag message:

- **Line 1 (subject):** The custom title from step 4, if one was given.
  If no custom title, **use the tag name itself** (e.g. `v0.9.0`) — git's
  `%(contents:subject)` skips leading blank lines, so a blank first line
  still picks up the first category header as `.TagSubject`. Using the tag
  name as subject ensures `.TagSubject == .Tag`, which the goreleaser guard
  suppresses, producing a clean release title with no suffix.
- **Line 2:** Blank.
- **Lines 3+:** Summary of highlights organized by category.

```
git tag -a v0.X.0 -m "<message>"
```

The first line of the annotation becomes the release title suffix via
GoReleaser's `name_template` (see `.goreleaser.yml`).

### 7. Push the tag

```
git push origin <tag>
```

GoReleaser takes over from here. Verify the workflow starts:

```
gh run list --workflow=release.yml --limit=1
```

### 8. Run post-flight verification

Read [post-flight.md](post-flight.md) in this skill's directory and
follow the post-flight verification procedure.

### 9. Write release highlights

After post-flight confirms the release is published, write a short user-facing
summary highlighting the changes that matter most to end users.

1. **Gather the raw changelog.** Run `gh release view <tag> --json body -q .body`
   to get the auto-generated release body.
2. **Research the actual changes.** Do not rely on PR titles or one-line
   summaries — they often undersell or misrepresent user impact. Launch an
   `Agent` sub-agent to read the full body, diff, and comments of every merged
   PR in the release (`gh pr view <number>`, `gh pr diff <number>`). The agent
   should identify which changes affect user-visible behavior, CLI flags,
   configuration, error messages, performance, or compatibility — and flag
   anything that looks like a breaking change or notable upgrade, even if the
   PR title doesn't say so.
3. **Draft highlights.** Write one or more paragraphs of prose (not
   bullet lists — use only one paragraph if only one is necessary)
   focusing on *what changed for the user*, not internal refactors.
   Bold the names of features or areas being discussed (e.g. **token mint**,
   **`fullsend init`**). Use code fences where showing a command or config
   snippet helps illustrate a change. Skip items that have no user-visible
   effect. Full coverage of every change is a non-goal — clarity and impact
   are the goals.
4. **Present the draft to the user.** Use `AskUserQuestion` to show the
   proposed highlights text and ask:

   > Here are the draft release highlights I'd prepend to the release body.
   > Edit freely or say "looks good" to proceed.

5. **Prepend to the release.** Once confirmed (with any edits applied), fetch
   the current release body, prepend the highlights separated by a horizontal
   rule (`---`), and update the release:

   ```
   gh release edit <tag> --notes "$(cat <<'EOF'
   <highlights>

   ---

   <existing body>
   EOF
   )"
   ```

### 10. Install the binary locally

Use `AskUserQuestion` to ask where to install (default: `~/.local/bin/`),
then run the install script using its repo-root-relative path:

```bash
bash skills/cutting-releases/scripts/install-binary.sh <tag> [install-dir]
```

The script downloads the archive, verifies its SHA-256 checksum, and
installs the binary as `fullsend-<tag>` so multiple versions can coexist.

## Notes

- **Pre-releases:** Tags with `-rc.N`, `-alpha.N`, or `-beta.N` suffixes are
  automatically marked as pre-releases by GoReleaser.
- **Never delete a published tag.** If a release is bad, cut a new patch or RC.
- **The changelog** is auto-generated from PR titles (which must follow conventional commit format). GoReleaser uses `changelog.use: github` in `.goreleaser.yml`, so merged PR titles — not individual commit subjects — are the source of release-note entries.
- **The `v0` tag** is a moving tag consumed by downstream orgs for reusable
  workflows. It is automatically moved by the release workflow after
  GoReleaser completes (skipped for pre-release tags).
- **The `fullsend-ai/agents` repo** is tagged with the same version
  automatically. After GoReleaser completes, the release workflow
  pushes the tag to agents using an org-owned GitHub App token
  (`RELEASE_APP_ID` / `RELEASE_APP_PRIVATE_KEY`). That tag push
  triggers agents' own `release.yml`, which creates a GitHub Release
  and moves its `v0` floating tag.
