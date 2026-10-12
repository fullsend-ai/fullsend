# AISDLC-289 implementation plan

This is a plan for a proof and decision, not authorization to adopt a gateway or
change Fullsend production code.

## Recommendation under evaluation

Prototype the gateway first as a serving/enforcement layer for an already
concrete automatic selection. Run gateway-as-selector as a separate experiment
only after the product owner resolves the scope questions in `research.md` and
a draft ADR defines its precedence, trust, identity, and fallback contracts.

## Phase 0: decisions and evidence gates

1. Use the authenticated 2026-10-12 Jira snapshot as the baseline, then refresh
   it immediately before any proposed Jira change. Treat AISDLC-15's comments
   as Hummingbird discovery/interest and an exploratory integration
   recommendation whose architect review/approval remained pending. Identify
   an available component/version and test it; do not infer gateway approval
   or automatic-selector capability.
2. Resolve AISDLC-288 decisions D1-D12 that affect this spike, especially
   placement, security floor, evaluation gate, routing unit, configuration
   surface, and required signals.
3. Decide whether gateway-as-selector is a v1 candidate or future work.
4. Choose a production-adjacent golden path and pinned baseline under
   AISDLC-5. Do not promote based on cost-only results.
5. Use AISDLC-345 as the authoritative attributable run/work-item cost source
   and AISDLC-282 as the cumulative-spend enforcement owner. Gateway cost
   telemetry is a reconciled observation, not a replacement for either.

Exit: reviewed decision register; no unresolved choice is encoded as a default.

## Phase 1: provider-neutral conformance harness

Build a test-only contract suite around an HTTP fake gateway and adapter
interfaces, not a vendor-specific production integration.

1. Implement the prototype policy manifest and identity types.
2. Implement fakes for policy discovery, `/v1/models`, streaming/non-streaming
   responses, decision metadata, OTEL export, and failures.
   Model runner-host credential acquisition for every control call at the
   configured gateway origin, using only the block's fixed ADR 0138 mode. Keep
   real credentials host-side; repository identity is authorization data, not
   an authentication mechanism. Split the auth harness by mode: OIDC validates
   JWT/JWKS signature, issuer, purpose-specific audience, `iat`/`exp`, required
   claims, and exact repository claim; API-key validates key correctness,
   active/revoked state, exact repository authorization, configured model/path
   scopes when defined, and expiry only when the gateway-key contract defines
   it. Never apply JWT properties to API keys.
3. Instrument the fake transport to prove which failures occur before any
   request body leaves Fullsend and which occur after the gateway/classifier
   receives task context.
4. Add the stage-aware failure matrix from `prototype.md`, including an
   explicitly approved same-gateway classifier fallback and fail-closed case.
   Once an alias wins or any selector control call begins, permit only an
   explicitly approved concrete `gateway/<model>` on that same configured
   block/auth mode, or fail closed—even before prompt transmission. A direct
   pinned result is a separate pre-contact precedence outcome requiring the new
   ADR, never a gateway-route fallback.
5. Run the suite against selected gateway releases in isolated test
   infrastructure. Record exact versions and config.
6. Treat any new manifest/policy endpoint, host transport, egress rule,
   credential substitution path, or configuration field as architecture for
   the new gateway-selector ADR. Do not inherit authorization from the current
   `/v1/models` probe or sandbox model-POST profile.

Exit: comparable capability/gap table for a specific available Hummingbird
component/version (if one can be identified), LiteLLM, agentgateway, Praxis,
and any other approved candidate. Discovery comments alone are not conformance
evidence.

The harness must also attach attester, trust domain, source, freshness, runner
request id, and assurance to every selected/served identity. Treat gateway
response metadata, gateway OTEL, and gateway exports as one trust domain. Add
provider-signed and independently collected upstream audit/account fixtures,
then test forged self-consistent gateway lies, conflicting, stale, absent, and
downgraded evidence against each preregistered gate.

## Phase 2: serving/enforcement proof

Use current `inference.gateway` configuration and a concrete
`gateway/<model>`; do not add automatic alias semantics.

1. Add an experimental runner-owned correlation seam to the gateway request.
   Prove the sandbox cannot override it.
2. Add a gateway adapter/export path that records actual served model/provider,
   deployment, attempts, usage, latency, and cost for streams and ordinary
   responses.
3. Join this record to existing Fullsend run/work-item trace, metrics, quality
   result, and artifact digest.
   The gateway record is gateway-attested, not independent proof. Where the use
   requires stronger assurance, reconcile it with provider-signed metadata or
   independently collected provider audit/account records using the runner-
   owned request id.
4. Verify per-model/provider authorization before task context is sent.
5. Fault-inject gateway and credential failures and prove ADRs 0137/0138:
   fixed auth mode, no direct-provider credential fallback, fail closed.
6. For every runtime whose production-adjacent OIDC pilot is planned for more
   than five minutes **or** beyond its observed token lifetime, require
   runtime-specific live proof. The proof exercise must itself outlive the
   observed token lifetime. Prove refresh scheduling derives from each token's
   `iat`/`exp`, a new placeholder generation settles and is re-seeded, the
   runtime re-reads it, and subsequent requests remain on the same gateway
   block and auth mode. Separately force refresh failure,
   re-seed failure, and expiry and prove fail-closed behavior with no stale
   token, direct/static credential, or auth-mode substitution. Test API-key
   mode separately with no OIDC loop: validate upstream gateway-key
   correctness, revocation/lifetime contract, repository authorization, and
   configured scopes. Separately prove that expiry/deletion of the run-scoped
   OpenShell provider credential limits sandbox use but does not revoke or
   expire the upstream gateway key.

Likely repo areas after an ADR approves production work:

* `internal/config/inference_gateway.go` for any provider-neutral metadata
  reference—not classifier policy by assumption;
* `internal/cli/run_gateway_route.go` for pre-sandbox validation/correlation;
* `internal/runtime/gateway_route.go` and runtime adapters for runner-owned
  transport metadata;
* `internal/cli/run.go`, telemetry, and metrics types for requested/selected/
  actual identity and decision correlation;
* `docs/reference/config-reference.md`, runtime docs, operator guide, and CLI
  references for every user-facing field/output change.

Exit: concrete selection is policy-checked before transmission and actual
serving identity is queryable end to end. Route code for pi/Codex/Claude is not
called ready for an OIDC pilot planned for more than five minutes or beyond the
observed token lifetime until that runtime has the live in-iteration rotation
evidence required by ADR 0137; the proof run itself outlives the observed token
lifetime.

## Phase 3: gateway-selector proof, if approved

1. Draft a new ADR. It must define:
   * its position relative to ADR 0091 and ADR 0112;
   * trusted policy source and versioning;
   * alias versus concrete versus actual-served identities;
   * closed candidate-set discovery/attestation;
   * classifier recipient and context rules;
   * run/thread pinning and prompt-cache behavior;
   * deterministic abstention/failure behavior;
   * gateway-internal attempt/fallback policy;
   * ADR 0137/0138 fail-closed and credential boundaries;
   * telemetry, rollback, and promotion.
2. Implement only the control-plane manifest and pre-transmission policy gate.
   Keep it behind an experimental opt-in. Every call uses a host-acquired
   endpoint-bound credential at the configured origin in the fixed mode. OIDC
   enforces JWT/JWKS signature, issuer, purpose-specific audience, `iat`/`exp`,
   required claims, and exact repository claim. API-key enforces secure key
   correctness, active/not-revoked state, exact repository authorization,
   configured model/path scopes when defined, and contract expiry only when
   defined; it has no JWT issuer/audience/claim requirements.
3. Add the alias data-plane call after the gate. Reject missing or inconsistent
   served-model metadata.
4. Prove that manifest/policy failures happen before transmission and that
   request-specific classification begins only after the gateway receives the
   request. Missing, expired, malformed, unsigned, denied, timed-out, and
   unreachable control responses may use only an ADR-approved concrete model
   on the same gateway block/auth mode or fail closed, including when zero
   prompt bytes were sent. For classifier timeout/abstention/malformed output,
   permit only the ADR-approved same-gateway fallback or fail closed; never
   retry via a direct pinned route.
5. Exercise shadow/offline evaluation on identical golden-path inputs. Do not
   expose task context to a shadow destination unless that transmission and
   incremental cost are explicitly approved.
6. Compare quality/completion, latency, total cost including classification,
   route failures, abstentions, fallback attempts, and cache effects.

Exit: new ADR accepted, conformance suite passes, and AISDLC-5 promotion gates
are met. Otherwise retain gateway serving-only use and pinned selection.

## Phase 4: rollout and rollback

1. Begin with one opt-in production-adjacent pilot and closed candidate set.
2. Alert on missing decision records, candidate drift, provider mismatch,
   gateway errors, auth-mode mismatch, and quality regression.
3. Rollback changes the automatic selection to the reviewed pinned concrete
   model for a subsequent run through the reviewed pre-selection/precedence
   seam. It is not an in-run failure fallback after a gateway route has won.
   The active run remains on the same gateway block/auth mode or fails closed;
   rollback does not expose direct credentials.
4. Preserve unsuccessful experiment records and artifacts.

## Security review checklist

* Provider/model/classifier approval occurs before any context transmission.
* Pull-request or sandbox-controlled data cannot change gateway URL, auth mode,
  policy manifest, candidate set, correlation identity, or security floor.
* All candidates satisfy context classification, capability, region, and
  provider policy.
* Gateway authorization independently denies models outside the repository's
  set.
* Every policy, manifest, candidate-list, and status control call goes to the
  configured gateway origin with a runner-host-acquired credential in the
  configured ADR 0138 mode; repository identity alone is never authentication.
* OIDC control calls validate JWT/JWKS signature, issuer, purpose-specific
  audience, `iat`/`exp`, required claims, and exact repository claim.
* API-key control calls validate secure key correctness, active/not-revoked
  state, exact repository authorization, configured model/path scopes when the
  contract defines them, and expiry only if that gateway-key contract defines
  it. JWT issuer, audience, claims, and expiry are never invented for API keys.
  Upstream gateway-key revocation/lifetime is independent from run-scoped
  OpenShell provider credential expiry/deletion: the latter limits sandbox use
  but does not revoke or expire the upstream key.
* Upstream credentials remain at the gateway and placeholders remain endpoint-
  bound.
* `Authorization` and `X-Api-Key` are stripped before every upstream; neither
  is reflected in responses, stream trailers, errors, telemetry, or logs.
* Gateway failure never triggers direct-provider credentials.
* OIDC and API-key never fall back to one another implicitly.
* After a gateway alias wins or any selector control call begins, all control-
  plane, credential, classifier, gateway, and upstream failures stay on the
  same configured gateway block/auth mode. Only a preapproved concrete
  `gateway/<model>` on that route may substitute; otherwise fail closed,
  regardless of whether any prompt bytes were sent.
* A direct pinned outcome can occur only before alias selection and gateway
  contact through a new-ADR precedence decision; it is not route fallback.
* Classifier timeout/abstention occurs after gateway transmission and either
  follows an explicitly approved same-gateway fallback with complete attempt
  evidence or fails closed.
* Alias presentation cannot hide actual model/provider or fallback attempts.
* Gateway response metadata is not trusted from model-generated content.
* Gateway response metadata, gateway OTEL, and gateway export remain one trust
  domain and cannot self-upgrade. Every identity is traceable to attester and
  assurance; weak/missing/conflicting/stale evidence follows preregistered
  unknown, quarantine, fail-closed, or gate-failure behavior.
* Classifier prompts and telemetry follow data-minimization/redaction rules.
* The threat assessment covers the gateway's concentrated access to prompts,
  completions, provider keys, policy, and telemetry, including compromise,
  misuse, falsified evidence, and widened candidate authorization.
* Route code implemented for pi/Codex/Claude is distinguished from the still-
  unproven live OIDC placeholder rotation inside a long-running iteration.

## Verification matrix

| Layer | Verification |
|---|---|
| Config | unknown fields/auth modes/candidates fail validation; base-branch trust preserved |
| Policy gate | every candidate and classifier recipient approved before send |
| OIDC control auth | host acquisition at configured origin; JWT/JWKS signature, issuer, purpose-specific audience, `iat`/`exp`, required claims, exact repository claim; missing/malformed/unsigned/unverifiable/not-yet-valid/expired/wrong-issuer/wrong-audience/wrong-repository/missing-claim plus denial/timeout/unreachable fail closed; header stripping and no reflection/logging |
| API-key control auth | host acquisition at configured origin; secure key correctness, active/revoked state, exact repository authorization, configured model/path scopes when defined, expiry only when key contract defines it; missing/incorrect/inactive/revoked/wrong-repository/out-of-scope/contract-expired plus denial/timeout/unreachable fail closed; no JWT requirements, header stripping, no reflection/logging |
| OIDC route | happy path plus missing, malformed, refused, not-yet-valid, and expired JWT/token cases; streams where applicable; no auth/direct substitution |
| API-key route | happy path plus missing, incorrect, inactive, revoked, and refused key cases; enforce key expiry only if the upstream gateway-key contract defines it; streams where applicable; no OIDC/direct substitution |
| OpenShell provider credential | expiry/deletion prevents further sandbox use and fails closed, but is not evidence that the separate upstream gateway API key was revoked or expired |
| Long run | any runtime-specific production-adjacent OIDC pilot planned for >5 minutes or beyond observed token lifetime requires live proof; the proof run outlives the observed lifetime and proves `iat`/`exp` scheduling, settled generation, re-seed, runtime re-read, same route; refresh/re-seed/expiry negatives fail closed; API-key separate with gateway-key contract revocation/lifetime independent of run-scoped OpenShell provider credential expiry/deletion |
| Failure | zero-byte and post-transmission failures; malformed/unsigned/missing/denied policy; classifier timeout/abstention; 401/403/429/5xx/stream interruption; no route/auth escape |
| Identity | requested/selected/served identities trace to attester and assurance; forged gateway lies, conflicts, staleness, absence, and downgrade cannot pass stronger gates |
| Telemetry | one correlation joins decision, attempts, run, quality, cost, artifact |
| Evaluation | repeated pinned-vs-candidate trials with uncertainty and quality floor |
| Rollback | tested future-run switch at the reviewed pre-selection seam; never an in-run failure fallback after a gateway route wins |

## Coverage and repository process if implemented

Any later Go production change must follow `docs/contributing/go-code.md`, add
unit/behavior tests, stage changes before `make lint`, run the required Go test
suites, and verify approximate patch coverage meets the repository's 80% target
and 75% enforced floor. Interface extensions require their documentation sync.
Any CLI/config/output change must update the reference and user guides. Any
architecture choice must follow the ADR process rather than silently reviving
PR 7808.

## Unresolved blockers

Implementation planning cannot be finalized until humans decide:

* selector versus serving-only v1 scope;
* routing unit and session pinning;
* candidate-set source of truth;
* permitted gateway-internal substitution;
* same-gateway classifier fallback versus fail-closed behavior, and the proof
  that no unapproved upstream received context;
* provider evidence availability, minimum assurance by use, and scoped
  residual-risk authority when independent reconciliation is unavailable;
* OIDC-only versus API-key pilot eligibility;
* policy ownership and rollback authority;
* first pilot, measures, and quality-equivalence threshold;
* AISDLC-15/Hummingbird discovery comments versus separately verified
  component availability and capability evidence.

## Definition of done

AISDLC-289 is complete when candidate gateways have comparable conformance
results, route code implemented for pi/Codex/Claude is clearly separated from
both automatic selection and the outstanding live in-iteration OIDC rotation
proof, all unresolved decisions are approved or explicitly deferred, the
recommended option has an actionable reviewed plan, and any production change
is gated on its required ADR and AISDLC-5 evidence. No Jira or code mutation is
part of this spike output.
