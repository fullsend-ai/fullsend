---
title: "29. Central token mint and shared apps for a secretless .fullsend"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
  - agent-architecture
topics:
  - identity
  - oidc
  - github-apps
  - deployment
---

# 29. Central token mint and shared apps for a secretless .fullsend

Date: 2026-05-05

## Status

Accepted

<!-- Once this ADR is Accepted, its content is frozen. Do not edit the Context,
     Decision, or Consequences sections. If circumstances change, write a new
     ADR that supersedes this one. Only status changes and links to superseding
     ADRs should be added after acceptance. -->

## Context

The Fullsend run layer security model must constrain two risks: unauthorized access to model APIs, and impersonation of Fullsend agents on the forge. The current architecture keeps LLM credentials and per-role GitHub App private keys as GitHub Actions secrets in the org’s `.fullsend` config repo, relies on org admins to protect that repo, and assumes only workflows defined there can read those secrets ([ADR 0007](0007-per-role-github-apps.md), [ADR 0008](0008-workflow-dispatch-for-cross-repo-dispatch.md)).

That layout has operational costs: enrolled repos must trigger `.fullsend` via `workflow_dispatch` authenticated with a long-lived fine-grained PAT so that caller-scoped secrets do not block access to PEMs in the config repo ([ADR 0008](0008-workflow-dispatch-for-cross-repo-dispatch.md)). Because those workflows can use the App keys, each org historically needed its own agent apps to avoid cross-org permission leakage. GitHub’s controls also make fully automated PAT and App lifecycle painful, which works against hands-off deployment.

Workload identity federation and related patterns already move LLM access toward short-lived, non-repo-stored credentials (see [ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md) and [security-threat-model.md](../problems/security-threat-model.md)). The remaining gap is GitHub agent identity: if App secrets leave the `.fullsend` repo, dispatch can revert to `workflow_call`, and orgs can stop minting their own Apps and PATs for baseline installs. That shift interacts with [ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md), which decouples shims from specific agent workflows via a stage-based dispatcher (`dispatch.yml`, stage markers, `gh workflow run` fan-out). [ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md) assumes [ADR 0008](0008-workflow-dispatch-for-cross-repo-dispatch.md)’s `workflow_dispatch` + PAT boundary between enrolled repos and `.fullsend`; it does not define a separate trust model beyond preserving that split. Moving to `workflow_call` and mint-issued tokens updates that assumption while keeping stage-based decoupling.

This direction is **complementary** to [ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md) (short-lived provider access) and **orthogonal** to [ADR 0009](0009-pull-request-target-in-shim-workflows.md) (shim trigger context). [ADR 0014](0014-admin-install-github-apps-secrets-v1.md) and installer specs will need follow-on updates for where PEMs live, without reversing per-role App semantics.

This decision **reverses** [ADR 0008](0008-workflow-dispatch-for-cross-repo-dispatch.md)’s use of `workflow_dispatch` with an org-scoped dispatch PAT for the enrolled-repo → `.fullsend` boundary. ADR 0008 chose that pattern because `workflow_call` could not expose config-repo secrets to the called workflow. Centralizing App private keys in a token mint removes the need for PEMs as repo secrets, so that constraint no longer applies and shims can integrate via `workflow_call`, eliminating the dispatch PAT for that path (subject to cutover / compatibility if PAT mode remains supported).

## Relationship to prior ADRs (summary)

- **[ADR 0007](0007-per-role-github-apps.md) — partially revised:** Per-role Apps and least-privilege by role remain goals; **PEM storage** leaves the `.fullsend` repo for **mint-held keys**. The **normative baseline** is a **central mint plus public shared Apps**; **self-managed** (org-operated mint plus **private** per-role Apps) is a **supported** path for early rollout and for orgs that never adopt cross-org shared Apps.
- **[ADR 0008](0008-workflow-dispatch-for-cross-repo-dispatch.md) — reversed (mechanism):** `workflow_dispatch` + dispatch PAT was required so `.fullsend` could use repo-stored PEMs without `workflow_call` secret scoping. Mint-held keys remove that requirement.
- **[ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md) — preserved in intent:** Stage-based indirection and marker fan-out remain; only the cross-repo **trigger and authentication** assumptions inherited from 0008 change when moving to `workflow_call` + OIDC-to-mint.
- **[ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md) — aligned:** Same “no long-lived secrets in `.fullsend`” direction for forge identity, alongside provider-based LLM access.
- **[ADR 0009](0009-pull-request-target-in-shim-workflows.md) — orthogonal:** Does not change `pull_request_target` reasoning; only how the shim calls `.fullsend` and how `.fullsend` obtains GitHub tokens.

## Options

- **Status quo:** Keep PEMs and any remaining provider secrets in `.fullsend`, retain `workflow_dispatch` and the org-level dispatch PAT, and keep per-org GitHub Apps ([ADR 0007](0007-per-role-github-apps.md), [ADR 0008](0008-workflow-dispatch-for-cross-repo-dispatch.md)).
- **Central mint + shared apps (this ADR):** Operate a token mint that alone holds App credentials; `.fullsend` workflows prove workload identity via OIDC and receive short-lived, org-scoped forge tokens.

## Decision

**Direction of travel (normative default).** Fullsend’s **intended end state** for routine adoption is a **central token mint** plus **public (unlisted), shared GitHub Apps** per role: App private keys live **only** with the mint, **not** in each org’s `.fullsend` repo; adopting orgs **install the same well-known Apps** and **trust a mint endpoint** (or a named deployment profile) instead of generating bespoke per-org Apps and org-level dispatch PATs for the baseline path. That combination is what this ADR treats as the **default** architecture to aim for once implementation, security hardening, and operations are ready.

**Phasing.** Reaching that end state may take time. **Early releases may default to self-managed**: a **single organization** runs **its own mint** and **its own private per-role Apps**, with PEMs held **only at that mint**—still **secretless `.fullsend`**, but without cross-org shared Apps until shared Apps and a centrally operated mint meet the project’s bar.

**Who may stay off the shared baseline.** Some orgs **may never** adopt public shared Apps and a shared mint (isolation policy, regulated environments, air gaps, or fully bespoke trust domains). **Self-managed** remains a **first-class, indefinite** option; it is **not** a dead-end branch of the architecture.

**Multi-org on one mint.** Multiple organizations may share **one** mint (for example operated by an enterprise admin or a vendor) and **one set** of public shared Apps that each org installs; concrete naming, PEM layout, and install flags belong in normative specs and tooling.

1. **Shared Apps (target baseline).** Define a small set of well-known GitHub Apps (per agent role or equivalent) that **routine adopting orgs** install. Private keys live only with the mint service that serves that profile, not in each org’s `.fullsend` config repo.
2. **Self-managed Apps (supported alternative).** A single org may use **private** per-role Apps registered for that org, with PEMs held only on **its** mint. This path does not require other tenants to trust the same App registrations; it coexists with the shared baseline for orgs that need isolation or are waiting out the phased rollout.
3. **Token mint.** The mint accepts OIDC tokens from approved workloads (GitHub Actions today), verifies that the caller is an allowed workflow from the expected `.fullsend` repository (e.g. using claims such as `job_workflow_ref`), and returns a **short-lived, org-scoped** token suitable for impersonating the correct App installation for that run.
4. **Workflow integration.** `.fullsend` workflows obtain forge tokens from the mint when they need GitHub API access instead of reading PEM secrets locally.
5. **Deployment profiles.** Multiple mint instances may exist (e.g. vendor-operated vs community-operated), each paired with its own App registrations and trust policies; orgs choose which mint to trust rather than creating bespoke Apps and PATs for the **shared** baseline path.
6. **Extensibility.** The same mint pattern can be extended to other CI platforms by validating that platform’s OIDC or workload tokens (e.g. Tekton pipeline service account tokens), and to other SCMs by minting the equivalent bot credentials once those forges are supported.

Non-sensitive configuration that today is stored as secrets only for convenience may move to org-level Actions variables or similar once the mint is authoritative for true secrets.

## Consequences

- For any mint-based path, `.fullsend` no longer stores App PEMs or model API secrets; enrollment and rotation shift toward **mint configuration**, **App installation** (shared or self-managed), and **OIDC trust**—not repo secret churn alone.
- Shim workflows can call `.fullsend` via **`workflow_call`**, removing the **org-level dispatch PAT** (`FULLSEND_DISPATCH_TOKEN`) for that integration path (subject to a defined cutover / compatibility mode if PAT mode remains supported).
- **Shared baseline:** onboarding emphasizes **installing the well-known shared Apps** and **trusting a mint endpoint** (and optional deployment profile) rather than bespoke per-org Apps and PATs for routine use. **Self-managed:** onboarding emphasizes **trusting that org’s mint** and **its private Apps**—still no long-lived PEMs in `.fullsend`.
- The mint and its backing key material become a **high-value target**: for deployments where **many orgs share one mint and shared Apps**, compromise can affect **every org** on that profile; for **self-managed**, blast radius is mainly that org’s installations—still worse than compromise of a single repo’s secrets if the mint aggregates multiple roles.
- **Blast radius (mint compromise):** An attacker who can mint or alter mint policy may obtain **short-lived tokens scoped to installations** the mint’s Apps already have in affected orgs—potentially acting as **any** Fullsend agent role those Apps represent, across repos those installations can reach. If a **review** (or similarly privileged) App installation can **approve merges** or satisfy branch protections, mint compromise could enable **self-approval** or **merge** paths at scale until keys are revoked and tokens expire. Mitigations include mint hardening, monitoring, key ceremony, narrow installations, and human CODEOWNERS / branch rules—not only repo secret placement.
- **Trust binding:** Reliance on OIDC claims such as **`job_workflow_ref`** (and related issuer/subject rules) is security-critical: the mint must only tokenize callers that match **pinned, expected workflow definitions** in the real `.fullsend` repo (and org/repo rules you define). Spoofing a **fake** `.fullsend` in another org still yields tokens **scoped to that attacker’s org installations**, not cross-tenant access to unrelated orgs—but within an org, impact can still be severe.
- **Availability:** Centralizing token issuance creates a **shared dependency**: if the mint is unreachable or unhealthy, agent workflows that depend on minted tokens may **stall** across dependent orgs (a **shared SPOF** unless you operate redundant endpoints, caches, or explicit fallback modes). This trades **per-repo secret sprawl and PAT operations** for **central operational responsibility** (uptime, incident response, key management).
- Follow-on ADRs or normative specs should spell out cutover, PAT compatibility mode, and concrete dispatcher/shim wiring. When **this** ADR is **Accepted**, updates to older ADRs and living docs follow repo supersession rules ([ADR 0001](0001-use-adrs-for-decision-making.md)): **accepted** ADRs are not rewritten—only **status** and links to the successor—while [`docs/architecture.md`](../architecture.md) carries current truth. A checklist for **[ADR 0007](0007-per-role-github-apps.md)**, **[ADR 0008](0008-workflow-dispatch-for-cross-repo-dispatch.md)**, and **[ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md)** is posted on the pull request for when this ADR is merged as Accepted.
