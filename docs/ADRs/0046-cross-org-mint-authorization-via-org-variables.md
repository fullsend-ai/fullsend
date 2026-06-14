---
title: "46. Cross-org mint authorization via org variables"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - identity
  - oidc
  - github-apps
  - cross-org
---

# 46. Cross-org mint authorization via org variables

Date: 2026-06-07

## Status

Accepted

## Context

The central token mint ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)) issues
short-lived GitHub App installation tokens to OIDC-authenticated workflows. Today the mint
scopes tokens to the caller's `repository_owner`: the App installation lookup and PEM
lookup both use the org from the OIDC `repository` claim.

Some workloads need to act on a **different** org than the workflow's owner. The e2e
test pool ([ADR 0040](0040-org-pool-for-parallel-e2e-tests.md)) runs CI from
`fullsend-ai/fullsend` but mutates dedicated pool orgs (`halfsend-01`, …). Future
cross-org agent flows ([#672](https://github.com/fullsend-ai/fullsend/issues/672),
[#1916](https://github.com/fullsend-ai/fullsend/issues/1916)) have the same shape.

The target org must explicitly authorize which foreign repos or orgs may request tokens
for a given role. A mint-operator central allowlist does not scale and does not give target
orgs control over their own policy.

## Decision

1. **Optional `target_org` on mint requests.** When omitted, or when equal to the caller's
   `repository_owner` (case-insensitive), behavior is unchanged from pre-0046 mint: same
   `mintToken` path, repo-based installation lookup, no FOREIGN check.

2. **Cross-org path** applies only when `target_org` is set and differs from the caller org:
   - Resolve the requested role's App installation on `target_org` via org-level installation lookup.
   - Read `FULLSEND_FOREIGN_<role>_REPOS` on the target org using that role's App installation
     token (`actions_variables: read`).
   - Deny if installation lookup fails, the variable is missing/empty, or the OIDC caller
     (`repository` or bare `repository_owner`) is not on the allowlist.
   - Mint a scoped installation token for the requested repos on the target org.

3. **Variable format.** Org-level GitHub Actions variable on the **target** org:
   - Name: `FULLSEND_FOREIGN_<ROLE>_REPOS` (uppercase role suffix, per [ADR 0014](0014-admin-install-github-apps-secrets-v1.md))
   - Value: comma-separated list of `org/repo` (exact `repository` match) and/or bare `org`
     (`repository_owner` match)

4. **Role-agnostic mechanism.** Any allowed role may use the cross-org path when the target
   org has installed that role's App and configured the FOREIGN variable. The `e2e` role
   ([#2155](https://github.com/fullsend-ai/fullsend/issues/2155)) is the first consumer.

5. **CLI.** `fullsend admin foreign allow|list|revoke` manages FOREIGN variables on a target org.

## Consequences

- Cross-org mint requests add GitHub API calls (FOREIGN variable cached with short TTL).
- Roles used on the cross-org path need `actions_variables: read` on their App permissions.
- Target orgs opt in by installing the role App and setting the FOREIGN allowlist.
- Same-org mint for enrolled orgs is unchanged: zero new API calls or permission changes.
- Pool org provisioning must install the e2e App and set `FULLSEND_FOREIGN_E2E_REPOS` for CI callers.
