---
title: "83. Repo-level foreign allow-list"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - mint
  - identity
  - oidc
  - least-privilege
  - cross-org
---

# 83. Repo-level foreign allow-list

Date: 2026-08-05

## Status

Accepted

Builds on [ADR 0060](0060-cross-org-mint-authorization-via-org-variables.md)
and [ADR 0078](0078-simplified-mint-authorization-policy.md).

## Context

[ADR 0060](0060-cross-org-mint-authorization-via-org-variables.md) introduced
cross-org mint authorization via org-level `FULLSEND_FOREIGN_<ROLE>_REPOS`
variables. This works well for broad org-wide grants but has two gaps:

1. **No repo-level granularity.** Operators cannot authorize a specific caller
   to mint tokens for *one* repository without authorizing them for the entire
   target org. Org-level FOREIGN grants produce installation-wide tokens,
   violating least privilege when only a single repo is needed.

2. **No intra-org cross-repo grants.** A per-repo caller
   (`PER_REPO_WIF_REPOS`) can only mint tokens scoped to its own repository.
   There is no mechanism for a per-repo caller to access another repository
   in the *same* org without adding the caller's org to `ALLOWED_ORGS` (which
   broadens access to all org-mode shapes) or setting up a cross-org flow
   with `target_org`.

Both gaps push operators toward overly broad grants.
[ADR 0078](0078-simplified-mint-authorization-policy.md) simplified the
authorization model by removing `PER_ORG_FOREIGN_COMPAT`, making this a
clean extension point.

## Decision

### Repo-level FOREIGN variable

The same `FULLSEND_FOREIGN_<ROLE>_REPOS` variable name used at the org level
can now be set as a **repo-level** GitHub Actions variable on any target
repository. The variable format is unchanged: comma-separated list of
`org/repo` (exact `repository` match) and/or bare `org`
(`repository_owner` match).

### Cross-org with specific repos

When a cross-org request specifies non-empty `repos` (e.g.,
`repos: ["target-repo"]`), the handler authorizes **exclusively** via
per-repo FOREIGN grants — the org-level variable is not consulted:

1. Check the **repo-level** `FULLSEND_FOREIGN_<ROLE>_REPOS` on each
   requested repository.
2. If **all** requested repos individually authorize the caller via their
   repo-level variables, mint proceeds scoped to those repos only.
3. If any requested repo does not authorize the caller, the request is denied.

Cross-org requests with `repos: ["*"]` (installation-wide) continue to use
only the org-level variable, unchanged from ADR 0060.

### Authorization boundary

Org-level and repo-level FOREIGN grants serve distinct scopes:

- **Org-level grant** → authorizes only installation-wide tokens
  (`repos: ["*"]`). Not consulted for repo-scoped requests.
- **Repo-level grant** → authorizes only the specific repos that set the
  variable. Not consulted for installation-wide requests.

### Intra-org cross-repo grants

Per-repo callers (`PER_REPO_WIF_REPOS`) requesting repos other than their
own within the same org are normally denied by `validateReposScope`. With
repo-level FOREIGN variables, the handler falls back to checking repo-level
grants when the scope check fails:

1. `validateReposScope` denies the per-repo caller for requesting non-self
   repos.
2. The handler checks repo-level `FULLSEND_FOREIGN_<ROLE>_REPOS` on each
   requested repo.
3. If all repos authorize the caller, the scope denial is overridden and
   the token is minted scoped to those repos.

This enables controlled intra-org cross-repo access without requiring the
caller's org to be in `ALLOWED_ORGS` and without changing the per-repo
caller's enrollment mode.

### Scope restriction

Repo-level grants restrict the minted token to the specific repos that set
the variable. A repo-level grant on `target-repo` does **not** authorize
access to other repos in the same org. This is the key difference from
org-level grants, which cover the entire org.

### Caching

Repo-level FOREIGN lookups use the same in-memory cache and inflight dedup
as org-level lookups, with a distinct cache key format:
`targetOrg/targetRepo/role` (vs `targetOrg/role` for org-level). Same TTL
(60 seconds), same negative caching.

### API permissions

Reading repo-level Actions variables requires `actions_variables: read`
(repo-level permission), distinct from the `organization_actions_variables:
read` used for org-level variables. The policy token for repo-level lookups
is scoped to the specific target repo.

## Consequences

- Operators can grant controlled cross-repo access at repo granularity
  without installation-wide tokens.
- Repo-level FOREIGN lookups add GitHub API calls (repo installation lookup,
  policy token creation, variable read) per target repo per uncached request.
  Cached lookups are free.
- Roles used with repo-level FOREIGN grants need `actions_variables: read`
  on their App permissions for the target repos.
- The `validateReposScope` constraint "foreign mint requires empty repos" is
  relaxed: cross-org requests may now specify non-empty repos when repo-level
  FOREIGN variables are set. Requests with `repos: ["*"]` continue to require
  org-level authorization.
- Per-repo callers gain a new authorization path for intra-org cross-repo
  access, gated by repo-level FOREIGN variables. This does not change the
  default behavior (without FOREIGN variables, per-repo callers are still
  restricted to their own repo).
- CLI management of FOREIGN variables (`fullsend admin foreign`) will need
  updates for repo-level support (tracked separately in
  [#2229](https://github.com/fullsend-ai/fullsend/issues/2229)).

### Related ADRs

| Topic | ADR |
|-------|-----|
| Cross-org mint authorization (org-level) | [0060](0060-cross-org-mint-authorization-via-org-variables.md) |
| Simplified mint authorization policy | [0078](0078-simplified-mint-authorization-policy.md) |
| Separate workflow-host allow-list | [0082](0082-workflow-host-allow-list.md) |
| Repos scope hardening | [0077](0077-mint-repos-scope-hardening.md) |
