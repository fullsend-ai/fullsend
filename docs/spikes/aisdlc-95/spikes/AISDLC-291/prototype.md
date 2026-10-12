# AISDLC-291 pseudocode prototype: host-side pre-run advisor

Status: investigation prototype, not accepted Fullsend architecture
Scope: top-level model and effort only
Mode: offline is safe to evaluate; live shadow requires destination/provider,
privacy, retention, credential, and egress approval; enforcement requires a
new ADR

## Design invariants

1. The advisor recommends; a deterministic Fullsend policy gate accepts,
   suppresses, rejects, or abstains to the static selection.
2. Existing flag/environment overrides remain effective in this prototype.
   Any different precedence requires a new ADR.
3. No model name implies strength. Security-stage choices are explicit sets.
4. Inputs are schema-allowlisted and trust-classified. Untrusted input cannot
   relax a security floor or any guarded control.
5. The advisor never receives secrets or selects runtime, credentials, gateway
   URL/auth, tools, policy, permissions, or persona models.
6. Serving route is derived only after policy acceptance. Gateway route failure
   is terminal and never falls back to direct credentials.
7. Recommendation, accepted model, requested runtime model, and actual served
   model are separate recorded concepts.
8. Recorded stochastic input/output/version supports audit, not a claim of
   deterministic replay.

## Contract types

```go
// Pseudocode: package names and schema are proposals, not current code.

type TrustClass string
const (
    TrustedPolicy      TrustClass = "trusted_policy"
    TrustedExecution   TrustClass = "trusted_execution"
    TrustedEvent       TrustClass = "trusted_event"
    AuthorShapedFact   TrustClass = "author_shaped_fact"
    UntrustedText      TrustClass = "untrusted_text"
    OperationalEvidence TrustClass = "operational_evidence"
)

type Signal struct {
    ID          string        // closed schema key, e.g. "change.file_count"
    Value       ScalarOrBoundedList
    Trust       TrustClass
    Source      string        // normalized-event/config/runtime field name
    Complete    bool          // false means absence cannot be inferred
    SchemaVer   string
}

type Selection struct {
    ModelRef     string        // requested/canonical refs recorded separately
    Effort       string
    ModelSource  string        // flag/env/agent/harness/default/overlay
    EffortSource string
}

type ModelPolicy struct {
    CandidateID      string        // stable opaque ID exposed to advisor
    CanonicalModel   string        // known only to gate, not inferred by name
    Provider         string
    ServingRoute     "direct" | "gateway"
    Runtime          string
    Capabilities     Set[string]
    SensitiveClasses Set[string]
    AllowedEfforts   Set[string]
}

type StagePolicy struct {
    ID                   string
    Version              string
    Mode                 "pinned" | "shadow" | "enforce"
    RequiredCapabilities Set[string]
    DataClass            string
    CandidateIDs         Set[string]

    // Exact choices, not a guessed model-tier comparison.
    AllowedAutomaticSelections Set[(CandidateID, Effort)]
    SecurityLocked             bool
    SecurityAllowedSelections  Set[(CandidateID, Effort)]

    AdvisorTimeout      Duration
    ApplicationPoint    "shadow_only" | "below_operator_override" |
                        "advisor_signal_to_overlay" | "undefined"
    ApplicationPointADR Optional[ADRRef] // required for enforce-capable point
    FailureAction       "static" | "named_escalation"
    NamedEscalation     Optional[(CandidateID, Effort)]
}

type AdviceRequest struct {
    DecisionID       UUID
    AdvisorID        string
    AdvisorVersion   string
    InputSchemaVer   string
    PolicyID         string
    PolicyVersion    string
    StaticSelection  Selection
    CandidateIDs     []string      // opaque IDs, closed set
    Signals          []Signal      // canonical order, bounded size
    InputDigest      SHA256        // canonical JSON; raw secrets never present
}

type Recommendation struct {
    DecisionID      UUID
    Disposition     "recommend" | "abstain"
    CandidateID     string
    Effort          string
    ReasonCodes     []KnownReasonCode
    ContributingIDs []string       // must reference request signal IDs

    // Optional and adapter-specific. Gate may compare it only when the
    // adapter policy declares score semantics/version and a threshold.
    Score           Optional[FiniteNumber]
    ScoreSemantics  Optional[string]
}

type Decision struct {
    DecisionID        UUID
    Outcome           "static" | "shadow" | "accepted" | "suppressed" |
                      "advisor_abstained" | "advisor_failed" | "gate_rejected"
    StaticSelection   Selection
    Recommendation    Optional[Recommendation]
    EffectiveSelection Selection
    GateReasonCodes   []KnownGateReason
    PolicyID          string
    PolicyVersion     string
    InputDigest       SHA256
    OverlayTraceID    Optional[string] // only when ADR 0112 participates
    ShadowEvaluation  Optional[GateEvaluation] // never changes EffectiveSelection
}

type GateEvaluation struct {
    HypotheticalOutcome   string
    HypotheticalSelection Selection
    ReasonCodes           []KnownGateReason
    Recommendation        Optional[Recommendation]
}

interface Advisor {
    // The host adapter owns strict decoding so malformed/unknown output is
    // observable rather than hidden behind a typed client.
    Invoke(ctx context.Context, boundedRequest []byte) ([]byte, error)
}

interface PolicyGate {
    Decide(req AdviceRequest, rec Recommendation, policy StagePolicy,
           models map[string]ModelPolicy) Decision
}
```

## Approved input construction

```go
func BuildAdviceRequest(ctx RunContext, static Selection,
                        policy StagePolicy) (AdviceRequest, error) {
    signals := []Signal{
        trusted("agent.name", ctx.AgentName, "runner"),
        trusted("agent.role", ctx.Harness.Role, "composed_harness"),
        trusted("runtime.name", ctx.Runtime.Name(), "runtime_resolution"),
        trusted("repo.id", ctx.Event.Repo, "normalized_event.repo"),
        trusted("event.source", ctx.Event.Source.System,
                "normalized_event.source.system"),
        trusted("selection.static_model_source", static.ModelSource,
                "ADR0091_resolution"),
        trusted("selection.static_effort_source", static.EffortSource,
                "ADR0091_resolution"),
    }

    // Add only fields named by the policy's versioned input schema.
    // Every added value retains its trust class and completeness.
    if policyAllows("change.file_count") {
        signals.add(authorShaped("change.file_count", ctx.Event.Diff.Files,
                                 ctx.Event.Diff.PathsComplete))
    }
    if policyAllows("change.paths") {
        signals.add(authorShapedBounded("change.paths", ctx.Event.Diff.Paths,
                                        maxPaths, ctx.Event.Diff.PathsComplete))
    }
    if policyAllows("request.text") {
        // Requires separate data/provider approval. Never read env/secrets.
        text := ScanRedactAndBound(ctx.ApprovedTaskText, maxTextBytes)
        signals.add(untrustedText("request.text", text))
    }

    rejectUnknownSignals(signals)
    rejectSecretsURLsCredentialsOrUnboundedValues(signals)
    canonical := CanonicalJSON(sortBySignalID(signals))

    return AdviceRequest{
        DecisionID:      NewUUID(),
        AdvisorID:       policyAdvisorID(),
        AdvisorVersion:  policyAdvisorVersion(),
        InputSchemaVer:  policyInputSchemaVersion(),
        PolicyID:        policy.ID,
        PolicyVersion:   policy.Version,
        StaticSelection: static,
        CandidateIDs:    sorted(policy.CandidateIDs),
        Signals:         signals,
        InputDigest:     SHA256(canonical),
    }, nil
}
```

Raw task text is excluded from the minimum schema. If later enabled, it is
never written to span attributes or ordinary logs. The audit store may retain
approved/redacted values under its data-retention policy; otherwise it retains
the digest and non-sensitive structured signals.

## Strict advisor adapter

```go
func CallAdvisor(parent context.Context, advisor Advisor, req AdviceRequest,
                 policy StagePolicy) AdvisorResult {
    ctx, cancel := context.WithTimeout(parent, policy.AdvisorTimeout)
    defer cancel()

    // The adapter has no tools, filesystem, forge token, gateway credential,
    // arbitrary network, or authority-bearing system fields.
    bytes, err := advisor.Invoke(ctx, EncodeBoundedRequest(req))
    if err != nil {
        return Failed(classifyAdvisorError(err)) // timeout/unavailable/cancelled
    }
    if len(bytes) > MaxAdvisorOutputBytes {
        return Failed("oversized_output")
    }

    rec, err := StrictJSONDecodeDisallowUnknownFields(bytes)
    if err != nil || rec.DecisionID != req.DecisionID {
        return Failed("malformed_output")
    }
    if rec.Disposition == "abstain" {
        requireEmpty(rec.CandidateID, rec.Effort)
        return Abstained(rec)
    }
    if rec.Disposition != "recommend" ||
       !req.CandidateIDs.contains(rec.CandidateID) ||
       !ValidEffort(rec.Effort) ||
       !knownReasonCodes(rec.ReasonCodes) ||
       !subset(rec.ContributingIDs, signalIDs(req.Signals)) ||
       !validAdapterScore(rec.Score, rec.ScoreSemantics) {
        return Failed("invalid_recommendation")
    }
    return Recommended(rec)
}
```

## Deterministic policy gate

Trusted policy/configuration is validated before any advisor call. The accepted
ADR must decide whether invalid trusted configuration fails the run or disables
the advisor; it may not silently widen policy. The prototype below uses
fail-before-execution for an enabled shadow/enforce policy.

The mode/application-point matrix is closed:

| Mode | `shadow_only` | `below_operator_override` | `advisor_signal_to_overlay` | `undefined` |
|---|---|---|---|---|
| `pinned` | static; point ignored | static; point ignored | static; point ignored | static; point ignored |
| `shadow` | valid; evidence only | config error | config error | config error |
| `enforce` | config error | valid only with the ADR that accepts this point | config error while ADR 0112 composition is unimplemented | config error |

Pinned mode never constructs an advisor and therefore does not validate unused
advisor configuration. Every config-error cell fails before request
transmission, route planning, or sandbox creation under the prototype contract.

```go
func ValidateModeApplicationPoint(policy StagePolicy,
                                  acceptedADRs AcceptedADRRegistry) error {
    switch (policy.Mode, policy.ApplicationPoint) {
    case ("shadow", "shadow_only"):
        return nil
    case ("enforce", "below_operator_override"):
        if policy.ApplicationPointADR.empty() ||
           !acceptedADRs.Accepts(policy.ApplicationPointADR,
                                 "below_operator_override") {
            return error("enforce application point lacks accepted ADR")
        }
        return nil
    default:
        // `pinned` bypasses this validator in RunAgent. All other pairs are
        // closed unless a future accepted ADR changes this matrix.
        return error("mode/application-point combination is not accepted")
    }
}

func ValidateTrustedAdvisorConfiguration(policy StagePolicy,
                                          models map[string]ModelPolicy,
                                          acceptedADRs AcceptedADRRegistry) error {
    if err := ValidateModeApplicationPoint(policy, acceptedADRs); err != nil {
        return err
    }
    return ValidateBoundedPolicyAndCatalog(policy, models)
}

func EvaluateAdvisorResult(req AdviceRequest, result AdvisorResult,
                           policy StagePolicy,
                           models map[string]ModelPolicy) GateEvaluation {
    if result.Failed {
        return evaluateFailureActionThroughSamePolicyGate(
            req, policy, models, "advisor_failed:"+result.Code)
    }
    if result.Abstained {
        return evaluation("advisor_abstained", req.StaticSelection,
                          result.Rec, [])
    }

    rec := result.Rec
    mp, ok := models[rec.CandidateID]
    if !ok || !policy.CandidateIDs.contains(rec.CandidateID) {
        return rejectedEvaluation(rec, req.StaticSelection,
                                  "candidate_not_approved")
    }
    if mp.Runtime != signal(req, "runtime.name") {
        return rejectedEvaluation(rec, req.StaticSelection,
                                  "runtime_incompatible")
    }
    if !mp.Capabilities.containsAll(policy.RequiredCapabilities) {
        return rejectedEvaluation(rec, req.StaticSelection,
                                  "capability_missing")
    }
    if !mp.SensitiveClasses.contains(policy.DataClass) {
        return rejectedEvaluation(rec, req.StaticSelection,
                                  "provider_not_approved_for_data")
    }
    if !mp.AllowedEfforts.contains(rec.Effort) {
        return rejectedEvaluation(rec, req.StaticSelection,
                                  "effort_not_supported")
    }
    if !policy.AllowedAutomaticSelections.contains((rec.CandidateID, rec.Effort)) {
        return rejectedEvaluation(rec, req.StaticSelection,
                                  "selection_not_allowed")
    }

    // The floor is exact reviewed policy, independent of advisor reasons and
    // author-shaped signals. No model-name/tier inference occurs.
    if policy.SecurityLocked &&
       !policy.SecurityAllowedSelections.contains((rec.CandidateID, rec.Effort)) {
        return suppressedEvaluation(rec, req.StaticSelection, "security_floor")
    }

    if !scoreMeetsAdapterSpecificThreshold(rec, policy) {
        return evaluation("advisor_abstained", req.StaticSelection, rec,
                          ["insufficient_score"])
    }

    // Preserve explicit operator intent in the prototype. The new ADR must
    // define any broader precedence before enforce is allowed.
    if isFlagOrEnvironment(req.StaticSelection.ModelSource) ||
       isFlagOrEnvironment(req.StaticSelection.EffortSource) {
        return suppressedEvaluation(rec, req.StaticSelection,
                                    "operator_override")
    }

    if policy.ApplicationPoint == "advisor_signal_to_overlay" {
        // Deliberately unavailable until a new ADR plus ADR 0112 implementation
        // defines signal trust, CEL schema, trace, precedence, and fallback.
        return rejectedEvaluation(rec, req.StaticSelection,
                                  "overlay_composition_not_implemented")
    }

    return acceptedEvaluation(rec, Selection{
        ModelRef: mp.CanonicalModel,
        Effort: rec.Effort,
        ModelSource: "advisor:"+req.AdvisorID+"@"+req.AdvisorVersion,
        EffortSource: "advisor:"+req.AdvisorID+"@"+req.AdvisorVersion,
    })
}

func Gate(req AdviceRequest, result AdvisorResult, policy StagePolicy,
          models map[string]ModelPolicy,
          acceptedADRs AcceptedADRRegistry) (Decision, error) {
    base := DecisionFromStatic(req, policy)
    if policy.Mode == "pinned" {
        return base.withOutcome("static"), nil
    }
    if err := ValidateTrustedAdvisorConfiguration(policy, models,
                                                   acceptedADRs); err != nil {
        return Decision{}, err // chosen prototype contract: fail before execution
    }

    evaluated := EvaluateAdvisorResult(req, result, policy, models)
    if policy.Mode == "shadow" {
        // Mechanical invariant: every result and every configured failure
        // action is evidence only. The executed value cannot differ.
        return base.withOutcome("shadow").
                    withShadowEvaluation(evaluated).
                    withEffectiveSelection(req.StaticSelection), nil
    }

    // Defense in depth: validation admits exactly one enforce-capable point.
    if policy.Mode != "enforce" ||
       policy.ApplicationPoint != "below_operator_override" ||
       !acceptedADRs.Accepts(policy.ApplicationPointADR,
                             "below_operator_override") {
        return Decision{}, error("enforce application point is not accepted")
    }
    return ApplyEnforcedEvaluation(base, evaluated), nil
}
```

`evaluateFailureActionThroughSamePolicyGate` validates a named escalation
through the same provider/capability/security checks. In shadow it is recorded
only as a hypothetical selection; `Gate` mechanically restores the static
effective selection. In enforce it may apply only after the application point
is accepted. It cannot choose a model dynamically, recurse into the advisor,
or bypass any gate.

## Runner placement and route composition

```go
func RunAgent(ctx context.Context, inputs RunInputs) error {
    // Existing work, refactored into an atomic result:
    harness := LoadAndComposeHarness(inputs) // overlays resolve here today
    runtime := ResolveRuntimeADR0091(inputs, harness)
    static := ResolveStaticModelAndEffortADR0091(inputs, harness, runtime)

    // If ADR 0112 is later implemented, static includes its resolved harness
    // value and trace. This does not grant the advisor a precedence over it.
    if inputs.AdvisorPolicy.Mode == "pinned" {
        // Pinned mode does not build/transmit an advisor request.
        return ContinueExistingRunWithStaticSelection(ctx, static)
    }
    if err := ValidateTrustedAdvisorConfiguration(inputs.AdvisorPolicy,
                                                  inputs.ModelPolicies,
                                                  inputs.AcceptedADRs); err != nil {
        return err // enabled shadow/enforce fails before request or sandbox
    }
    req, err := BuildAdviceRequest(inputs.Context, static.Selection,
                                   inputs.AdvisorPolicy)
    if err != nil { return err }
    result := CallAdvisor(ctx, inputs.Advisor, req, inputs.AdvisorPolicy)
    decision, err := Gate(req, result, inputs.AdvisorPolicy,
                          inputs.ModelPolicies, inputs.AcceptedADRs)
    if err != nil { return err }

    RecordDecisionBeforeSideEffects(decision) // no raw untrusted text/secrets

    if decision.Outcome == "gate_rejected" &&
       inputs.AdvisorPolicy.Mode == "enforce" &&
       decision.hasFatalConfigurationError() {
        return error("routing advisor policy is invalid")
    }

    // Only the accepted effective selection mutates the run.
    effective := decision.EffectiveSelection
    canonical := ResolveApprovedAlias(effective.ModelRef)

    // Existing code derives credentials/routes after the gate.
    switch ProviderPrefix(canonical.Model) {
    case "gateway":
        requireGatewayPilotReadiness(inputs.Pilot, runtime,
                                     inputs.RunConfig.InferenceGateway)
        plan, err := planGatewayRoute(inputs.RunConfig, runtime,
                                      inputs.SandboxName,
                                      [canonical.Model], true)
        if err != nil { return err }
        route, err := startGatewayRoute(ctx, plan)
        if err != nil { return err }
        // Any later control-plane, credential, gateway-side classifier,
        // gateway, or upstream failure returns error. Never try direct
        // OpenAI/Vertex/static key and never switch oidc <-> api-key.
        require(route.Success)
    default:
        ValidateConfiguredFallbackChainAgainstPolicyOrOperatorPrecedence(
            inputs.FallbackModels, inputs.AdvisorPolicy, inputs.ModelPolicies)
        PrepareExistingDirectRoute(ctx, runtime, canonical.Model)
    }

    sandbox := CreateSandboxWithEffectiveRoute(...)
    metrics, runErr := RunExistingRuntime(sandbox, canonical.Model,
                                          effective.Effort)

    served := ActualServedModel{
        Model: metrics.Model,
        Verification: runtimeServedModelCapability(runtime, metrics),
        // "requested_only" is explicit when the runtime provides no served id.
    }
    RecordRunOutcome(decision.DecisionID, canonical, served, metrics, runErr)
    return runErr
}
```

```text
function requireGatewayPilotReadiness(pilot, runtime, gatewayBlock):
  if not pilot.productionAdjacent: return
  if gatewayBlock.authMode == "api-key":
    require upstream gateway key is active/not revoked
    require gateway key satisfies contract-defined scope/expiry, if any
    require run-scoped OpenShell provider-credential lifetime bound is enforced
    require provider expiry/deletion is not treated as gateway-key revocation
    require no OIDC refresh/re-seed loop is claimed or started
    return
  if pilot.maximumDuration <= 5 minutes and
     pilot.maximumDuration <= pilot.observedOIDCTokenLifetime: return

  proof = pilot.routeReadinessEvidence[runtime]
  require proof is production-adjacent and outlives observed token lifetime
  require proof refresh schedule is derived from each token's iat and exp
  require proof observes new placeholder generation settle then re-seed
  require proof observes runtime re-read of the new placeholder generation
  require proof subsequent requests retain the same gateway block and OIDC mode
  require proof covers refresh failure, re-seed failure, and expired token
  require every negative case fails closed without direct-provider setup or
          oidc-to-api-key switching
```

### Overlay composition states

```text
CURRENT MAIN:
  model/effort overlays unavailable -> advisor shadow compares against static

ADR 0112 IMPLEMENTED, AFTER-OVERLAY MODE ACCEPTED:
  overlay resolution -> static + overlay trace -> advisor -> deterministic gate
  new ADR defines whether accepted advice may replace overlay result

ADVISOR-SIGNAL-TO-OVERLAY MODE PROPOSED:
  advisor recommendation -> explicitly untrusted typed CEL input -> overlays
  unavailable until new ADR + schema + trust + trace + fallback implementation

MUTUALLY EXCLUSIVE MODE ACCEPTED:
  validate exactly one of advisor enforcement or deterministic overlay routing
  pinned/static baseline remains common and recorded
```

## Telemetry and correlation

```go
type RoutingDecisionRecord struct {
    DecisionID          UUID
    TraceID             string
    PolicyIDVersion     string
    AdvisorIDVersion    string
    InputSchemaVersion  string
    InputDigest         string
    SignalMetadata      []{ID, Trust, Source, Complete} // values privacy-gated
    StaticSelection     Selection
    Recommendation      Optional[Recommendation]
    GateOutcome         string
    GateReasons         []string
    EffectiveSelection  Selection
    OverlayTraceID      Optional[string]
    RequestedModel      string
    RequestedRoute      "direct" | "gateway"
    GatewayAuthMode     Optional["oidc" | "api-key"] // never credential value
    ServedModel         Optional[string]
    ServedVerification  "provider_reported" | "gateway_reported" |
                        "requested_only" | "unknown"
    AttemptsFallbacksEscalations []AttemptRecord
    CostLatencyUsage    UsageRecord
    CostEvidenceRef     Optional[AISDLC345EvidenceRef]
    CumulativeSpendState Optional[AISDLC282EnforcementState]
    QualityOutcomeRef   Optional[string]
    ArtifactOutcomeRef  Optional[string]
}
```

The advisor record references AISDLC-345's authoritative attributable
run/work-item cost evidence instead of establishing a second cost authority.
Any AISDLC-282 cumulative-spend state is a read-only enforcement input/result;
the advisor neither maintains nor overrides cumulative spend enforcement.

Emit:

- a `model_routing.advisor` span with bounded categorical attributes;
- a local `routing-decision.json` joined into `metrics.json` by decision ID;
- plan output that shows static, recommended, gate result, and effective model
  without raw task signals; and
- an outcome join keyed by decision ID for later AISDLC-5 evaluation.

Never put raw prompt/diff, credentials, gateway URL, or sensitive repository
content in span attributes, logs, or status comments.

## Test seams and cases

### Injectable seams

```go
FakeAdvisor          // recommend, abstain, timeout, error, malformed raw output
FakeClock/Deadline   // exact timeout/cancellation cases
PurePolicyGate       // table-driven, no network
FakeCanonicalizer    // aliases and provider prefixes
Existing fake runtime/dummy-playback
Gateway route planner/provider seams already used by run_gateway_route tests
In-memory decision recorder and OTel exporter
Independent fake outcome evaluator
```

### Minimum test matrix

1. pinned mode never calls advisor;
2. shadow mode records but executes static selection for recommendations,
   timeout, unavailable, cancelled, malformed, oversized, unknown output,
   abstention, insufficient score, policy rejection, security suppression,
   `FailureAction=static`, and `FailureAction=named_escalation`;
3. timeout, error, malformed, oversized, unknown field, mismatched decision
   ID, NaN/infinite score, and abstention all take the documented path;
4. recommendation outside candidate set or with unsupported effort fails gate;
5. provider/data or capability mismatch cannot be accepted;
6. author-shaped/untrusted signals cannot relax `SecurityLocked` exact choices;
7. flag/environment override suppresses a recommendation in prototype mode;
8. enforce with undefined application point fails before sandbox creation;
9. ADR 0112 trace is optional on current main and mandatory only in the
   accepted overlay composition;
10. gateway recommendation runs existing route planning after gate;
11. gateway OIDC/API-key failure never calls a direct-route fallback;
12. direct recommendation never inherits gateway credentials/config;
13. requested and served model are distinct; a runtime lacking served evidence
    records `requested_only`, not a false provider report;
14. no raw untrusted text or credential appears in logs/spans/decision record;
15. all attempts and quality/artifact outcomes join by decision ID;
16. rerunning a stochastic fake may differ while both observed outputs remain
    independently auditable; no deterministic-replay assertion is made; and
17. cancellation before advisor, during advisor, during route setup, and after
    sandbox start produces one terminal decision/outcome record;
18. an advisor-selected direct model's configured fallback chain is either
    policy-approved or handled under explicitly accepted operator precedence;
    no unreviewed cross-provider fallback is introduced; and
19. invalid trusted shadow/enforce configuration fails before advisor client
    construction, request transmission, route planning, or sandbox creation;
    pinned mode does not build or validate unused advisor configuration; and
20. a table-driven test crosses every `pinned`/`shadow`/`enforce` mode with
    every application point: pinned always stays static without an advisor;
    shadow admits only `shadow_only`; enforce admits only
    `below_operator_override` with its accepted ADR; every other enabled pair
    takes the documented pre-side-effect configuration-error path; and
21. for every pi/Codex/Claude runtime selected for a long-running OIDC pilot,
    production-adjacent evidence spans the observed token lifetime and proves
    `iat`/`exp` scheduling, generation settle/re-seed, runtime re-read, and
    same-route/auth continuity; refresh/re-seed/expiry failures fail closed
    without direct-provider setup or API-key switching. API-key cases instead
    verify upstream-key active/revoked state and contract-defined scope/expiry,
    separately enforce the run-scoped OpenShell provider-credential lifetime,
    prove provider expiry/deletion does not revoke the gateway key, and start no
    re-seed loop.

## Prototype limitations

- No production configuration schema is chosen; that belongs in the new ADR.
- No model/capability catalog exists in Fullsend today. `ModelPolicy` is a
  proposed exact policy contract, not a revival of PR 7808's tier catalog.
- No advisor endpoint/credential route is selected.
- No v1 normalized-event adapter populates the proposed change facts.
- Persona/subagent and runtime routing are excluded.
- Quality-gate retry/resume/follow-up behavior remains a human decision.
- Actual served-model verification remains runtime/gateway-specific.
- ADR 0137 live OIDC rotation readiness is per runtime; implemented route code
  and unit fault injection do not substitute for the production-adjacent proof.
