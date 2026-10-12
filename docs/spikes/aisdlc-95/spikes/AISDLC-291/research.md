# AISDLC-291 research: pre-run routing advisor

Status: proposed for reporter review; not yet persisted to Jira
Repository baseline: `fullsend-ai/fullsend@e37e3a003084d5d9dfb5c3d93af2590460e4bebe`
Researched: 2026-10-12

## Research question

Can a host-side advisor recommend a concrete top-level model and effort before
sandbox creation, while leaving Fullsend—not the classifier—in control of
provider approval, capability requirements, security floors, accepted
precedence, serving route, fallback, and audit records?

This is an implementation investigation, not an accepted architecture. Any
learned/LLM component that affects a live Fullsend selection requires a new
ADR. First-release scope, inputs, security downgrade policy, enforcement
precedence, pilot, and budget semantics remain human decisions from
AISDLC-288.

## Jira evidence boundary

An authenticated live Jira reconciliation on 2026-10-12 confirms that
AISDLC-95 `depends on` AISDLC-5 and has `Related` links to AISDLC-282 and
AISDLC-345. AISDLC-345 owns authoritative attributable cost evidence at
run/work-item granularity; AISDLC-282 owns cumulative spend enforcement.
AISDLC-95 may consume both as advisor inputs/evaluation evidence but does not
own either contract. AISDLC-291 remains a New, unassigned Spike under the In
Progress AISDLC-287 Epic. This document makes no Jira changes.

## Executive finding

A pre-run advisor is technically compatible with Fullsend's host-runner shape:
the runner already resolves the harness, runtime, per-agent settings, model
aliases, credential route, provider set, and sandbox configuration on the host.
The safest insertion point is after the existing static selection has been
computed and before any model-dependent provider/credential planning or
sandbox creation.

The advisor must be **advisory, not authoritative**:

1. a strictly bounded component reads an approved, trust-classified signal
   contract and returns a recommendation or abstention;
2. a deterministic Fullsend policy gate validates that output and derives the
   accepted model and effort;
3. existing runner code derives the direct or `gateway/` serving route from
   the accepted model; and
4. outcomes feed a separate evaluation/promotion process.

The design is viable for an offline experiment now. A live shadow experiment
is non-enforcing but not automatically safe: a remote advisor can still
transmit task signals, so its destination, provider, credentials, signal set,
privacy/retention policy, and egress must be approved first. Enforcement is
blocked on a new ADR defining where an accepted recommendation sits relative
to ADR 0091 and, if used, the still-unimplemented ADR 0112 overlay result.
Fullsend also lacks a provider-neutral capability/model policy catalog, a
uniform actual-served-model signal, and populated normalized-event change facts.

## LangChain reference approach

[LangChain's “How to Build a Model Router in the Harness”](https://www.langchain.com/blog/how-to-build-a-model-router-in-the-harness)
describes the strongest directly relevant external example found for this
spike:

1. inspect production traces to understand the real task distribution;
2. select fast, balanced, and performance model choices using task evidence,
   provider guidance, benchmarks, cost, and latency;
3. classify the first human message using task-specific tier criteria;
4. choose the cheapest model likely to complete the task;
5. keep that model for the whole thread; and
6. evaluate it against a fixed-model baseline using offline evaluation or a
   live A/B test.

The first classifier used a general LLM with structured output; the later
version used Jev and reportedly classified almost 50 times faster. In the
reported Open SWE A/B test of 973 threads, routed threads had a median cost of
$0.94 versus $2.61 for an always-frontier control (64% lower). Merged-PR rates
were 29.2% versus 27.3% (`p = 0.49`), and PR-open rates were 38.9% versus 39.6%
(`p = 0.82`). A second fast-only control was stopped before statistical
significance because engineers reported immediate quality problems.

### What this evidence does and does not establish

It supports these hypotheses:

- task-aware classification benefits from harness/domain context;
- one selection per thread avoids mid-thread cache loss;
- always-frontier can overspend and always-cheap can damage quality;
- task analysis and outcome measures must precede enforcement; and
- decision-model latency may be low enough for pre-run use.

It does **not** prove that Fullsend will reproduce the savings or quality
result. Open SWE's workload, model pool, thread semantics, user population,
cost accounting, and “merged PR” success measure differ from Fullsend's. Merge
rate is also confounded by task difficulty, repository policy, reviewer
behavior, and selection effects. The experiment does not establish Fullsend's
security floors, approved-provider controls, deterministic fallback, gateway
credential boundaries, persona routing, or golden-path acceptance threshold.

### Required Fullsend adaptations

- Fullsend's default runtime prompt is deliberately content-free; the actual
  task comes through the agent definition and event/config context. It cannot
  simply classify `runtime.DefaultAgentPrompt` as LangChain classifies a first
  human message.
- Initial human/issue/comment text is author-controlled and may contain prompt
  injection. If admitted at all, it is untrusted data, not instructions, and
  transmission to an advisor provider requires explicit provider approval and
  a data-minimizing contract.
- The advisor must return a recommendation; Fullsend policy decides whether it
  is accepted.
- Model/provider/capability policy must use explicit, reviewed entries. PR
  7842 removed the model catalog/tier design from accepted ADR 0112, so the
  prototype must not assume global model tiers exist.
- The accepted model still follows the existing direct-provider or gateway
  route. An advisor does not own credentials or failover.
- Fullsend evaluation must use AISDLC-5's repeated trials and deterministic
  artifact checks, not transfer LangChain's merged-PR result as a threshold.

## Current Fullsend integration evidence

### Existing host resolution path

`internal/cli/run.go` currently:

1. loads the normalized event/config and composes the harness;
2. resolves runtime with flags/environment above per-agent/repo config;
3. applies `agents:` model/effort, then model/effort flag/environment overrides;
4. resolves `models.aliases`;
5. derives whether parent/children need direct OpenAI, Vertex, or gateway
   credentials;
6. calls `planGatewayRoute` for concrete `gateway/` selections;
7. creates the sandbox; and
8. bootstraps/runs the selected runtime.

This makes the proposed host hook concrete: compute the complete existing
static selection, invoke/gate the advisor, then continue alias/route/provider
planning using the **accepted** selection. Model-dependent work must not run
before the gate and then be reused after the model changes.

The present code applies effort overrides later than model/provider
classification. An implementation should extract a single
`resolveStaticSelection` step rather than insert advisor logic between today's
partially ordered statements.

### Precedence is unresolved, not free

ADR 0091's accepted chain is:

```text
flag > FULLSEND_* > agents: entry > repo/harness > default
```

There is no accepted “advisor” position. The rejected PR 7808 proposed a
separate routing-policy position, but closing that PR means its precedence is
not available for reuse. A new ADR must decide whether automatic selection:

- acts only in shadow;
- can replace a static value below explicit flag/environment overrides;
- produces an input consumed by overlays; or
- uses another explicit composition.

Until then, enforcement must refuse to start rather than guess.

### Conditional ADR 0112 composition

ADR 0112 is Accepted and defines deterministic overlay routing for `model` and
`effort`, resolution trace, guarded-field trust, and one retry without overlays
when an overlay-selected model is not served. It also says learned/LLM routing
needs a separate ADR.

It is not implemented on the reviewed `main`: `ForgeConfig` still lacks model
and effort, the architecture/field reference say “decided, not yet
implemented,” and no overlay-resolution trace exists.

A new advisor ADR must choose one of these explicitly:

- **After-overlay recommendation:** the advisor sees the fully resolved static
  result, including any overlay. If the gate replaces it, the new precedence
  and fallback relationship must be recorded.
- **Advisor signal into overlays:** the recommendation becomes a new CEL input.
  Its trust classification, schema, error behavior, and trace are new ADR work;
  it is not on ADR 0112's trusted allowlist.
- **Mutually exclusive modes:** a run uses either deterministic overlay routing
  or advisor enforcement, with a defined pinned baseline.

The prototype supports shadow-after-static without choosing an enforcement
ordering. It requires an accepted application point before it can mutate the
selection.

The proposed configuration is a closed matrix, not a permissive enum. Pinned
mode ignores all unused advisor configuration and never calls the advisor.
Shadow admits only a `shadow_only` application point and always executes the
static selection. Enforcement admits only an application point explicitly
accepted by the new ADR (the prototype names `below_operator_override`);
`shadow_only`, `undefined`, and the unimplemented
`advisor_signal_to_overlay` point are configuration errors before request
transmission or sandbox creation. A future ADR can revise the matrix; a string
value alone cannot authorize enforcement.

### Gateway and direct serving routes

ADRs 0137 and 0138 are accepted serving/credential boundaries with route code
implemented for pi, Codex, and Claude; they are not automatic routing:

- a concrete `gateway/<model>` selects the configured gateway route;
- `oidc` and `api-key` are explicit credential modes with no implicit fallback;
- gateway failure is terminal for that route and cannot fall back to direct
  provider WIF/static credentials; and
- a configured gateway does not move non-gateway models onto the route.

Once a concrete gateway selection is accepted, any subsequent control-plane,
credential, gateway-side classifier, gateway, or upstream failure must remain
on the same configured gateway block/auth mode or fail closed. None may trigger
direct-provider planning or credential preparation.

The advisor may recommend an approved concrete gateway model or an approved
direct model. After policy acceptance, existing route planning owns the route.
The advisor may not supply a base URL, auth mode, credential, provider header,
fallback route, or model-list mutation.

Route code presence does not establish long-running OIDC pilot readiness. ADR
0137 explicitly leaves live placeholder rotation within one running iteration
to be proven. Before any production-adjacent advisor pilot uses a runtime with
OIDC for more than five minutes or beyond the observed token lifetime, that
runtime needs live evidence that refresh scheduling derives from
each issued token's `iat`/`exp`; a new placeholder generation settles and is
re-seeded; the runtime re-reads it; and subsequent requests continue through
the same gateway block and OIDC mode. A failed refresh/re-seed or expired token
must terminate without direct-provider fallback or switching to API-key. API-
key mode is assessed separately: the upstream gateway key must be active/not
revoked and satisfy any scope/expiry defined by its own contract; the run-
scoped OpenShell provider-credential lifetime remains separately enforced and
there is no OIDC re-seed loop. Expiration or deletion of that provider
credential limits sandbox use but does not revoke the upstream gateway key.
The pilot records the per-runtime proof
artifact as an operational prerequisite; it does not claim that unit fault
injection is the live proof.

### Telemetry gaps

Current `metrics.json` records requested model, source, runtime, reported model,
usage/cost, iterations, and per-model usage where available. It does not record
an advisor/policy version, normalized signal digest, recommendation, rejection
reason, accepted decision, confidence/abstention, or one correlation identity
across later quality/forge outcomes.

`RunMetrics.Model` is intended to hold the provider-reported model, but runtime
support is not uniform. Codex's JSON stream carries neither model nor cost, and
the runner knows only the requested model. AISDLC-95's actual-served-model
attribution therefore needs an explicit capability/gap treatment; requested
model must never be mislabeled as independently verified served model.

## Advisor input contract

Inputs must be allowlisted by schema. “Available in the event” is not enough.

| Input class | Examples | Trust/use rule |
|---|---|---|
| Trusted policy | policy ID/version, approved model/provider entries, agent/stage security profile, pinned selection, allowed automatic choices | Read from trusted config/harness ref. Authoritative for the deterministic gate; never supplied by the classifier. |
| Trusted execution | runtime, agent/role, static selection and source, forge type, repo identity | Host-derived. May bound eligibility and capabilities. |
| Trusted event subset | normative `repo`, `source.system`, `actor.role`, base repo and fork status when present | Only the closed normative trusted-field list has this status. Missing values remain missing. |
| Author-shaped structured facts | changed paths/counts/additions/deletions, linked work-item state/labels, branch refs | May inform quality/cost recommendations if approved. Cannot establish or relax a security floor. `paths_complete=false` means absence is unknown. No v1 adapter currently populates ADR 0112 change facts. |
| Untrusted text | issue/comment/request text, titles, diff/source content | Highest injection/data-exposure risk. Exclude in the minimum contract unless explicitly approved. If enabled: bound length, label as data, scan/redact, use no tools, transmit only to an approved provider, never log raw content, and never let it change authority/security constraints. |
| Operational evidence | availability, latency/cost table, historical quality summaries | Versioned and provenance-bearing. Useful for selection, but not a security authority. Historical outcomes enter through reviewed policy/evaluation lifecycle unless D10 authorizes something else. |

The request contains no secrets, credentials, raw environment, gateway URL,
repository tokens, or unbounded files. Forge lookups must use `forge.Client`,
never `gh` or direct forge APIs outside the forge implementation.

## Recommendation versus accepted decision

The advisor output is deliberately weaker than the gate result:

| Advisor may return | Advisor may not decide |
|---|---|
| concrete model reference from the request's candidate IDs | whether a provider is approved for sensitive context |
| allowed effort value | runtime, persona/subagent model, tools, sandbox policy, permissions |
| abstention with closed reason code | gateway URL/auth/credential mode or direct-route fallback |
| closed reason codes and contributing signal IDs | the security floor or capability requirements |
| adapter-defined score only when its semantics/version are declared | promotion, quality equivalence, budget ownership |

The policy gate canonicalizes the recommended model, checks the exact approved
entry, runtime compatibility, capabilities, provider/data policy, explicit
security-stage allowed set, effort set, budget rule, and precedence. Unknown or
malformed output, timeout, advisor error, abstention, insufficient adapter-
specific score, or policy rejection produces the pinned/static choice or an
explicitly configured escalation—never an invented fallback.

Model “strength” is not inferred from names. A security floor is represented by
exact reviewed permitted choices (often pinned-only plus named escalations),
not by a nonexistent global tier order.

## Security analysis

### External prompt injection

The LangChain design reads the first human message. In Fullsend that content is
attacker/author-controlled. A classifier prompt saying “ignore routing rules”
must be data with no authority. The adapter must use a strict schema, no tools,
closed outputs, size/time limits, and no secrets. Even a successful injection
can at most produce a recommendation that the deterministic gate may reject.

### Insider/compromised credentials

Policy and advisor configuration must come from the same trusted/base ref used
for harness/config. A pull request cannot redirect the advisor endpoint, widen
providers, add candidate models, lower a security floor, or change gateway
auth. Operator flag/environment overrides retain their accepted precedence
unless a new ADR explicitly changes it.

### Drift and supply chain

Advisor engine, model/version, prompt/criteria, input schema, policy, candidate
set, and operational evidence snapshot are versioned. A changed classifier or
model is a new policy candidate requiring evaluation. The decision record
captures the observed input/output/version for audit; it does not claim a
stochastic classifier will emit the same output when rerun.

The advisor model is itself a supply-chain component. Provider/model diversity
may reduce correlated failure, but changing providers also changes data policy
and behavior and must pass the same approval/evaluation gate.

## Failure behavior

| Condition | Proposed deterministic host behavior |
|---|---|
| advisor disabled/pinned mode | do not call advisor; use existing static selection |
| shadow mode | after destination/provider/privacy approval, call, decode, and evaluate for evidence, but execute static selection for every advisor outcome; a configured named escalation is hypothetical only |
| timeout/unavailable/cancelled | record failure code; shadow executes static; enforce uses static or the approved named escalation |
| malformed/oversized/unknown output | reject entire output and record code; shadow executes static; enforce follows approved failure action |
| abstention/insufficient adapter-specific score | record abstention; shadow executes static; enforce uses static unless an approved failure action says otherwise |
| unapproved model/provider/capability/effort | record gate rejection; shadow executes static; enforce uses static or a separately validated approved escalation |
| security-floor conflict | gate suppression; shadow and enforce preserve the floor unless the named selection is inside its exact allowed set |
| explicit CLI/environment model or effort | preserve operator override under the prototype; final rule requires new ADR |
| accepted direct model unavailable | preserve the existing runtime-specific behavior in a prototype; the new ADR must define the advisor-selected unserved-model outcome and how every fallback is checked against the approved pool; no advisor retry is inferred |
| accepted overlay model unavailable | ADR 0112 fallback applies only after that seam is implemented and only if overlays selected it |
| accepted gateway model route fails | fail the run; never switch to direct credentials or another auth mode |
| long-running OIDC refresh/re-seed/re-read fails or token expires | fail the run on the same gateway/OIDC route; never prepare direct-provider credentials or switch to API-key |
| quality gate fails after run | follow the separately approved bounded restart/resume/follow-up design; no recursive advisor loop by default |

## Evaluation approach

1. Start with offline classification on a versioned, representative dataset.
2. Compare the advisor recommendation and gate result with the pinned baseline;
   record abstentions, policy rejections, and confusion by task/security class.
3. Run both policies on identical golden-path inputs with repeated trials.
4. Apply the same deterministic artifact checks, then compare completion,
   reviewer assessment, cost, latency, retries, and escalation with uncertainty.
5. Shadow in production-adjacent traffic only after input/provider/privacy
   approval. Shadow means the recommendation cannot change the live model. A
   gateway-backed pilot expected to outlive its OIDC token also requires the
   production-adjacent, per-runtime live rotation proof above.
6. Enforce only after the new ADR, approved precedence, and AISDLC-5 promotion
   gate. A cost win cannot compensate for a quality-equivalence failure.

The evaluator must be independent of the routed agent/advisor. Merge rate can
be one delayed outcome, but not the sole quality measure.

## Unresolved human decisions

1. Is v1 limited to top-level model/effort, as this prototype assumes for
   compatibility with ADR 0112, or is scope expanded?
2. What exact precedence/application point does an accepted recommendation
   have relative to flags/environment, `agents:`, harness, and overlays?
3. Is the advisor allowed to see untrusted task text or only structured
   allowlisted signals?
4. Which signal set is required for v1, and who owns each signal's provenance?
5. May a security-relevant stage use anything except pinned plus named
   escalation choices?
6. What score/abstention semantics are portable enough for the chosen adapter?
   If none, should the adapter itself return only recommend/abstain?
7. What is the first golden-path pilot, quality threshold, sample size, and
   promotion authority?
8. Does AISDLC-5 block live shadow, enforcement, or only promotion/rollout?
9. What routing-time budget unit does AISDLC-95 own?
10. Is the advisor local/embedded, reached through an approved model provider,
    or exposed through another host service? What data may leave the runner?
11. How is the actual served model verified for runtimes that do not report it?
12. If both advisor and ADR 0112 overlays are enabled, which of the three
    composition options above is chosen?
13. How does the approved model/provider pool constrain an existing
    `FULLSEND_FALLBACK_MODELS` chain after an advisor selection, and what typed
    unavailable-model outcome returns to the pinned selection?

## Recommendation

Proceed with a contract-first **offline prototype**, followed by shadow only
after destination/provider/privacy approval; do not begin live enforcement:

- use a host-side `Advisor` interface and pure deterministic `PolicyGate`;
- begin with the smallest structured signal set and no raw task text;
- record recommendation separately from accepted/executed/served model;
- execute the static selection for every shadow outcome, including a
  hypothetical named escalation;
- evaluate a general classifier and Jev-like decision model behind the same
  adapter contract; and
- write the required ADR only after the experiment identifies the necessary
  inputs, error behavior, and viable quality gate.

This recommendation is reversible and yields evidence without creating an
unreviewed precedence mechanism.

## Proposed Jira-ready findings for AISDLC-291

> A host-side pre-run advisor is feasible because Fullsend already resolves
> runtime, harness, model aliases, provider credentials, gateway routing, and
> sandbox configuration before the sandbox exists. The advisor should return a
> recommendation or abstention over an approved candidate set; a deterministic
> Fullsend gate—not the classifier—must enforce provider/capability policy,
> exact security-stage choices, effort, budget, and accepted precedence.
>
> The LangChain Open SWE router is relevant external evidence: it classifies
> the first human message once per thread, keeps one model for the thread, and
> reported 64% lower median cost across a 973-thread frontier-baseline A/B test
> without a statistically measurable merge/open-rate difference. Those results
> are not transferable proof for Fullsend, and its fast-only test demonstrates
> why a cheap baseline is unsafe.
>
> Fullsend-specific blockers are: a new ADR for any learned/LLM live decision;
> an approved advisor position relative to ADR 0091 and conditional ADR 0112
> overlays; implementation of ADR 0112 if that composition uses overlays; a
> closed trust-classified signal contract; an explicit model/provider/capability
> policy; uniform actual-served-model attribution; and AISDLC-5 evaluation and
> promotion evidence. Start offline; use non-enforcing shadow only after the
> advisor destination, provider, signal transmission, and privacy controls are
> approved. Preserve
> ADRs 0137/0138: an accepted `gateway/` model uses the existing gateway route,
> whose failure never falls back to direct-provider credentials or another auth
> mode. Route code exists for pi, Codex, and Claude, but a long-running OIDC
> pilot remains gated on ADR 0137's per-runtime live placeholder-rotation proof:
> token `iat`/`exp` scheduling, generation settle/re-seed, runtime re-read, and
> same-route/auth continuity, including fail-closed negative cases. API-key
> gateway-key revocation/contract expiry and run-scoped OpenShell provider-
> credential lifetime remain separate; provider expiry/deletion does not revoke
> the upstream key.

## Sources

Inspected 2026-10-12:

- [LangChain, “How to Build a Model Router in the Harness”](https://www.langchain.com/blog/how-to-build-a-model-router-in-the-harness),
  published 2026-10-01.
- Approved AISDLC-291 text and authenticated 2026-10-12 AISDLC-95 related-work
  snapshot, including AISDLC-5, AISDLC-282, and AISDLC-345 boundaries.
- Fullsend PR 7842, merged 2026-10-06; docs-only ADR 0112 decision.
- Fullsend PR 7808, closed 2026-10-02; rejected separate routing-policy design.
- `docs/ADRs/0091-per-agent-runtime-model-effort.md`.
- `docs/ADRs/0112-overlays-may-set-any-harness-field.md`.
- `docs/ADRs/0137-inference-gateway-credential-route.md`.
- `docs/ADRs/0138-inference-gateway-api-key-credential-mode.md`.
- `docs/normative/normalized-event/v1/README.md`.
- `docs/problems/adaptive-agent-selection.md`.
- `docs/problems/security-threat-model.md`.
- `internal/cli/run.go`, `internal/cli/run_overrides.go`, and
  `internal/cli/run_gateway_route.go`.
- `internal/config/inference_gateway.go`.
- `internal/harness/forge.go`.
- `internal/runtime/runtime.go`.
