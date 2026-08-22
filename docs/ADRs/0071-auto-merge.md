---
title: "71. Auto-merge"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - github-apps
  - least-privilege
  - auto-merge
---

# 71. Auto-merge

Date: 2026-06-30

## Status

Accepted

## Context

Our review bot (`fullsend-ai-review`) can approve PRs, but GitHub only counts
an approval toward branch protection when the reviewer has write access
(`contents: write`). The review app has `contents: read` — giving it write
access would violate the least-privilege boundary in
[ADR 0007](0007-per-role-github-apps.md). See
[agent-architecture.md](../problems/agent-architecture.md) and
[security-threat-model.md](../problems/security-threat-model.md).

We also need the bot's approval to satisfy CODEOWNERS for dependency files
(`go.mod`, `go.sum`). GitHub App bots cannot appear as CODEOWNERS entries.

The review agent implementation now lives in `fullsend-ai/agents`, but
auto-merge touches core design concerns — GitHub App identity boundaries,
credential isolation, and CODEOWNERS policy — that are central architectural
decisions. This ADR records the decision here; implementation follows in the
agents repo.

## Options

### A. Add `contents: write` to the review app

Collapses the permission boundary between review and implementation roles.
Every repo that installs the review app would grant it write access it doesn't
otherwise need.

### B. Create a separate merge agent

A new agent with its own harness config, slug, and app identity
(`fullsend-ai-merge`). Repos opt in by installing the merge app. Clean
separation, but duplicates orchestration logic and adds a new agent to
maintain.

### C. New app identity, same review agent, env-var toggle

Create a `fullsend-ai-merge` app with `contents: write` and
`pull_requests: write`, but teach the existing review agent to auto-merge when
an env var (`REVIEW_AUTO_MERGE=true`) is set. Repos that want auto-merge
install the merge app and set the var; repos that want read-only reviews keep
`fullsend-ai-review` unchanged.

### D. Exempt dependency files via blank-owner CODEOWNERS entries

Use [blank-owner entries](https://github.com/orgs/community/discussions/23064)
to remove the CODEOWNERS requirement for specific files. Orthogonal to the app
permission question — addresses the CODEOWNERS gate, not the approval gate.

## Decision

Use options C and D together.

1. **Create a `fullsend-ai-merge` app** with `contents: write` and
   `pull_requests: write`. Repos that want bot-driven auto-merge install this
   app alongside or instead of `fullsend-ai-review`.

2. **Add auto-merge behavior to the review agent**, gated behind
   `REVIEW_AUTO_MERGE`. When the var is set and the review verdict is
   `approve`, the post-script enables GitHub auto-merge on the PR. No new
   agent harness — same `review.yaml`, same skills, same post-script.

3. **Use blank-owner CODEOWNERS entries** for files bots should be able to
   approve (starting with `go.mod` and `go.sum`).

This is the most **reversible** path. If a dedicated merge agent turns out to
be the better UX, having once had a more capable review agent does not make
that harder — we stand up the new agent and deprecate the env var. Going the
other direction (standalone agent first, then collapsing back) would be harder
because the standalone agent accumulates its own orchestration logic that must
be reconciled.

### Token handoff

When `REVIEW_AUTO_MERGE` is set, the harness mints a token from the
`fullsend-ai-merge` app instead of `fullsend-ai-review`. If the merge app is
not installed on the repo, the agent fails hard — this is a misconfiguration.
Normal review operations (reading code, posting comments) do not change; only
the auto-merge step requires the elevated token.

### Security considerations

- **Credential isolation.** The merge app token is minted per-run and scoped to
  the triggering repo. The review agent never holds both the merge token and a
  provider credential simultaneously — sandbox isolation
  ([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)) keeps them
  separated.
- **Prompt injection blast radius.** A compromised review agent with the merge
  token can enable auto-merge on the PR it was invoked for. It cannot merge
  arbitrary PRs — branch protection (required reviewers, status checks) still
  gates the actual merge. The blast radius is one PR, same as today.
- **CODEOWNERS bypass scope.** Blank-owner entries only remove the code-owner
  review requirement for the listed paths (`go.mod`, `go.sum`). CI status
  checks remain the primary safety gate for those files.
- **`REVIEW_AUTO_MERGE` delivery protection.** The env var is set in the
  repo's workflow file, not by the agent. An attacker who can modify the
  workflow file already has write access to the repo.

### Implementation deferral

Implementation of `REVIEW_AUTO_MERGE` in `review.yaml` and `post-review.sh`
is deferred to a follow-up PR. This ADR records the decision; the follow-up
wires it up.

## Consequences

- The review app stays read-only — no permission creep.
- Auto-merge is opt-in per repo, visible in the GitHub App installation list.
- No new agent to maintain — auto-merge is a small addition to `post-review.sh`.
- If the env-var approach proves wrong, standing up a dedicated agent is not made harder by this decision.
- Blank-owner CODEOWNERS entries remove code-owner review for listed files; CI status checks remain the primary safety gate.
- The merge app's `contents: write` is a higher-value target if compromised — same mitigations from [ADR 0007](0007-per-role-github-apps.md) apply.
