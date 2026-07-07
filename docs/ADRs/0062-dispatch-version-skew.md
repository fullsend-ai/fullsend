---
title: "62. Resolving per-repo dispatch version skew"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - versioning
  - workflows
  - per-repo
  - dispatch
---

# 62. Resolving per-repo dispatch version skew

Date: 2026-06-25

## Status

Accepted

## Context

[ADR 48](0048-automatic-updates.md) decided that Fullsend offers two tags to
track updates: version (`vMAJOR.MINOR.PATCH`) and moving (`latest`). During
its implementation a major problem was found: in per-repo mode
`reusable-dispatch.yml` has hardcoded references to `reusable-<stage>.yml`
at `v0`. This is what we call "version skew". The version skew does not happen
on per-org mode, as organizations reference `reusable-<stage>.yml` directly.

This ADR presents a few options to solve this problem and recommends a solution.

## Options

### A. Re-introduce `reusable-dispatch.yml` to the user repository

Move `reusable-dispatch.yml` back into the enrolled repo so the `uses:` ref
can be templated at install time by the CLI.

**Rejected** We extracted dispatch specifically to reduce update noise in
user repos. Re-introducing it undoes that benefit.

### B. Convert dispatch to a composite action

Replace the dispatch workflow with a composite action that handles routing
and agent execution in a single job.

**Rejected** Composite actions cannot spawn separate jobs, so it is not
possible.

### C. Release branches with ref rewriting

Change the release process to create a release branch where `@v0` references
in `reusable-dispatch.yml` are rewritten to `@vX.Y.Z` before tagging.

1. Branch from `main`.
2. Rewrite all `uses: ...@v0` to `uses: ...@vX.Y.Z` in `reusable-dispatch.yml`.
3. Update default values for `fullsend_version`, etc.
4. Commit, tag the branch with `vX.Y.Z` and `latest`.

**Rejected** Introduces significant repository-level complexity.

### D. Merge stage workflows into dispatch

Inline all six stage workflows as conditional jobs directly inside
`reusable-dispatch.yml`. The `uses:` lines to stage workflows disappear
entirely.

This impacts per-org mode as its workflows reference directly `reusable-<stage>.yml`.

**Accepted** Removes the problem completely at the cost of a large file. This file
can be eventually simplified to make it easier to handle.

## Decision

The decision is to merge the stage workflows into the dispatch workflow to avoid the
version skew it introduced in the first place.

However, as per-org mode is deprecated (see ADR 44),
`reusable-<stage>.yml` will be kept to allow per-org mode to continue to work. When
per-org mode is removed, then `reusable-<stage>.yml` files will be removed. During
the deprecation period `reusable-<stage>.yml` files need to be in sync
with the merged `reusable-dispatch.yml`.

## Implementation

See [merge-stage-workflows plan](../plans/merge-stage-workflows.md).

## Consequences

* `reusable-<stage>.yml` stage logic is inlined into `reusable-dispatch.yml`; standalone files are retained until per-org mode is removed per ADR 44.
* `reusable-dispatch.yml` grows significantly in size.
* `reusable-dispatch.yml` and `reusable-<stage>.yml` must stay in sync during the deprecation period.
