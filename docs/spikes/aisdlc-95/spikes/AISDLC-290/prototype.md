# AISDLC-290 pseudocode prototype

Status: investigation prototype, not accepted architecture

## Invariants

1. Evidence is append-only and content-addressed.
2. Historical outcomes can create a proposal only; they cannot modify a live
   policy, harness, advisor, candidate set, or security floor.
3. Training/proposal data and independent held-out evaluation are separated by
   stable task identity and time.
4. Quality/security gates execute before cost comparison. Cost-only evidence
   can never promote.
5. Human approval signs an exact versioned artifact; production accepts only
   that artifact or a prior approved rollback target.
6. Reproducible offline analysis does not imply repeatable stochastic advisor
   or agent calls.
7. ADR 0112 compilation is unavailable until its accepted design is
   implemented; this prototype does not invent PR 7808's schema or modes.

## Versioned evidence schema

```go
type EvidenceRecordV1 struct {
    SchemaVersion       "fullsend.routing-evidence/v1"
    RecordID            UUID
    RunID               string
    TraceID             string
    WorkItemID          string
    TaskCaseID          string     // stable across retries/variants
    Attempt             uint
    ObservedAt          Timestamp
    OutcomeMaturedAt    Optional[Timestamp]

    RepositoryRef       string
    BaseSHA             string
    HeadSHA             string
    HarnessSHA256       SHA256
    AgentDefinitionSHA  SHA256
    ConfigSHA256        SHA256
    RuntimeVersion      string
    // Neutral inventory. The digest binds exact candidate model-policy
    // entries, provider/capability data,
    // serving routes, and auth constraints used for this run.
    ModelPolicyInventoryVersion string
    ModelPolicyInventorySHA256  SHA256
    PolicyArtifactID    Optional[string]
    AdvisorVersion      Optional[string]
    MeasurementVersions map[string]string

    TaskStrata          map[KnownStratum]Scalar
    SignalSnapshot      []VersionedSignal // value, source, trust, completeness
    InputDigest         SHA256

    AssignmentKind      "paired" | "randomized" | "observational"
    AssignmentGroup     string
    AssignmentPropensity Optional[float64]
    StaticSelection     Selection
    Recommendation      Optional[Recommendation]
    AcceptedSelection   Selection
    RequestedModel      string
    SelectedEvidence    IdentityEvidence
    ActualServed        Optional[IdentityEvidence]
    Route               "direct" | "gateway"
    GatewayAuthMode     Optional["oidc" | "api-key"]
    GatewayRouteReadinessRef Optional[OperationalEvidenceRef]

    DeterministicChecks []CheckResult
    QualityMeasurements []Measurement
    CompletionOutcome   Outcome
    HumanReview         Optional[ReviewOutcome]
    Usage               UsageOrUnknown
    Cost                MoneyOrUnknown
    CostEvidenceRef     Optional[AISDLC345EvidenceRef]
    CumulativeSpendState Optional[AISDLC282EnforcementState]
    Latency             DurationOrUnknown
    AttemptsAndFallbacks []AttemptRecord
    ArtifactSHA256      Optional[SHA256]

    MissingFields       []KnownField
    ExclusionReason     Optional[KnownReason]
    Provenance          ProvenanceEnvelope
    RecordSHA256        SHA256
}

type Assurance string // requested-only | runtime-reported | gateway-attested |
                      // provider-attested | independently-reconciled
type IdentityEvidence struct {
    Role "requested" | "selected" | "served"
    Identity Optional[ServedModelIdentity]
    AttesterID string
    TrustDomain string
    Source string
    Assurance Assurance
    RunnerRequestID UUID
    ObservedAt Timestamp
    ValidUntil Optional[Timestamp]
    Digest SHA256
    Signature Optional[Signature]
}
type AssurancePolicy struct {
    AllowedByUseAndRole map[UseCase]map[IdentityRole]set[Assurance]
    MaxAge Duration
    ResidualRisk Optional[ReviewedScopedException]
}

type EvidenceBundle struct {
    SchemaVersion    string
    Records          []EvidenceRecordV1 // sorted by RecordID
    RecordDigests    []SHA256
    MeasurementSpecs []VersionedMeasurementSpec
    TaskManifest     VersionedTaskManifest
    CostTable        VersionedCostTable
    CreatedByCodeSHA string
    CreatedAt        Timestamp
    BundleSHA256     SHA256
}
```

`CostEvidenceRef` points to AISDLC-345's authoritative attributable
run/work-item evidence; the ledger does not mint a competing authoritative
cost. `CumulativeSpendState` is a read-only enforcement result owned by
AISDLC-282, not a policy-authoring counter or enforcement implementation.

Unknown is never encoded as zero. Assurance records the attester, not a guess:
gateway response/OTEL/export share one trust domain, and matching copies cannot
be upgraded. Runtime reports that echo gateway content remain runtime-reported.
Provider-signed metadata or independent upstream audit/account records joined
by runner-owned request id can satisfy stronger preregistered gates.

## Ingest, validate, and join

```text
function BuildEvidenceBundle(rawTraces, metrics, evals, forgeOutcomes, manifests):
  normalize each source under a pinned parser/schema version
  reject unknown enum values, invalid timestamps, NaN/Inf, negative usage/cost
  verify content hashes and immutable source locations

  join run <-> trace as one-to-one
  join run <-> attempts as one-to-many with unique attempt number
  join trace <-> measurement as one-to-many with unique (name, version)
  join task <-> outcome as zero-or-one at declared maturity timestamp
  assert joins did not multiply attempts, usage, cost, or outcomes
  require every authoritative cost claim references AISDLC-345 evidence
  never infer cumulative-spend authorization; consume AISDLC-282 state only

  mark missing data explicitly
  quarantine records with conflicting model/provider/policy identity
  quarantine absent, stale, or weaker-than-required identity evidence
  never coerce unknown identity or assurance upward
  mark observational records without assignment propensity
  group retries and near-duplicates under stable TaskCaseID

  canonicalize and hash each record
  canonicalize sorted bundle and hash it
  return immutable EvidenceBundle
```

## Split, bias, leakage, and drift controls

```text
function PrepareStudy(bundle, preregisteredStudy):
  require bundle.schema and measurement versions are allowed
  require study thresholds were committed before candidate results are opened

  group by TaskCaseID                         # retries never cross a split
  time-split proposal/validation from held-out evaluation
  quarantine near-duplicate task fingerprints across splits
  stratify by repository, stage, risk, task type, and declared workload facts

  for observational records:
    report candidate/control overlap by stratum
    report assignment imbalance and missing outcome rates per candidate
    if propensity is present, run weighted sensitivity analysis
    never treat adjusted observational estimate as independent causal proof

  compute drift report for:
    signal/stratum distribution and missingness
    model/provider/runtime versions
    quality/completion/cost/latency outcomes
    route/auth/fallback rates

  if overlap insufficient, leakage found, or drift exceeds preregistered rule:
    return StudyInvalid(reason)                # no candidate generation/promotion
  return StudyPartitions(proposal, validation, heldout, driftReport)
```

## Candidate proposal generation

```go
type AbstractDeterministicPolicy struct {
    PolicyID          string
    Version           SemVer
    AllowedSignals    []SignalSpec
    AllowedSelections []ApprovedModelEffortPair
    Rules             []DeterministicRule
    DefaultSelection  ApprovedModelEffortPair
    SecurityFloors    []StageFloor
}

type AdvisorPolicy struct {
    PolicyID          string
    Version           SemVer
    SignalSchema      []SignalSpec
    CandidateIDs      []ApprovedCandidateID
    AdvisorIdentity   VersionedAdvisor
    Gate              DeterministicGateSpec
    Abstention        DeterministicAction
    ApplicationPoint  ADRApprovedApplicationPoint
}

interface CandidateProposer {
    Propose(bundleDigest SHA256, proposalPartition ReadOnlyEvidence,
            searchSpace ReviewedSearchSpace) []PolicyProposal
}

function GenerateProposals(study, searchSpace):
  require searchSpace cannot add model/provider/signal/security choices
  proposals = proposer.Propose(study.bundleDigest, study.proposal, searchSpace)
  for proposal in proposals:
    canonicalize proposal
    reject rule using unapproved/incomplete signal
    reject selection outside approved concrete model/effort pairs
    reject security floor weaker than reviewed floor
    reject proposal containing credentials, endpoint, or gateway auth mutation
    attach explanation, training evidence digest, and limitations
    write proposal artifact ONLY                    # never live config
  return proposals
```

The proposer can be deterministic search or stochastic. Either way its output
is an untrusted proposal until validation, independent evaluation, and human
signature. A recorded seed supports offline replay only for components that
actually honor deterministic execution.

## Independent repeated evaluation

```text
function EvaluateCandidate(proposal, heldoutManifest, pinnedBaseline, studySpec):
  evaluator must not be proposal generator
  freeze exact task inputs, harness/config/model/advisor versions and thresholds

  for task in heldoutManifest.tasks:
    for repetition in 1..studySpec.repetitions:
      baselineRun  = ExecuteIsolated(pinnedBaseline, task, repetition)
      candidateRun = ExecuteIsolated(proposal, task, repetition)
      record both even on failure/timeout
      run deterministic checks first
      score versioned quality/completion measurements

  return EvaluationReport(
    evidenceDigest,
    taskManifestDigest,
    proposalDigest,
    per-run records,
    exclusions/missingness,
    quality/completion distributions and confidence bounds,
    latency/failure/escalation distributions,
    total cost including classifier/evaluation cost,
    bias/drift report,
    reportCodeSHA)
```

Re-running report generation over the immutable bundle must reproduce the
report. Re-running model/advisor calls is expected to produce a distribution,
not byte-identical outcomes.

## Promotion and rejection gate

```text
function Gate(report, preregisteredThresholds):
  require report evidence/proposal/task/config digests match frozen study
  if provenance/schema/join/leakage/drift invalid: REJECT("invalid evidence")
  if any identity evidence is absent, stale, conflicting, or below the
     preregistered assurance for its use/role: REJECT("identity assurance")
  if any security/static/provider/route check fails: REJECT("security")
  if any required deterministic task check fails: REJECT("deterministic")
  if quality lower confidence bound < noninferiority floor: REJECT("quality")
  if completion lower confidence bound < completion floor: REJECT("completion")
  if power/sample requirement not met: REJECT("inconclusive")
  if failure/escalation/latency SLO fails: REJECT("operations")
  for each gateway candidate/run in the claimed pilot envelope:
    if auth mode == oidc and (maximum duration exceeds 5 minutes or
                              maximum duration exceeds observed token lifetime):
      require current per-runtime production-adjacent readiness evidence
      require it covers token iat/exp scheduling, new generation settle/re-seed,
              runtime re-read, and same gateway route/auth continuity
      require refresh/re-seed/expiry negatives fail closed without direct or
              api-key escape
    if auth mode == api-key:
      require upstream gateway key is active/not revoked and satisfies its
              contract-defined scope/expiry, if any
      require run-scoped OpenShell provider-credential lifetime bound
      require provider expiry/deletion is not treated as gateway-key revocation
      require no OIDC refresh/re-seed loop
    # This gate consumes the operational proof; offline policy authoring does
    # not create, weaken, or declare the route ready.

  # Cost is evaluated only after every preceding gate passes.
  savings = CompareCost(candidate, baseline)
  return ELIGIBLE_FOR_HUMAN_REVIEW(savings, all limitations)

function HumanApprove(eligibleReport, reviewer):
  require reviewer has declared policy-approval role
  show exact semantic diff, evidence, failures, missingness, uncertainty, cost
  approval = reviewer explicitly approves exact ProposalSHA256
  if not approval: retain RejectionRecord; stop

  # Produces exact bytes for the separate approval service. This evaluator/
  # review process has no registry or production write capability.
  approvalCandidate = BuildUnsignedPolicyArtifact(proposal, report, reviewer)
  return ApprovalRequest(
      canonicalBytes=approvalCandidate.CanonicalBytes(),
      sha256=SHA256(approvalCandidate.CanonicalBytes()))
```

## Signed/versioned artifact and production application

```go
type ApprovedPolicyArtifact struct {
    SchemaVersion       string
    PolicyID            string
    PolicyVersion       SemVer
    Kind                "deterministic" | "advisor"
    AbstractPolicy      CanonicalPolicy
    RenderedHarness     Optional[[]byte] // deterministic/ADR 0112 only
    RenderedSHA256      Optional[SHA256]
    EvidenceBundleSHA   SHA256
    EvaluationReportSHA SHA256
    ThresholdSpecSHA    SHA256
    ApprovedBy          ReviewerIdentity
    ApprovedAt          Timestamp
    Supersedes          Optional[ArtifactID]
    RollbackTarget      Optional[ArtifactID]
}

type ApprovedArtifactEnvelope struct {
    Payload             ApprovedPolicyArtifact
    PayloadSHA256       SHA256
    SignerKeyID         string
    Signature           Signature // covers canonical payload bytes + digest
}

type ApprovalArtifactRef struct {
    ArtifactID string
    SHA256     SHA256
    Registry   TrustedRegistryID
}

type ActivationReference struct {
    Artifact ApprovalArtifactRef
    ReviewedChangeID string
    ReviewedRenderedSHA256 Optional[SHA256]
    ActivatedAt Timestamp
}

function PublishApprovalArtifact(approvalRequest, approvalService):
  # Human approval/signing service is separate from the offline pipeline.
  artifact = StrictDecode(approvalRequest.canonicalBytes)
  verify SHA256(approvalRequest.canonicalBytes) == approvalRequest.sha256
  verify schema, content hashes, human-approved semantic diff
  verify signer authorization under policy effective at approval time
  envelope = approvalService.SignExactPayload(
      payload=artifact,
      payloadSHA256=approvalRequest.sha256)
  ref = approvalService.PublishContentAddressed(envelope)
  return ref

function PrepareActivationProposal(approvalRef, approvalRegistry,
                                   activationPolicy):
  # Read-only: returns review material; never edits config/harness/live state.
  envelope = approvalRegistry.FetchExact(approvalRef.ArtifactID,
                                         approvalRef.SHA256)
  verify registry identity, envelope signature and payload digest
  artifact = envelope.Payload
  verify schema and signer authorization as of approval
  apply activationPolicy's explicit signer-revocation rule at activation time
  verify evidence/report/threshold/rendered digests and semantic diff
  verify candidate model-policy inventory and serving-route metadata still match
  return ActivationProposal(exactArtifactRef=approvalRef,
                            semanticDiff=artifact.diff,
                            renderedBytes=artifact.RenderedHarness,
                            compatibility=CompatibilityReport)

function ActivateThroughIndependentReview(proposal, repositoryReview):
  # Outside the offline pipeline. A human-reviewed repository/config change
  # records the exact approved artifact ID/digest and, for a deterministic
  # policy, the exact rendered harness bytes whose digest the artifact binds.
  require repositoryReview explicitly approves proposal.exactArtifactRef
  return repositoryReview.CommitActivationReferenceAndRenderedBytes(
      proposal.exactArtifactRef, proposal.renderedBytes)

function LoadActiveArtifact(activeRef, approvalRegistry, activationPolicy):
  envelope = approvalRegistry.FetchExact(activeRef.Artifact.ID,
                                         activeRef.Artifact.SHA256)
  artifact = envelope.Payload
  verify active reference digest and reviewed activation change
  verify envelope signature/payload digest and artifact schema/content digests
  verify signer under the policy's continued-use/revocation rule
  verify current runtime, signal schema, model-policy inventory, candidate
         entries, provider metadata, serving routes, and rollback target are
         compatible with this artifact

  if artifact.Kind == "deterministic":
    require ADR0112ImplementationAvailable()
    rendered = CompileAbstractPolicyToOverlays(artifact.AbstractPolicy)
    require SHA256(rendered) == artifact.RenderedSHA256
    require activeRef.ReviewedRenderedSHA256 == artifact.RenderedSHA256
    require SHA256(ReadActiveRenderedHarness()) == artifact.RenderedSHA256

  if artifact.Kind == "advisor":
    require artifact.ApplicationPoint has accepted ADR
    load exact bounded advisor/gate policy named by active reference

  never read recent outcomes during installation
  return artifact
```

Production application is deterministic for a deterministic artifact:

```text
Resolve ADR0091 inputs
Read active artifact ID/digest from independently reviewed production reference
Fetch exact bytes from trusted content-addressed approval registry
Verify signature, approval/activation policy, digest, and current compatibility
Resolve ADR0112 overlay rules using current event/config facts
Record policy artifact digest + overlay trace + effective model/effort
Derive direct or gateway serving route
If gateway: preserve ADR0137/0138 route/auth and fail closed
If long-running gateway OIDC: require external per-runtime live-readiness proof
If gateway API-key: require active/non-revoked upstream key and its own
  contract-defined scope/expiry; separately enforce the run-scoped OpenShell
  provider lifetime; provider expiry/deletion does not revoke gateway key;
  no OIDC re-seed loop
```

An advisor artifact makes the **gate and policy version** deterministic, not
the recommendation. Each stochastic recommendation/input/output/version is
recorded and evaluated as an observed sample.

## Rollback and drift

```text
function ObservePolicyHealth(activeArtifact, productionEvidence):
  append evidence; never mutate activeArtifact
  report drift, missing telemetry, quality/failure/route changes
  if preapproved stop condition fires:
    either alert and pause for human decision
    or select activeArtifact.RollbackTarget only if automatic rollback was
       explicitly approved in the artifact/ADR
  fetch rollback target from approval registry by exact ID/digest
  verify target signature, signer-revocation policy, schema, runtime, signal,
         candidate inventory, provider/route/auth, and activation compatibility
  activate exact prior artifact through the pre-approved or human-reviewed
         activation path; record rollback decision and cause
  never synthesize a replacement policy from the triggering evidence
```

## Telemetry contract

Every production run records:

* active artifact ID/version/digest and abstract/rendered digest;
* applicable rule and signal-schema version;
* static selection and source, overlay trace when applicable;
* advisor request digest/recommendation/gate result when applicable;
* effective/requested/actual-served model identities and provenance;
* route/auth mode without secrets, attempts/fallback/escalation;
* trace/run/work-item/task-case correlation;
* measurement versions, cost/latency/usage, quality/completion, artifact digest;
* explicit missingness and later outcome update identity.

## Test seams

```go
type FakeEvidenceSources struct { Traces, Metrics, Evals, Outcomes []Record }
type FakeCandidateProposer struct { Proposals []PolicyProposal }
type FakeIndependentEvaluator struct { Runs []RecordedRun }
type FakeGate struct { Thresholds ThresholdSpec }
type FakeSigner struct { KeyID string; Valid bool }
type FakeApprovalRegistry struct { Artifacts map[ArtifactID][]byte; ReadOnly bool }
type FakeActivationRepository struct { Active Optional[ActivationReference] }
type FakeSignerPolicy struct { AuthorizedAtApproval, AuthorizedAtActivation bool; Revoked bool }
type FakeClock struct { Now Timestamp }
type FakeDriftDetector struct { Report DriftReport }
type RecordingInstaller struct { Installed []ArtifactID; LiveWrites int }
```

Required tests:

1. Reject duplicate/multiplying joins, stale/malformed schema, unknown-as-zero,
   conflicting identities, immature outcomes, and hash mismatch.
2. Keep all attempts of one task in one split; detect near-duplicate leakage.
3. Report assignment imbalance, overlap, propensity, exclusions, and
   candidate-specific missingness; observational evidence alone cannot satisfy
   independent promotion when the study requires paired/randomized evidence.
4. Candidate proposer cannot add signal/model/provider/effort/security choices
   or write live configuration.
5. Cost improvement with quality/security/deterministic/SLO failure rejects.
6. Inconclusive confidence/power rejects even when point estimates and cost
   look favorable.
7. Independent evaluator uses frozen tasks/config/thresholds and retains all
   failed, timed-out, and excluded runs.
8. Rebuilding a report from the same immutable bundle is identical; rerunning
   stochastic calls is tested statistically, not for byte equality.
9. Invalid signer, artifact substitution at the same ID, altered semantic diff,
   rendered hash mismatch, untrusted registry, unsupported schema, and
   unaccepted advisor application point reject.
10. Stale approval whose candidate inventory/route metadata changed before
    activation rejects; activation must reference the exact approved digest.
11. Exercise signer authorization as of approval and activation plus every
    defined revocation/continued-use policy branch.
12. Deterministic artifact refuses installation until ADR 0112 exists.
13. Production resolution records artifact/rule/overlay/effective/served
    identities and preserves ADR 0137/0138 failure boundaries.
14. Drift never edits live policy; rollback accepts only a signed target
    compatible with current schema/runtime/signals/candidate inventory/routes
    and records cause. No replacement is synthesized.
15. Offline proposer/evaluator cannot write the approval registry or activation
    repository; only the separate approval and reviewed activation actors can.
16. Self-consistent forged gateway response/OTEL/export records remain gateway-
    attested and cannot satisfy provider/data-policy or independent promotion
    gates.
17. Conflicting, stale, absent, and downgraded identity evidence stays unknown
    or quarantined and fails the exact preregistered enforcement/promotion gate.
18. A long-running OIDC gateway candidate cannot promote without a current,
    production-adjacent proof for every selected runtime covering `iat`/`exp`
    scheduling, generation settle/re-seed, runtime re-read, same-route/auth
    continuity, and fail-closed negative cases. API-key candidates use a
    separate upstream-key active/revoked and contract-defined scope/expiry
    checks plus the run-scoped OpenShell provider-lifetime check; provider
    expiry/deletion does not revoke the gateway key, and no OIDC re-seed loop
    is claimed.
