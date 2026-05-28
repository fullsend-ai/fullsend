---
title: "43. Public mint mode with wildcard allowlists"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - oidc
  - token-mint
  - deployment
  - reusable-workflows
  - github-apps
---

# 43. Public mint mode with wildcard allowlists

Date: 2026-05-27

## Status

Accepted

## Context

[ADR 0029](0029-central-token-mint-secretless-fullsend.md) establishes a central token mint that exchanges GitHub OIDC tokens for short-lived, role-scoped installation tokens. Today the mint is configured with **explicit** org and repo registries (`ALLOWED_ORGS`, `PER_REPO_WIF_REPOS`) and **fail-closed** workflow provenance (`job_workflow_ref` prefixes for `{org}/.fullsend/`, `fullsend-ai/fullsend/`, or registered per-repo workflows). That model fits **self-managed** and **single-tenant** deployments where the operator curates every org and repo.

A **public** (multi-tenant) mint profile is needed so many orgs can share one mint endpoint without pre-registering each tenant in env vars on every install. `ALLOWED_ORGS=*` enables that openness at the mint application layer and implies a permissive STS provider behind existing `WIF_PROVIDER_NAME` routing (see below). Provenance must shift to **trusted upstream workflow code** in `fullsend-ai/fullsend` ([ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md)), because arbitrary customer workflows must not be able to call a globally reachable mint.

[ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) **decides** per-org event-driven dispatch: static `workflow_call` jobs in `dispatch.yml` call `reusable-{stage}.yml` in `fullsend-ai/fullsend` (same property as per-repo `reusable-dispatch.yml`), replacing `gh workflow run`, org-local thin callers, and the `# fullsend-stage:` scanner. That is the org-side prerequisite for public mint provenance—mint no longer needs to accept `{org}/.fullsend/` in `job_workflow_ref`.

Explicit allowlists must remain available so the same mint implementation can run in **tight** mode (no wildcards) for private or regulated deployments. [ADR 0035](0035-layered-content-resolution.md) customizes agent **content** (harness, skills, policies) at runtime; it does not define mint trust.

## Options

### Tight mint only (status quo)

Keep explicit `ALLOWED_ORGS` and `PER_REPO_WIF_REPOS` only; no `*` semantics. Public multi-tenant adoption requires operational registration on every install.

**Rejected** for the public profile: does not meet the goal of a shared mint endpoint without per-tenant env churn.

### Public mint with wildcards and upstream-only workflow provenance

Introduce a **public mint mode** when `ALLOWED_ORGS` contains `*`, with `job_workflow_ref` restricted to workflows under `fullsend-ai/fullsend/.github/workflows/`. Explicit org/repo lists without `*` continue to denote **tight** deployments.

**Chosen.**

## Decision

1. **Public vs tight mode.** There is no separate `MINT_TRUST_MODE` flag.
   - **Public mint:** `ALLOWED_ORGS` contains `*`. Any `repository_owner` passing other checks may request tokens.
   - **Tight mint:** `ALLOWED_ORGS` is a comma-separated list of orgs and does **not** contain `*`. Only listed orgs pass `checkAllowedOrg`.
   - **Deny lists:** Not supported. Tighter scope is achieved by omitting `*` and listing orgs/repos explicitly, not by mixing `*` with exclusions.

2. **Public STS provider via `WIF_PROVIDER_NAME`.** The GCF mint validates GitHub OIDC by exchanging the JWT with GCP STS against a WIF provider id from `resolveWIFProvider`. That step is **mint authentication only**—it must not be used for LLM / Vertex access. Agents continue to use `FULLSEND_GCP_WIF_PROVIDER` (repo/org-scoped providers and IAM from install), never the mint’s STS provider.

   **`WIF_PROVIDER_NAME` (existing):** Required on the Cloud Function. It is the **default** provider id used when a repo is **not** listed in `PER_REPO_WIF_REPOS` (and not `{org}/.fullsend`, which also uses the default). No new env var is required for public mode.

   **Tight mint:** `WIF_PROVIDER_NAME` points at the org-merged provider (CEL lists known orgs). `PER_REPO_WIF_REPOS` may list specific `owner/repo` values so those repos use per-repo provider ids (`gh-{owner}-{repo}`) instead of the default.

   **Public mint:** Operators provision a **single permissive** WIF provider (CEL does not enumerate orgs/repos) and set **`WIF_PROVIDER_NAME` to that provider’s id**. Leave `PER_REPO_WIF_REPOS` **unset or empty** so every `repository` claim falls through to the default—matching current `resolveWIFProvider` behavior without code changes. Mint authorization is in application checks (`ALLOWED_ORGS=*`, `job_workflow_ref`, `ROLE_APP_IDS`, installation scoping), not in STS org/repo enumeration.

   **Note:** `PER_REPO_WIF_REPOS=*` does **not** mean “all repos use the default provider” in today’s implementation; it registers the literal key `*` and does not match real repo names. Public mode should **omit** `PER_REPO_WIF_REPOS`, not set it to `*`.

   **Out of scope here:** Provisioning pools, providers, CEL text, Cloud Function deployment, and IAM bindings is deferred to a **mint infrastructure ADR**. This ADR only requires a permissive provider behind `WIF_PROVIDER_NAME` for public deployments and that it is **never** wired for LLM access.

3. **`PER_REPO_WIF_REPOS` (tight mode optional).** Remains for **tight** deployments: explicit `owner/repo` entries select per-repo STS providers and enable legacy `job_workflow_ref` paths under `{owner}/{repo}/.github/workflows/`. Unused in public mode (empty/unset).

4. **Workflow provenance in public mode.** When `ALLOWED_ORGS` contains `*`, `prevalidateOIDCToken` **only** accepts `job_workflow_ref` under `fullsend-ai/fullsend/.github/workflows/`. Reject `{org}/.fullsend/` and `{owner}/{repo}/` self-workflow prefixes.

5. **Org event dispatch ([ADR 0041](0041-synchronous-workflow-call-event-dispatch.md)).** Per-org and per-repo installs converge on upstream reusables for **event-driven** agent runs. Public mint does not carve out an exception for legacy `{org}/.fullsend/` workflow paths. Non-event entry points that [ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) still allows on `workflow_dispatch` (e.g. `repo-maintenance.yml`, manual prioritize) must either call an upstream `reusable-*.yml` or use **tight** mint until they do. Implementation of ADR 0041 in `.fullsend` is a separate delivery track; the **design** for org dispatch is not open.

6. **Ref pinning.** Any ref on `fullsend-ai/fullsend` is acceptable in `job_workflow_ref` (e.g. `@refs/tags/v0`, `@refs/heads/main`, commit SHA). The mint validates **repository and path** (`fullsend-ai/fullsend/.github/workflows/<file>`), not a pinned tag set. Stricter ref policy may be added later without changing the public/tight split.

7. **Shared mint caller.** Mint requests in public mode must run inside upstream reusable workflows and use `fullsend-ai/fullsend/.github/actions/mint-token` at a caller-chosen ref. The reusable **workflow file** is the attestable unit in `job_workflow_ref`, not the composite action path.

8. **Unchanged mint gates.** Issuer, audience (`OIDC_AUDIENCE`), expiry/skew, STS exchange, `ALLOWED_ROLES`, `ROLE_APP_IDS`, and per-role `rolePermissions` downscoping remain mandatory.

9. **Custom agent content (in scope).** Layered overrides under `customized/` / `.fullsend/customized/` remain supported for **built-in** stages: harness, skills, agent markdown, and policies loaded inside existing reusable workflows. That does not change mint `role` or provenance.

10. **New agent roles (not derived from an existing role).** Adding a capability that needs a **new** GitHub App identity or permission set (not “coder with a different harness”) requires, in order:
   - a new **shared** GitHub App (installed by adopting orgs),
   - mint configuration updates (`ROLE_APP_IDS`, `rolePermissions`, `ALLOWED_ROLES`),
   - a new **shared** workflow file under `fullsend-ai/fullsend/.github/workflows/`.
   Reusing an existing role’s App (e.g. fix → coder) is unchanged ([ADR 0007](0007-per-role-github-apps.md)).

11. **Deferred to future ADRs (mint-only):**
    - Dedicated reusable workflow(s) for custom agent **stages** beyond the built-in set.
    - **Prioritize** on `workflow_dispatch`: whether manual prioritize mints via an upstream `reusable-*.yml` or remains tight-mint-only until wired ([ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) keeps non-event `workflow_dispatch` entry points).
    - Mint infrastructure: WIF pool/provider provisioning, Cloud Function deployment, CEL definitions, abuse controls, WAF, and monitoring.

12. **Normative specs (`docs/normative/`).** Not required for this decision. The reference mint is implemented in `internal/mint/` and configured via documented env vars; this ADR plus [ADR 0029](0029-central-token-mint-secretless-fullsend.md) are the contract for that implementation. A versioned normative spec is **out of scope** until there are multiple independent mint implementations that must interoperate on the same byte-level env and claim rules.

## Consequences

- Public mode: `ALLOWED_ORGS=*`, permissive provider id in `WIF_PROVIDER_NAME`, `PER_REPO_WIF_REPOS` empty. Tight mode: explicit `ALLOWED_ORGS`, org-merged provider in `WIF_PROVIDER_NAME`, optional `PER_REPO_WIF_REPOS` list.
- Provider provisioning and IAM are deferred to a mint infrastructure ADR; this ADR does not specify how providers are created.
- Custom agents remain configuration-only unless a new shared App, mint role, and upstream workflow file are added.
- Org and per-repo **public** mint both assume event-driven runs mint from `fullsend-ai/fullsend` reusables per [ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) and [ADR 0033](0033-per-repo-installation-mode.md); legacy `{org}/.fullsend/` provenance is not supported in public mode.
- Ref-any on `fullsend-ai/fullsend` trades pinning rigor for simpler rollout; supply-chain risk on upstream repo refs is accepted for now.
- Public and tight modes share the same mint routing code; only deployed provider CEL and env values differ. LLM access stays on install-scoped providers, not the mint STS provider.

### Related ADRs

| Topic | ADR |
|-------|-----|
| Org event dispatch (`dispatch.yml` → `workflow_call` → `reusable-*.yml`) | [0041](0041-synchronous-workflow-call-event-dispatch.md) |
| Per-repo dispatch chain | [0033](0033-per-repo-installation-mode.md) |
| Upstream reusables and `mint-token` | [0031](0031-reusable-workflows-for-action-installed-distribution.md) |
| Central mint | [0029](0029-central-token-mint-secretless-fullsend.md) |

### Remaining deferred decisions

| Topic | Disposition |
|-------|-------------|
| Custom-agent reusable workflow | Future ADR |
| Prioritize mint path under public mode | Resolve when non-event workflows are wired to reusables |
| Mint WIF providers, IAM, deployment, abuse/WAF | Future ADR (all provisioning) |
| Normative mint contract | Deferred until multiple implementations; not needed now |
| Stricter `@ref` allowlist on upstream workflows | Optional later hardening |
