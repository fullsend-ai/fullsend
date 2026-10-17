# AISDLC-290 research: offline policy authoring

Status: proposed for reporter review; not yet persisted to Jira
Repository baseline: `fullsend-ai/fullsend@e37e3a003084d5d9dfb5c3d93af2590460e4bebe`
Researched: 2026-10-12

## Research question

Can Fullsend use versioned evaluation and production evidence to propose,
independently test, review, and promote routing policy without allowing
historical outcomes or cost optimization to mutate live selection directly?

This spike evaluates two uses:

1. **Standalone deterministic path:** offline analysis proposes a reviewed,
   signed deterministic policy. Production selection remains deterministic,
   for example through ADR 0112 overlays once implemented.
2. **Advisor governance path:** the same evidence lifecycle versions and
   evaluates the candidate set, signal schema, rubric/classifier, thresholds,
   failure behavior, and promotion of a pre-run advisor policy. The live
   advisor may remain stochastic; it is bounded by the promoted artifact and a
   deterministic Fullsend gate.

## Jira evidence boundary

An authenticated live Jira reconciliation on 2026-10-12 confirms that
AISDLC-95 `depends on` AISDLC-5 and has `Related` links to AISDLC-282 and
AISDLC-345. AISDLC-345 owns authoritative attributable cost evidence at
run/work-item granularity; AISDLC-282 owns cumulative spend enforcement.
AISDLC-95 consumes those inputs for routing policy evaluation without
duplicating either contract. AISDLC-290 remains a New, unassigned Spike under
the In Progress AISDLC-287 Epic. No external system is changed here.

## Executive finding

Offline policy authoring is viable as the governance/evidence backbone of any
AISDLC-95 option. It is also viable as a standalone low-risk first increment
once ADR 0112 is implemented: production routing can remain a reviewed,
versioned deterministic mapping from allowed task facts to `model` and
`effort`.

It cannot, by itself, satisfy an interpretation of `auto` that requires a new
request-specific learned decision on every invocation. In the standalone form,
automation analyzes evidence and drafts a policy; humans approve a fixed
artifact; production applies deterministic rules. Calling that `auto` would be
accurate only if stakeholders intentionally define `auto` as managed,
evidence-updated deterministic policy rather than live classification.

The central safety invariant is a one-way promotion boundary:

```text
immutable evidence -> validated bundle -> proposal -> independent evaluation
-> security/quality gates -> human approval -> signed policy artifact
-> deterministic production application
```

Historical outcomes never write live configuration, candidate pools, advisor
weights, or security floors. Cost is optimized only among candidates that
already pass security, deterministic acceptance, and agreed quality/
completion non-inferiority gates.

## Current Fullsend baseline

### Accepted selection and serving decisions

* ADR 0091 defines static runtime/model/effort precedence.
* ADR 0112 accepts deterministic per-task `model`/`effort` routing through
  harness overlays, trace, and one unserved-model retry without overlays. It
  explicitly defers learned/LLM routing to a new ADR.
* ADR 0112 remains unimplemented on the reviewed baseline. `ForgeConfig` still
  lacks overlay `model`/`effort`, and no overlay-resolution trace exists.
* PR 7808's separate routing schema, model-tier inventory, and shadow/enforce modes were
  rejected. Offline policy authoring must not revive them by assumption.
* ADRs 0137/0138 define gateway serving/credential boundaries, with route code
  implemented for pi, Codex, and Claude. A promoted policy may select an
  approved concrete `gateway/<model>`, but control-plane, credential,
  classifier, gateway, and upstream failure after that route is selected cannot
  escape to direct credentials or change auth mode. ADR 0137's live in-
  iteration OIDC placeholder-rotation proof remains a per-runtime operational
  prerequisite for long-running pilots; this offline-policy spike consumes the
  proof and does not own its implementation.

### Existing evidence/telemetry capabilities

Fullsend already supplies useful but incomplete building blocks:

* distributed traces correlate a run with `fullsend.work_item_id`, agent,
  runtime, requested model, serving endpoint, usage, cost, and artifacts;
* `internal/evalmeasure` loads versioned per-agent measurement manifests,
  scores trace fitness/run health, emits `gen_ai.evaluation.result`, and keeps
  an idempotence ledger keyed by trace/evaluation/version;
* `metrics.json` and runtime transcripts provide additional requested/reported
  model, token, cost, turn, and iteration evidence where runtimes expose it.

These do not yet form an AISDLC-95 policy ledger. Actual-served model is not
uniformly known, subagent provider attribution is incomplete, current fitness
is not a task-quality oracle, and forge outcomes can mature after the run.
Missing or unknown evidence must never be treated as a zero cost or successful
outcome.

### Relevant problem-document guidance

`docs/problems/adaptive-agent-selection.md` proposes a versioned experiment
ledger, shadowing, confidence thresholds, human checkpoints, and pressure tests
for fitness honesty, context buckets, cold start, exploration, and selection
bias. It leaves the fitness function, sampling, drift, model-version changes,
and governance open. It is a problem document, not an accepted architecture.

`docs/problems/testing-agents.md` recommends deterministic/static checks before
expensive behavioral evaluation, agent-level golden sets rather than prompt-
only tests, periodic re-evaluation for drift, and statistical gates based on a
lower confidence bound rather than only a point estimate. Thresholds and trial
counts remain product/evaluation decisions.

## Evidence model requirements

Each row needs immutable identity and provenance, not merely a metric tuple:

* run/trace/work-item/task-case identity and attempt number;
* observation time and outcome maturity time;
* repository/base/head, harness content digest, agent definition digest,
  config/policy/advisor/runtime versions, and an approved model-policy inventory
  digest binding the exact candidate entries, provider/capability metadata,
  serving routes, and auth constraints;
* normalized input/task-stratum digest and trust-classified signal snapshot;
* static selection, recommendation if any, accepted selection, requested model,
  and independently verified actual-served model when available;
* assignment mechanism and propensity/probability where randomized selection
  exists; otherwise mark the record observational;
* deterministic check results, versioned quality measurements, human review
  outcomes, completion state, cost, latency, usage, fallback/escalation, and
  artifact digest;
* explicit missingness, exclusion reason, and data-quality findings.
* a reference to the applicable per-runtime gateway route-readiness evidence
  when an OIDC run may exceed five minutes or its observed token lifetime,
  including proof scope/duration and auth mode; absence is an operational
  unknown, not success.

Selected and served identity are observations, not bare strings. Each records
attester, trust domain, source, runner-owned request id, observed/expiry time,
and one assurance: `requested-only`, `runtime-reported`, `gateway-attested`,
`provider-attested`, or `independently-reconciled`. Gateway response metadata,
gateway OTEL, and gateway export share one trust domain and cannot corroborate
one another upward. Provider-neutral stronger evidence is provider-signed
metadata or independently collected upstream audit/account records joined by
the runner-owned request id.

The study preregisters minimum assurance by identity role and use: requested-
only is enough only for shadow analysis of intended choice; served-behavior
shadowing requires gateway-attested evidence; production enforcement requires
gateway-attested selected/served identity plus an approved gateway-trust risk
decision; provider/data-policy compliance requires provider-attested or
independently-reconciled evidence; promotion based on served/provider/cost
requires independently-reconciled evidence. Missing, weaker, conflicting, or
stale observations remain unknown/quarantined, fail closed, or fail that gate.
They are never upgraded. If provider evidence is unavailable, any lower-
assurance exception must be approved before results, scoped and time-bounded,
and cannot relabel evidence or override a compliance requirement.

All joins are cardinality-checked. A trace may have multiple attempts and
measurements; the policy author must not accidentally multiply cost or promote
an incomplete outcome through a many-to-many join.

## Bias, leakage, and drift controls

### Selection bias

Historical production assignments are not interchangeable. Easier work may
have received cheaper models, humans may selectively merge strong outputs, and
failed runs may lack outcome telemetry. Controls:

* mark observational versus randomized/paired records;
* preserve assignment propensity where available;
* stratify by task/repository/stage/risk and require candidate/control overlap;
* prefer paired or randomized repeated golden-path evaluation for promotion;
* treat reweighting/propensity analysis as sensitivity evidence, not a
  substitute for an independent experiment;
* report exclusions/missingness by candidate, not only globally.

### Leakage

Split by stable task/work-item group and time, not by individual attempt. A
retry, near-duplicate, or later outcome for one task cannot cross training,
validation, and held-out evaluation. The held-out manifest and gate thresholds
are frozen before candidate results are opened.

### Drift

Version model/provider endpoints, signal schema, workload strata, measurement
definitions, and cost tables. Compare the promotion population with current
traffic for input-distribution, missingness, outcome, route, and model-version
drift. Drift invalidates or pauses the policy according to a pre-approved rule;
it never retrains and promotes a replacement directly.

## Standalone deterministic policy path

The authoring system proposes abstract rule changes constrained to reviewed
signals and an approved concrete model/effort set. It outputs a proposal and an
explanation, never edits a harness or `.fullsend/config.yaml` itself.

After independent evaluation and human approval, a compiler can render the
accepted abstract rules into ADR 0112 overlay syntax. That compiler depends on
ADR 0112 implementation and must preserve its precedence, security limitation,
trace, and fallback. Author-shaped facts may influence cost/quality selection
but cannot establish a security floor; security-sensitive top-level selection
remains strongest unless trusted policy permits otherwise.

The production artifact records both the abstract policy and exact rendered
harness digest. Approval publishes that immutable content-addressed artifact to
a trusted approval registry/review output with an authorized signature. A
separate, independently reviewed repository activation change references its
exact ID and digest. Production verifies that active reference against the
approved source before deterministic overlay resolution and emits the artifact
digest in the run trace. This separation avoids requiring an artifact to be
active before it can be installed. The offline analysis pipeline can produce
proposal bytes but has no approval-registry or production-reference write path.
A rollback activates a compatible prior approved artifact through the same
reference process; it does not generate a new rule from recent outcomes.

## Governance path for a pre-run advisor

Offline authoring may instead produce an advisor policy artifact containing:

* closed candidate IDs and exact provider/route/capability policy;
* signal schema and trust classifications;
* advisor implementation/model/rubric/version;
* normalization, timeout, abstention, and deterministic gate/fallback behavior;
* security-stage allowed pairs and explicit application point;
* evaluation manifest, thresholds, and evidence-bundle digest.

Independent repeated evaluation runs the exact candidate artifact against the
same pinned baseline. A stochastic advisor is not declared reproducible merely
because its request and response were recorded. The **offline analysis is
reproducible** when the immutable evidence bundle, code version, seed where
applicable, and configuration regenerate the same report/proposal. Individual
advisor or agent calls remain samples from a stochastic system; promotion
depends on repeated outcome distributions and deterministic gates.

The promoted artifact only bounds the advisor. At runtime the deterministic
policy gate still validates the recommendation, and the chosen direct or
gateway serving route remains owned by existing boundaries.

## Promotion and rejection principles

A candidate is promotable only in this order:

1. provenance, schema, join, and leakage validation pass;
2. static/security/provider/route constraints pass;
   required identity assurance and freshness also pass for every claimed use;
3. every deterministic task acceptance check passes;
4. quality and completion meet the pre-registered non-inferiority threshold
   using repeated independent runs and the agreed confidence rule;
5. latency, escalation/failure, and operational SLOs pass;
   for a long-running gateway OIDC candidate this includes a current per-runtime
   ADR 0137 live proof covering token `iat`/`exp` scheduling, new placeholder
   generation settle/re-seed, runtime re-read, same-route/auth continuity, and
   fail-closed refresh/re-seed/expiry negatives. API-key candidates instead
   prove the upstream gateway key is active/not revoked and satisfies its own
   contract-defined scope/expiry, if any; separately enforce the run-scoped
   OpenShell provider-credential lifetime and absence of an OIDC re-seed loop.
   Provider-credential expiration/deletion does not revoke the gateway key;
6. cost is compared only among candidates still eligible;
7. an authorized human reviews evidence, limitations, and diff and signs the
   exact artifact.

Lower cost can break a tie or optimize within the eligible set. It cannot
waive quality, security, missing data, inadequate power, or drift failures.
Rejected proposals and their evidence are retained to prevent repeated bad
experiments and selective reporting.

## What this can mean for AISDLC-95 `auto`

| Interpretation | Offline authoring fit |
|---|---|
| Automatically analyze evidence and propose a reviewed deterministic policy | Complete fit |
| Automatically update policy after a human-approved promotion | Fit only if “update” means installing an already signed artifact, never learning from live outcomes in place |
| Dynamically choose per invocation from live task content | Not satisfied standalone; requires pre-run advisor or gateway selector architecture |
| Supply evidence/governance for a dynamic advisor | Strong fit; recommended regardless of live decision placement |
| Uncontrolled online learning | Explicitly excluded |

## Recommendation

Adopt offline policy authoring as the mandatory evidence and governance
lifecycle for AISDLC-95 candidates. Evaluate the standalone deterministic path
as the lowest-risk first increment after ADR 0112 is implemented. Do not label
it the final `auto` experience until stakeholders choose the intended meaning.
If a pre-run advisor advances, reuse the same ledger, independent evaluation,
promotion gate, signed artifact, and rollback mechanism.

## Unresolved human decisions

1. Does AISDLC-95 `auto` require a live per-invocation decision, or can managed
   evidence-updated deterministic routing satisfy v1?
2. Who owns policy proposal, independent evaluation, security review, approval,
   signing, installation, and rollback?
3. What exact golden path, deterministic checks, quality/completion metric,
   non-inferiority margin, confidence rule, and minimum trial count apply?
4. Which signals/strata are valid in v1, and which may influence security-
   relevant selection?
5. Are observational production records eligible only for hypothesis
   generation, or may they contribute to promotion evidence?
6. What task-level grouping prevents leakage and defines comparable control/
   candidate populations?
7. What drift thresholds pause, rollback, or require re-evaluation, and may a
   pre-approved automatic rollback select the prior signed artifact?
8. What signing/trust mechanism and content-addressed approval registry are
   authoritative, what independent review activates a digest in production,
   and how does signer revocation apply at approval, activation, and continued
   use?
9. Does advisor enforcement wait for ADR 0112, compose after it, or remain a
   mutually exclusive application mode under a new ADR?
10. How are model/provider version changes handled: invalidate evidence,
    discount it, or create a new stratum?

## Source traceability

| Source | Classification | Claims supported |
|---|---|---|
| AISDLC-288/292 | Lead-accepted local foundation | Acceptance envelope, decisions, ownership boundaries |
| AISDLC-289 | Lead-accepted local spike | Gateway serving/selector boundary and actual-model evidence needs |
| AISDLC-291 | Lead-accepted local spike | Pre-run advisor contract, integration gaps, stochastic audit distinction |
| ADR 0091 | Accepted/current | Static precedence |
| ADR 0112 / PR 7842 | Accepted, unimplemented | Deterministic overlay target, trace/fallback, new-ADR requirement |
| PR 7808 | Rejected/closed | Its routing schema and modes are not baseline |
| ADRs 0137/0138 | Accepted; route code implemented for pi/Codex/Claude, live OIDC rotation proof remains per-runtime | Gateway route/auth/fail-closed constraints and long-running pilot-readiness evidence boundary |
| `docs/problems/adaptive-agent-selection.md` | Open problem document | Ledger, shadow/evaluation ideas, bias/drift/governance questions |
| `docs/problems/testing-agents.md` | Open problem document | Golden-set, repeated/statistical, and deterministic-first guidance |
| `internal/evalmeasure`, telemetry, metrics | Current implementation | Existing versioned measurements, trace joins, and evidence gaps |
| AISDLC-5 authenticated link and description context | Jira dependency/evidence discipline | AISDLC-95 depends on repeated trials, exact config, deterministic checks, and evidence before adoption; exact gated milestone remains a human decision |
| AISDLC-345 authenticated related-work context | Jira ownership boundary | Authoritative attributable run/work-item cost evidence consumed by this policy pipeline |
| AISDLC-282 authenticated related-work context | Jira ownership boundary | Cumulative spend enforcement; not implemented by the routing policy ledger |
