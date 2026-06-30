---
title: "59. Public mint mode with wildcard allowlists"
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

# 59. Public mint mode with wildcard allowlists

Date: 2026-06-28

## Status

Accepted

## Context

[ADR 0029](0029-central-token-mint-secretless-fullsend.md) establishes a central token mint that exchanges GitHub OIDC tokens for short-lived, role-scoped installation tokens. Today the mint is configured with **explicit** org and repo registries (`ALLOWED_ORGS`, `PER_REPO_WIF_REPOS`) and **fail-closed** workflow provenance (`job_workflow_ref` prefixes for `fullsend-ai/fullsend/`, registered per-repo workflows, and legacy `{org}/.fullsend/` paths). That model fits **self-managed** and **single-tenant** deployments where the operator curates every org and repo.

A **public** (multi-tenant) mint profile is needed so many orgs can share one mint endpoint without pre-registering each tenant in env vars on every install. `ALLOWED_ORGS=*` enables that openness at the mint application layer and implies a permissive STS provider behind existing `WIF_PROVIDER_NAME` routing (see below). Provenance must shift to **trusted upstream workflow code** in `fullsend-ai/fullsend` ([ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md)), because arbitrary customer workflows must not be able to call a globally reachable mint.

Per-repo installs ([ADR 0033](0033-per-repo-installation-mode.md)) already call upstream reusables directly; [ADR 0044](0044-deprecate-per-org-installation-mode.md) deprecates the per-org model whose `{org}/.fullsend/` config repo was the remaining source of org-local workflow provenance. [ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) (Accepted) completes the convergence for legacy per-org event paths by replacing `gh workflow run` fan-out with synchronous `workflow_call` to `reusable-{stage}.yml` in `fullsend-ai/fullsend`. Public mint provenance therefore targets the per-repo + upstream-reusable model; `{org}/.fullsend/` refs are legacy and not supported in public mode.

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
   - **Tight mint:** `ALLOWED_ORGS` is a comma-separated list of orgs and does **not** contain `*`. Only listed orgs pass org validation.
   - **Deny lists:** Not supported. Tighter scope is achieved by omitting `*` and listing orgs/repos explicitly, not by mixing `*` with exclusions.

2. **Public STS provider via `WIF_PROVIDER_NAME`.** The GCF mint validates GitHub OIDC by exchanging the JWT with GCP STS against a WIF provider id from `resolveWIFProvider`. That step is **mint authentication only**—it must not be used for LLM / Vertex access. Agents continue to use `FULLSEND_GCP_WIF_PROVIDER` (repo/org-scoped providers and IAM from install), never the mint’s STS provider.

   **`WIF_PROVIDER_NAME` (existing):** Required on the Cloud Function. It is the **default** provider id used when a repo is **not** listed in `PER_REPO_WIF_REPOS`. No new env var is required for public mode.

   **Tight mint:** `WIF_PROVIDER_NAME` points at the org-merged provider (CEL lists known orgs). `PER_REPO_WIF_REPOS` may list specific `owner/repo` values so those repos use per-repo provider ids (`gh-{owner}-{repo}`) instead of the default.

   **Public mint:** Operators provision a **single permissive** WIF provider (CEL does not enumerate orgs/repos) and set **`WIF_PROVIDER_NAME` to that provider’s id**. Leave `PER_REPO_WIF_REPOS` **unset or empty** so every `repository` claim falls through to the default provider—no new env var and no change to WIF provider **routing** semantics. Mint authorization for public mode is enforced in org validation, workflow provenance, and installation scoping—not in STS org/repo enumeration.

   **Note:** `PER_REPO_WIF_REPOS=*` does **not** mean “all repos use the default provider” in today’s implementation; it registers the literal key `*` and does not match real repo names. Public mode should **omit** `PER_REPO_WIF_REPOS`, not set it to `*`.

   **Out of scope here:** Provisioning pools, providers, CEL text, Cloud Function deployment, and IAM bindings is deferred to a **mint infrastructure ADR**. This ADR only requires a permissive provider behind `WIF_PROVIDER_NAME` for public deployments and that it is **never** wired for LLM access.

3. **`PER_REPO_WIF_REPOS` (tight mode optional).** Remains for **tight** deployments: explicit `owner/repo` entries select per-repo STS providers and enable `job_workflow_ref` paths under `{owner}/{repo}/.github/workflows/`. Unused in public mode (empty/unset).

4. **Workflow provenance in public mode.** When `ALLOWED_ORGS` contains `*`, mint validation **only** accepts `job_workflow_ref` under `fullsend-ai/fullsend/.github/workflows/`. Reject `{org}/.fullsend/` (legacy per-org) and `{owner}/{repo}/` self-workflow prefixes.

   Per-repo installs converge on upstream reusables via [ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md) and [ADR 0033](0033-per-repo-installation-mode.md); [ADR 0044](0044-deprecate-per-org-installation-mode.md) removes the per-org config repo as a long-term provenance path. This is why a single `fullsend-ai/fullsend/` trust prefix is viable in public mode.

5. **Event dispatch convergence ([ADR 0041](0041-synchronous-workflow-call-event-dispatch.md), Accepted).** Per-repo installs and legacy per-org installs (until removed by [ADR 0044](0044-deprecate-per-org-installation-mode.md)) converge on upstream reusables for **event-driven** agent runs. Public mint does not carve out an exception for legacy `{org}/.fullsend/` workflow paths. Non-event entry points that [ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) still allows on `workflow_dispatch` (e.g. `repo-maintenance.yml`, manual prioritize) must either call an upstream `reusable-*.yml` or use **tight** mint until they do.

6. **Ref pinning.** Any ref on `fullsend-ai/fullsend` is acceptable in `job_workflow_ref` (e.g. `@refs/tags/v0`, `@refs/heads/main`, commit SHA). The mint validates **repository and path** (`fullsend-ai/fullsend/.github/workflows/<file>`), not a pinned tag set. Stricter ref policy may be added later without changing the public/tight split.

   This is distinct from [ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md) SHA pinning, which governs **caller workflow refs**—each org or repo controls which upstream code runs with its secrets. Mint-level ref validation is a separate layer: accepting any upstream ref trades pinning rigor for simpler rollout and avoids mint ops churn or misconfiguration that could block legit orgs. The mint itself is deployed from the same upstream repo (future mint deployment ADR), limiting the incremental protection mint-level pinning would provide.

7. **Shared mint caller.** Mint requests in public mode must run inside upstream reusable workflows and use `fullsend-ai/fullsend/.github/actions/mint-token` at a caller-chosen ref. The reusable **workflow file** is the attestable unit in `job_workflow_ref`, not the composite action path.

8. **Shared role credentials (mandatory gates).** Issuer, audience (`OIDC_AUDIENCE`), expiry/skew, STS exchange, `ALLOWED_ROLES`, and per-role `rolePermissions` downscoping remain mandatory. `ROLE_APP_IDS` and PEM secrets (`fullsend-{role}-app-pem` in Secret Manager) are **global per role**, shared across all orgs on a mint—not keyed by org. That shared model is what makes `ALLOWED_ORGS=*` viable: new orgs need only install the shared public Apps; they do not require per-org PEM provisioning or org-scoped app ID entries in mint env vars.

   **Cross-org isolation:** Shared PEMs do not let Org A mint tokens for Org B. The mint derives the requesting org from the OIDC `repository_owner` claim and asks GitHub’s App installation API for an installation token scoped to that org’s installation of the role App. If the App is not installed on the requesting org, minting fails. Optional `repos` in the mint request further downscope to repositories within that org’s installation ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)).

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
    - Hosted mint enrollment policy: whether the fullsend-operated hosted mint adopts public mode (`ALLOWED_ORGS=*`) or remains tight with explicit enrollment.

12. **Normative specs (`docs/normative/`).** Not required for this decision. The reference mint is implemented in `internal/mintcore/` and configured via documented env vars; this ADR plus [ADR 0029](0029-central-token-mint-secretless-fullsend.md) are the contract for that implementation. A versioned normative spec is **out of scope** until there are multiple independent mint implementations that must interoperate on the same byte-level env and claim rules.

## Consequences

- Public mode: `ALLOWED_ORGS=*`, permissive provider id in `WIF_PROVIDER_NAME`, `PER_REPO_WIF_REPOS` empty. Tight mode: explicit `ALLOWED_ORGS`, org-merged provider in `WIF_PROVIDER_NAME`, optional `PER_REPO_WIF_REPOS` list.
- Provider provisioning and IAM are deferred to a mint infrastructure ADR; this ADR does not specify how providers are created.
- Custom agents remain configuration-only unless a new shared App, mint role, and upstream workflow file are added.
- Per-repo **public** mint assumes event-driven runs mint from `fullsend-ai/fullsend` reusables per [ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) and [ADR 0033](0033-per-repo-installation-mode.md). Legacy `{org}/.fullsend/` provenance is not supported in public mode and is deprecated by [ADR 0044](0044-deprecate-per-org-installation-mode.md).
- Ref-any on `fullsend-ai/fullsend` at the mint layer trades pinning rigor for simpler rollout; caller-level SHA pinning per [ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md) remains the recommended org/repo control.
- Public and tight modes share the same mint routing code; only deployed provider CEL and env values differ. LLM access stays on install-scoped providers, not the mint STS provider.
- **Dispatch authorization** ([ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)) complements public mint: `ALLOWED_ORGS=*` opens the mint to any org, but agent runs still require write permission at the dispatch layer—mitigating cost exposure and unauthorized inference triggers.
- **Rollback:** Public-to-tight rollback is config-only: replace `ALLOWED_ORGS=*` with an explicit org list and repopulate `PER_REPO_WIF_REPOS` as needed. No data loss or PEM rotation required; existing shared role credentials continue to serve all enrolled orgs.
- **Basename gate:** An early draft required explicit `ALLOWED_WORKFLOW_FILES` basenames in public mode; that restriction was dropped because `job_workflow_ref` provenance is the real security gate and basename filtering adds operator churn without blocking meaningful attacks.

### Related ADRs

| Topic | ADR |
|-------|-----|
| Org event dispatch (`dispatch.yml` → `workflow_call` → `reusable-*.yml`) | [0041](0041-synchronous-workflow-call-event-dispatch.md) (Accepted) |
| Per-org deprecation | [0044](0044-deprecate-per-org-installation-mode.md) |
| Per-repo dispatch chain | [0033](0033-per-repo-installation-mode.md) |
| Upstream reusables and `mint-token` | [0031](0031-reusable-workflows-for-action-installed-distribution.md) |
| Dispatch authorization (abuse complement) | [0054](0054-require-authorization-on-all-agent-dispatch-paths.md) |
| Central mint | [0029](0029-central-token-mint-secretless-fullsend.md) |

### Remaining deferred decisions

| Topic | Disposition |
|-------|-------------|
| Custom-agent reusable workflow | Future ADR |
| Prioritize mint path under public mode | Resolve when non-event workflows are wired to reusables ([ADR 0041](0041-synchronous-workflow-call-event-dispatch.md)) |
| Mint WIF providers, IAM, deployment, abuse/WAF | Future ADR (all provisioning) |
| Hosted mint public-mode enrollment | Resolve when moving hosted mint to `ALLOWED_ORGS=*` |
| Normative mint contract | Deferred until multiple implementations; not needed now |
| Stricter `@ref` allowlist on upstream workflows | Optional later hardening |
