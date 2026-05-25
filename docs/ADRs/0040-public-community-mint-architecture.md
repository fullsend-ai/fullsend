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

[ADR 0029](0029-central-token-mint-secretless-fullsend.md) defines a **community deployment profile**: public (unlisted) shared GitHub Apps per role, keys held only at a **central mint**, and adopters that trust a stable `FULLSEND_MINT_URL` instead of bespoke per-org Apps. [#914](https://github.com/fullsend-ai/fullsend/issues/914) covers shared App registration and install UX; this ADR records **how the public mint is built, secured, operated, and scaled**.

Hosting and platform trade-offs (GCP vs Cloudflare, cost, dual-console ops, JWKS port) are documented in the spike [Community token mint hosting (GCP vs Cloudflare)](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md) ([#915](https://github.com/fullsend-ai/fullsend/issues/915)). This ADR states the **architecture**; the spike remains the reference for **platform choice rationale and phasing**.

## Options

- **Defer a dedicated public profile** and reuse an internal Red Hat mint project — rejected: violates isolation, blast-radius, and community trust boundaries.
- **Imperative CLI deploy** (`fullsend admin install` / GCF provisioner) for the public mint — rejected for production: no enforced review or audit trail ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)).
- **Long-term GCP + Cloudflare split edge** (GCF origin + proxied hostname) as steady state — rejected as default ongoing ops model; see spike (dual dashboards, $0 budget). Acceptable only as a **short bridge** if funded.

## Decision

The **public community mint** is a **stateless, internet-facing** HTTPS service that implements the existing `POST /v1/token` contract ([mint-token action](../../.github/actions/mint-token/action.yml), [infrastructure reference](../guides/admin/infrastructure-reference.md)). Adopters configure only **`FULLSEND_MINT_URL`** and OIDC audience; **hosting location is opaque** to consumers.

### Deployment

1. **Launch posture:** GCP Cloud Function (`fullsend-mint`), Go implementation in `internal/mint`, deployed only via **GitOps** ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263))—not the tenant CLI provisioner.
2. **Dedicated GCP project** for the community profile: mint function, WIF pool/providers, and Secret Manager for shared App PEMs. **No** Vertex inference, LLM credentials, or internal Red Hat mint resources in that project.
3. **Install modes:** **Org-level** (default WIF provider, `.fullsend` / upstream `fullsend-ai/fullsend` workflow refs) and **per-repo** (`PER_REPO_WIF_REPOS`, repo-scoped providers)—both on the same public mint.
4. **Steady-state hardening (target):** port mint to **Cloudflare Workers** with **JWKS**-based GitHub OIDC validation and the same claim policy, via pluggable `TokenValidator` / `PEMAccessor`—one operational surface for edge + compute. Details and phasing: [hosting spike](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md).
5. **Tenant-operated mints** remain separate: self-managed orgs may use the CLI/GCF provisioner on GCP today; Cloudflare may become an optional **tenant** target when the Worker implementation exists. Tenant hosting choices do not affect public consumers.

### Security

1. **Trust model ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)):** Callers present GitHub Actions OIDC JWTs; the mint returns **short-lived, org-scoped** installation tokens with **role minimum permissions**—never the App’s full grant.
2. **Launch validation path:** GitHub JWT → **GCP STS** exchange against **Workload Identity Federation** (CEL-bound org/repo/workflow) plus in-function **fail-closed** checks (`iss`, `aud`, `job_workflow_ref`, `ALLOWED_ORGS`, `ALLOWED_WORKFLOW_FILES`, per-repo provider routing).
3. **No auth proxy** in front of the mint; abuse defense at launch is **allowlists** and monitoring. **WAF / rate limits** arrive with the Worker steady state (or a **time-boxed** CF bridge only if budget and ops capacity exist—see spike).
4. **Secrets:** Shared community App PEMs **only** in Secret Manager for the community project (layout per [#914](https://github.com/fullsend-ai/fullsend/issues/914)); never in adopters’ `.fullsend` repos.
5. **Blast radius:** Compromise of the public mint affects **all orgs** on that profile; mitigations include GitOps-only deploys, monitoring, key rotation, narrow App installations, and human branch protections—not repo-stored PEMs.

### Monitoring

1. **SLO owner:** **Red Hat Fullsend Bootstrap** until community operations assumes on-call.
2. **Targets:** **99.5%** monthly availability for `POST /v1/token` (excluding GitHub OIDC/API outages); **p95 &lt; 2s** mint latency.
3. **Signals (GCP launch):** GCF 5xx and latency, STS/Secret Manager errors, synthetic unauthenticated `POST /v1/token` (expect 401), GitOps audit for allowlist/env changes. Optional deploy notifications ([#1262](https://github.com/fullsend-ai/fullsend/issues/1262)).
4. **Signals (Worker steady state):** Worker errors, WAF blocks, same synthetic on the public URL—single-console triage per spike.
5. **Runbook:** mint health → edge (when present) → GitHub OIDC status; prefer **managed** WAF rules over custom policy authoring.

### Scaling

1. **Workload shape:** Stateless request/response; roughly **one mint call per agent job** that needs forge tokens—low QPS at community scale, bursty with Actions concurrency.
2. **Horizontal scaling:** Cloud Functions and Workers scale instances automatically; no mint-side session store.
3. **Limits:** Enforce existing caps (e.g. `repos` list size, request body size); tune **rate limits** at the edge when Scenario 3 (or a funded bridge) is live.
4. **Cost:** Community budget is **~$0**; launch stays on GCP **free tiers** without Cloud Armor + external HTTPS LB; steady-state Workers **Free** tier at expected volume unless traffic or bundle limits require Paid—see spike.

### Operations

1. **Change control:** All production changes through **GitOps** ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)); no individual `gcloud functions deploy` to prod.
2. **Incident response:** Revoke or rotate shared App keys, tighten `ALLOWED_ORGS` / workflow allowlists, disable profile in enrollment docs; Bootstrap team pages until community handoff.
3. **Portability:** Maintain **multiple mint hosts** long-term; second `TokenValidator` (JWKS) and parity tests (STS ≡ JWKS) are strategic work, not launch blockers for [#914](https://github.com/fullsend-ai/fullsend/issues/914).
4. **Documentation:** Platform comparison, OIDC STS vs JWKS, cost, and dashboard trade-offs live in the [hosting spike](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md)—update the spike when platform facts change; update this ADR only when the **architectural commitments** change.

## Consequences

- Community adopters get a **stable, reviewed, isolated** mint path aligned with ADR 0029 without depending on Red Hat-internal infrastructure.
- Bootstrap team carries **SLO and incident** responsibility with **single-console** ops at launch; Worker migration reduces edge cost and dual-dashboard risk when executed.
- **~$0 budget** forbids LB+Armor as the primary hardening path; security at launch leans on **OIDC binding and allowlists** until Workers or funded edge exists.
- GitOps and a dedicated project add **operational overhead** up front but improve auditability versus CLI deploys.
- Normative follow-ons remain: shared App definitions ([#914](https://github.com/fullsend-ai/fullsend/issues/914)), GitOps repo layout ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)), JWKS parity CI, and criteria for transferring SLO ownership to community ops.
