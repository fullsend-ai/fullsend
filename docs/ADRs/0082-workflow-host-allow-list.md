---
title: "82. Separate workflow-host allow-list from caller allow-list"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - mint
  - identity
  - oidc
  - least-privilege
---

# 82. Separate workflow-host allow-list from caller allow-list

Date: 2026-08-04

## Status

Accepted

Builds on [ADR 0078](0078-simplified-mint-authorization-policy.md).

## Context

Mint trust previously tangled two distinct allow-lists:

1. **Callers** — which orgs/repos may request tokens (`ALLOWED_ORGS`,
   `PER_REPO_WIF_REPOS`, and related enrollment paths).
2. **Workflow hosts** — which repos may appear in `job_workflow_ref` as
   the source of workflows that call the mint.

The coupling made it hard to allow a repo to *obtain* tokens without also
treating it as a trusted place to *host* minting workflow code (and the
reverse). For example, per-repo callers listed in `PER_REPO_WIF_REPOS`
were automatically accepted as workflow hosts via `ValidateWorkflowRef`,
which checked the caller's own repository as a valid workflow source.

Installs that vendor reusable workflows into a consumer repo (e.g.
`github setup --vendor`) change `job_workflow_ref` to that consumer repo.
Those workflows cannot mint unless the consumer is explicitly listed as a
workflow host — which was previously only possible by adding it to
`PER_REPO_WIF_REPOS`, conflating caller enrollment with workflow-host
trust.

## Decision

### Separate controls

Introduce `WORKFLOW_HOST_REPOS`, a new environment variable listing repos
whose workflows are trusted to call the mint in per-repo mode. This is
independent of `ALLOWED_ORGS` and `PER_REPO_WIF_REPOS`.

### Per-repo mode

Per-repo callers (those in `PER_REPO_WIF_REPOS`) have their
`job_workflow_ref` validated against `WORKFLOW_HOST_REPOS`. The upstream
repo (`fullsend-ai/fullsend`) is always accepted. When
`WORKFLOW_HOST_REPOS` is not set, it defaults to `fullsend-ai/fullsend`
only.

### Per-org mode

Per-org callers (those whose `repository_owner` is in `ALLOWED_ORGS`)
have their `job_workflow_ref` hard-wired to two sources:
- The caller's own org `.fullsend` config repo (`{org}/.fullsend`)
- The upstream `fullsend-ai/fullsend` repo

No separate allow-list is consulted. This matches the operational model
where per-org installs rely on `{org}/.fullsend` as their workflow host.

### Dual enrollment

When a caller is both an enrolled repo (`PER_REPO_WIF_REPOS`) and its
org is an enrolled org (`ALLOWED_ORGS`), **both** workflow-ref validation
modes apply. The workflow may come from:
- Per-repo sources: any repo in `WORKFLOW_HOST_REPOS` (plus upstream)
- Per-org sources: `{org}/.fullsend` config repo (plus upstream)

The handler tries per-org validation first, then falls back to per-repo
validation. If either succeeds, the workflow ref is accepted. Scope
treatment uses per-org mode (the superset) — dual enrollment only
expands the set of accepted workflow hosts.

### Public mode

Public mode (`PER_REPO_WIF_REPOS=*`) uses the same per-repo validation
path — `WORKFLOW_HOST_REPOS` and the `ALLOWED_WORKFLOW_FILES` basename
gate both apply. The only difference between public and tight per-repo
mode is caller enrollment: `PER_REPO_WIF_REPOS=*` means every repo is
accepted as a caller without explicit listing.

> **Note:** [ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md)
> dropped the basename gate for public mode ("Basename gate: that restriction
> was dropped"). This ADR supersedes that exception: public mode now
> applies `ALLOWED_WORKFLOW_FILES` via the shared per-repo validation path.

### CLI and status surfaces

- `fullsend mint workflow-host add|remove|list` manages the
  `WORKFLOW_HOST_REPOS` env var on the mint.
- `fullsend mint status` displays the effective workflow-host allow-list.
- `GET /v1/status` includes `workflow_host_repos` in the response.

## Consequences

- Operators can grant a repo caller access (via `PER_REPO_WIF_REPOS`)
  without implicitly trusting it as a workflow host. Workflow-host trust
  requires an explicit `WORKFLOW_HOST_REPOS` entry or the use of upstream
  workflows.
- Vendored workflow installs (`--vendor`) require a one-time admin action
  to add the vendored-workflow repo to `WORKFLOW_HOST_REPOS` before those
  workflows can mint tokens.
- Existing per-repo callers that previously relied on hosting their own
  workflows (via the old `ValidateWorkflowRef` logic that accepted the
  caller's own repo from `PER_REPO_WIF_REPOS`) must either switch to
  upstream workflows or be added to `WORKFLOW_HOST_REPOS`.
- Per-org callers see no behavior change — their workflow host validation
  was already restricted to `.fullsend` and upstream.
- The default `WORKFLOW_HOST_REPOS` value (`fullsend-ai/fullsend`) matches
  the previous behavior for callers using upstream workflows.

### Related ADRs

| Topic | ADR |
|-------|-----|
| Simplified mint authorization policy | [0078](0078-simplified-mint-authorization-policy.md) |
| Public mint mode (basename gate exception superseded above) | [0059](0059-public-mint-mode-with-wildcard-allowlists.md) |
| Test-only `owner/prefix*` host patterns for behaviour-test preview mints | [0084](0084-lease-uniquely-named-repos-for-dev-behaviour-tests.md) |
