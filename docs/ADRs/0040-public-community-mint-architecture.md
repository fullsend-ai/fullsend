---
title: "40. Public community mint architecture"
status: Proposed
relates_to:
  - agent-infrastructure
  - security-threat-model
  - operational-observability
topics:
  - identity
  - oidc
  - github-apps
  - deployment
  - operations
  - key-rotation
---

# 40. Public community mint architecture

Date: 2026-05-25

## Status

Proposed

<!-- Once this ADR is Accepted, its content is frozen. Do not edit the Context,
     Decision, or Consequences sections. If circumstances change, write a new
     ADR that supersedes this one. Only status changes and links to superseding
     ADRs should be added after acceptance. -->

## Context

[ADR 0029](0029-central-token-mint-secretless-fullsend.md) already establishes the **goal**: a **community deployment profile** with public (unlisted) shared GitHub Apps per role, App keys held **only** at a centrally operated mint, and routine adopters trusting a stable **`FULLSEND_MINT_URL`** instead of bespoke per-org Apps and dispatch PATs. Self-managed mints remain a separate, coexisting path; they are not a substitute for delivering the **public** mint.

This ADR does **not** revisit whether to build that mint. It records **how** the community-operated public mint is **deployed, secured, monitored, scaled, and run** once shared Apps and enrollment exist ([#914](https://github.com/fullsend-ai/fullsend/issues/914)). The open work is **implementation and operations**, not product direction.

Platform and phasing choices (interim GCP vs steady-state Workers, cost, operations consoles) are analyzed in the spike [Community token mint hosting (GCP vs Cloudflare)](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md) ([#915](https://github.com/fullsend-ai/fullsend/issues/915)). This ADR defines the **steady-state** architecture; the spike holds **interim paths, comparisons, and rejected postures**.

## Options

Ways to **achieve** the ADR 0029 community mint (same `POST /v1/token` contract, org + per-repo install modes, opaque URL to consumers):

| Option | Summary |
|--------|---------|
| **A. Dedicated community mint on GCP (interim)** | Go Cloud Function in a **mint-only** GCP project; OIDC via **STS + WIF**; PEMs in Secret Manager; prod deploy via **GitOps** ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)). |
| **B. Reuse the internal Red Hat mint** for community adopters | Single mint endpoint and project for internal and public tenants. |
| **C. Tenant-style CLI provisioner** for the public mint | Same imperative `fullsend admin install` / GCF path self-managed orgs use. |
| **D. GCP origin + Cloudflare edge (steady state)** | Keep GCF; public hostname proxied for WAF/rate limits long term. |
| **E. Cloudflare Workers (steady state)** | Port mint to Workers; OIDC via **JWKS**; edge and compute in one ops surface. |
| **F. GCP + Cloud Armor + external HTTPS LB** | Harden edge entirely in GCP without Cloudflare. |

**B** is rejected: shared infrastructure with internal workloads breaks **isolation** and community **trust boundaries**. **C** is rejected for production: no enforced review or deploy audit ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)). **D** is rejected as the **long-term** default (dual dashboards, poor fit for ~$0 budget on meaningful WAF—see spike); acceptable only as a **short bridge**. **F** is rejected on ~$0 community budget (LB baseline cost); spike documents trade-offs.

**Chosen:** **E** defines the architecture below. **A** is an acceptable **interim** implementation until **E** is ready; **D** only as a short bridge if edge is urgent before **E** (spike).

## Decision

Fullsend **will operate** a **public community mint** as required by [ADR 0029](0029-central-token-mint-secretless-fullsend.md). The **steady-state** design (option **E**) is a **stateless, internet-facing** mint on **Cloudflare Workers**, exposing the existing `POST /v1/token` contract ([mint-token action](../../.github/actions/mint-token/action.yml), [infrastructure reference](../guides/admin/infrastructure-reference.md)). Adopters set **`FULLSEND_MINT_URL`** and OIDC audience only; hosting is opaque to them.

Until that Worker implementation is production-ready, the same contract and trust bar may run temporarily on **GCP Cloud Function** (option **A**, STS + WIF)—see the [hosting spike](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md) for interim phasing, cost, and rejected postures. **Self-managed tenant** mints stay on separate paths (CLI/GCF today); they are not described here.

### Deployment

1. **Runtime:** Mint logic on **Cloudflare Workers**, implemented via the shared `internal/mint` handler with a **JWKS** `TokenValidator` and pluggable `PEMAccessor` (port from today’s GCF code).
2. **Release:** Production deploys only through **GitOps** ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)) (e.g. Wrangler + reviewed merges)—not the tenant CLI provisioner.
3. **Isolation:** Community mint **must not share** infrastructure with Vertex/inference, internal Red Hat mints, or unrelated Workers (e.g. docs/admin). PEMs and mint configuration live in a **dedicated** trust domain (Worker secrets and/or a vault scoped to this profile—layout per [#914](https://github.com/fullsend-ai/fullsend/issues/914)).
4. **Public URL:** Stable **`FULLSEND_MINT_URL`** on a community hostname; TLS and edge policy colocated with the Worker.
5. **Install modes:** **Org-level** (`.fullsend` / upstream `fullsend-ai/fullsend` workflow refs) and **per-repo** (equivalent of today’s per-repo trust routing)—both supported on the **same** public mint.

### Security

1. **Trust model ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)):** GitHub Actions OIDC JWT in, **short-lived, org-scoped** installation token out; **role minimum permissions** enforced in mint logic.
2. **OIDC validation:** Verify JWT signature against GitHub’s **JWKS** (`token.actions.githubusercontent.com`), then **fail-closed** application checks: `iss`, `aud`, `job_workflow_ref`, `ALLOWED_ORGS`, `ALLOWED_WORKFLOW_FILES`, and per-repo policy routing (parity with today’s GCF behavior—prove in CI).
3. **Edge:** **Managed WAF** and **rate limits** on `POST /v1/token` in the same Cloudflare surface as the Worker; no separate origin dashboard for abuse control.
4. **No auth proxy** in front of callers; the mint remains directly reachable with Bearer OIDC only.
5. **Secrets:** Shared community App PEMs **only** at the mint operator boundary—never in adopters’ `.fullsend` repos.
6. **PEM rotation:** **Automated** rotation of shared App private keys (generate, install on GitHub Apps, update mint material, retire old keys without service disruption) is **necessary** for long-running community operations but **deferred** to a **future ADR** that defines ceremony, tooling, and rollout. Until then, rotation is **manual** or GitOps-assisted under operator runbooks.
7. **Blast radius:** One compromised public mint affects **all orgs** on the profile; mitigate with GitOps-only changes, monitoring, timely PEM rotation, narrow App installations, and forge branch protections.

### Monitoring

1. **Owner:** **Red Hat Fullsend Bootstrap** until community operations assumes on-call.
2. **SLOs:** **99.5%** monthly availability for `POST /v1/token` (excluding GitHub OIDC/API outages); **p95 &lt; 2s** latency.
3. **Signals:** Worker errors and latency, WAF block/challenge rates, synthetic `POST /v1/token` without token (expect 401), GitOps/deploy audit trail; optional deploy notifications ([#1262](https://github.com/fullsend-ai/fullsend/issues/1262)).
4. **Triage:** Single console (Cloudflare)—Worker health, then WAF, then external GitHub status; prefer **managed** rulesets over bespoke policy.

### Scaling

1. **Shape:** Stateless request/response; ~one mint per agent job needing forge access—low baseline QPS, bursty with Actions.
2. **Capacity:** Workers scale automatically; no mint-side session store.
3. **Limits:** Keep request/body/`repos` caps; tune **edge rate limits** as adoption grows.
4. **Cost:** Community budget **~$0** at expected volume—Workers **Free** tier where sufficient; Paid tier only if traffic or bundle limits require it ([spike](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md)).

### Operations

1. **Change control:** GitOps-only production changes ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)).
2. **Incidents:** Rotate shared App keys (manual procedure until automated rotation ADR), tighten allowlists, update enrollment guidance; Bootstrap pages until community handoff.
3. **Key lifecycle:** Follow-on ADR required for **automated PEM rotation** (see Security)—not a launch blocker for [#914](https://github.com/fullsend-ai/fullsend/issues/914).
4. **Evolution:** Hosting comparisons and interim GCP details stay in the [hosting spike](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md); change this ADR when **steady-state** commitments change.

## Consequences

- Delivers the **ADR 0029 community profile** in operable form: shared mint endpoint, isolated from internal Red Hat mint and inference infrastructure.
- **Launch (option A)** unblocks [#914](https://github.com/fullsend-ai/fullsend/issues/914) enrollment against a real `FULLSEND_MINT_URL` without waiting for the Worker port.
- **Steady state (option E)** improves edge posture and **single-console** ops versus a permanent GCP+CF split (option D).
- Bootstrap team owns **SLOs and incidents** until community ops exists; GitOps adds process overhead but matches the security bar for a shared token issuer.
- **~$0 budget** keeps launch on GCP free tiers; LB+Armor (option F) and paid CF edge (option D long term) stay off the critical path unless funding appears.
- Remaining work is **execution**: shared Apps ([#914](https://github.com/fullsend-ai/fullsend/issues/914)), GitOps layout ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)), JWKS parity CI, SLO handoff criteria—not a replan of whether the public mint should exist.
- **Automated PEM rotation** must be specified in a **future ADR**; operating without it increases reliance on manual ceremony and incident-time rotation discipline.
