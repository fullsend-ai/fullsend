---
name: cutting-releases
description: >
  Use when the user wants to tag a release, cut a release candidate, or ship a
  new version. Also use when asking about release process, versioning, or how
  GoReleaser is configured.
allowed-tools: Read, Grep, Glob, AskUserQuestion, Bash(git tag:*), Bash(git log:*), Bash(git diff:*), Bash(git pull:*), Bash(git push:*), Bash(gh release:*), Bash(gh run:*), Bash(git checkout:*), Bash(git fetch:*), Bash(make lint:*), Bash(bash skills/cutting-releases/scripts/install-binary.sh:*)
---

# Cutting Releases

Releases are driven by annotated git tags. When a tag matching `v*` is pushed,
the `.github/workflows/release.yml` workflow runs GoReleaser, which builds
binaries, generates a changelog, and creates the GitHub release. The release
title comes from the tag annotation via `name_template` in `.goreleaser.yml`.

## Pre-Flight Release Check

Run this audit **before** tagging. The goal is to verify that moving
the `v0` reusable-workflow tag and publishing new scaffold templates
will not break downstream orgs.

### A. Diff reusable workflows

Compare the current `v0` tag to `main` for all reusable workflows:

```
git diff v0..origin/main -- .github/workflows/reusable-*.yml
```

For each changed workflow, classify:
- **Additive** (new optional inputs, new env vars) — safe.
- **Default change** (different default values) — check downstream callers.
- **Breaking** (removed inputs, renamed jobs, changed required outputs) — block.

### B. Diff scaffold templates

```
git diff v0..origin/main -- internal/scaffold/fullsend-repo/
```

Scaffold files are deployed at `github setup` time, not consumed live.
Changes here affect **new installs and re-scaffolds only**, not running
workflows. Still note anything that alters agent behavior (skill files,
harness configs, hook scripts).

### C. Review CLI changes

```
git log --oneline v0..origin/main -- cmd/ internal/cli/
```

Check for:
- Renamed flags or sub-commands — deprecated aliases must be preserved.
- Changed defaults (pool names, regions, project IDs) — document migration.
- Removed functionality — block or add deprecation notice.

### D. Check CI on main

```
gh run list --branch=main --limit=5
```

All recent runs should be passing. If E2E tests are failing, investigate
before releasing.

### E. Present summary

Summarize findings to the user in a table:

| Area | Changes | Breaking? |
|------|---------|-----------|
| Reusable workflows | ... | No/Yes |
| Scaffold templates | ... | No/Yes |
| CLI | ... | No/Yes |

Give a **GO / NO-GO** verdict. Do not proceed until the user confirms.

---

## Process

Follow these steps in order.

### 1. Confirm the branch

Releases should be cut from `main`. Verify you are on `main` and up to date:

```
git checkout main && git pull
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

The answer becomes the tag subject line. If blank, do **not** use the version
as the subject — leave the subject empty so that GoReleaser's `name_template`
renders just the tag without duplication.

### 5. Gather changes since last tag

```
git log --oneline <previous-tag>..HEAD
```

Summarize the changes into categories (features, fixes, refactors). Exclude
commits that start with `docs:`, `test:`, `chore:`, `ci:`, or `build:` — GoReleaser filters
these from the changelog anyway.

### 6. Create the annotated tag

Build the tag message:

- **Line 1 (subject):** The custom title from step 4, if one was given.
  If no custom title, **omit the subject line** — start the annotation
  body directly with the highlights. This avoids duplicating the version
  in the release title.
- **Lines 3+:** Summary of highlights organized by category.

```
git tag -a v0.X.0 -m "<message>"
```

The first line of the annotation flows into the GitHub release title via
GoReleaser's `name_template: "{{ .Tag }}{{ if and .TagSubject (ne .TagSubject .Tag) }}: {{ .TagSubject }}{{ end }}"`.

### 7. Push the tag

```
git push origin <tag>
```

GoReleaser takes over from here. Verify the workflow starts:

```
gh run list --workflow=release.yml --limit=1
```

### 8. Move the `v0` tag

Downstream orgs reference reusable workflows via `@v0`. After the
version tag is pushed, move `v0` to the same commit:

```
git tag -f v0 <tag>
git push origin v0 --force
```

This updates all `@v0` workflow references immediately. The Sandbox
Images workflow (triggered by tag push) will also run.

### 9. Wait for workflows

Wait for both the Release workflow (triggered by the `v*` tag) and
the Sandbox Images workflow (triggered by the `v0` tag move) to
complete:

```
gh run list --workflow=release.yml --limit=1
gh run list --workflow=sandbox-images.yml --limit=1
```

Both must pass before proceeding.

### 10. Verify the release

Once the workflows complete, confirm the release was created:

```
gh release view <tag>
```

Check that the title, changelog, and binary assets look correct.

### 11. Install the binary locally

Ask the user where to install (default: `~/.local/bin/`), then run
the install script using its repo-root-relative path:

```bash
bash skills/cutting-releases/scripts/install-binary.sh <tag> [install-dir]
```

The script downloads the release archive, verifies its SHA-256 checksum
against the release's `checksums.txt`, and installs the binary as
`fullsend-<tag>` so multiple versions can coexist.

## Post-Flight Verification

After the release and sandbox-images workflows pass, verify that
downstream orgs can resolve the new `@v0` tag.

### A. Check downstream orgs

Use `AskUserQuestion` to ask which downstream orgs and repos to verify:

> Which orgs/repos should I check for post-release verification?
> (e.g. "fullsend-ai, konflux-ci/integration-service, redhat-developer/rhdh-agentic")

For each org or repo provided, check recent workflow runs:

```
gh run list --repo <org>/.fullsend --limit=3
gh run list --repo <org>/<target-repo> --limit=3
```

Look for runs that started **after** the `v0` tag move. Confirm they
completed without workflow-resolution errors (e.g. "could not find
reusable workflow").

### B. Retrigger a verification run

If no natural workflow runs occurred after the tag move, find a recent
failed or cancelled run and present it to the user for confirmation
before retriggering:

> I found run `<run-id>` (failed) in `<org>/<repo>`. Retrigger it
> to verify `@v0` resolves?

Once confirmed:

```
gh run rerun <run-id> --failed --repo <org>/<repo>
```

Watch for the run to complete. A successful run confirms the new `v0`
tag is working.

### C. Present post-flight summary

Summarize results to the user:

| Org/Repo | `@v0` Refs | Status |
|----------|-----------|--------|
| org/.fullsend | Confirmed | Passing |
| org/target-repo | Confirmed | Passing |

Note any failures that are **unrelated** to the tag move (e.g. agent
runtime errors, external API issues) vs. failures caused by the
release.

---

## Notes

- **Pre-releases:** Tags with `-rc.N`, `-alpha.N`, or `-beta.N` suffixes are
  automatically marked as pre-releases by GoReleaser.
- **Never delete a published tag.** If a release is bad, cut a new patch or RC.
- **The changelog** is auto-generated from commit messages. Conventional commit
  prefixes (`feat:`, `fix:`, etc.) produce clean changelogs.
- **The `v0` tag** is a moving tag consumed by downstream orgs for reusable
  workflows. Always move it as part of the release process (step 8).
