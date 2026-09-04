---
title: "75. Lite auth mode"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - identity
  - per-repo
  - least-privilege
---

# 75. Lite auth mode

Date: 2026-07-22

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Per-repo installation ([ADR 0033](0033-per-repo-installation-mode.md)) historically required provisioning per-role GitHub Apps ([ADR 0007](0007-per-role-github-apps.md)) and a token mint ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)), creating nontrivial setup friction. While the Per-role App + Mint architecture remains the default for scaled, highly isolated environments, some per-repo adopters need a zero-ceremony opt-in path using the default `secrets.GITHUB_TOKEN`.

A design spike investigated using `secrets.GITHUB_TOKEN` (`github-actions[bot]`). Because GitHub suppresses events triggered by `GITHUB_TOKEN`, standard stage handoffs (`labeled`, `pull_request_review.submitted`) cannot initiate subsequent workflow steps natively. However, `workflow_dispatch` is exempt from event suppression and can be invoked securely to chain stages without over-privileged scopes (maintaining `contents: read` for triage and review, per `internal/mintcore/github.go`).

Additionally, native auto-merge requires `APPROVE` reviews, which fail with a 422 error if the same identity that authored the PR (e.g., `GITHUB_TOKEN`) tries to approve it. Exchanging a Bring-Your-Own-App (BYOA) PEM for a separate token bypasses this restriction, but exposes the PEM to prompt-injection or runner-compromise threats if not properly isolated.

## Options

- **`GITHUB_TOKEN` with native event triggers:** Rejected because GitHub suppresses downstream workflows triggered by `GITHUB_TOKEN` events.
- **`GITHUB_TOKEN` with `repository_dispatch` handoffs:** Rejected because `repository_dispatch` requires `contents: write` on triage/review, violating least-privilege role definitions.
- **`GITHUB_TOKEN` with `workflow_dispatch` handoffs:** Chosen. Only requires `actions: write` (already held), preserving `contents: read` for triage/review.
- **Single self-owned App with native handoffs (no mint):** Rejected for default lite mode because storing the PEM as a static secret introduces standing-credential risk.
- **Bring Your Own App (BYOA) for Review:** Chosen for auto-merge. Bypasses the 422 self-approval error by using a dedicated Reviewer App token for the review approval step.

## Decision

Introduce **Lite Auth Mode** as an opt-in alternative to the default Per-role GitHub App + Mint architecture for per-repo installations. In Lite Auth Mode:

1. **Authentication:** The default `GITHUB_TOKEN` is used for triage, code, and review steps instead of a mint-provided token.
2. **Handoffs:** Stage handoffs use `workflow_dispatch` (`gh workflow run`) rather than `repository_dispatch` or native events. This requires only `actions: write`, allowing triage and review to maintain least-privilege `contents: read`. `post-review.sh` and `post-retro.sh` will also be updated to use `workflow_dispatch` to close pipeline loops.
3. **Auto-Merge (BYOA):** To bypass GitHub's self-approval 422 restriction, operators can provide a dedicated "Reviewer" App ID and PEM as repository secrets (`FULLSEND_REVIEWER_APP_ID`, `FULLSEND_REVIEWER_APP_PEM`).
4. **BYOA Security Architecture:** To mitigate prompt-injection threats, the Reviewer App token **MUST** be minted in an isolated workflow step or job *after* the LLM agent has safely exited, ensuring the PEM is never exposed to the LLM execution context.

## Consequences

- Per-repo installations can adopt Lite Auth Mode with zero GitHub-credential secrets, though GCP inference credentials are still required.
- Auto-merging PRs requires provisioning a dedicated Reviewer App to bypass the 422 self-approval restriction.
- Isolating the Reviewer App token exchange in a post-execution job eliminates the threat of exposing the PEM to an untrusted LLM context.
- Triage and review jobs maintain least-privilege `contents: read` scopes, while only code and fix carry `contents: write`.
- The reusable-dispatch workflow and shim configurations require updating to handle `workflow_dispatch` inputs and routing hints.
- Per-role least-privilege scoping is lost at the identity level, but GHA job-level permissions still restrict the ephemeral `GITHUB_TOKEN`.
- Lite Auth Mode remains a strictly opt-in mechanism, preserving the Per-role GitHub App + Mint mode as the default for scaled installations.
