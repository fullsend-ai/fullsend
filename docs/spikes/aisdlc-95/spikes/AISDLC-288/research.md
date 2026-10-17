# AISDLC-288 research: acceptance criteria for automatic model routing

Status: proposed for reporter review; not yet persisted to Jira
Repository baseline: `fullsend-ai/fullsend@e37e3a003084d5d9dfb5c3d93af2590460e4bebe`
Researched: 2026-10-12

## Purpose

This spike turns AISDLC-95's explicit requirements and remaining implied/open
questions into a reviewable decision set. It does **not** choose an automatic
routing architecture. AISDLC-289, AISDLC-290, and AISDLC-291 investigate the
three candidate placements.

## Authenticated Jira snapshot

Red Hat Jira was reconciled through authenticated, read-only access on
2026-10-12. The live facts used below are:

- AISDLC-40 is the live Jira parent of AISDLC-95. The authenticated hierarchy
  is `AISDLC-40 -> AISDLC-95 -> AISDLC-287 -> AISDLC-288..292`.
- AISDLC-95 is an **In Progress Feature** with start date 2026-10-07.
- AISDLC-287 is its child **Epic** and is **In Progress**. Its legacy `Epic
  Status` custom field still says `To Do`; that legacy value is not its workflow
  status.
- AISDLC-288 through AISDLC-292 are child **Spikes** under AISDLC-287. Each is
  **New** and unassigned.
- AISDLC-95 `depends on` AISDLC-5 and has `Related` links to AISDLC-15,
  AISDLC-116, AISDLC-282, and AISDLC-345.
- AISDLC-144 is relevant configuration/precedence context but is not linked to
  AISDLC-95 in the live snapshot.
- AISDLC-116's description says it is blocked by AISDLC-24, but Jira encodes
  their actual issue link as `Related`. Prose must not be presented as a live
  blocker relationship.

These are observed Jira facts, not product decisions. Jira should still be
re-read immediately before a later mutation so post-snapshot drift is not
silently overwritten.

## Executive finding

AISDLC-95 already states a substantial solution-neutral acceptance envelope:
automatic routing is opt-in; pinned routing remains the baseline and rollback;
selection is constrained to approved models/providers and policy; failures and
escalations are deterministic and visible; promotion requires repeated
golden-path equivalence against the pinned baseline; and every selection must
be attributable to its policy, inputs, serving result, cost, latency, and
outcome.

Twelve product decisions remain open. They concern the first-release routing
surface, classifier eligibility, security downgrades, dependency gates, pilot,
budget scope, configuration surface, quality-gate escalation, online learning,
gateway evidence, and v1 signals. None can safely be resolved by interpreting
ADR 0112 or the closed PR 7808: ADR 0112 deliberately defers learned/LLM
routing, and PR 7808 was rejected in favor of ADR 0112.

## Architectural baseline

### Accepted decisions

1. **Static precedence exists.** ADR 0091 resolves top-level runtime, model,
   and effort as flags > `FULLSEND_*` environment > the agent's `agents:`
   entry > repo-wide runtime or harness model/effort > default.
2. **Deterministic per-task routing is harness overlays.** ADR 0112 allows
   overlays to set `model` and `effort` using the existing CEL mechanism; it
   does not add a second `routing:` language.
3. **Model and effort are deliberately unguarded harness fields.** Author-shaped
   change facts may select among reviewed model/effort branches. Therefore an
   overlay cannot establish a security floor. A security-relevant harness must
   keep its strongest safe choice at the top level and loosen only when an
   appropriate positively proven condition is acceptable.
4. **Guarded fields fail closed.** Under ADR 0112, an overlay setting a guarded
   field may read only `runtime.forge`, trusted-ref `config`, and the normative
   event/entity allowlist. Fields added later are untrusted until explicitly
   admitted.
5. **Resolution is observable.** The plan and a dedicated span must record
   overlay matches, fields set, precedence overrides, and model/effort fallback.
6. **Unserved overlay model fallback is fixed.** The runner retries once with
   the model and effort it would have used without overlays; existing
   runtime-specific `FULLSEND_FALLBACK_MODELS` behavior then applies.
7. **Runtime and persona models are outside overlay routing.** ADR 0112 says
   neither is a harness field. Persona model resolution is separately governed
   by ADR 0104 and, for Codex-native children, ADR 0126.
8. **LLM/learned routing is deferred.** ADR 0112 explicitly requires a new ADR
   for it.
9. **The current inference gateway is a route, not an automatic router.** ADR
   0137 selects a gateway by a concrete `gateway/` model prefix. When that
   route applies, gateway failure never falls back to a direct-provider route
   or static provider credential.
10. **A selector does not inherit a generic gateway control plane.** If an
   external-gateway option adds alias/policy/candidate discovery, every such
   call must use the configured gateway origin and that block's fixed ADR 0138
   auth mode. The runner acquires and retains the real credential host-side.
   Authentication is mode-specific: `oidc` validates a JWKS-backed JWT
   signature, issuer, purpose-specific audience, token `iat`/`exp`, required
   claims, and exact repository claim; `api-key` securely validates the opaque
   key, active/revoked state, exact repository authorization, configured model/
   path scopes when its contract defines them, and expiry only when that key
   contract defines expiry. An opaque key is not required to have JWT claims,
   issuer, audience, or signature. Shared controls require the configured
   origin, fixed mode with no substitution, exact repository authorization,
   caller credential headers stripped before every upstream and never reflected
   or logged, and fail-closed malformed, denied, timed-out, or unreachable
   calls. A gateway alias that has won selection remains on the same block/auth
   mode for control-plane, credential, classifier, gateway, and upstream
   failures, or the run fails; it never escapes to a direct-provider route. The
   reusable upstream gateway API key remains valid until revoked or until its
   own contract-defined expiry; expiration or deletion of the separate run-
   scoped OpenShell provider credential limits sandbox use but does not revoke
   that gateway key. Any new endpoint, host-side transport, egress rule, or
   configuration surface belongs in the selector ADR. This boundary
   concentrates prompt, completion, repository-policy, and provider-key risk
   at the gateway and requires explicit operator controls and review.

### Accepted but not implemented on the reviewed baseline

ADR 0112 was merged by PR 7842 on 2026-10-06 as a documentation-only decision.
Current `internal/harness/forge.go` still defines `OverlayEntry` by embedding
`ForgeConfig`, and `ForgeConfig` has neither `model` nor `effort`. The living
field reference and `docs/architecture.md` both say ADR 0112 is decided but not
implemented. No `config.overlay_resolution` implementation is present.

Consequently, acceptance criteria must not claim that automatic routing can
already "plug into overlays." Implementation work must first deliver or depend
on the ADR 0112 seam, including the typed unserved-model signal noted by PR
7842.

### Rejected historical alternative—not current architecture

Closed PR 7808 proposed a second `.fullsend/config.yaml` `routing:` language,
`off`/`shadow`/`enforce` modes, model tiers/catalogs, an `allowed_models` list,
protected agents, runtime routing, persona routing, provider preflight, and an
offline learning ledger. It was closed on 2026-10-02 in favor of PR 7842 after
the Technical Review Forum discussion.

Those ideas may inform questions or tests, but they are not accepted Fullsend
decisions and must not appear as requirements without a new decision/ADR.

Per-prompt in-sandbox routing was also rejected: it uses author-controlled
prompts and loses prompt-cache continuity.

## Explicit AISDLC-95 acceptance envelope

The following criteria were explicit in the retrieved AISDLC-95 description.
The identifiers here are local traceability labels, not Jira IDs.

| ID | Explicit criterion | Testable evidence |
|---|---|---|
| E1 | Operators can choose pinned or opt-in automatic policy without product-code changes. Automatic routing is not mandatory. | Versioned configuration examples and config-validation tests. |
| E2 | A documented rollback to pinned mode exists. | Rollback runbook plus a successful rollback exercise. |
| E3 | Automatic selection is bounded by an approved model pool and provider policy. | Policy/config tests that reject an out-of-pool model and unapproved provider. |
| E4 | Policy can constrain capability, quality/latency floors, budget, escalation, and human review at project/workflow/stage scope. | Policy contract and conformance cases for every supported scope; decisions in D1/D7 are prerequisites to final wording. |
| E5 | Sensitive context never goes to an unapproved provider, and no out-of-policy downgrade is silent. | Negative security tests and decision records for suppression/failure. |
| E6 | Model unavailable, policy violation, budget condition, insufficient confidence, and quality-gate failure each have deterministic, visible fallback or escalation behavior. | Failure matrix proving exactly one terminal outcome per case. |
| E7 | A failed routing policy remains pinned or follows only its configured fallback/escalation path. | Fault-injection runs and trace assertions. |
| E8 | A representative golden path is versioned with fixed inputs, configuration, deterministic checks, and a pinned-model baseline. | Immutable evaluation manifest and artifacts. |
| E9 | Pinned and automatic policies run identical inputs repeatedly; automatic routing passes the same deterministic checks and an agreed quality/completion tolerance. | Repeated-trial report with uncertainty and a pre-agreed promotion threshold. |
| E10 | Material differences are attributable to the routing decision and resulting artifacts; a policy that fails equivalence cannot be promoted. | Joined decision/run/artifact records and a blocked-promotion test. |
| E11 | At least one production-adjacent, opt-in pilot runs against an approved model pool. | Pilot record; D6 decides which workflow. |
| E12 | Every routed invocation has a queryable record of policy/version, signals/reasons, selected model, fallback/escalation, cost, latency, and outcome. Cost must consume the authoritative attributable run/work-item evidence contract owned by AISDLC-345 rather than create a second total. | Query examples, completeness checks, and reconciliation with AISDLC-345 evidence, including unknown/incomplete cost rather than treating it as zero. |
| E13 | Dashboards compare quality, completion, latency, cost, and escalation with the pinned baseline. Cost reduction cannot substitute for quality equivalence. | Dashboard and promotion-gate evidence. |

## Implied criteria requiring human validation

These are reasonable implications of the description and linked architecture,
but they are **not accepted criteria** until the named decision is made.

| ID | Candidate implied criterion | Why it is implied | Required decision |
|---|---|---|---|
| I1 | Record the concrete model and provider that actually served, not only an alias/recommendation. | E12 requires attribution; a gateway alias or fallback can differ from the served model. | D2 and D11 |
| I2 | Record immutable policy/advisor versions, normalized decision inputs, and the observed output so an auditor can reconstruct what happened. Do not claim this makes a stochastic classifier deterministically replayable. | E9/E10/E12 require comparable repeated runs and attribution. | D3 and D10 |
| I3 | Use one correlation identity across the applicable decision or policy-resolution components, provider/gateway telemetry, attempts, quality gates, and final artifact. | Without it, E10 and E12 cannot be demonstrated end to end. | D2 and D9 |
| I4 | Treat historical quality as input to a reviewed, versioned policy update rather than uncontrolled online learning. | AISDLC-5 evidence discipline and the adaptive-selection problem require human promotion; AISDLC-95 does not explicitly decide online adaptation. | D10 |
| I5 | Consume AISDLC-345's authoritative attributable run/work-item cost evidence for routing records and budget inputs; limit AISDLC-95's control to its selected routing unit; leave cumulative multi-run, per-agent/per-work-item enforcement to AISDLC-282. | The live related items separate evidence (AISDLC-345), routing choice (AISDLC-95), and cumulative enforcement (AISDLC-282). | D7 |
| I6 | Bound any post-quality-gate escalation by attempt count, terminal outcome, and attribution of incremental cost. | E6 requires deterministic escalation; repeated escalation can become an unbounded cost loop. | D9 |
| I7 | Require shadow or offline evaluation evidence before an automatic policy can enforce. | E8-E10 imply pre-promotion comparison, but the word `shadow` is not explicit and was part of rejected PR 7808. | D5 |
| I8 | A gateway route failure must not fall back to direct provider credentials. | Accepted ADR 0137 makes this a security boundary. | No product choice for the current gateway route; D2 must preserve it. |

## Human decision register

No option in this table is silently selected. `Blocking` identifies what
cannot be finalized until a human chooses.

| ID | Question | Options to validate | Architectural evidence / consequence | Blocking |
|---|---|---|---|---|
| D1 | What does v1 route? | A: top-level `model` and `effort` only. B: include runtime. C: include persona/subagent models. D: another explicit combination. | ADR 0112 supports only A. AISDLC-144 concerns layered configuration and precedence; it does **not** own or resolve persona/subagent routing. Runtime and persona/subagent routing therefore remain unresolved and need an explicit owner and likely a new ADR if included. | Scope, policy schema, evaluation population |
| D2 | Which placement(s) produce or apply the effective choice? | A: external gateway at request time. B: pre-run advisor. C: offline-authored deterministic policy/overlays. D: a defined composition. | ADR 0137 gateway is currently a serving route; ADR 0112 owns deterministic overlays; a live advisor/LLM needs a new ADR. | Integration contract, timing, served-model record, fallback ownership |
| D3 | What does “deterministic initial policy” permit? | A: deterministic rules only. B: classifier/LLM may recommend if bounded, versioned, explainable, and able to abstain. | ADR 0112 leaves LLM-based routing open. Recording a stochastic classifier's inputs, output, and version makes the observed decision auditable; it does not guarantee the classifier will emit the same output when rerun. | Confidence/abstention criteria and audit/reproduction claim |
| D4 | May a security-relevant stage be downgraded automatically? | A: remain pinned; automatic may only escalate. B: downgrade only from trusted facts under an approved rule. C: general bounded downgrade. | ADR 0112 says model/effort overlays cannot enforce a floor because author-shaped facts can steer them. | Security acceptance tests and policy floors |
| D5 | What exactly does AISDLC-5 block? | A: all implementation. B: enforcement/promotion only. C: production pilot only. D: another milestone split. | AISDLC-5 supplies repeated-trial/evidence discipline; the live `depends on` link does not define the blocked milestone. | Delivery ordering and Definition of Ready |
| D6 | What is the first golden-path pilot? | A: review using AISDLC-116 and AISDLC-24 evidence. B: another named stage. C: a cross-stage suite. | AISDLC-116 is a related review-cost use case. Its description says it is blocked by AISDLC-24, but the actual Jira link is `Related`; no live decision selects either as AISDLC-95's pilot or encodes AISDLC-24 as its blocker. | E8, E9, E11 dataset and measures |
| D7 | What is “budget” in AISDLC-95? | A: per invocation. B: per stage. C: per workflow run. D: selection hint only. Combinations must state precedence. | AISDLC-345 owns authoritative attributable cost evidence consumed by routing. AISDLC-282 owns cumulative per-agent/per-work-item spend enforcement. Neither related link chooses AISDLC-95's routing-unit budget behavior. | E4/E6 policy and failure semantics |
| D8 | What counts as “configuration without code changes”? | A: a custom harness composed on a built-in is sufficient. B: `.fullsend/config.yaml` must configure built-ins directly. C: both. | ADR 0112 says deterministic overlays require a custom harness; the direct repo-policy alternative was rejected. AISDLC-144 supplies configuration-layer and precedence context only, not routing scope. | Operator UX and configuration acceptance test |
| D9 | How does quality-gate escalation execute? | A: restart the whole run. B: resume the run. C: create a separate follow-up run. | These have different prompt-cache, state, authorization, cost, and attribution semantics. ADR 0112 only defines fallback for “model not served.” | E6, I3, I6 |
| D10 | How may historical quality affect production? | A: reviewed offline policy revisions only. B: bounded online adaptation after a promotion gate. C: no adaptive behavior in v1. | `adaptive-agent-selection.md` recommends shadowing and a human checkpoint but leaves learning design open. | Policy lifecycle and evaluation-reproduction semantics |
| D11 | What claim may AISDLC-95 make from AISDLC-15 today? | A: consumer interest plus an exploratory component-integration recommendation, explicitly noting pending architect review. B: an approved convergence recommendation, but only after that approval is recorded. C: available gateway/automatic-selector capability, but only after separate capability evidence exists. | AISDLC-15 comments document Hummingbird's explicit interest in standardizing on an ADLC model gateway, sandbox, and observability components. Discovery material recommends exploratory component-level integration, including an ADLC gateway direction, while later comments still request architect review/approval. The current evidence supports A, not B or C; it does not select AISDLC-95's architecture. | Related-work wording and D2 evidence |
| D12 | Which inputs are required in v1? | Select a closed minimum from stage, risk, context size, capability, history, latency, availability, and budget; defer the rest explicitly. | A growable or text-bearing input surface conflicts with fail-closed trust and makes v1 unevaluable. ADR 0112's change facts are untrusted and no adapter populates them yet. | Input contract, test matrix, data availability |

## Decision-ready acceptance-criteria templates

These templates are intentionally parameterized. Replace every bracketed
token only after the corresponding decision above is approved.

### Scope and selection

1. In v1, automatic routing affects **[D1 surface]** once per **[routing
   unit]**, at the placement and time defined by **[D2]**. Fields outside that
   surface remain resolved by their accepted static mechanisms. A gateway may
   decide at request time, a pre-run advisor before sandbox creation, and an
   offline authoring system before the production run; the criterion does not
   choose among them.
2. The effective choice is produced by **[D2 placement/composition]**. Any
   recommendation, policy output, alias, or concrete selection is accepted for
   execution only if the appropriate enforcement layer confirms it is inside
   the approved model/provider set and satisfies required capabilities.
3. The v1 input contract is the closed set **[D12 signals]**. Each input has a
   named source, trust classification, missing-value behavior, and versioned
   schema. Unknown fields do not become eligible automatically.
4. **[D3 classifier rule]**. If a live classifier is permitted, abstention,
   malformed output, timeout, or unavailable service deterministically uses
   the pinned selection unless a separately approved escalation applies. Its
   recorded input, output, and version support audit of the observed decision;
   exact output replay is required only if **[D3]** explicitly chooses it and
   the implementation can prove it.

### Security and precedence

5. Security-relevant stages follow **[D4 floor rule]**. No recommendation can
   weaken provider approval, credential route, sandbox policy, tools,
   permissions, review requirements, or other guarded controls.
6. The approved placement documents how it composes with ADR 0091 static
   precedence, ADR 0112 deterministic overlay resolution/trace/fallback when
   overlays participate, and ADR 0137 gateway route isolation when a gateway
   route participates. A live learned/LLM decision requires its own ADR. Any
   new precedence or fallback behavior must be accepted there rather than
   inferred from ADR 0112's unimplemented seam.
7. A selected `gateway/` model remains on the ADR 0137 gateway route. Gateway
   control-plane, credential, authentication, authorization, classifier,
   availability, or upstream failure either chooses an explicitly approved
   concrete `gateway/<model>` through the same configured block and fixed ADR
   0138 auth mode, or is terminal; it never reveals, prepares, or falls back to
   direct-provider credentials. Every selector endpoint uses the configured
   origin, a runner-host-acquired credential, the configured fixed mode without
   substitution, and exact repository authorization. In `oidc`, it validates a
   JWKS-backed JWT signature, issuer, purpose-specific audience, `iat`/`exp`,
   required claims, and exact repository claim. In `api-key`, it securely
   validates the opaque key's correctness, active/revoked state, exact
   repository authorization, contract-defined model/path scopes, and expiry
   only if the key contract defines it—never invented JWT properties. Both
   modes strip caller credential headers before every upstream, neither reflect
   nor log credentials, and fail malformed, denied, timed-out, or unreachable
   calls closed. OpenShell provider-credential expiry/deletion limits sandbox
   use but does not revoke the distinct upstream gateway API key. The selector
   ADR must approve every new endpoint, transport, egress rule, and
   configuration surface and account for the gateway's concentrated prompt/
   completion, repository-policy, and provider-key risk.

### Evaluation and promotion

8. **[D5 dependency milestone]** cannot proceed until AISDLC-5's repeated
   trial, exact configuration capture, deterministic checks, and uncertainty
   requirements are satisfied.
9. The first golden path is **[D6 pilot]**, with a versioned input set,
   pinned-model baseline, repeated runs, deterministic pass checks, and
   pre-agreed quality/completion non-inferiority threshold.
10. Enforcement requires **[D10 lifecycle and whether shadow/offline evaluation is
    required]**. A candidate that reduces cost but misses the quality gate is
    rejected and cannot be promoted.

### Failure, budget, and evidence

11. Budget means **[D7 definition]**. The policy states the terminal action
    for authoritative cost evidence that is missing/unknown, exhausted, or
    exceeded. Routing consumes AISDLC-345's attributable cost evidence without
    creating a conflicting total; cumulative per-agent/per-work-item
    enforcement remains owned by AISDLC-282 unless explicitly re-scoped.
12. A quality-gate escalation uses **[D9 execution model]**, with at most
    **[attempt bound]**, a defined terminal outcome, and incremental cost and
    latency attributed to the originating decision.
13. Each invocation records the applicable policy/advisor identity and version,
    normalized input digest, observed recommendation/decision and reasons,
    accepted concrete model/provider or alias, applicable ADR 0112 overlay and
    ADR 0091 precedence results, every attempt/fallback/escalation, actual served
    model/provider, usage/cost/latency, quality result, and final
    artifact/outcome under one correlation identity. This reconstructs the
    observed decision; it does not by itself make a stochastic component
    deterministically replayable.
14. Operator configuration uses **[D8 surface]**, has validation and examples,
    and provides a tested rollback to the pinned selection without a product
    code change.

## Related-work traceability

This table separates live Jira link facts from descriptive scope and proposed
criteria impact. A description's prose is not promoted to a Jira relationship.

| Work item | Live Jira fact (2026-10-12) | Scope/evidence boundary | Criteria impact |
|---|---|---|---|
| AISDLC-40 | Parent of AISDLC-95. | Parent outcome boundary. AISDLC-95 contributes governed model routing toward that outcome; it does not own the whole AISDLC-40 outcome. | Preserve outcome traceability, but infer no additional AISDLC-95 acceptance criterion from parentage alone. |
| AISDLC-5 | AISDLC-95 `depends on` AISDLC-5. | Evaluation-first improvement, repeated trials, exact configuration capture, and uncertainty. The link does not say which AISDLC-95 milestone it blocks. | D5 and E8-E10. |
| AISDLC-116 | `Related` to AISDLC-95. | Concrete review-cost use case; candidate pilot only. Its prose says “blocked by AISDLC-24,” while the actual 116↔24 issue link is `Related`. | D6; no pilot or blocker is selected by the link. |
| AISDLC-24 | No direct AISDLC-95 link in the snapshot; `Related` to AISDLC-116. | Review-agent evaluation suite that could supply evidence if humans choose the review pilot. | Potential D6 evidence, not an established AISDLC-95 dependency. |
| AISDLC-15 | `Related` to AISDLC-95. | Jira comments contain discovery evidence that Hummingbird is interested in ADLC's model gateway and an exploratory recommendation for component-level integration. Architect review/approval was still requested. The evidence does not prove that a gateway or automatic selector is available or capable of satisfying AISDLC-95. | D11; retain as consumer-interest/integration evidence, not an implementation or architecture decision. |
| AISDLC-345 | `Related` to AISDLC-95. | Owns authoritative attributable run/work-item usage and cost evidence, explicit unknown/incomplete cost, and reconciliation; attribution does not itself enforce a budget. | E12, D7, and I5 evidence source. |
| AISDLC-282 | `Related` to AISDLC-95. | Owns cumulative spend thresholds and enforcement per agent and work item across automatic and manual runs. | D7/I5 enforcement boundary. |
| AISDLC-144 | Not linked to AISDLC-95 in the snapshot. | Layered configuration, field representation, and source precedence only. It does not resolve runtime or persona/subagent model routing. | D1 remains open; D8 uses it only as configuration context. |

## Proposed Jira-ready update for AISDLC-288

This is proposed result text, not an external update:

> Research found that AISDLC-95 already has an explicit solution-neutral
> acceptance envelope for opt-in/pinned modes, approved-policy selection,
> deterministic failure handling, golden-path equivalence, pilot evidence,
> attributable decision records, and baseline-comparative dashboards. Fullsend
> ADR 0112 is the accepted architecture for deterministic model/effort routing
> through overlays, but it is not implemented on the reviewed main branch; it
> does not decide where an automatic selector belongs. A live learned/LLM
> routing decision still requires a separate ADR. PR 7808's separate routing
> language, tiers, modes, runtime routing, and persona routing were rejected
> alternatives, not current
> requirements.
>
> Twelve human decisions remain: v1 routing surface; routing placement/timing;
> classifier eligibility; security-stage downgrade policy; AISDLC-5 milestone
> gate; golden-path pilot; budget unit; meaning of “configuration without code
> changes”; quality-gate escalation execution; historical-quality lifecycle;
> the interpretation of AISDLC-15 gateway evidence; and the closed v1 signal
> set. Parameterized,
> testable criteria for each decision are attached in the local research
> artifact. AISDLC-95 acceptance criteria should not be finalized until each is
> approved or explicitly deferred with an owner.
>
> The authenticated 2026-10-12 Jira reconciliation also establishes three
> ownership boundaries: AISDLC-345 supplies authoritative attributable cost
> evidence, AISDLC-282 owns cumulative per-agent/per-work-item enforcement, and
> AISDLC-144 concerns layered configuration/precedence rather than resolving
> persona or subagent routing. AISDLC-15 comments verify Hummingbird interest in
> an ADLC model gateway and an exploratory component-integration recommendation,
> with architect review/approval still pending; they do not prove an available
> gateway or automatic-selector capability. AISDLC-116's description calls
> itself blocked by AISDLC-24, while their actual issue link is `Related`.

## Sources

Retrieved or inspected 2026-10-12 unless otherwise stated:

- Jira authenticated read-only snapshot: AISDLC-40, AISDLC-95, AISDLC-287
  through AISDLC-292, AISDLC-5, AISDLC-15, AISDLC-24, AISDLC-116,
  AISDLC-144, AISDLC-282, and AISDLC-345. The authenticated hierarchy is
  `AISDLC-40 -> AISDLC-95 -> AISDLC-287 -> AISDLC-288..292`; workflow status,
  start date, issue-link types/directions, and the cited descriptions were
  reconciled on 2026-10-12.
- AISDLC-15 Jira comments, read 2026-10-12: comment 18463472 records
  Hummingbird's interest in ADLC model-gateway, sandbox, and observability
  components plus an exploratory component-integration recommendation;
  comments 18870650 and 18871027 record the later architect-review request and
  handoff, without recorded approval in the retrieved thread.
- [PR 7842](https://github.com/fullsend-ai/fullsend/pull/7842), merged
  2026-10-06; documentation-only ADR 0112 decision.
- [PR 7808](https://github.com/fullsend-ai/fullsend/pull/7808), closed
  2026-10-02 in favor of PR 7842.
- `docs/ADRs/0112-overlays-may-set-any-harness-field.md`.
- `docs/ADRs/0091-per-agent-runtime-model-effort.md`.
- `docs/ADRs/0104-per-persona-model-resolution.md`.
- `docs/ADRs/0126-fullsend-owned-codex-subagents.md`.
- `docs/ADRs/0137-inference-gateway-credential-route.md`.
- `docs/ADRs/0138-inference-gateway-api-key-credential-mode.md`.
- `docs/architecture.md`.
- `docs/contributing/harness-fields.md`.
- [`docs/normative/normalized-event/v1/README.md`](../../../../normative/normalized-event/v1/README.md), especially change-fact
  availability and the closed trusted-field list used by ADR 0112.
- [`docs/problems/adaptive-agent-selection.md`](../../../../problems/adaptive-agent-selection.md), especially fitness, shadow-mode,
  human-promotion, and deterministic-safety questions.
- [`docs/problems/security-threat-model.md`](../../../../problems/security-threat-model.md), applying the repository's threat
  priority and model-as-toolchain/provider considerations.
- `internal/harness/forge.go`.
