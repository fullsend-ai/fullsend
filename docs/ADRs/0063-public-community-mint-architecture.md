---
title: "63. Public community mint architecture"
status: Accepted
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

# 63. Public community mint architecture

Date: 2026-05-25

## Status

Accepted

<!-- Once this ADR is Accepted, its content is frozen. Do not edit the Context,
     Decision, or Consequences sections. If circumstances change, write a new
     ADR that supersedes this one. Only status changes and links to superseding
     ADRs should be added after acceptance. -->

## Context

[ADR 0029](0029-central-token-mint-secretless-fullsend.md) establishes the **goal**: a **community deployment profile** with public (unlisted) shared GitHub Apps per role, App keys held **only** at a centrally operated mint, and routine adopters trusting a stable **`FULLSEND_MINT_URL`** instead of bespoke per-org Apps and dispatch PATs.

Since this ADR was drafted, related decisions landed on `main`:

- **[ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md)** (Accepted) defines **public mint trust policy**: `ALLOWED_ORGS=*`, upstream-only `job_workflow_ref` under `fullsend-ai/fullsend/.github/workflows/`, global per-role `ROLE_APP_IDS` and PEM secrets, and enrollment via **installing the shared Apps** (no per-org mint env churn). Custom per-repo workflow provenance is **tight-mode only** via `PER_REPO_WIF_REPOS`.
- **[ADR 0060](0060-cross-org-mint-authorization-via-org-variables.md)** (Accepted) adds optional `target_org` minting for workloads like the e2e pool ([ADR 0040](0040-org-pool-for-parallel-e2e-tests.md)).
- **[ADR 0044](0044-deprecate-per-org-installation-mode.md)** (Accepted) deprecates per-org `.fullsend` installs; the public profile targets **per-repo** installs calling upstream reusables ([ADR 0033](0033-per-repo-installation-mode.md), [ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md)).
- Mint logic now lives in **`internal/mintcore/`** (shared by GCF `internal/mint/` and standalone **`cmd/mint/`**, which already uses **JWKS** verification).

This ADR records **how** the community-operated public mint is **deployed, secured at the edge, monitored, scaled, and run**. **OIDC claim rules and enrollment policy** are normative in [ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md); this ADR fulfills the **mint infrastructure** item deferred there (WIF/provider layout, deployment, WAF, monitoring).

Platform and phasing choices (interim GCP vs steady-state Workers, cost, operations consoles) are analyzed in the [hosting spike](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md) ([#915](https://github.com/fullsend-ai/fullsend/issues/915)). [#1145](https://github.com/fullsend-ai/fullsend/issues/1145) depends on this architecture for zero-GCP installs against the hosted mint.

## Options

Ways to **achieve** the ADR 0029 community mint (same `POST /v1/token` contract, opaque URL to consumers):

| Option | Summary |
|--------|---------|
| **A. Dedicated community mint on GCP (interim)** | Go Cloud Function (`internal/mint/` + `mintcore`) in a **mint-only** GCP project; OIDC via **STS + WIF**; PEMs in Secret Manager; prod deploy via **GitOps** ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)). |
| **B. Reuse the internal Red Hat mint** for community adopters | Single mint endpoint and project for internal and public tenants. |
| **C. Tenant-style CLI provisioner** for the public mint | Same imperative `fullsend admin install` / GCF path self-managed orgs use. |
| **D. GCP origin + Cloudflare edge (steady state)** | Keep GCF; public hostname proxied for WAF/rate limits long term. |
| **E. Cloudflare Workers (steady state)** | Port mint to Workers; OIDC via **JWKS** (`mintcore.JWKSVerifier`); edge and compute in one ops surface. |
| **F. GCP + Cloud Armor + external HTTPS LB** | Harden edge entirely in GCP without Cloudflare. |

**B** is rejected: shared infrastructure with internal workloads breaks **isolation** and community **trust boundaries**. **C** is rejected for production: no enforced review or deploy audit ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)). **D** is rejected as the **long-term** default (dual dashboards, poor fit for ~$0 budget on meaningful WAF—see spike); acceptable only as a **short bridge**. **F** is rejected on ~$0 community budget (LB baseline cost).

**Chosen:** **E** defines the steady-state architecture below. **A** is an acceptable **interim** implementation until **E** is ready; **D** only as a short bridge if edge is urgent before **E** (spike).

## Decision

Fullsend **will operate** a **public community mint** as required by [ADR 0029](0029-central-token-mint-secretless-fullsend.md). The **steady-state** design (option **E**) is a **stateless, internet-facing** mint on **Cloudflare Workers**, exposing the existing `POST /v1/token` contract ([mint-token action](../../.github/actions/mint-token/action.yml), [infrastructure reference](../guides/infrastructure/infrastructure-reference.md)). Adopters set **`FULLSEND_MINT_URL`** and OIDC audience only; hosting is opaque.

Until the Worker implementation is production-ready, the same contract and trust bar may run temporarily on **GCP Cloud Function** (option **A**, STS + WIF). **Self-managed tenant** mints stay on separate paths (CLI/GCF or `cmd/mint/`); they are not described here.

### Trust and enrollment (hosted profile)

The hosted public mint **will use public mint mode** per [ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md):

- **`ALLOWED_ORGS=*`** — any org may request tokens after other checks pass; **installing the shared role Apps is enrollment** ([#914](https://github.com/fullsend-ai/fullsend/issues/914), [#1145](https://github.com/fullsend-ai/fullsend/issues/1145)). No `EnsureOrgInMint` / per-org `ALLOWED_ORGS` updates for new adopters.
- **`job_workflow_ref`** — **upstream reusables only** (`fullsend-ai/fullsend/.github/workflows/`). Legacy `{org}/.fullsend/` and custom `{owner}/{repo}/` workflow paths are **not** supported on the public profile ([ADR 0044](0044-deprecate-per-org-installation-mode.md)).
- **`PER_REPO_WIF_REPOS`** — **unset/empty** on the hosted mint. Per-repo custom workflow provenance remains a **tight-mode** feature for self-managed mints only.
- **Shared credentials** — `ROLE_APP_IDS` and PEM secrets are **global per role** (`fullsend-{role}-app-pem`), not keyed by org ([ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md) §8). Cross-org isolation uses `repository_owner` + installation lookup, not separate PEMs per org.
- **`aud` validation** — enforced in **`mintcore`** application code (`OIDC_AUDIENCE`) on both STS and JWKS paths; it is **not** a WIF/STS responsibility and carries over unchanged on Workers.
- **Abuse complement** — [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md) dispatch authorization limits who can trigger agent runs; mint openness does not bypass write checks at dispatch.
- **Cross-org** — [ADR 0060](0060-cross-org-mint-authorization-via-org-variables.md) applies on the same hosted endpoint (e.g. e2e pool).

### Deployment

1. **Runtime:** **`mintcore`** handler on **Cloudflare Workers** with **`JWKSVerifier`** and pluggable `PEMAccessor` (parity with `cmd/mint/` today).
2. **Interim (option A):** GCF wrapper in `internal/mint/` with **`STSVerifier`**; public-mode env per [ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md) §2 (permissive `WIF_PROVIDER_NAME`, empty `PER_REPO_WIF_REPOS`).
3. **Release:** Production deploys only through **GitOps** ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263))—not the tenant CLI provisioner.
4. **Isolation:** Community mint **must not share** infrastructure with Vertex/inference, internal Red Hat mints, or unrelated Workers. PEMs and mint configuration live in a **dedicated** trust domain.
5. **Public URL:** Stable **`FULLSEND_MINT_URL`** on a community hostname; TLS and edge policy colocated with the Worker.

### Security (edge and operations)

1. **Trust model ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)):** OIDC JWT in, **short-lived, org-scoped** installation token out; **role minimum permissions** in mint logic.
2. **OIDC validation:** JWKS signature verification (steady state) or STS exchange (interim), then the **same** `mintcore` claim checks as today—including `iss`, `aud`, org allowlist, and workflow provenance per [ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md). Prove STS ≡ JWKS in CI before cutover.
3. **Edge:** **Managed WAF** and **rate limits** on `POST /v1/token` in the same Cloudflare surface as the Worker.
4. **No auth proxy** in front of callers; Bearer OIDC only.
5. **Secrets:** Shared community App PEMs **only** at the mint operator boundary.
6. **PEM rotation:** **Automated** rotation is **necessary** but **deferred** to a **future ADR**; until then, rotation is manual or GitOps-assisted.
7. **Blast radius:** One compromised public mint affects **all orgs** on the profile; mitigate with GitOps-only changes, monitoring, timely PEM rotation, narrow App installations, and forge branch protections.

### Monitoring

1. **Owner:** **Red Hat Fullsend Bootstrap** until community operations assumes on-call.
2. **SLOs:** **99.5%** monthly availability for `POST /v1/token` (excluding GitHub OIDC/API outages); **p95 &lt; 2s** latency.
3. **Signals:** Worker errors and latency, WAF block/challenge rates, synthetic `POST /v1/token` without token (expect 401), GitOps/deploy audit trail ([#1262](https://github.com/fullsend-ai/fullsend/issues/1262)).
4. **Triage:** Single console (Cloudflare)—Worker health, then WAF, then external GitHub status.

### Scaling

1. **Shape:** Stateless request/response; low baseline QPS, bursty with Actions.
2. **Capacity:** Workers scale automatically; no mint-side session store.
3. **Limits:** Keep request/body/`repos` caps; tune **edge rate limits** as adoption grows.
4. **Cost:** Community budget **~$0** at expected volume ([spike](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md)).

### Operations

1. **Change control:** GitOps-only production changes ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)).
2. **Incidents:** Rotate shared App keys (manual until automated rotation ADR), tighten allowlists if needed, update enrollment guidance.
3. **Evolution:** Hosting comparisons and interim GCP details stay in the [hosting spike](../spikes/2026-05-25-community-mint-hosting-gcp-vs-cloudflare.md).

## Consequences

- Delivers the **ADR 0029 community profile** in operable form, with **trust policy** in [ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md) and **ops/deployment** here.
- **Launch (option A)** unblocks [#914](https://github.com/fullsend-ai/fullsend/issues/914) and [#1145](https://github.com/fullsend-ai/fullsend/issues/1145) without waiting for the Worker port.
- **Steady state (option E)** improves edge posture and **single-console** ops versus a permanent GCP+CF split (option D).
- Bootstrap owns **SLOs and incidents** until community ops exists.
- **~$0 budget** keeps launch on GCP free tiers; LB+Armor (option F) and paid CF edge (option D long term) stay off the critical path unless funding appears.
- Remaining work: shared Apps ([#914](https://github.com/fullsend-ai/fullsend/issues/914)), GitOps layout ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)), JWKS parity CI, public-mode implementation in `mintcore`, SLO handoff criteria.
- **Automated PEM rotation** must be specified in a **future ADR**.
