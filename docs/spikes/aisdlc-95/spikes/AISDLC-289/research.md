# AISDLC-289 research: external model gateway

Status: proposed for reporter review; not yet persisted to Jira
Researched: 2026-10-12

## Research question

Can an external model gateway implement AISDLC-95's opt-in automatic model
selection while preserving Fullsend's approved model/provider policy,
credential isolation, deterministic failure behavior, pinned rollback, quality
evaluation, and end-to-end attribution?

## Jira evidence boundary

An authenticated live Jira reconciliation on 2026-10-12 confirms that
AISDLC-95 has a `Related` link to AISDLC-15. AISDLC-15 comments record
Hummingbird gateway discovery/interest and recommend exploratory component
integration. Architect review/approval of that recommendation remained pending,
and the comments do not establish that Hummingbird is approved, available, or
an automatic selector. This research must not be read as an adoption decision.
AISDLC-95 is In Progress; AISDLC-289 remains a New, unassigned Spike under the
In Progress AISDLC-287 Epic.

## Executive finding

An external gateway can occupy either of two materially different roles:

1. **Gateway as automatic selector.** Fullsend requests a routing alias and the
   gateway chooses a concrete model from task/request content. This is feasible
   in products such as LiteLLM, and virtual/conditional models are emerging in
   agentgateway. It has the highest integration and governance burden: the
   gateway sees context before Fullsend knows the selected provider; request-
   time decisions can repeat or change during an agent loop; aliases can hide
   the concrete serving identity; and gateway-internal fallbacks can silently
   substitute a model unless the contract forbids or reports it.
2. **Gateway as serving/enforcement layer.** A Fullsend pre-run advisor or
   reviewed offline policy selects a concrete `gateway/<model>`. Fullsend
   validates that concrete choice before context transmission; the gateway
   authorizes it, holds upstream credentials, serves it, and emits actual-
   serving telemetry. This reuses ADRs 0137/0138 with less ambiguity and is the
   safer initial composition to evaluate. It does not make the gateway the
   automatic decision engine.

The research does **not** recommend adoption yet. Gateway-as-selector cannot
meet AISDLC-95 merely by accepting an `auto` model alias. It needs a closed,
versioned candidate-set contract, a pre-transmission policy gate, stable
session semantics, concrete served-model evidence, deterministic fallback,
and correlation to Fullsend outcomes. None exists in current Fullsend code.

## Current Fullsend baseline

### Static and deterministic selection

* ADR 0091 defines model/effort precedence: flags, environment, per-agent
  configuration, repository/harness settings, then defaults.
* ADR 0112 accepts deterministic per-task `model` and `effort` selection via
  harness overlays and requires resolution trace/fallback evidence. The
  reviewed repository labels this decision not yet implemented.
* PR 7808's separate `routing:` policy, runtime routing, model tiers, and
  shadow/enforce schema were rejected in favor of ADR 0112. They cannot be
  assumed as current design.
* Learned or LLM-based routing still requires a new ADR.

### Implemented route code and outstanding live proof

ADRs 0137 and 0138 are accepted. Their route code is implemented for pi,
Codex, and Claude, but the ADR 0137 live proof for rotating an OIDC placeholder
inside one running iteration before token expiry is still outstanding:

* A concrete `gateway/<model>` prefix chooses an inference-gateway credential
  and serving route. The gateway is not currently an automatic router.
* `inference.gateway` is loaded from trusted committed configuration. Pull
  requests cannot redirect their own run. The runner owns gateway configuration
  and endpoint-bound credential placeholders.
* The block explicitly selects `oidc` (default) or `api-key`. There is no
  implicit precedence or fallback between credential modes.
* Gateway unavailability, rejection, missing credentials, or a partial block
  fails closed. The run never escapes to OpenAI WIF, a direct provider route,
  or a provider static key.
* OIDC refresh scheduling is intended to derive from each token's `iat`/`exp`,
  create and settle a new placeholder generation, re-seed it, and have the
  runtime re-read it. Unit paths do not prove that handoff live during a run
  longer than the observed token lifetime. Any production-adjacent OIDC pilot
  planned for more than five minutes **or** beyond its observed token lifetime
  requires that runtime-specific live proof; the proof exercise itself must
  outlive the observed token lifetime to demonstrate refresh. Each proof-
  required runtime also needs negative refresh/re-seed/expiry evidence.
  API-key mode is separate: it has no OIDC re-seed loop. Upstream gateway-key
  correctness, active/revoked state, repository authorization, configured
  scopes, and any key expiry are properties of the gateway's API-key contract.
  Expiry/deletion of the run-scoped OpenShell provider credential limits
  sandbox use but does not itself revoke or expire the upstream gateway key.
* pi uses the configured model list; Codex and Claude accept a gateway id
  after the prefix without consulting that list. Consequently the existing
  list is not a universal approved-candidate policy.
* Current telemetry records the resolved request model and `gateway` serving
  endpoint. It does not establish a general, trusted record of a dynamic
  gateway alias's concrete model/provider. `gen_ai.request.model` represents
  the request/resolved runtime model, not necessarily a hidden substitution.

### Relevant code seams

* `internal/config/inference_gateway.go`: gateway URL/auth/model-list schema and
  validation.
* `internal/cli/run_gateway_route.go`: decides whether the runner owns the
  route and creates the credential plan before sandbox execution.
* `internal/runtime/gateway_route.go` and runtime-specific gateway files:
  endpoint-bound placeholder and runtime wiring.
* `internal/cli/inference_gateway.go`: authenticated `/v1/models` status probe;
  proves which ids the gateway authorizes for a repository, but not an auto
  alias's internal candidate expansion.
* `internal/cli/run.go` and distributed-tracing documentation: runtime/request
  model, provider endpoint, cost, usage, and work-item correlation.

## Primary gateway evidence

### LiteLLM

LiteLLM's official beta Auto Routing documentation demonstrates that a gateway
can expose a normal model alias whose implementation classifies each request.
It supports heuristic, LLM, JEV, keyword, semantic, and custom classifiers;
maps a tier to a pinned model or pool; can pin a session; and can optionally
return the raw selected model rather than restamping the alias. Classifier
failure defaults to another scorer or a default model. Its separate failover
documentation supports ordered model-group substitution and records the
original group and attempted fallback count.

Implications for AISDLC-95:

* The capability is real, but beta configuration/defaults can change.
* `return_raw_model_name: false` means an alias can intentionally conceal the
  selected model from the client response. AISDLC-95 must require concrete
  identity independent of such a presentation default.
* Classifiers may receive user/system/prior-turn content. Provider approval
  must therefore happen before classifier transmission, not only before the
  selected completion.
* Default degradation to heuristic/default-model is a product choice, not the
  deterministic pinned behavior AISDLC-95 can assume.
* Gateway-internal model substitution can stay on the ADR 0137 route, but every
  candidate must be approved and every attempt reported. It must never cause
  Fullsend to escape to a direct-provider credential.

Sources:

* <https://docs.litellm.ai/docs/proxy/auto_routing>
* <https://docs.litellm.ai/docs/proxy/reliability>

### agentgateway

Official agentgateway documentation distinguishes concrete models, aliases,
and virtual models. A concrete model has one provider; aliases rewrite a
client-facing id; virtual models can use weighted, failover, or conditional
routing. Authorization can run before routing and again on a selected backend
or provider. Backend authentication is separate from authorization.

Implications:

* This separation maps well to AISDLC-95's requirement that an approved policy
  gate precede context transmission and that the selected provider be checked.
* A friendly alias is not proof of one concrete identity. Fullsend needs the
  virtual model's candidate closure and actual selected target.
* The current Fullsend operator path was live-validated against agentgateway,
  but that validates credential custody and serving, not automatic selection
  or OIDC placeholder rotation within one iteration beyond token lifetime.

Sources:

* <https://agentgateway.dev/docs/kubernetes/latest/documentation/llm/models/about/>
* <https://agentgateway.dev/docs/kubernetes/latest/documentation/security/authorization/>
* <https://agentgateway.dev/docs/standalone/latest/documentation/configuration/security/backend-authn/>

### Praxis AI

Praxis AI's primary repository documents request routing by model, provider
format, tool composition, and other extracted facts, together with credential
injection, guardrails, metering, and observability. Its accepted intelligent-
router epic describes semantic, cost, latency, and pressure-aware routing as a
roadmap; most of those checklist items remain open. Praxis AI also labels
itself alpha software.

Implication: Praxis provides relevant enforcement and routing primitives, but
the source does not establish a production-ready automatic selector satisfying
AISDLC-95. A proof must test the exact release and features rather than rely on
the roadmap.

Sources:

* <https://github.com/praxis-proxy/ai>
* <https://github.com/praxis-proxy/ai/issues/74>

## Option assessment

| Dimension | Gateway as selector | Gateway as serving/enforcement layer |
|---|---|---|
| Decision timing | At one or more model API requests after sandbox start | Before sandbox/context, by advisor or reviewed deterministic policy |
| Context exposure | Gateway/classifier must inspect request content; all possible processing destinations need prior approval | Fullsend validates concrete gateway model/provider before context leaves the sandbox boundary |
| ADR 0091/0112 composition | New alias position and precedence require a new ADR; request-time switching is outside ADR 0112 | Concrete result can enter an accepted selection seam, subject to whichever advisor/overlay ADR applies |
| ADR 0137/0138 composition | Uses same route only if alias is `gateway/...`; failure must remain on that route and auth mode fixed | Direct fit: selected `gateway/<model>` uses current fail-closed route |
| Identity | Alias may differ from selected and served identities | Requested identity is concrete; still verify gateway/upstream did not substitute |
| Prompt cache/thread stability | Requires explicit session pin and definition of the routing unit | One pre-run selection naturally preserves a run/thread model |
| Fallback | Gateway defaults and internal failover must be overridden/bounded and fully visible | A direct pinned/advisor result may be a distinct new-ADR precedence outcome only before concrete gateway selection or gateway contact; it is never fallback after the gateway route wins. Serving-route failure remains on the same block/auth mode or is terminal |
| Quality evaluation | Harder to reconstruct because classification may occur each request | Easier: one decision joins to one run and pinned baseline |
| Operational ownership | Gateway owns classifier, policy, candidate inventory, serving, and much telemetry | Fullsend/advisor owns decision; gateway owns authorization, credentials, serving, and serving telemetry |
| Initial fit | Possible but high-risk and contract-heavy | Strongest use of current architecture; not itself a complete automatic solution |

## Required provider-neutral contract for gateway-as-selector

Before any implementation recommendation, candidate gateways must demonstrate:

1. **Authenticated policy discovery.** Given a versioned routing alias, return the complete
   transitive set of concrete model/provider/API targets, capabilities, data
   region/classification, and policy digest. Unknown or dynamic expansion is
   rejected. Every policy/candidate control call goes to the configured gateway
   origin using a host-acquired credential in the block's fixed ADR 0138 mode.
   Authentication is checked according to the block's one fixed mode. For
   OIDC, the gateway validates the JWT against JWKS, issuer, purpose-specific
   audience, `iat`/`exp`, required claims, and exact repository claim. For
   API-key, it validates secure key correctness, active/not-revoked state,
   exact repository authorization, configured model/path scopes where the key
   contract defines them, and expiry only when that contract defines expiry.
   JWT concepts are never invented for API keys. A caller-supplied repository
   identity is never authentication.
2. **Pre-transmission authorization.** Fullsend validates every candidate and
   the classifier destination against project/workflow/stage policy before any
   task context is transmitted. This is control-plane preflight only: the
   request-specific classification cannot occur until the request context
   reaches the gateway/classifier. Gateway authorization independently
   enforces the same closed set.
3. **Stable decision unit.** The gateway pins one selection for the approved
   run/thread/session unless an explicitly governed escalation creates a new
   attempt. No per-tool-turn reclassification by default.
4. **Concrete result with provenance.** Every streamed and non-streamed
   response yields identity observations for requested, selected, and served
   identities. Each binds an attester, trust domain, source, runner-owned
   request id, observation time/freshness, and assurance. Gateway response
   metadata, gateway OTEL, and gateway exports are one gateway trust domain;
   agreement among them is not independent proof of the upstream.
5. **Correlation.** A runner-owned correlation id/trace context joins gateway
   decision, provider attempt, Fullsend run, quality gate, and artifact. The
   sandbox cannot overwrite it.
6. **Route-isolated failure.** Once the gateway alias wins selection or any
   selector control call begins, a missing, expired, malformed, unsigned,
   denied, timed-out, or unreachable manifest/candidate/policy response may
   only choose an explicitly approved concrete `gateway/<model>` on the same
   configured gateway block and same ADR 0138 auth mode, or fail closed. This
   remains true even when zero prompt bytes were sent. A direct pinned outcome
   is possible only as a distinct advisor/static precedence result before the
   gateway alias is chosen and before gateway contact; its placement and
   semantics require the new routing ADR. A
   classifier timeout, abstention, or malformed decision happens only after
   request context reaches the gateway/classifier; it may use only an
   explicitly approved same-gateway deterministic fallback that proves no
   unapproved upstream received context and reports all attempts, or it fails
   closed. Gateway/auth/upstream failure after the request begins likewise
   cannot escape the route. No condition switches ADR 0138 auth mode or falls
   back from the gateway to a direct-provider credential.
7. **Auditable alias semantics.** Requested alias, selected model, provider
   deployment, and response `model` are distinct fields. Presentation aliasing
   cannot erase the selected/served identities.
8. **Complete operator contract.** The gateway strips both caller credential
   headers before every upstream request, never reflects credentials in a
   response or log. OIDC fail-closes an unverifiable JWT/JWKS signature, wrong
   issuer or purpose-specific audience, invalid `iat`/`exp`, missing required
   claims, or wrong repository claim. API-key fail-closes an incorrect,
   inactive, revoked, wrong-repository, out-of-scope, or contract-expired key;
   issuer/audience/JWT claims do not apply to that mode. Both modes fail closed
   on malformed policy, denial, timeout, or unreachable control service.
   Streaming and non-streaming data paths obey the same route/auth isolation.
   A new policy endpoint, host-side transport or credential substitution path,
   egress permission, or configuration field is new architecture for the
   gateway-selector ADR; neither `/v1/models` nor the existing sandbox POST
   profile supplies it implicitly.

## Security concentration and operational proof

The gateway concentrates task prompts, completions, routing policy, upstream
provider credentials, and serving telemetry in one boundary. A compromise or
misconfiguration can therefore expose content, misuse provider keys, falsify
selection evidence, or widen candidate access. The operator contract above is
necessary but does not remove this concentration risk; release-pinned threat
assessment, least privilege, egress restriction, credential-header stripping,
redacted logs, independent serving evidence, and tested revocation remain pilot
gates.

For each runtime admitted to a production-adjacent OIDC pilot (pi, Codex, or
Claude), planning a run for more than five minutes **or** beyond the observed
token lifetime triggers the runtime-specific live-proof prerequisite. The proof
exercise itself must outlive the observed token lifetime and demonstrate
`iat`/`exp`-derived refresh scheduling, settled placeholder generation,
re-seed, runtime re-read, and continued requests through the same gateway block
and auth mode. Negative runs must show refresh failure, re-seed failure, and
token expiry fail closed without a direct route, static credential, auth-mode
switch, or stale-token continuation. API-key evidence is reported separately
and must not be credited as OIDC refresh proof. The API-key case validates the
upstream gateway key's correctness, revocation/lifetime contract, repository
authorization, and configured scopes independently from the run-scoped
OpenShell provider credential: expiry/deletion of the latter prevents further
sandbox use but neither revokes nor expires the upstream gateway key.

## Identity evidence and assurance contract

Every identity observation has one explicit assurance:
`requested-only`, `runtime-reported`, `gateway-attested`,
`provider-attested`, or `independently-reconciled`. Runtime evidence that
merely repeats gateway content is not independent. Stronger evidence may come
from provider-signed response metadata or independently collected upstream
audit/account records joined by the runner-owned request id. Missing, weaker,
conflicting, or stale evidence remains unknown, is quarantined, fails closed,
or fails the relevant gate according to preregistered policy; it is never
silently upgraded.

Proposed conservative preregistration:

| Use | Minimum assurance | Required response |
|---|---|---|
| Shadow intended-choice analysis | `requested-only`; no served/provider claim | served identity remains unknown |
| Shadow served-behavior analysis | `gateway-attested` | quarantine weaker/conflicting/stale rows |
| Production routing enforcement | `gateway-attested` for selected/served, with approved gateway-trust residual risk | fail closed |
| Provider/data-policy compliance | `provider-attested` or `independently-reconciled` | fail closed; gateway-only is insufficient |
| Promotion using served/provider/cost claims | `independently-reconciled` | fail promotion gate |

If stronger provider evidence is unavailable, a human may preregister a
scoped, time-bounded residual-risk exception with owner, rationale, expiry,
rollback, and affected gate. The exception never relabels gateway evidence and
cannot override a legal/provider-policy evidence requirement.

## Recommendation for the spike

Evaluate the gateway first as an enforcement/serving layer behind a concrete
selection. Treat gateway-as-selector as a separate experimental candidate that
must pass the contract above and obtain a new ADR before enforcement. Do not
make Hummingbird, LiteLLM, agentgateway, or Praxis the presumed product choice.

## Unresolved human decisions

1. Is gateway-as-selector in v1 scope, or should v1 restrict the gateway to
   serving/enforcement behind a pre-run concrete selection?
2. What is the routing unit: Fullsend run, thread, iteration, user turn, or API
   request? Is session pinning mandatory?
3. Is classifier processing by a third-party decision service a separate
   provider approval/data-transmission decision?
4. Must an auto alias publish a closed candidate set in trusted Fullsend config,
   or may Fullsend trust a versioned gateway policy manifest/attestation?
5. May gateway-internal fallback substitute a different concrete model within
   the approved set, or only another deployment of the same model identity?
   Does the same-gateway policy permit a deterministic default after classifier
   timeout/abstention, and what evidence proves no unapproved upstream received
   context before it runs?
6. Which providers expose signed metadata or independently retrievable audit/
   account records, and what scoped residual risk is acceptable when stronger
   served-model evidence is unavailable, especially for streams?
7. Who owns routing policy lifecycle and rollback: gateway operator, repository
   operator, or Fullsend?
8. Does the first pilot require OIDC only, or is ADR 0138 `api-key` eligible
   despite its reusable-secret risk?
9. Is there a currently available Hummingbird component/version to test, and
   which of this spike's selector/serving capabilities does it actually meet?

## Source traceability

| Source | Classification | Used for |
|---|---|---|
| AISDLC-288 and AISDLC-292 findings | Jira spike inputs | Acceptance envelope, unresolved decisions, related-work limits |
| ADR 0091 | Accepted/current | Static precedence |
| ADR 0112 / PR 7842 | Accepted, not implemented | Deterministic overlay decision and learned-routing deferral |
| PR 7808 | Rejected/closed | Separate routing schema is not baseline |
| ADRs 0137/0138 | Accepted; route code implemented for pi/Codex/Claude; live in-iteration OIDC placeholder rotation remains unproven | Gateway route, credential ownership, explicit auth, fail-closed behavior, and pilot prerequisite |
| Current gateway config/CLI/runtime/telemetry code | Current implementation | Available seams and attribution gap |
| LiteLLM official docs | External primary source; beta feature | Concrete gateway-selector and fallback behavior |
| agentgateway official docs | External primary source | Concrete/alias/virtual identity and policy placement |
| Praxis AI repo/epic | External primary source; alpha/roadmap | Available primitives versus unimplemented intelligent-routing goals |
| AISDLC-15 live comments, authenticated 2026-10-12 | Observed Jira evidence | Hummingbird discovery/interest and exploratory integration recommendation with architect review/approval pending; no approval, availability, or automatic-selector claim |
