# AISDLC-290 implementation plan

This is a staged research/implementation plan. It does not authorize production
policy changes, a new user-facing schema, or Jira edits.

## Phase 0: product and governance decisions

Resolve and record:

1. whether standalone evidence-updated deterministic routing satisfies v1
   `auto`, or whether a live advisor is required;
2. policy lifecycle roles and separation of proposer/evaluator/approver;
3. golden path, task grouping, checks, metrics, thresholds, trial count, and
   confidence/non-inferiority rule under AISDLC-5;
4. observational-evidence eligibility and required independent experiment;
5. allowed signals, strata, model/effort pairs, provider/route/data policy;
6. drift/stop/rollback thresholds and whether only human or pre-approved
   automatic rollback may activate a prior signed artifact;
7. signing mechanism, content-addressed approval registry, independent
   activation review, retention/audit access, and signer authorization/
   revocation semantics at approval, activation, and continued use;
8. advisor precedence/application point and its required new ADR.

Exit: pre-registered study and governance specifications; no encoded defaults
stand in for open decisions.

## Phase 1: evidence inventory and gap closure

1. Map current trace, `metrics.json`, transcript, eval-measurement, artifact,
   and forge-outcome fields to `EvidenceRecordV1`. Map cost through
   AISDLC-345's authoritative attributable run/work-item evidence rather than
   creating a competing source of truth; consume AISDLC-282 cumulative-spend
   enforcement only as a read-only policy input.
2. Classify actual-served model provenance per runtime/gateway; do not substitute
   requested model where evidence is absent.
   Record attester, trust domain, source, runner request id, freshness, and
   assurance for selected and served identities. Gateway response/OTEL/export
   remain one trust domain. Inventory provider-signed metadata and independently
   retrieved upstream audit/account reconciliation options.
3. Define stable RunID/TraceID/WorkItemID/TaskCaseID joins and outcome-maturity
   updates without many-to-many multiplication.
4. Version measurement manifests, task manifests, cost tables, parsers,
   harness/config/advisor identities, and a neutral approved model-policy
   inventory digest binding exact candidate/provider/capability/route metadata.
5. Add privacy/retention/redaction constraints before ingesting production
   evidence.
6. Inventory per-runtime gateway route-readiness evidence separately from
   route-code presence. For long-running OIDC pilots, reference the ADR 0137
   production-adjacent proof (`iat`/`exp` scheduling, generation settle/re-seed,
   runtime re-read, same-route/auth continuity, and fail-closed negative cases).
   Treat absence as unknown. For API-key, separately record upstream gateway-
   key active/revoked and contract-defined scope/expiry evidence plus the run-
   scoped OpenShell provider-credential lifetime; provider expiration/deletion
   does not revoke the gateway key, and no OIDC re-seed loop is inferred.
   AISDLC-290 consumes these artifacts; it does not own or manufacture them.

Likely existing seams to reuse after design approval:

* `internal/evalmeasure` for versioned trace-derived measurements and
  idempotence—not as a complete policy ledger;
* `internal/telemetry` and `internal/cli/run.go` for run correlation and policy
  digest attributes;
* runtime progress/metrics collectors for actual-served provenance gaps;
* forge adapters through `forge.Client` for matured forge outcomes.

Exit: field-level data contract and explicit gap register.

## Phase 2: offline read-only prototype

Build a standalone/test-only pipeline that cannot import production config
writers:

1. ingest immutable exported evidence;
2. validate schemas, hashes, join cardinality, missingness, and provenance;
3. create task/time-grouped partitions and leakage report;
4. produce selection-bias, overlap, propensity, and drift reports;
5. generate proposal artifacts within a fixed reviewed search space;
6. regenerate identical reports from the same bundle;
7. prove filesystem/API capabilities cannot write the approval registry,
   activation repository, live harness, or config.

Exit: versioned evidence bundle and proposals suitable for review, not
installation.

## Phase 3: independent evaluation harness

1. Freeze task manifest, baseline, candidate artifacts, exact runtime/model/
   config versions, repetitions, and thresholds before opening results.
2. Execute paired repeated baseline/candidate runs in isolated environments.
3. Run deterministic checks before scoring qualitative measurements.
4. Capture every failure, timeout, exclusion, classifier cost, fallback, and
   later outcome; unknown remains unknown.
5. Compute quality/completion uncertainty, operational measures, and cost.
6. Enforce ordered gates; cost is inspected only after all preceding gates.
7. Enforce the preregistered minimum assurance for shadow intent, shadow served
   behavior, production enforcement, provider/data-policy compliance, and
   promotion. Unknown/weak/conflicting/stale evidence never passes upward.
8. Enforce the operational gateway-readiness gate: any long-running OIDC
   candidate has current production-adjacent proof for every selected runtime;
   API-key candidates satisfy upstream-key active/revoked and contract-defined
   scope/expiry checks plus their separate run-scoped OpenShell provider-
   credential lifetime bound; neither is substituted for the other.

Exit: independently generated evaluation report plus reproducibility manifest.

## Phase 4A: standalone deterministic artifact

Blocked until ADR 0112 implementation exists.

1. Draft an abstract deterministic policy format scoped only to approved
   signals and concrete model/effort pairs; do not copy PR 7808's rejected
   schema.
2. Implement a pure compiler to ADR 0112 overlays with semantic diff and exact
   rendered digest.
3. Add a separate human approval/signing step that publishes the exact artifact
   to a content-addressed trusted registry.
4. Generate a read-only activation proposal for an independently reviewed
   repository change referencing exact artifact ID/digest.
5. At production load, verify the active reference, approval artifact,
   signature/revocation policy, digests, and current model-policy/route
   compatibility.
6. Emit active artifact/rule/overlay trace in plan output, metrics, and OTEL.
7. Test rollback to an exact compatible prior signed artifact through the same
   activation boundary.

Exit: deterministic production application is attributable and reproducible
from the approved artifact.

## Phase 4B: advisor policy artifact

Requires the separate advisor ADR.

1. Package candidate set, signal schema, advisor/rubric/version, deterministic
   gate, abstention/failure, security floor, and application point.
2. Sign/promote the exact artifact after independent evaluation.
3. At runtime record the stochastic request/recommendation and deterministic
   gate result separately.
4. Reuse current direct/gateway route planning only after acceptance; preserve
   ADRs 0137/0138 for gateway models.

Exit: advisor behavior is bounded by a promoted artifact without claiming
individual stochastic calls are replayable.

## Phase 5: pilot, monitor, rollback

1. Start one opt-in production-adjacent pilot against the pinned baseline.
2. Monitor evidence completeness, workload/model drift, quality/completion,
   failures/escalations, latency, total cost, and route/auth mismatches.
3. Stop on pre-approved safety/quality conditions.
4. Roll back only to the verified prior signed artifact; never train or install
   a replacement from the triggering live evidence.
5. Retain accepted, rejected, inconclusive, and rolled-back experiments.

## Security and architecture review checklist

* The proposal pipeline has no live config/Jira/forge write capability.
* Historical outcomes cannot mutate production policy or security floors.
* Trusted and author-shaped signals remain distinguished; author-shaped facts
  cannot enforce a security floor under ADR 0112.
* Models/providers/routes/classifier destinations come from a reviewed closed
  set.
* Gateway selections keep ADR 0137 fail-closed routing and ADR 0138 fixed auth
  across control-plane, credential, classifier, gateway, and upstream failures.
* Long-running OIDC gateway promotion consumes, but does not create, the live
  per-runtime rotation/continuity proof; API-key semantics remain separate.
* PR 7808's rejected schema/modes are not treated as accepted design.
* Secrets/raw sensitive content are excluded or handled under explicit policy.
* Evidence/artifacts are content-addressed, signed, retained, and auditable.
* Approval artifacts and active production references are separate: approval
  publishes immutable signed bytes; independent review activates an exact
  digest; production verifies both.
* The offline proposer/evaluator has no approval-registry or activation write
  capability.
* Quality/security precede cost; missing/inconclusive evidence cannot promote.
* Identity evidence never self-upgrades: gateway-generated channels are one
  trust domain, and every stronger gate checks attester, assurance, and
  freshness. Any residual-risk exception is prior, scoped, expiring, and does
  not relabel evidence or waive mandatory compliance proof.
* Proposer, evaluator, and authorized approver are independently attributable.

## Verification matrix

| Concern | Required proof |
|---|---|
| Data integrity | schemas, hashes, join cardinality, missingness, maturity |
| Bias/leakage | task grouping, held-out split, overlap/propensity report, frozen thresholds |
| Drift | version and population/outcome monitoring with stop rule |
| Proposal isolation | no live writes; fixed approved search space |
| Evaluation | repeated paired runs; deterministic checks; uncertainty |
| Promotion | security/quality/SLO gates before cost; human signature |
| Application | exact registry artifact plus independently reviewed active reference; signer/revocation and compatibility checks; deterministic overlay/advisor gate record |
| Gateway | attester/assurance for selected/served identity; forged self-consistent gateway lies, conflicts, staleness, absence, and downgrade fail stronger gates; no direct credential/auth-mode fallback; long-running OIDC promotion requires external per-runtime live rotation/continuity evidence; API-key separately proves upstream-key active/revoked and contract scope/expiry plus run-scoped OpenShell provider lifetime, whose expiration/deletion does not revoke the gateway key |
| Rollback | exact prior signed and currently compatible artifact; no synthesized replacement |
| Reproducibility | same bundle reproduces report; stochastic calls treated as samples |

## Repository process for later implementation

Before changing Go code, read `docs/contributing/go-code.md`. Extend interfaces
and their documentation together, use `forge.Client` for forge operations,
stage changes before `make lint`, run appropriate Go/behavior tests, and verify
approximate Go patch coverage meets the 80% target and 75% enforced floor.
Document every new config field in `docs/reference/config-reference.md` and
update CLI/user guides for flags, output, or behavior changes. Any new routing
or advisor architecture requires the ADR process.

## Definition of done

AISDLC-290 is complete when stakeholders choose what offline authoring means
for `auto`, the versioned evidence/proposal/evaluation/promotion contracts pass
the prototype tests, selection bias/drift and stochastic reproducibility are
explicit, cost-only promotion and direct live mutation are mechanically
impossible, rollback uses an exact signed artifact, and standalone/advisor
plans identify their ADR and implementation prerequisites. This spike itself
makes no production or Jira changes.
