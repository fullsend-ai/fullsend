---
title: "55. Label-gated elevated agent permissions"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - authorization
  - github-apps
  - workflows
  - code-agent
---

# 55. Label-gated elevated agent permissions

Date: 2026-06-23

## Status

Accepted

## Context

The coder agent cannot push `.github/workflows/` changes because `workflows: write` is intentionally withheld from default coder tokens ([ADR 0007](0007-per-role-github-apps.md)). Issue [#470](https://github.com/konflux-ci/fullsend/issues/470) needs workflow edits without granting every code run elevated permissions.

Prior approaches that verified authorization inside the mint service or via prompt instructions were rejected over injection risk. The threat model prioritizes external injection over insider abuse.

## Decision

Introduce a reusable `authorization.Gate` registry. The first gate, `workflow-change`, uses:

- `workflow-change-needed` — triage signals anticipated workflow work
- `workflow-change-allowed` — a repository collaborator authorizes elevation

**Label verification runs in enrolled workflows and post-scripts via `fullsend auth check`.** Mint accepts an optional `elevations` list and mechanically merges each gate's `MintElevation` permissions into the role baseline. Mint does not read issues or labels.

Gates enforce at three phases:

| Phase | When | Outcome if blocked |
|-------|------|--------------------|
| `mint` | Before token mint | Mint proceeds without elevation (exit 10/11 posts comment) |
| `pre-run` | Before sandbox | Agent skipped |
| `pre-push` | Before git push | Push blocked; `workflow-change-needed` applied |

After `workflow-change-allowed` is applied, a subsequent non-collaborator agent-influencing comment (e.g. `/fs-*`) invalidates the label (stale check).

The coder GitHub App manifest includes `workflows: write` as a **ceiling** permission; mint grants it only when the workflow passes `elevations: ["workflow-change"]` after a successful auth check.

## Consequences

- Unblocks workflow edits for enrolled agents without org-wide `workflows: write` on every code run.
- Existing orgs must approve the new coder app permission in GitHub org settings and re-approve the installation; `fullsend admin` surfaces stale-permission warnings.
- Future elevated permissions can reuse the same gate + CLI + elevations pattern.
- Defense in depth: label gate (CLI) + mint downscoping + pre-push enforcement + review protected-path rules.
- Elevation spoofing via direct mint API calls is mitigated by OIDC audience and `job_workflow_ref` allowlisting — only enrolled agent workflows reach mint.
