---
name: cutting-releases
description: >
  Use when the user wants to tag a release, cut a release candidate, or ship a
  new version. Also use when asking about release process, versioning, or how
  GoReleaser is configured.
---

# Cutting Releases

Releases are driven by annotated git tags. When a tag matching `v*` is pushed,
the `.github/workflows/release.yml` workflow runs GoReleaser, which builds
binaries, generates a changelog, and creates the GitHub release. The release
title comes from the tag annotation via `name_template` in `.goreleaser.yml`.

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

The answer becomes the tag subject line. If blank, the tag message is just the
version number.

### 5. Gather changes since last tag

```
git log --oneline <previous-tag>..HEAD
```

Summarize the changes into categories (features, fixes, refactors). Exclude
commits that start with `docs:`, `test:`, or `chore:` — GoReleaser filters
these from the changelog anyway.

### 6. Create the annotated tag

Build the tag message:

- **Line 1 (subject):** The tag subject from step 4, or just the version.
- **Lines 3+:** Summary of highlights organized by category.

```
git tag -a v0.X.0 -m "<message>"
```

The first line of the annotation flows into the GitHub release title via
GoReleaser's `name_template: "{{ .Tag }}{{ if .TagSubject }}: {{ .TagSubject }}{{ end }}"`.

### 7. Push the tag

```
git push origin <tag>
```

GoReleaser takes over from here. Verify the workflow starts:

```
gh run list --workflow=release.yml --limit=1
```

### 8. Verify the release

Once the workflow completes, confirm the release was created:

```
gh release view <tag>
```

Check that the title, changelog, and binary assets look correct.

## Notes

- **Pre-releases:** Tags with `-rc.N`, `-alpha.N`, or `-beta.N` suffixes are
  automatically marked as pre-releases by GoReleaser.
- **Never delete a published tag.** If a release is bad, cut a new patch or RC.
- **The changelog** is auto-generated from commit messages. Conventional commit
  prefixes (`feat:`, `fix:`, etc.) produce clean changelogs.
