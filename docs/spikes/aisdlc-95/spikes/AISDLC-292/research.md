# AISDLC-292 research: related-work boundaries for AISDLC-95

Date: 2026-10-12

## Purpose

This spike refines the boundaries between AISDLC-95, its parent AISDLC-40,
its child refinement epic, dependencies, related evaluation work, budget work,
configuration work, and the accepted Fullsend architecture. It does not choose
or implement an automatic-routing solution.

## Authenticated Jira snapshot

The following facts were read from live Jira with authenticated access on
2026-10-12. They are observed state, not a proposal to change Jira:

* AISDLC-95 is a Feature, is **In Progress**, and has start date 2026-10-07.
  It is parented to AISDLC-40.
* AISDLC-40 owns the broader outcome of keeping agent costs attributable and
  bounded. AISDLC-95 contributes governed model routing to that outcome; it
  does not own the entire AISDLC-40 outcome. This parentage supplies scope
  context and does not, by itself, create an AISDLC-95 acceptance criterion.
* AISDLC-287 is an Epic parented to AISDLC-95. Its workflow status is **In
  Progress**; its legacy `Epic Status` custom field is still **To Do**. Those
  fields are distinct and must not be conflated.
* AISDLC-288, AISDLC-289, AISDLC-290, AISDLC-291, and AISDLC-292 are Spikes
  parented to AISDLC-287. Each is **New** and unassigned.
* AISDLC-95 `depends on` AISDLC-5 and has `Related` links to AISDLC-15,
  AISDLC-116, AISDLC-282, and AISDLC-345.
* AISDLC-144 is relevant configuration-precedence context, but the snapshot
  does not show it as a live AISDLC-95 link.
* AISDLC-116's description says it is blocked by AISDLC-24, but their actual
  Jira issue link is `Related`. Description prose is not authoritative link
  state.

Jira may change after the capture time, so any later mutation must still be
preceded by a fresh read and an exact user-approved diff.

## Executive boundary map

| Source | Evidence-backed role in AISDLC-95 | Boundary to preserve | Relationship recommendation |
|---|---|---|---|
| AISDLC-40 | Live parent of AISDLC-95 and owner of the broader attributable/bounded agent-cost outcome | AISDLC-95 contributes governed model routing; it does not inherit ownership of the complete cost outcome, and parentage alone creates no new acceptance criterion | Preserve the observed AISDLC-40 -> AISDLC-95 parent/child hierarchy; describe the contribution boundary without inventing a dependency or criterion |
| AISDLC-5 | Evidence discipline: repeated trials, exact configuration capture, deterministic checks, and evidence before adoption | It supplies evaluation discipline; the exact AISDLC-95 milestone gated is not yet decided | Preserve the observed AISDLC-95 `depends on` AISDLC-5 link; clarify its milestone/exit condition in prose rather than inventing a different link |
| AISDLC-116 | Concrete review-cost use case | It is a possible pilot, not automatically AISDLC-95's golden path | `relates to` unless the first pilot is explicitly chosen |
| AISDLC-24 | Evaluation-suite context for AISDLC-116 | AISDLC-116 prose says "blocked by," but the live Jira link is only `Related`; it indirectly affects AISDLC-95 only if that pilot and dependency are explicitly approved | Preserve the observed `Related` link; do not report or create a blocker without a decision |
| AISDLC-15 | Hummingbird discovery/interest and an exploratory component-integration recommendation in comments; architect review/approval remained pending | The comments do not establish an approved or available gateway and do not describe an automatic selector | Preserve the observed `Related` link; treat Hummingbird only as a candidate requiring capability, availability, and architecture approval validation |
| AISDLC-345 | Authoritative attributable cost evidence at run/work-item granularity | AISDLC-95 consumes this evidence for routing evaluation and attribution; it does not redefine the cost evidence contract | Preserve the observed `Related` link and state the consumption boundary |
| AISDLC-282 | Cumulative spend enforcement per agent/work item across repeated runs | AISDLC-95 may consume budget state as a routing input only after that meaning is chosen; it must not duplicate cumulative-cap enforcement | Preserve the observed `Related` link and state the enforcement boundary |
| AISDLC-144 | Layered configuration and precedence context | It does not own persona/subagent routing. That routing scope remains unresolved; ADR 0112 also excludes runtime/persona models | Do not claim a live link or assign persona/subagent routing to AISDLC-144; cite only as context unless humans decide otherwise |
| PR 7842 / ADR 0112 | Accepted deterministic routing architecture | Harness overlays set `model` and `effort`; trace, precedence, trust, and one-retry fallback are decided, but the repository says this is not yet implemented | Link as an accepted architectural constraint; do not describe it as shipped behavior |
| ADR 0137 / current `inference.gateway` implementation | Accepted route with code implemented for pi, Codex, and Claude; live in-iteration OIDC placeholder rotation proof remains outstanding | A concrete `gateway/<model>` selection chooses this route; the gateway route is not an automatic model router and does not fall back to direct-provider credentials. Runs beyond the observed OIDC lifetime are not pilot-ready for a runtime until production-adjacent rotation/re-seed/re-read continuity is proven there | Treat route code and live readiness as separate states; gate long-running OIDC pilots on per-runtime proof while preserving the same route/auth mode and fail-closed behavior |
| ADR 0138 / current gateway auth implementation | Accepted and implemented explicit gateway credential modes | A block selects `oidc` or `api-key`; there is no implicit precedence or fallback between modes | Automatic selection must not silently change credential mode or escape to another credential route |
| PR 7808 | Closed alternative | A separate `routing:` policy and runtime-routing mechanism were not selected | Link only as rejected design history; do not reintroduce its concepts as current architecture |

## Fullsend architecture evidence

### Current accepted decision

`docs/ADRs/0112-overlays-may-set-any-harness-field.md` is Accepted and was
merged by PR 7842. It decides:

* Deterministic per-task model routing reuses harness `overlays:`; an overlay
  may set `model` and `effort`.
* Existing ADR 0091 precedence and merge rules remain in force.
* All fields except `model`, `effort`, `description`, and `doc` are guarded and
  may branch only on trusted facts. `model` and `effort` are deliberately not
  guarded because they change quality/cost rather than authority.
* Author-shaped inputs can therefore steer model and effort. These overlays
  cannot enforce a security floor. Security-sensitive harnesses should keep
  their strongest model/effort and restrictive values at top level and loosen
  them only when a trusted fact proves that is safe.
* Plan output and a dedicated span record matched overlays, affected fields,
  precedence overrides, and model/effort fallback.
* When a model selected by an overlay is not served, the runner retries once
  with the model and effort that would have applied without overlays. Runtime
  `FULLSEND_FALLBACK_MODELS` behavior then remains runtime-specific.
* Runtime and persona models are not harness fields and overlays cannot route
  them.
* LLM-based routing is deferred to AISDLC-95 and requires a separate ADR.

`docs/architecture.md` explicitly labels ADR 0112's behavior "Decided, not yet
implemented." Any AISDLC-95 plan must therefore name ADR 0112 implementation as
a prerequisite or companion delivery where it relies on those mechanics.

### Rejected alternative

ADR 0112 records PR 7808's separate `routing:` policy as an option not chosen,
because it would add a second conditional mechanism and route runtime as well.
It also rejects per-prompt sandbox routing because it consumes
author-controlled prompts and loses prompt-cache continuity. PR 7808 was
closed. These are historical alternatives, not accepted requirements.

### Current inference-gateway code and outstanding live proof

Since AISDLC-292 was written, two accepted ADRs and their implementation have
made the gateway boundary more precise:

* ADR 0137 makes `inference.gateway` a credential and serving route of its
  own. A runtime reaches it only after a concrete model selection such as
  `gateway/<model>`. The prefix selects the route and the remaining id is sent
  to the gateway. Configuration of a gateway block does not automatically
  route other models through it and does not choose a model based on task
  characteristics. It is therefore not AISDLC-95's automatic router.
* The route fails closed. If the configured gateway is unreachable or rejects
  the credential, the run fails; it never falls back to a direct provider,
  WIF, or a provider static key. This prevents a gateway security boundary
  from being bypassed silently.
  The same no-escape rule applies to control-plane, credential, gateway-side
  classifier, gateway, and upstream failures after a gateway route is chosen.
* ADR 0138 adds explicit `oidc` and `api-key` credential modes. `oidc` is the
  default and recommended mode. `api-key` supports gateways that cannot trust
  forge OIDC, at the cost of a reusable secret. A block names exactly one
  mode: there is no implicit precedence or fallback between them. Missing
  credentials fail the run before the agent starts.
* Current code implements these rules in
  `internal/config/inference_gateway.go`,
  `internal/cli/run_gateway_route.go`, and the pi, Codex, and Claude runtime
  gateway wiring. The config is runner-owned, provider credentials remain
  behind endpoint-bound placeholders, and `gateway/` selection is validated
  before sandbox execution.
* “Implemented” is not the same as fully proven for long-running OIDC use.
  ADR 0137 still records live placeholder rotation within one running
  iteration as outstanding. For each runtime admitted to a production-adjacent
  pilot, a run over five minutes or beyond the observed token lifetime must
  prove refresh scheduling
  from that token's `iat`/`exp`, a new placeholder generation settling and
  being re-seeded, the runtime re-reading it, and requests continuing through
  the same gateway block and OIDC mode. Failed refresh/re-seed or an expired
  token must fail closed without direct-provider fallback or an auth-mode
  switch. ADR 0138 API-key mode is a separate lifecycle: it has no OIDC
  re-seed loop; the upstream gateway key remains valid until revoked or its own
  contract-defined expiry, while the run-scoped OpenShell provider-credential
  lifetime is separately enforced. Provider-credential expiry/deletion limits
  sandbox use but does not revoke the gateway key.

An automatic-routing design may select a concrete gateway model and then use
this implemented route. It must compose with, rather than weaken, the
credential-mode and fail-closed boundaries above. The serving route does not
by itself decide whether a task should use that model. Conversely, AISDLC-95
need not require every automatic-routing candidate to use a gateway.

### Still open in repository design documents

`docs/problems/adaptive-agent-selection.md` says learned/LLM routing remains
open and identifies selection bias, workload bucketing, model-version changes,
fitness design, governance, and safety interaction as unresolved concerns.
`docs/problems/security-threat-model.md` leaves pre-run cost estimation open
and separately calls for deterministic loop circuit breakers and cost budgets.
This supports treating automatic selection as a distinct, governed decision;
it does not supply the missing decision.

## Proposed Jira-ready update for AISDLC-95

The following text is proposed, not approved. It is written to paste into the
feature after the live Jira fields and relationships are revalidated.

### Related work and ownership boundaries

> AISDLC-95 is a child of AISDLC-40. AISDLC-40 owns the broader outcome of
> keeping agent costs attributable and bounded; AISDLC-95 contributes governed
> model routing to that outcome and does not own the entire outcome. Parentage
> alone adds no new AISDLC-95 acceptance criterion.
>
> Within that contribution, AISDLC-95 makes model-selection cost, latency,
> quality, fallback, and outcome attributable and keeps pinned operation as the
> safe rollback. AISDLC-345
> owns the authoritative attributable run/work-item cost evidence that this
> routing work consumes. AISDLC-282 owns cumulative spend enforcement across
> repeated runs. AISDLC-95 does not duplicate either contract.
>
> * **AISDLC-5 — evaluation dependency.** Supplies repeated trials, exact
>   configuration capture, deterministic checks, and evidence requirements.
>   Jira already records AISDLC-95 as depending on AISDLC-5. The team must
>   decide whether that dependency gates implementation, enforcement and
>   policy promotion, or production rollout; this clarification does not by
>   itself change the link type.
> * **AISDLC-116 / AISDLC-24 — possible review pilot.** AISDLC-116 provides a
>   concrete review-cost use case. Its description says AISDLC-24 blocks it,
>   but the actual Jira link is `Related`; do not represent a live blocker
>   unless the relationship is explicitly approved and changed. This pair
>   affects AISDLC-95 only if review is selected as the first golden-path pilot.
> * **AISDLC-15 — gateway discovery.** Its comments record Hummingbird
>   discovery/interest and an exploratory component-integration recommendation.
>   Architect review/approval of that recommendation remained pending. The
>   comments do not establish an approved or currently available gateway, and
>   they do not establish an automatic selector. Fullsend now has a separate, implemented
>   `inference.gateway` credential/serving route under ADRs 0137 and 0138;
>   that route is evidence about integration constraints, not evidence that
>   AISDLC-15 selected a gateway or that the gateway performs automatic
>   routing.
> * **AISDLC-345 — attributable cost evidence.** AISDLC-345 owns authoritative
>   run/work-item cost evidence. AISDLC-95 consumes it for evaluation,
>   decision records, and outcome attribution.
> * **AISDLC-282 — cumulative budget ownership.** AISDLC-282 owns cumulative
>   spend enforcement across repeated runs per agent/work item. AISDLC-95 may
>   consume approved budget state as an input but does not own enforcement.
> * **AISDLC-144 — configuration precedence context.** AISDLC-144 owns layered
>   configuration/precedence work. It does not own persona/subagent routing.
>   Persona and subagent model routing are not covered by ADR 0112's harness
>   overlays, and their product ownership remains unresolved.
> * **Fullsend PR 7842 / ADR 0112 — accepted deterministic foundation.**
>   Deterministic routing uses harness overlays to set `model` and `effort`
>   with existing precedence, a resolution trace, and defined fallback. The
>   decision is accepted but is not yet implemented. LLM-based routing requires
>   a separate ADR.
> * **Fullsend ADRs 0137 and 0138 — implemented route code with an OIDC live-
>   rotation proof gap.** A concrete `gateway/<model>` selection uses the
>   configured gateway route; pi, Codex, and Claude have route wiring.
>   The route does not select models automatically and never falls back to
>   direct-provider credentials when the gateway fails. Its `oidc` and
>   `api-key` credential modes are explicit, with no implicit fallback. An
>   automatic selector may choose an approved gateway model only while
>   preserving these fail-closed credential and serving boundaries. Any
>   production-adjacent long-running OIDC pilot must first prove live refresh,
>   placeholder generation settle/re-seed, runtime re-read, and same-route/
>   same-auth continuity for each selected runtime. API-key gateway-key
>   revocation/contract expiry and run-scoped OpenShell provider-credential
>   lifetime remain separate; provider expiry/deletion does not revoke the key.
> * **Fullsend PR 7808 — rejected alternative.** A separate `routing:` policy
>   and runtime routing were not selected and are not current architecture.

### Proposed scope guard

> An automatic-routing design must preserve and compose with accepted
> precedence, trace, trust, and fallback boundaries where they apply. A design
> that uses deterministic harness overlays must identify whether ADR 0112
> implementation is a prerequisite. A design that selects `gateway/<model>`
> must preserve ADRs 0137 and 0138's explicit credential mode and fail-closed
> route, without falling back to direct-provider credentials. Learned or
> LLM-based routing requires a new ADR. Cost reduction cannot substitute for
> quality equivalence or provider/security policy.

## Relationship reconciliation and possible changes

The following distinguishes observed links from proposed changes:

1. **Observed:** the hierarchy is AISDLC-40 -> AISDLC-95 -> AISDLC-287 ->
   AISDLC-288 through AISDLC-292. Preserve that hierarchy. AISDLC-40 owns the
   broader attributable/bounded agent-cost outcome; AISDLC-95 contributes
   governed model routing and does not own the entire outcome. Do not derive a
   new AISDLC-95 criterion from parentage alone.
2. **Observed:** AISDLC-95 `depends on` AISDLC-5. Preserve the link while a
   human clarifies the exact gated milestone and exit condition.
3. **Observed:** AISDLC-95 has `Related` links to AISDLC-15, AISDLC-116,
   AISDLC-282, and AISDLC-345. Preserve them and add ownership prose if approved.
4. **Observed:** AISDLC-116 and AISDLC-24 are `Related`; only AISDLC-116's
   description says "blocked by." Do not report or create a blocker by inference.
5. **Observed:** AISDLC-144 is not a live AISDLC-95 link in this snapshot.
   **Proposal:** cite it as configuration-precedence context in prose; add a
   relationship only after explicit approval.
6. Put PR 7842, ADR 0112, and PR 7808 in description references, not Jira
   dependency links.

## Unresolved questions requiring human decisions

1. Which AISDLC-95 milestone does the existing AISDLC-5 dependency gate: implementation,
   shadow/enforcement promotion, or production rollout?
2. Is AISDLC-116/AISDLC-24 the first golden-path pilot, or merely one candidate?
3. What exact routing-time budget signal may AISDLC-95 consume—per invocation,
   stage, or workflow run—without taking ownership of AISDLC-282 enforcement?
4. Who owns persona/subagent routing, and is it included, deferred, or excluded
   from the first release? AISDLC-144 must not be assumed to own it.
5. Is there a currently available Hummingbird component/version that can be
   evaluated, and what does it actually implement? AISDLC-15's comments alone
   establish discovery/interest while architect review/approval remained
   pending; they do not prove approval, availability, or auto-selection.
6. Does the AISDLC-95 delivery plan depend on implementing ADR 0112 first, or
   will that implementation be delivered in the same program increment?
7. Which candidate solutions, if any, must use the implemented
   `inference.gateway` route rather than direct provider routes?
8. Should AISDLC-116's description be corrected to match its `Related` link to
   AISDLC-24, or should a separately approved link change make it a blocker?

## Source traceability

| Source | Status | Claims supported |
|---|---|---|
| AISDLC-40, authenticated snapshot | Observed Jira content and hierarchy | Live parent of AISDLC-95; owns the broader attributable/bounded agent-cost outcome to which governed model routing contributes; parentage alone adds no AISDLC-95 criterion |
| `docs/ADRs/0112-overlays-may-set-any-harness-field.md` | Accepted decision | Overlay fields, trusted-field rule, trace, fallback, security-floor limitation, runtime/persona exclusion, LLM-routing deferral |
| `docs/architecture.md` | Current evolving architecture | ADR 0112 is decided but not implemented |
| `docs/ADRs/0137-inference-gateway-credential-route.md` | Accepted decision; route code implemented for pi/Codex/Claude; named live proof remains open | Gateway is a concrete per-model credential/serving route; fail-closed behavior; no direct-provider fallback; live in-iteration OIDC placeholder rotation must be proven per pilot runtime |
| `docs/ADRs/0138-inference-gateway-api-key-credential-mode.md` | Accepted and implemented decision | Explicit `oidc` and `api-key` modes; no implicit precedence/fallback; missing credential fails closed |
| `internal/config/inference_gateway.go`, `internal/cli/run_gateway_route.go`, runtime gateway files | Current code | Config/auth validation, route planning, gateway model selection, endpoint-bound credential wiring |
| `docs/problems/adaptive-agent-selection.md` | Open problem document | Learned routing remains open; evaluation, bias, drift, governance, and safety questions |
| `docs/problems/security-threat-model.md` | Threat-model guidance | Pre-run cost estimation remains open; deterministic cost/circuit-breaker concerns |
| Fullsend PR 7842 | Merged; docs-only, verified from the PR and ADR record | Vehicle that accepted ADR 0112 |
| Fullsend PR 7808 | Closed/rejected, verified from the PR and ADR record | Separate routing-policy alternative was not chosen |
| Authenticated live Red Hat Jira snapshot, 2026-10-12 | Observed Jira state | 40/95/287/288-292 hierarchy, types, statuses, assignees, start date, and 95/5/15/116/282/345 plus 116/24 link types |
| AISDLC-15 comments, authenticated snapshot | Observed Jira content | Hummingbird discovery/interest and exploratory integration recommendation with architect review/approval pending; no approval/availability/automatic-selector claim |
| AISDLC-144, authenticated snapshot | Observed Jira content | Layered configuration/precedence context; no persona/subagent ownership and no observed live 95 link |
