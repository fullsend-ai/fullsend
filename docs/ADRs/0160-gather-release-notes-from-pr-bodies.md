---
title: "160. Gather release notes from PR bodies"
status: Accepted
relates_to:
  - contributor-guidance
topics:
  - release-process
  - contributor-experience
---

# 160. Gather release notes from PR bodies

Date: 2026-11-06

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

GoReleaser builds the release notes from the subject line of every commit in
the release range. It groups the commits by Conventional Commits type.
[COMMITS.md](../../COMMITS.md) documents the prefix rules, and gitlint
enforces them.

This approach has two problems:

1. A commit subject is a poor release note. The author writes it for
   reviewers, not for users. It has no room for migration steps or context.
2. The breaking-change warning depends on the `!` marker in every commit
   subject. When one commit lacks the marker, the warning disappears from the
   release notes. This has caused real incidents (#8366).

The Kubernetes project solves this differently. Every PR carries a
`release-note` block in its body. CI checks that the block exists. At release
time, a tool reads the blocks from the merged PRs and builds the notes from
them.

This ADR aims to make release notes prose written for users, and to keep
breaking-change warnings from disappearing silently.

## Options

1. **Keep the commit-subject changelog.** Nothing changes for contributors.
   The two problems above remain.
2. **Use `release-note` blocks in PR bodies.** The release pipeline gathers
   the blocks at release time. This adds a requirement to every PR.
3. **Write the notes by hand at release time.** This gives the best text. But
   the release author must re-read every change in the range, which does not
   scale.

## Decision

We adopt `release-note` blocks in PR bodies as the source for release notes.

Every PR must contain a fenced `release-note` block in its body. The block
holds one or more lines of prose written for users. Write `NONE` when the
change has no effect users can see. Start the block with `action required`
when the change breaks something. A CI check rejects a PR whose block is
missing or empty. The check also rejects a `NONE` block when the PR title or
any commit subject carries the `!` marker: such a PR must start its block
with `action required`. This also applies to PRs that autonomous agents open,
because agents author many of our PRs.

At release time, the release pipeline reads the blocks from the merged PRs in
the tag range, skips `NONE` entries, and renders markdown. That markdown
replaces GoReleaser's commit-subject changelog. The pipeline passes it to
GoReleaser through its release-notes file input. GoReleaser keeps its job of
building, signing, and publishing artifacts.

This ADR decides the contract, not the tool. Any mechanism that reads the
blocks from merged PRs satisfies it. The Kubernetes `release-notes` tool is
one candidate, but the pipeline can swap the mechanism without a new ADR.

Conventional Commits prefixes stay. Gitlint keeps enforcing them. They no
longer feed the release notes.

## Forge scope

GitHub only. Fullsend cuts its own releases from the GitHub repository, so
this decision affects that pipeline alone. GitLab and Forgejo are forges that
Fullsend operates on as a product. How this repository builds its release
notes does not affect them.

## Consequences

- Every PR must include a valid `release-note` block, whether a human or an
  agent wrote it. CI enforces this. The PR template carries the block with a
  `NONE` default.
- Release notes become prose written for users. A breaking change is visible
  as soon as its PR exists, instead of depending on commit-subject markers.
- The release pipeline must map commits in the tag range to their merged
  PRs. Whatever mechanism does this needs the GitHub API.
- The `!` marker in commit subjects and PR titles stays required, per
  COMMITS.md and gitlint. A PR that carries the marker cannot ship without an
  `action required` entry, so a missing marker on one commit no longer hides
  the breaking change. A breaking change that carries no marker anywhere
  still needs human or agent review to catch.
- Releases cut before this decision keep their commit-subject changelogs.
  Only later releases use the gathered notes.
