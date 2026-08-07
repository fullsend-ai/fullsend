---
title: "77. Mint repos scope hardening with PER_ORG_FOREIGN_COMPAT"
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

# 77. Mint repos scope hardening with PER_ORG_FOREIGN_COMPAT

Date: 2026-08-02

## Status

Accepted

## Context

[ADR 0060](0060-cross-org-mint-authorization-via-org-variables.md) authorized cross-org minting via FOREIGN org variables and treated empty `repos` as installation-wide on both same-org and foreign paths. Same-org callers could also request arbitrary repo lists. That blast radius conflicts with least privilege in the [security threat model](../problems/security-threat-model.md): a compromised enrolled workflow could mint tokens for repos other than the one that authenticated.

Org-mode dispatch still needs limited exceptions — `.fullsend` callers minting across enrolled repos, and enrolled callers minting `{self,.fullsend}` — without restoring unrestricted same-org scope.

## Options

1. **Keep ADR 0060 defaults.** No code change; rely on WIF enrollment alone. Rejected: enrollment does not bound `repos` to the authenticating repository.
2. **Strict requesting-repo only, no exceptions.** Simplest rule. Rejected: breaks established `.fullsend` / org-mode dispatch shapes.
3. **Default-deny with an explicit compat flag.** Deny installation-wide and broad same-org lists by default; gate the known org-mode shapes behind `PER_ORG_FOREIGN_COMPAT`.

## Decision

After OIDC verification, the mint enforces `repos` scope as follows:

1. **Normalize** a sole `["*"]` entry to empty (alias for empty `repos` only). Mixed `*` lists remain invalid pattern input.
2. **Foreign (cross-org)** requests require empty `repos`. Non-empty lists are denied. FOREIGN gating from ADR 0060 still applies.
3. **Same-org** requests must list exactly the requesting repository's bare name. Empty `repos` (installation-wide) is always denied on the same-org path.
4. **`PER_ORG_FOREIGN_COMPAT`** (env / worker config; truthy = `1`/`true`/`yes`) unlocks only these same-org exceptions:
   - Caller repository `.fullsend`: any non-empty validated repo list
   - Other enrolled callers: exactly `[.fullsend]` or `{requestingBare, .fullsend}`
5. The effective flag is visible on `GET /v1/status` as `per_org_foreign_compat` and in `fullsend mint status` (from traffic env; absent = off).

This revises the installation-wide and unrestricted same-org `repos` consequences of ADR 0060; FOREIGN allowlists and `target_org` remain as decided there.

## Consequences

- Same-org workflows that omitted `repos` or listed unrelated repos break until they request only the calling repo, or operators enable `PER_ORG_FOREIGN_COMPAT` for the documented org-mode shapes.
- Foreign mints cannot carry a non-empty `repos` list; installation-wide foreign tokens remain possible only with empty `repos` plus FOREIGN authorization.
- Hosted and standalone mints must set `PER_ORG_FOREIGN_COMPAT` explicitly where org-mode dispatch is required; the default is off.
- ADR 0060's earlier allowance of same-org installation-wide tokens is superseded for `repos` policy by this decision (FOREIGN mechanism unchanged).

> **Later note:** [ADR 0083](0083-repo-level-foreign-allow-list.md) relaxes
> the foreign empty-repos constraint. Cross-org requests with non-empty
> `repos` are now permitted when each target repo has a repo-level
> `FULLSEND_FOREIGN_<ROLE>_REPOS` variable authorizing the caller.
