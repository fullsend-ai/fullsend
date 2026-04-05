---
title: "14. Admin install: GitHub Apps and `.fullsend` credential surface (v1)"
status: Proposed
relates_to:
  - governance
  - agent-architecture
  - repo-readiness
topics:
  - admin-install
  - github-apps
  - secrets
---

# 14. Admin install: GitHub Apps and `.fullsend` credential surface (v1)

Date: 2026-04-05

## Status

Proposed

## Context

The admin CLI creates or reuses per-role GitHub Apps and must persist the minimum credentials for Actions and agents to authenticate as those apps. [ADR 0012](0012-admin-install-fullsend-repo-files-v1.md) (when accepted) will fix the org config repo’s files and layout; [ADR 0013](0013-admin-install-enrollment-v1.md) (when accepted) will fix how repos are enrolled. This decision isolates **app lifecycle outcomes** and **where secrets and variables live**, so tooling and docs can stay aligned without bundling enrollment or repo file layout.

## Decision

**Adopt credential surface v1** as specified in [SPEC.md](../normative/admin-install/v1/adr-0014-github-apps-and-secrets/SPEC.md): expected app slugs and manifest/install behavior per role, and repository secrets `FULLSEND_<ROLE>_APP_PRIVATE_KEY` plus variables `FULLSEND_<ROLE>_APP_ID` on the `.fullsend` repo, with uppercase role suffixes matching `internal/layers/secrets.go` and `internal/cli/admin.go`.

## Consequences

- Any change to secret/variable names or install outcomes must update the SPEC and this ADR together.
- Adopters can grep `.fullsend` for `FULLSEND_` to audit stored app credentials without reading Go source.
- Client secret and webhook secret from the manifest flow remain outside this v1 repo surface unless a future ADR extends the surface.
