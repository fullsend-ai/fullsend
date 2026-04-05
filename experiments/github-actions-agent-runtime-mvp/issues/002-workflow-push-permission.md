# 002: Fix Agent Workflow Push Permission (Resolved)

## Problem

The fix agent's GitHub App token lacked the `workflows` write permission, causing `git push` to fail silently when commits included `.github/workflows/` changes. The agent reported success but no commit appeared on the PR.

## Resolution

Granted `workflows` write permission on the `fullsend-agent[bot]` GitHub App. CODEOWNERS rule on `.github/workflows/` ensures human approval is still required at merge time — the agent can push workflow changes, but they can't be merged without owner review.

## Guardrails

- CODEOWNERS: `.github/workflows/` requires owner group approval
- The fix agent can iterate on workflow files during the review/fix loop
- Final merge is gated on human approval for any PR touching workflows
