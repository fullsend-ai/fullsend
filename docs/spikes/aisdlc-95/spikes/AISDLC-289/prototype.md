# AISDLC-289 pseudocode prototype

This prototype compares a gateway automatic selector with the lower-risk
gateway serving/enforcement composition. It is intentionally provider-neutral
and not production code.

## Identity and policy types

```go
type RoutingAlias struct {
    ID            string // e.g. "cost-quality-v1"; never a model identity
    PolicyVersion string
}

type ConcreteModel struct {
    CanonicalID   string // immutable org/model/version, not display alias
    ServingRoute string // "gateway" for this spike
    Provider     string // actual upstream provider/security domain
    API          string // responses, messages, chat-completions
    Capabilities set[string]
    DataClasses  set[string]
    Region       string
}

type ApprovedRoutingPolicy struct {
    Scope              Scope // project/workflow/stage
    Alias              RoutingAlias
    CandidateModels    []ConcreteModel // closed transitive set
    CandidateSetDigest SHA256
    Classifier         ClassifierIdentity // also a data recipient
    Fallback           FallbackPolicy
    SecurityFloor      SecurityFloor
    RoutingUnit        enum{Run, Thread} // not per-request in v1 prototype
}

type GatewayDecision struct {
    DecisionID         UUID
    RequestedAlias     RoutingAlias
    RequestedEvidence  IdentityEvidence // runner/config attested; requested-only
    CandidateSetDigest SHA256
    Selected           IdentityEvidence
    Cause              string
    Confidence         optional<float>
    Attempts           []ServingAttempt
    ActualServed       IdentityEvidence
    GatewayPolicyHash  SHA256
}

type Assurance string // requested-only | runtime-reported | gateway-attested |
                      // provider-attested | independently-reconciled

type IdentityEvidence struct {
    Role            string // requested | selected | served
    Identity        Optional[ConcreteModel]
    AttesterID      string
    TrustDomain     string
    Source          string
    Assurance       Assurance
    RunnerRequestID UUID
    ObservedAt      Timestamp
    ValidUntil      Optional[Timestamp]
    EvidenceDigest  SHA256
    Signature       Optional[Signature]
}

type AssurancePolicy struct {
    AllowedByUseAndRole map[UseCase]map[IdentityRole]set[Assurance]
    MaxEvidenceAge      Duration
    ResidualRisk        Optional[ReviewedScopedException]
}

// RequestModel, Selected, and ActualServed are separate. Equality is checked,
// never inferred from a response presentation alias.
```

## Trusted interfaces

```go
interface GatewayControlPlane {
    // New host-side transport proposed by the selector ADR. Every call uses
    // the configured gateway origin and an endpoint-bound host credential;
    // RepositoryIdentity is an authorization claim to verify, never auth.
    ResolvePolicy(alias RoutingAlias, credential HostGatewayCredential)
        -> SignedPolicyManifest

    // Existing /v1/models-style authorization is useful but insufficient;
    // this must expose an auto alias's transitive concrete candidates.
    AuthorisedConcreteModels(credential HostGatewayCredential)
        -> []ConcreteModel
}

interface GatewayCredentialBroker {
    // Acquires the configured block's one fixed ADR 0138 mode. Real material
    // stays host-side and is never returned to the sandbox or policy payload.
    AcquireForControlCall(
        origin ConfiguredGatewayOrigin,
        authMode enum{OIDC, API_KEY},
        exactRepository RepositoryIdentity,
    ) -> HostGatewayCredential | Denial
}

type HostGatewayCredential =
    OIDCControlCredential{JWT, JWKSKeyID, Issuer,
                          PurposeSpecificAudience, IssuedAt, ExpiresAt,
                          RequiredClaims, ExactRepositoryClaim}
  | APIKeyControlCredential{SecretHandle, ActiveState,
                            ExactRepositoryAuthorization,
                            ConfiguredModelPathScopes,
                            OptionalContractExpiry}

interface RoutingPolicyGate {
    ValidateBeforeTransmission(
        scope Scope,
        manifest SignedPolicyManifest,
        local ApprovedProviderPolicy,
        contextClass DataClassification,
    ) -> ApprovedRoutingPolicy | Denial
}

interface GatewayDataPlane {
    Invoke(
        aliasOrConcrete string,
        request ModelRequest,
        credential GatewayCredentialPlaceholder,
        runnerOwned CorrelationHeaders,
    ) -> Stream<ModelEvent>, GatewayResponseMetadata
}

interface DecisionRecorder {
    RecordPreflight(PolicyApprovalRecord)
    RecordDecision(GatewayDecision)
    JoinOutcome(DecisionID, FullsendRunID, QualityResult, ArtifactDigest)
}

interface IdentityReconciler {
    // Gateway response metadata, gateway OTEL and gateway export all have the
    // same gateway trust domain. They cannot corroborate one another upward.
    // ProviderEvidence is provider-signed metadata or independently collected
    // upstream audit/account data joined by RunnerRequestID.
    Reconcile(gateway []IdentityEvidence, provider []ProviderEvidence,
              policy AssurancePolicy) -> ReconciliationResult
}
```

`SignedPolicyManifest` is accepted only from the configured ADR 0137 gateway
origin and must bind alias, version, candidate set, classifier destination,
expiry, and digest. For every control path, OIDC mode validates JWT/JWKS
signature, issuer, purpose-specific audience, `iat`/`exp`, required claims, and
exact repository claim. API-key mode instead validates secure key correctness,
active/not-revoked state, exact repository authorization, configured model/path
scopes when its contract defines them, and expiry only when that gateway-key
contract defines expiry; it does not invent JWT issuer, audience, claims, or
expiry. The gateway strips `Authorization` and `X-Api-Key` before every
upstream, never reflects them in responses, and never logs credential values.
Missing, invalid, malformed, unsigned, denied, timed-out, or unreachable
responses fail closed subject only to the same-route fallback below. If a
signed/attested control-plane contract is not viable,
the same closed set must live in trusted Fullsend configuration. A bare
`/v1/models` alias is not enough. The endpoint, host transport, egress,
credential substitution, and configuration are new architecture requiring the
gateway-selector ADR; the existing sandbox POST profile supplies none of them.

## Alternative A: gateway as selector

```text
function ResolveRoutingBeforeGatewayContact(run, proposedSelection):
  # Placement is defined only by the new routing ADR.
  outcome = ResolveNewADRSelectionPrecedence(run, proposedSelection)
  if outcome is approved DIRECT_PINNED_CONCRETE:
      # This is a final precedence outcome, not fallback. No gateway alias was
      # selected and no gateway control/data call or credential acquisition ran.
      return PlanDirectPinnedBeforeGatewayContact(outcome)
  require outcome is GATEWAY_ALIAS
  return PlanGatewaySelectorRun(run, outcome.alias, run.contextMetadata)

function PlanGatewaySelectorRun(run, alias, contextMetadata):
  # Happens before sandbox creation and before task/context transmission.
  assert alias was selected through a newly accepted precedence seam

  block = ResolveTrustedInferenceGatewayConfig(run.repository)
  assert block.auth is exactly OIDC or API_KEY       # ADR 0138
  assert block is complete and applies
  sameRouteFallback = ResolveTrustedPreapprovedGatewayFallback(
      run.baseBranchConfig, alias, block)
  if sameRouteFallback exists:
      require sameRouteFallback is concrete gateway/model
      require sameRouteFallback.block == block
      require sameRouteFallback.authMode == block.auth

  # Alias has now won. From here, no control/data failure can escape this block.
  controlCredential = credentialBroker.AcquireForControlCall(
      block.origin, block.auth, run.repositoryIdentity)
  require controlCredential is host-only and endpoint-bound to block.origin
  manifest = gatewayControl.ResolvePolicy(alias, controlCredential)
  authorised = gatewayControl.AuthorisedConcreteModels(controlCredential)

  approval = policyGate.ValidateBeforeTransmission(
      run.scope, manifest, run.approvedProviderPolicy, contextMetadata.class)
  require manifest.candidates is closed and nonempty
  require every manifest.candidate is in authorised
  require manifest.classifier destination is approved for contextMetadata.class
  require approval.securityFloor cannot be lowered by author-shaped input

  correlation = RunnerOwnedCorrelation(run.traceID, newDecisionID())
  plan = GatewaySelectorPlan(block, approval, sameRouteFallback, correlation)
  RecordPreflight(plan.withoutSecrets())
  # This preflight does NOT produce the request-specific selected model.
  # Classification needs the model request and therefore occurs only after
  # task context is transmitted to the approved gateway/classifier.
  return plan

function InvokeFirstAndContinuationRequests(plan, request):
  # Sandbox cannot set/override correlation or routing policy headers.
  credential = SeedEndpointBoundCredential(plan.block.auth) # existing route
  # Transmission boundary: once Invoke starts, request context has left
  # Fullsend for the approved gateway and possibly its approved classifier.
  events, metadata = gatewayData.Invoke(
      "gateway/" + plan.policy.alias.ID,
      request,
      credential.placeholder,
      plan.correlation)

  require metadata.decisionID == plan.correlation.decisionID
  require metadata.policyVersion == plan.policy.alias.PolicyVersion
  require metadata.candidateSetDigest == plan.policy.CandidateSetDigest
  require metadata.selected.Identity in plan.policy.CandidateModels
  require metadata.actualServed.Identity in plan.policy.CandidateModels
  require metadata.authMode == plan.block.auth

  if continuation request:
      require metadata.decisionID and metadata.selected equal first request
      # v1 does not reclassify tool turns or silently switch models.

  if metadata missing, inconsistent, or unapproved:
      terminate run as ROUTING_CONTRACT_VIOLATION
      do not retry by sending context to another destination

  RecordDecision(metadata)
  return events

function HandleSelectorFailure(stage, error, routeState):
  if stage is CONTROL_PLANE_PREFLIGHT:
      assert no task/model request body was sent
      # Alias selection or the first control call commits the ADR 0137 route,
      # even with zero prompt bytes sent.
      if routeState.sameRouteFallback is configured:
          require routeState.sameRouteFallback was loaded from trusted config
              before the control call
          require routeState.sameRouteFallback is explicitly approved concrete
              gateway/model on routeState.block and routeState.block.auth
          return PlanConcreteOnSameGatewayRoute(routeState.sameRouteFallback)
      fail closed

  if stage is CLASSIFIER_AFTER_GATEWAY_TRANSMISSION:
      # Timeout, abstention and malformed decision occur after the gateway or
      # its approved classifier received context. Direct/pinned-route retry is
      # not available merely because no upstream completion was selected yet.
      if routeState.policy.sameGatewayClassifierFallback is configured:
          fallback = routeState.policy.sameGatewayClassifierFallback
          require fallback is deterministic
          require fallback target is in routeState.policy closed approved set
          require gateway proves no unapproved upstream received context
          require gateway reports classifier attempt and fallback cause
          continue on the SAME gateway route and SAME ADR0138 auth mode
      else:
          fail closed

  if stage is GATEWAY_AUTH or GATEWAY_AVAILABILITY:
      fail closed
      never switch ADR0138 auth mode
      never use direct-provider WIF/static credential  # ADR 0137

  if stage is UPSTREAM_AFTER_SELECTION:
      if routeState.policy permits SAME_MODEL_DEPLOYMENT_FAILOVER:
          allow gateway to try another approved deployment of same identity
      else if routeState.policy explicitly permits MODEL_SUBSTITUTION:
          require substitute in closed approved set and record every attempt
      else:
          fail closed
```

Important: zero prompt transmission does not permit a route escape. Once the
alias has won or the first selector control call begins, every preflight,
credential, classifier, gateway, and upstream failure stays on the same
configured gateway block and ADR 0138 auth mode or fails closed. A direct
pinned outcome can happen only before alias selection and gateway contact as a
separate advisor/static precedence result defined by the new routing ADR.
Classification needs request context. A classifier default/pinned model is
eligible only as an explicit
same-gateway fallback within the approved closed candidate set, with evidence
that no unapproved upstream received context and with every attempt recorded.
Otherwise the route fails closed.

## Alternative B: gateway as serving/enforcement layer

```text
function ResolveServingPlanBeforeGatewayContact(run, recommendation):
  # Advisor/overlay/offline policy makes the automatic decision elsewhere.
  effective = ResolveAcceptedSelectionPrecedence(run, recommendation)
  if effective is approved DIRECT_PINNED_CONCRETE:
      # Final precedence outcome, not fallback: no gateway route was selected
      # and no gateway control/data call or credential acquisition has begun.
      return PlanDirectPinnedBeforeGatewayContact(effective)
  require effective is ConcreteModel
  require effective.ServingRoute == "gateway"
  return PlanConcreteGatewayRun(run, effective)

function PlanConcreteGatewayRun(run, effective):
  # The concrete gateway route has won. No later denial/failure can escape it.
  block = ResolveTrustedInferenceGatewayConfig(run.repository)
  require block.auth is fixed and route applies
  sameRouteFallback = ResolveTrustedPreapprovedGatewayFallback(
      run.baseBranchConfig, effective, block)
  if sameRouteFallback exists:
      require sameRouteFallback is concrete gateway/model
      require sameRouteFallback.block == block
      require sameRouteFallback.authMode == block.auth

  approval = policyGate.ValidateConcreteBeforeTransmission(
      run.scope, effective, run.approvedProviderPolicy, run.contextClass)
  if denied:
      if sameRouteFallback exists:
          return PlanConcreteOnSameGatewayRoute(sameRouteFallback)
      fail closed

  statusCredential = credentialBroker.AcquireForControlCall(
      block.origin, block.auth, run.repositoryIdentity)
  require gateway /v1/models at block.origin authorises effective.CanonicalID
      using statusCredential with exact repository binding

  return ExistingADR0137RoutePlan(
      model="gateway/" + effective.CanonicalID,
      auth=block.auth,
      correlation=RunnerOwnedCorrelation(...))

function ServeConcrete(plan, request):
  response = InvokeThroughExistingGatewayRoute(plan, request)
  require response.actualServed.Identity == plan.concreteModel
      OR response.attempt is an approved same-identity deployment failover
  record requested, actual served, auth mode, usage, latency, cost, trace id
  return response
```

This alternative requires an actual-served evidence contract but no gateway
classifier, alias candidate expansion, or request-time selection precedence.
Both alternatives retain the gateway's concentrated access to prompts,
completions, upstream provider keys, policy, and telemetry; compromise impact
and operator controls must be evaluated explicitly rather than inferred from
route correctness.

## Response and telemetry contract

```json
{
  "decision_id": "uuid",
  "fullsend_run_id": "opaque",
  "requested": {"kind": "alias", "id": "cost-quality-v1", "version": "7"},
  "candidate_set_sha256": "...",
  "selected": {"identity": {"canonical_model": "vendor/model@version", "provider": "provider-a"}, "attester": "gateway-1", "trust_domain": "gateway", "assurance": "gateway-attested", "source": "gateway-response", "observed_at": "..."},
  "actual_served": {"identity": {"canonical_model": "vendor/model@version", "provider": "provider-a", "deployment": "region-a-3"}, "attester": "provider-a", "trust_domain": "provider-a", "assurance": "provider-attested", "source": "provider-signed-response", "observed_at": "..."},
  "cause": "policy-rule-or-classifier",
  "confidence": 0.91,
  "attempts": [{"model": "...", "provider": "...", "outcome": "served"}],
  "gateway_policy_sha256": "...",
  "auth_mode": "oidc",
  "usage": {"input_tokens": 1, "output_tokens": 1},
  "cost_usd": 0.01,
  "cost_observation_source": "gateway-attested",
  "aisdlc_345_cost_evidence_ref": null,
  "latency_ms": 42
}
```

Transport requirements:

* Decision metadata must be available for both streaming and non-streaming
  responses without trusting model-generated body text.
* Gateway telemetry must carry runner-injected W3C trace context or an equally
  unforgeable correlation id. The gateway also exports the decision record to
  an operator-controlled sink; response metadata alone is insufficient if the
  sandbox can suppress it.
* `response.model` may retain a presentation alias, but it never substitutes
  for `actual_served.canonical_model`.
* Missing usage/cost is represented as unknown, not zero.
* Gateway-reported cost is an observation, not the authoritative attributable
  run/work-item cost contract. Evaluation joins it to AISDLC-345 evidence and
  preserves disagreements; the gateway cannot override AISDLC-282 cumulative
  spend enforcement.
* Every requested/selected/served identity carries its attester, trust domain,
  assurance, evidence time, and runner-owned request id. Gateway response,
  gateway OTEL, and gateway export remain `gateway-attested` even when they
  agree. Runtime reports that echo them remain `runtime-reported`.
* A gate calls `IdentityReconciler` and checks its preregistered minimum.
  Provider-signed metadata or independently collected upstream audit/account
  records can establish stronger assurance. Absence, conflict, staleness, and
  downgrade remain explicit unknown/quarantine/failure; no code path upgrades
  evidence based only on self-consistency.

## Deterministic failure matrix

| Failure | Selector alternative | Serving-only alternative |
|---|---|---|
| Policy manifest missing/expired/malformed/unsigned/denied/timed out/unreachable | Explicitly approved concrete `gateway/<model>` on same configured block and auth mode, or fail closed—even with zero prompt bytes; never direct/static outside route | N/A |
| OIDC control credential missing/invalid/expired; bad JWT/JWKS signature; wrong issuer/purpose-specific audience; invalid `iat`/`exp`; missing claims; wrong exact-repository claim | Fail closed on same route; no auth/direct substitution | Same for any OIDC-authenticated gateway path |
| API-key control credential missing/incorrect/inactive/revoked; wrong exact-repository authorization; configured model/path scope denial; expired only when key contract defines expiry | Fail closed on same route; never apply JWT checks or substitute OIDC/direct auth | Same for any API-key-authenticated gateway path |
| Candidate/classifier recipient unapproved | Same-route approved concrete fallback or fail closed; no direct escape | Same configured gateway block/auth-mode approved concrete fallback or fail closed; no direct escape |
| Classifier timeout/abstention/malformed output | After gateway transmission: explicit deterministic same-gateway fallback within approved set, with no unapproved upstream exposure and all attempts reported, or fail closed; never retry a direct pinned route | N/A; advisor owns its pre-run behavior |
| Gateway unavailable/refuses auth | Fail route; no direct-provider fallback | Same |
| OIDC unavailable | Fail; never switch to API key | Same |
| API key missing | Fail; never switch to OIDC/direct key | Same |
| Actual model not in approved set | Contract violation; terminate and alert | Contract violation; terminate and alert |
| Serving metadata absent | Fail closed for pilot/enforcement | Fail closed for pilot/enforcement |
| Evidence weak, conflicting, or stale | Unknown/quarantine or fail the preregistered gate; never upgrade | Same |
| Gateway internal model failure | Only explicit approved same-route fallback; report attempts | Prefer same-model deployment failover; substitutions need explicit approval |

## Test seams

```go
type FakeGatewayControlPlane struct {
    Manifest SignedPolicyManifest
    Models []ConcreteModel
    Error error
}
type FakeGatewayDataPlane struct {
    Metadata GatewayResponseMetadata
    Events []ModelEvent
    Error error
}
type FakePolicyGate struct { Decision ApprovalOrDenial }
type RecordingDecisionSink struct { Records []Record }
type FakeIdentityReconciler struct { Result ReconciliationResult }
type FakeProviderAuditSource struct { Evidence []ProviderEvidence }
type FakeCredentialRoute struct {
    AuthMode string
    DirectProviderFallbackAttempted bool
}
type RecordingGatewayControlTransport struct {
    Origin ConfiguredGatewayOrigin
    PresentedCredential Optional[HostGatewayCredential]
    RepositoryClaim RepositoryIdentity
    UpstreamHeaders []HTTPHeader
    ResponseHeaders []HTTPHeader
    Logs []LogRecord
}
type FakeOIDCRefreshLifecycle struct {
    Tokens []TokenClaims // iat, exp, iss, aud, repository
    PlaceholderGenerations []PlaceholderGeneration
    RuntimeReads []PlaceholderGenerationID
    RefreshError Optional[error]
    ReseedError Optional[error]
}
type FakeGatewayAPIKeyContract struct {
    Correct bool
    Active bool
    Revoked bool
    ExactRepositoryAuthorization RepositoryIdentity
    ModelPathScopes set[string]
    ContractExpiry Optional[Timestamp]
    UpstreamGatewayKeyRevoked bool
    OpenShellProviderCredentialDeleted bool
}
```

Required tests:

1. Reject open-ended candidate set, bad signature/digest, unknown provider,
   classifier recipient, or context classification before request body send.
   For missing, expired, malformed, unsigned, denied, timed-out, and
   unreachable policy/candidate responses, assert no direct provider route,
   provider secret, or auth substitution is initialized, even with zero prompt
   bytes; only a preapproved concrete model on the same gateway block/auth mode
   may run.
2. Instrument the fake transport to distinguish zero request-body sends during
   control-plane preflight from selector/classifier receipt after Invoke starts.
3. Verify alias, selected, and actual served identity never collapse into one
   field and alias restamping cannot hide substitution.
4. Verify author-controlled request headers cannot override policy, candidate
   digest, correlation, auth mode, or gateway URL.
5. Verify classifier timeout, abstention, and malformed decision occur after
   gateway transmission and can only take an approved same-gateway fallback or
   fail closed; assert no direct/pinned-route retry is attempted.
6. Verify the same-gateway classifier fallback proves no unapproved upstream
   received context and records classifier/fallback attempts and cause.
7. Verify session mismatch and missing response metadata fail as configured.
8. Fault-inject OIDC expiry, missing API key, 401/403/429/5xx, stream failure,
   gateway timeout, and internal provider failure; assert no direct-provider
   credential fallback and no implicit auth-mode switch.
9. Verify all gateway-internal attempts stay in the approved candidate set and
   are recorded, including a successful fallback.
10. Verify streaming and non-streaming decision records correlate to the same
   Fullsend run, quality score, cost, and artifact.
11. Run identical tasks against pinned and candidate policies repeatedly and
   block promotion when quality equivalence fails.
12. Forge self-consistent gateway response, OTEL, and export identities; verify
   they stay gateway-attested and cannot pass provider/compliance or independent
   reconciliation gates.
13. Exercise absent, conflicting, stale, and downgraded evidence; verify
   unknown stays unknown and the configured quarantine, fail-closed, or gate-
   failure behavior occurs.
14. On every `ResolvePolicy`, `AuthorisedConcreteModels`, and future control
    call, verify host-side acquisition at the configured origin in the one
    fixed mode; identity parameters alone never authorize. Split conformance:
    * OIDC: validate JWT through JWKS, issuer, purpose-specific audience,
      `iat`/`exp`, required claims, and exact repository claim. Cover missing,
      malformed, unsigned, unverifiable, expired/not-yet-valid JWTs, wrong
      issuer/audience/repository, missing claims, denial, timeout, and
      unreachable origin.
    * API-key: validate secure key correctness, active/not-revoked state, exact
      repository authorization, configured model/path scopes when defined, and
      expiry only when the gateway-key contract defines it. Cover missing,
      incorrect, inactive, revoked, wrong-repository, out-of-scope, and
      contract-expired keys without applying JWT issuer/audience/claim checks;
      also cover denial, timeout, and unreachable origin.
15. Verify both caller credential headers are stripped before every upstream,
    never reflected in any response (including stream headers/trailers), and
    never written to logs. Run applicable checks for streaming and non-
    streaming control/data transports.
16. For each runtime whose production-adjacent OIDC pilot is planned for more
    than five minutes **or** beyond its observed token lifetime, require
    runtime-specific live proof. The proof exercise must itself outlive the
    observed token lifetime. Assert scheduling uses that token's `iat` and
    `exp`, a new placeholder generation settles and is re-seeded, the runtime
    re-reads it, and later requests retain the same gateway block/auth mode.
17. For each proof-required OIDC runtime, in separate live runs force refresh
    failure, re-seed failure, and expiry; assert fail-closed termination with
    no stale-token use, direct-provider setup, credential substitution, or
    auth-mode switch. Test API-key mode separately with no refresh/re-seed loop:
    prove upstream gateway-key correctness, active/revoked state, repository
    authorization, configured scopes, and contract expiry when defined. Prove
    that expiry/deletion of the run-scoped OpenShell provider credential limits
    sandbox use but does not revoke or expire the upstream gateway key, and
    that upstream key revocation is independently enforced by the gateway.
18. Deny `ValidateConcreteBeforeTransmission` after a concrete gateway model
    wins. Assert the run uses only a trusted preapproved concrete fallback on
    the same configured gateway block/auth mode or fails closed; no direct
    route/provider/secret is prepared merely because no prompt was sent.
