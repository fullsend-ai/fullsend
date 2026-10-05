---
name: cutting-releases
description: >
  Use when the user wants to tag a release, cut a release candidate, or ship a
  new version. Also use when asking about release process, versioning, or how
  GoReleaser is configured.
allowed-tools: Read, Grep, Glob, AskUserQuestion, Agent, Bash(git tag:*), Bash(git log:*), Bash(git diff:*), Bash(git pull:*), Bash(git push:*), Bash(git rev-parse:*), Bash(gh release:*), Bash(gh run:*), Bash(gh api:*), Bash(gh pr:*), Bash(git checkout:*), Bash(git fetch:*), Bash(skopeo inspect:*), Bash(grep:*), Bash(bash skills/cutting-releases/scripts/install-binary.sh:*)
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

Check the latest **final** tags (the first lines of the output) —
RC/alpha/beta tags sort above their final, so exclude anything with a
`-` suffix:

```
git tag --sort=-v:refname | grep -v -- -
```

Decide the next **target final version** following semver. Every release is
cut as a release candidate first (step 6) and promoted to this final version
only after the RC gate passes and the fleet images are repinned (step 8) —
so what you pick here is the version the release will end up as, not the
first tag you push:

| Change type | Example target version |
|---|---|
| Breaking / major milestone | `v1.0.0` |
| New functionality (MVP, feature set) | `v0.X.0` |
| Bug fixes only | `v0.0.X` |

### 3. Confirm the version with the user

Use `AskUserQuestion` to present your proposed version tag and the rationale
for your choice. For example:

> I'd suggest `v0.2.0` (cut first as `v0.2.0-rc.1`) — there are 5 new `feat:`
> commits since `v0.1.0` and no breaking changes. Does that look right, or
> would you prefer a different version?

Do not proceed until the user confirms.

### 4. Ask for a tag subject

Use `AskUserQuestion` to ask:

> Any special title for this release? (e.g. "MVP Release Candidate 1")
> Leave blank to use just the version tag.

The answer becomes the tag subject line. If blank, use the tag name itself as
the subject so that GoReleaser's `name_template` guard (`ne .TagSubject .Tag`)
suppresses it, producing a clean release title without duplication.

### 5. Gather changes since the last final

```
git log --oneline <previous-final>..HEAD
```

Summarize changes into categories (features, fixes, refactors). Exclude
`docs:`, `test:`, `chore:`, `ci:`, `build:` commits — GoReleaser filters these anyway.

### 6. Create the RC tag

Every release starts as a release candidate. Record the commit you are
tagging as `<rc-sha>` (`git rev-parse HEAD`).

Build the tag message:

- **Line 1 (subject):** The custom title from step 4, if one was given.
  If no custom title, **use the tag name itself** (e.g. `v0.9.0-rc.1`) —
  git's `%(contents:subject)` skips leading blank lines, so a blank first
  line still picks up the first category header as `.TagSubject`. Using the
  tag name as subject ensures `.TagSubject == .Tag`, which the goreleaser
  guard suppresses, producing a clean release title with no suffix.
- **Line 2:** Blank.
- **Lines 3+:** Summary of highlights organized by category.

```
git tag -a vX.Y.Z-rc.N <rc-sha> -m "<message>"
git rev-parse vX.Y.Z-rc.N^{commit}
```

The second command must print `<rc-sha>`.

The first line of the annotation becomes the release title suffix via
GoReleaser's `name_template` (see `.goreleaser.yml`).

### 7. Push the RC tag

```
git push origin vX.Y.Z-rc.N
gh run list --workflow=release.yml --branch vX.Y.Z-rc.N --limit=1
```

Expect about 15 minutes before anything is published: the gate runs
agents' release tier, one functional-test case per agent (the full
suite takes about 45; see Notes).
When it passes, GoReleaser publishes the `vX.Y.Z-rc.N` binaries as a
prerelease. `v0` does not move, but `tag-agents` tags
`fullsend-ai/agents` at `vX.Y.Z-rc.N`. The images come from the
separate Sandbox Images run for the same tag push.

**Hold window:** from this push until the final tag is pushed, do not
merge PRs touching `images/sandbox`, `images/code` or
`.github/workflows/sandbox-images.yml` — any such merge forces
`rc.N+1` at step 9.

If the run fails, see "When a release run fails" in Notes.

### 8. Repin the fleet harness images

Once the RC run is green, repin the agents harness images to this RC's
digests. The final tag waits until this repin PR is merged.

1. **Check the image build.** The Sandbox Images run for the tag must
   be green; if it failed, re-run it (`gh run rerun <run-id> --failed`)
   and wait before resolving digests:

   ```
   gh run list --workflow=sandbox-images.yml --branch vX.Y.Z-rc.N --limit=1
   ```

2. **Resolve the digests.** Needs `skopeo`; on macOS pass
   `--override-os linux`. Image tags have no `v` prefix:

   ```
   skopeo inspect --override-os linux --no-tags docker://ghcr.io/fullsend-ai/fullsend-sandbox:X.Y.Z-rc.N
   skopeo inspect --override-os linux --no-tags docker://ghcr.io/fullsend-ai/fullsend-code:X.Y.Z-rc.N
   ```

   Record each result's `Digest` (the multi-arch index digest) as the
   sandbox and code RC digests; B3 in post-flight checks against them.
   Its `org.opencontainers.image.revision` label must equal `<rc-sha>`;
   if not, the tag was rebuilt from another commit — stop and
   investigate.

3. **Hand off the repin PR against `fullsend-ai/agents`.** List the
   harness files that pin these images on agents `main`:

   ```
   for f in $(gh api "repos/fullsend-ai/agents/contents/harness?ref=main" --jq '.[].name'); do gh api "repos/fullsend-ai/agents/contents/harness/$f?ref=main" --jq ".content|@base64d|split(\"\n\")[]|select(test(\"image:.*fullsend-(sandbox|code)\"))|\"$f: \"+."; done
   ```

   Expect one line per harness that pins an image; empty output or an
   error means discovery failed — stop. This skill can't clone or open
   PRs in agents, so use `AskUserQuestion` to give the user the
   discovered lines and both RC digests, and ask them to open the repin
   PR with
   [fullsend-ai/agents#1570](https://github.com/fullsend-ai/agents/pull/1570)
   as the template: each `image:` gets the matching index digest
   (`@sha256:...`), never a tag or a per-platform digest.

4. **Wait for the repin PR to merge,** and record its merge commit as
   `<repin-merge-sha>` (`gh pr view <n> --repo fullsend-ai/agents
   --json mergeCommit --jq .mergeCommit.oid`); post-flight B2 needs it.

### 9. Tag and push the final release

Tag the final at `<final-sha>`, normally `<rc-sha>` itself: the
release workflow pins GoReleaser's current tag to the pushed ref, so a
final can share the RC's commit. If you move the final to a later
commit instead, first check that the images have not changed since the
RC:

```
git log --oneline <rc-sha>..<final-sha> -- images/sandbox images/code .github/workflows/sandbox-images.yml
```

If it prints anything, do not tag the final: cut `rc.N+1` at
`<final-sha>` (step 6) and repin again. Tag with the step 6 message
minus `-rc.N`:

```
git tag -a vX.Y.Z <final-sha> -m "<message>"
git rev-parse vX.Y.Z^{commit}
git push origin vX.Y.Z
```

The second command must print `<final-sha>`. This run is the first
gate against the repinned images. On success it publishes the binaries
and GitHub Release, moves `v0`, and tags agents `vX.Y.Z`. If it fails,
see "When a release run fails" in Notes.

### 10. Run post-flight verification

Read [post-flight.md](post-flight.md) in this skill's directory and
follow the post-flight verification procedure.

### 11. Write release highlights

After post-flight confirms the release is published, write a short user-facing
summary highlighting the changes that matter most to end users.

1. **Gather the raw changelog.** Run `gh release view <tag> --json body -q .body`
   to get the auto-generated release body. For a final it is expected to
   span the previous final to this one, not just the RC.

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

### 12. Install the binary locally

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
- **Never delete a tag that published binaries or a GitHub Release.** If
  a shipped release is bad, cut a new patch or RC. Only a blocked final
  may be deleted (see "When a release run fails").
- **The changelog** is auto-generated from PR titles (which must follow conventional commit format). GoReleaser uses `changelog.use: github` in `.goreleaser.yml`, so merged PR titles — not individual commit subjects — are the source of release-note entries.
- **The `v0` tag** is a moving tag consumed by downstream orgs for reusable
  workflows. It is automatically moved by the release workflow after
  GoReleaser completes (skipped for pre-release tags).
- **Agents validation runs before anything is published.** On a `v*` tag
  push the workflow first runs `resolve-agents` (verifies the tag still
  points at the commit that triggered the run, checks the gate secrets
  are configured, and records agents' `main` SHA), then `validate-agents`,
  which runs agents' functional tests (via a cross-repo reusable workflow
  call) against the release tag. A cross-repo call runs agents' release
  tier: one case per agent, blocking on case exits and deterministic
  checks only. Only if those pass does `recheck-tag` re-verify that the
  tag still points at the triggering commit, and `release` run
  GoReleaser. A failure at any of these steps means no binaries or
  GitHub Release are published and `v0` does not move; a Slack
  notification reports the release as blocked.
- **Images are built per tag push, not per release.** Sandbox Images
  runs for every `v*` tag regardless of the gate, and the build is not
  reproducible: two builds of one commit give different digests. That is
  why the fleet pins the RC's digests (step 8), never a final's.
- **When a release run fails** (step 7 or 9):
  - *Flake or infrastructure problem:* `gh run rerun <run-id> --failed`.
    The tag stays.
  - *Fix merged in `fullsend-ai/agents` only:* `gh run rerun <run-id>`
    (the whole run), so `resolve-agents` and `validate-agents` resolve
    agents `main` again; `--failed` keeps the agents SHA from the first
    attempt. (This follows from `release.yml`; v0.44.0 was re-tagged
    instead.)
  - *Fix needs a commit in this repo:* for an RC, cut `rc.N+1` (step 6).
    For a final, confirm `gh release view vX.Y.Z` reports no release,
    delete the tag (`git tag -d vX.Y.Z && git push origin
    :refs/tags/vX.Y.Z`), then cut `rc.N+1`.

  Re-pushing a final tag rebuilds its `:X.Y.Z` images; that is harmless,
  since the fleet pins RC digests.
- **Same-commit finals:** before fullsend#7955 (v0.44.0), a final on the
  same commit as its RC failed in `release` with `422 already_exists`,
  because GoReleaser picked the RC tag. The workflow now pins the
  current tag to the pushed ref and, for a final, the previous tag to
  the last final; no release has exercised this yet.
- **The `fullsend-ai/agents` repo** is tagged with the same version last,
  by the `tag-agents` job, using an org-owned GitHub App token
  (`RELEASE_APP_ID` / `RELEASE_APP_PRIVATE_KEY`). It tags the agents
  `main` commit the gate validated, for RC tags as well as finals. This
  is the only step that can fail after the binary has shipped; when it
  does, a Slack notification is sent and only the agents tag is missing.
  That tag push triggers agents' own `release.yml`, which creates a
  GitHub Release and, for non-prerelease tags, moves its `v0` floating
  tag.
