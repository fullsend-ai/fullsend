# SPIKE: Community token mint hosting (GCP vs Cloudflare)

**Issue:** [#915](https://github.com/fullsend-ai/fullsend/issues/915) · **Parent:** [#914](https://github.com/fullsend-ai/fullsend/issues/914) · **Epic:** [#912](https://github.com/fullsend-ai/fullsend/issues/912)  
**ADR:** [0029](../ADRs/0029-central-token-mint-secretless-fullsend.md) · [0040](../ADRs/0040-public-community-mint-architecture.md) (architecture) · **GitOps:** [#1263](https://github.com/fullsend-ai/fullsend/issues/1263) · **Date:** 2026-05-25

## Decision

**Launch the public mint on Scenario 1** (100% GCP, dedicated project, [GitOps](#public-mint-operations) per [#1263](https://github.com/fullsend-ai/fullsend/issues/1263)), with **org and per-repo** modes unchanged.

**Do not treat Scenario 2 (GCP + Cloudflare) as the long-term default.** Two consoles (GCP origin + Cloudflare edge) are ongoing human cost for the **Red Hat Fullsend Bootstrap** team (no dedicated SRE). That cost is **not** offset by saving a one-time port—re-platforming is largely AI-driven; **monitoring and incidents are human-driven**.

**Steady-state target for a hardened public edge: Scenario 3** (100% Cloudflare Workers), not Scenario 2—**one operational surface** (Workers + WAF + alerts) while preserving the same `POST /v1/token` contract. Scenario 2 remains a **short bridge** only if WAF/rate limits are needed before the Worker port lands.

Public mint consumers only use **`FULLSEND_MINT_URL`**; hosting is opaque. **Self-managed tenant** mints stay on GCP via the CLI provisioner today; Cloudflare becomes an optional tenant target when Scenario 3 exists.

## Scenarios

| # | Posture | Operator sees |
|---|---------|---------------|
| **1** | GCF + WIF + Secret Manager in a **mint-only** GCP project | **One** primary stack (Cloud Monitoring / Logging); edge via Cloud Armor + external HTTPS LB if required |
| **2** | Same GCF origin; public URL is **Cloudflare-proxied** | **Two** stacks: GCP (origin health, STS, SM) + Cloudflare (WAF, blocks, 5xx at edge) |
| **3** | Mint on **Workers**; OIDC via JWKS + same claim rules; PEMs in Worker secrets (or external vault) | **One** primary stack (Cloudflare); GitOps via Wrangler |

Today’s code: native Go GCF (`internal/mint`), pluggable `TokenValidator` / `PEMAccessor`, tenant deploy via `internal/dispatch/gcf`, contract in [mint-token](../../.github/actions/mint-token/action.yml) and [infrastructure reference](../guides/admin/infrastructure-reference.md).

### OIDC trust: STS/WIF (today) vs JWKS (Scenario 3)

GitHub Actions sends the mint a short-lived **OIDC JWT** in the `Authorization` header. The mint must prove the token is genuine and matches policy (`job_workflow_ref`, allowed orgs/workflows, etc.).

| Approach | Used in | What it means |
|----------|---------|----------------|
| **STS + WIF** | Scenario 1 (today) | The mint sends the JWT to **GCP Security Token Service**, which validates it against a **Workload Identity Federation** pool (CEL rules on repo/org). GCP returns a federated token; the mint also decodes and checks claims in Go. Trust is anchored in **GCP**. |
| **JWKS** | Scenario 3 (target) | **JWKS** = *JSON Web Key Set*: the public signing keys GitHub publishes (e.g. `https://token.actions.githubusercontent.com/.well-known/jwks`). The mint **verifies the JWT signature** against those keys locally (no GCP STS call), then runs the **same** claim checks in application code. Trust is anchored in **GitHub’s keys + mint logic**. |

A Scenario 3 port is often called “STS→JWKS” because the **authorization outcome** should match; only the **validation backend** changes. Per-repo WIF provider selection becomes equivalent routing in mint config, not a second GCP pool lookup at request time.

## Integrated evaluation

All requirements below apply together—not as a separate “constraints” checklist.

| Factor | 1 — GCP | 2 — GCP + CF | 3 — CF |
|--------|---------|--------------|--------|
| **Launch speed** | ● Shipping binary + GitOps | ● + DNS/WAF rules | ◐ Worker port + parity tests |
| **Public vs tenant deploy** | GitOps ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)); CLI for tenants | Same | GitOps (Wrangler); CLI for tenants later |
| **Isolated from inference/LLM** | ● Dedicated GCP project only | ● Same project; CF zone for mint host | ● No Vertex in mint project; don’t colocate inference Workers |
| **Org + per-repo WIF** | ● Today | ● Same origin | ◐ JWKS path must keep per-repo provider logic |
| **ADR 0029 trust** | ● STS + WIF + handler claims | ● Unchanged at origin | ◐ Prove JWKS path ≡ STS path in CI |
| **Internet-facing abuse** | ◐ Armor+LB setup (still one vendor) | ● Easy WAF/RL | ● WAF/RL in same console as compute |
| **Ongoing human ops** | ● **Single dashboard**; paging from GCP | ○ **Dual dashboard**; split incident triage | ● **Single dashboard**; paging from CF |
| **One-time engineering** | Low | Low–medium | Medium (AI-friendly port behind existing interfaces) |
| **Long-term multi-host** | ◐ GCP-centric | ◐ Split | ● Adds non-GCP option for tenants/public |
| **Blocks [#914](https://github.com/fullsend-ai/fullsend/issues/914)** | No | No | No if launch on 1 first |
| **Cost (~$0 community budget)** | ● Bare GCF+WIF+SM | ○ Free CF = weak edge; paid CF breaks budget | ● Workers Free at community volume |

### Cost (~$0 community budget)

Community mint traffic is **low** (roughly one `POST /v1/token` per agent job batch), so **compute** is not the cost driver—**fixed-price edge SKUs** are.

| Posture | Typical spend at community scale | Fits ~$0? |
|---------|----------------------------------|-----------|
| **Scenario 1 — bare GCF + WIF + SM** | Stays within GCP free tiers for invocations, federation, and SM access at expected volume | **Yes** — best match for launch |
| **Scenario 1 — Cloud Armor + external HTTPS LB** | LB has **baseline monthly cost** even at zero mint traffic | **No** — rules out “hardened GCP edge” on a $0 budget |
| **Scenario 2 — GCF + Cloudflare** | GCF ~$0; **meaningful** WAF/rate limits usually need **paid** CF (Pro/Business), not Free | **Poor value on $0**: dual dashboards + thin Free-tier rules |
| **Scenario 3 — Workers** | Workers **Free** tier is sufficient at community QPS; **Workers Paid** only if volume or bundle limits grow later | **Yes** for steady state without GCP LB fees |

**Effect on the decision (with ops and security):**

- **Reinforces** launch on **Scenario 1** without Armor/LB—allowlists and GCP alerts are the $0 edge story until something else is funded or Scenario 3 ships.
- **Discourages** a **long-lived Scenario 2** bridge unless a CF plan is donated; otherwise you pay in **operator time** (two consoles) without buying real protection.
- **Keeps Scenario 3** as the **$0 steady-state** path for WAF/rate limits **plus** one dashboard once the JWKS port is done—it does **not** force skipping Scenario 1 at launch (both can be ~$0 for compute; the port is engineering time, not subscription).

### Does two dashboards push toward 1 or 3?

**Toward 1 or 3—not 2.**

- **Scenario 2** optimizes **edge convenience** at the price of **permanent split-brain ops**: Bootstrap must correlate GCF 5xx/latency with Cloudflare origin errors and WAF blocks; synthetic checks should hit the public hostname *and* the origin; runbooks always have two hops. Alert fan-in to one pager helps but does not remove the second UI for tuning and incidents.
- **Scenario 1** keeps **one vendor console** for mint health. The tradeoff is weaker **default** edge on a bare `cloudfunctions.net` URL—mitigate at launch with fail-closed env allowlists (`ALLOWED_ORGS`, `ALLOWED_WORKFLOW_FILES`) and GCP alerts. **Cloud Armor + LB** is the one-dashboard GCP hardening option but **not** on a ~$0 budget (see [Cost](#cost-0-community-budget)); funded abuse response or **Scenario 3** is the realistic hardening path.
- **Scenario 3** is the way to get **strong edge + single dashboard** without Scenario 2’s ops tax. The STS→JWKS port is a **one-time** cost; Bootstrap’s recurring load is CF-only. Prefer this over staying on Scenario 2 indefinitely.

**Scenario 2 is justified only as a time-boxed bridge** (weeks, not years): public hostname needs WAF before Workers ship, and Bootstrap accepts dual-console overhead temporarily.

## Public mint operations

- **Deploy:** GitOps ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263))—no prod `gcloud functions deploy` by individuals; optional [#1262](https://github.com/fullsend-ai/fullsend/issues/1262) deploy notifications.
- **Project:** Community mint **only**—no shared GCP project with Vertex/inference or internal Red Hat mint resources.
- **SLO owner:** Red Hat Fullsend Bootstrap until community ops exists. Target: **99.5%** availability for `POST /v1/token` (excl. GitHub outages), **p95 &lt; 2s**.
- **Signals (Scenario 1):** GCF 5xx/latency, STS/SM errors, synthetic `POST /v1/token` (expect 401), allowlist-change audit via GitOps.
- **Signals (Scenario 3):** Worker errors, WAF blocks, same synthetic on public URL—no origin correlation.
- **Portability:** Second `TokenValidator` (JWKS) and `PEMAccessor`; parity tests STS vs JWKS—strategic, not launch-blocking.

## Phasing

| Phase | Choice | Rationale |
|-------|--------|-----------|
| **Launch** | **Scenario 1** + GitOps + isolated project | Fastest; one console; unblocks [#914](https://github.com/fullsend-ai/fullsend/issues/914) |
| **Bridge (optional)** | **Scenario 2** | Only if abuse/WAF needed before Workers *and* CF budget exists; poor fit on $0 + dual console |
| **Hardened steady state** | **Scenario 3** | WAF + rate limits + **one** ops surface; avoids chronic dual-dashboard |
| **Parallel** | JWKS/port work | AI-heavy; does not block launch |
| **Tenants** | GCP CLI path now; CF when Scenario 3 matures | Tenants choose; public URL stays opaque |

## Open follow-ups

1. GitOps layout for mint + WIF/SM without PEMs in git ([#1263](https://github.com/fullsend-ai/fullsend/issues/1263)).
2. Allowlist-only edge until Scenario 3 (Armor+LB excluded on ~$0 budget unless funding appears).
3. Rate-limit thresholds for public hostname (when edge exists).
4. Criteria to transfer SLO ownership from Bootstrap to community ops.

## References

- [Infrastructure reference — Token mint](../guides/admin/infrastructure-reference.md)
- [ADR 0029](../ADRs/0029-central-token-mint-secretless-fullsend.md)
- [ADR 0040 — Public community mint architecture](../ADRs/0040-public-community-mint-architecture.md)
- [#1263](https://github.com/fullsend-ai/fullsend/issues/1263) · [#1262](https://github.com/fullsend-ai/fullsend/issues/1262)
- [#915](https://github.com/fullsend-ai/fullsend/issues/915) discussion (2026-05-25)
