---
title: "62. Auto-merge"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - github-apps
  - least-privilege
  - auto-merge
---

# 62. Auto-merge

Date: 2026-06-30

## Status

Accepted

## Context

Our review bot (`fullsend-ai-review`) can approve PRs, but GitHub only counts
an approval toward branch protection when the reviewer has write access
(`contents: write`) to the repository. The review app intentionally has
`contents: read` — giving it write access would violate the least-privilege
boundary established in [ADR 7](0007-per-role-github-apps.md).

We also need the bot's approval to satisfy CODEOWNERS for dependency files
(`go.mod`, `go.sum`). GitHub App bots cannot be listed as CODEOWNERS entries —
they are not "users" in GitHub's model.

## Options

### A. Add `contents: write` to the review app

Collapses the permission boundary between review and implementation roles.
Every repo that installs the review app would grant it write access it doesn't
otherwise need.

### B. Create a separate `fullsend-ai-merge` app

A new app with `contents: write` and `pull_requests: write`. Repos opt in to
auto-merge by installing this app instead of (or alongside) the review app.
The trust escalation is explicit and visible in GitHub's UI.

### C. Exempt dependency files via blank-owner CODEOWNERS entries

Use [blank-owner entries](https://github.com/orgs/community/discussions/23064)
to remove the CODEOWNERS requirement for specific files. This is orthogonal to
the app permission question — it addresses the CODEOWNERS gate, not the
approval-counts gate.

## Decision

Use options B and C together:

1. **Create a `fullsend-ai-merge` app** with `contents: write` and
   `pull_requests: write`. Its approvals count toward branch protection. Repos
   that want bot-driven auto-merge install this app; repos that only want
   informational reviews keep `fullsend-ai-review`.

2. **Use blank-owner CODEOWNERS entries** for files that bots should be able to
   approve (starting with `go.mod` and `go.sum`). This removes the CODEOWNERS
   gate for those files without affecting other paths.

## Consequences

- The review app stays read-only — no permission creep.
- Auto-merge is an explicit opt-in per repo, visible in the GitHub App installation list.
- Blank-owner CODEOWNERS entries remove code-owner review for the listed files; CI status checks remain the primary safety gate for those paths.
- Adding a new app increases the number of apps to manage and enroll per org.
- The merge app's `contents: write` permission is a higher-value target if compromised — the same mitigations from [ADR 7](0007-per-role-github-apps.md) apply (repo-scoped install, PEM in config repo secrets).
