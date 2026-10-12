# AISDLC-291 implementation plan: pre-run routing advisor

Status: proposed, contingent on AISDLC-288 decisions and a new ADR
No Jira or production code changes are authorized by this plan.

## Goal

Deliver an evidence-backed host-side advisor that can be evaluated offline and,
only after destination/provider/privacy approval, in non-enforcing shadow mode;
then promote only after Fullsend accepts a new ADR defining live precedence,
security, fallback, and observability.

## Dependencies and gates

| Gate | Required outcome |
|---|---|
| AISDLC-288 decisions | v1 surface, security downgrade rule, signal set, budget unit, pilot, AISDLC-5 milestone, and quality-gate escalation are approved or deferred |
| AISDLC-292 boundaries | AISDLC-345 cost-evidence consumption, AISDLC-282 cumulative-spend enforcement, unresolved persona/subagent ownership, gateway scope, and evaluation ownership stay explicit |
| New Fullsend ADR | advisor placement/timing, config ownership, precedence, signal trust, abstention/failure, ADR 0112 composition, ADR 0137/0138 route behavior, and trace contract accepted before enforcement |
| ADR 0112 implementation | required only if the accepted composition uses model/effort overlays or their trace/fallback |
| AISDLC-5 evidence gate | repeated golden-path equivalence and promotion threshold satisfied at the milestone selected by humans |
| Provider/privacy review | every signal and advisor provider/data route approved before live shadow transmits data |
| Gateway route readiness | route code exists for pi/Codex/Claude, but each runtime selected for a production-adjacent OIDC pilot over five minutes or beyond its observed token lifetime supplies ADR 0137's live rotation/continuity proof; API-key mode is evaluated separately |

## Phase 1: decision and ADR work

1. Resolve the twelve human questions in `research.md`.
2. Write a new ADR comparing at least:
   - offline-only deterministic policy authoring;
   - host pre-run advisor after static resolution;
   - advisor signal consumed by overlays; and
   - external gateway decision.
3. For the pre-run option, decide:
   - the exact insertion/application point in ADR 0091 precedence;
   - whether explicit flags/environment can ever be replaced;
   - how `agents:` and harness values form the pinned baseline;
   - one of the ADR 0112 composition states from `prototype.md`;
   - exact security-stage permitted choices;
   - timeout/abstention/malformed/policy-rejection actions;
   - unserved advisor-model behavior and approved-pool validation of existing
     fallback chains;
   - advisor service credential/data route; and
   - decision record and actual-served-model requirements.
4. Do not amend ADR 0112's substantive decision in place. A new decision may
   reference or supersede only the necessary boundary.
5. Do not restore PR 7808's `routing:` schema, tiers, protected-agent list, or
   modes by accident. Any similar concept must be justified independently.

The ADR must publish a closed mode/application-point matrix. The prototype
baseline is: pinned ignores the point and never calls; shadow admits only
`shadow_only`; enforce admits only an explicitly ADR-accepted
`below_operator_override`; all other enabled pairs fail configuration before
advisor transmission, route planning, or sandbox creation. In particular,
`advisor_signal_to_overlay` remains invalid until its separate ADR 0112
composition is accepted and implemented.

Exit: accepted ADR and approved product decisions; otherwise stop at offline
research.

## Phase 2: offline experiment harness

1. Build a versioned dataset from the chosen pilot with:
   - approved/redacted signals;
   - task/security strata;
   - pinned configuration and model/provider versions;
   - deterministic artifact checks;
   - independent outcome labels; and
   - cost/latency/attempt data.
2. Implement the `AdviceRequest`/`Recommendation` schema in an experiment
   package, not the production runner.
3. Implement adapters behind one interface:
   - simple deterministic rules as a control;
   - a general structured-output classifier; and
   - Jev or another decision model if provider/data approval exists.
4. Keep raw task text excluded initially. Add it only as a separately measured
   experiment with injection/privacy controls.
5. Measure recommendation distribution, abstention, malformed/error/latency,
   gate rejection, quality by stratum, cost, and calibration where score
   semantics exist.
6. Record observed input/output/version. Do not test or advertise stochastic
   output as exactly replayable.

Exit: one or more advisor candidates justify—or fail to justify—live shadow.

## Phase 3: production contract and pure gate

After ADR acceptance:

1. Add a small package such as `internal/routingadvisor` containing only:
   - versioned request/recommendation/decision types;
   - strict bounded decoder;
   - `Advisor` interface;
   - pure deterministic policy gate; and
   - reason-code registry.
2. Put config types in `internal/config` only after the ADR chooses a schema.
   Update `docs/reference/config-reference.md` for every new field.
3. Validate config on both write and `fullsend run` read paths, following ADR
   0091's dual-validation pattern.
4. Represent security floors and provider/capability policy as explicit exact
   sets; do not infer model strength from names.
5. Make unknown fields, duplicate JSON keys, oversized values, invalid model
   refs, invalid efforts, non-finite scores, and unsupported score semantics
   fail closed to the documented decision.
6. Unit-test the gate independently of any model or network.

Exit: contract/gate meet the test matrix with no runner integration.

## Phase 4: runner integration in shadow

1. Refactor `internal/cli/run.go` to compute an atomic static resolution result
   before model-dependent provider planning:
   - runtime and source;
   - model/effort and sources;
   - canonical alias result;
   - applicable overlay trace when ADR 0112 exists; and
   - security/stage profile reference.
2. Add a focused orchestration file such as `internal/cli/run_advisor.go`; do
   not grow runtime adapters with policy logic.
3. Insert the advisor after static resolution and before:
   - `NeedsOpenAIProvider` / `NeedsGatewayRoute`;
   - Vertex/OpenAI credential preparation;
   - `planGatewayRoute`; and
   - sandbox creation.
4. In shadow, always execute static selection. Do not mutate `h.Model`,
   `h.Effort`, aliases, child models, or provider planning.
   The same invariant applies to timeout, malformed output, abstention, gate
   rejection, security suppression, and both `static` and `named_escalation`
   failure actions. Record the latter only as a hypothetical result.
   Configure this phase with `ApplicationPoint=shadow_only`; reject every
   other shadow application point before constructing or calling the advisor.
5. Use `forge.Client` for any forge data lookup. Prefer already normalized
   event fields; do not shell out to `gh` or call forge APIs from CLI code.
6. Give the advisor an independent short timeout under the run context. It has
   no tools, repository credentials, arbitrary filesystem, or arbitrary
   network.
7. If the advisor is remote, implement its credential/egress design exactly as
   accepted by the ADR; do not reuse the agent's gateway/provider credentials
   implicitly.
8. Before a gateway-backed production-adjacent shadow expected to exceed five
   minutes or the observed OIDC token lifetime, require per-runtime live
   evidence that refresh
   timing uses each token's `iat`/`exp`, a new placeholder generation settles
   and is re-seeded, the runtime re-reads it, and requests continue on the same
   gateway block/OIDC mode. Negative refresh/re-seed/expiry cases fail closed
   without direct-provider preparation or auth-mode switching. For API-key,
   verify the upstream gateway key is active/not revoked and satisfies any
   contract-defined scope/expiry; separately enforce the run-scoped OpenShell
   provider-credential lifetime and do not run an OIDC re-seed loop. Provider-
   credential expiration/deletion limits sandbox use but does not revoke the
   gateway key.

Exit: approved production-adjacent shadow produces decisions without changing
runs or transmitting any signal outside its approved destination/privacy
contract.

## Phase 5: telemetry and outcome join

1. Add `routing-decision.json` or an ADR-approved equivalent with the record in
   `prototype.md`.
2. Add a `model_routing.advisor` OTel span and bounded categorical attributes.
3. Extend `metrics.json` with decision ID and routing summary while preserving
   requested-versus-reported model semantics.
4. Show static/recommended/gate/effective values in plan output; never print raw
   untrusted signals.
5. Add runtime capability metadata for actual-served-model evidence:
   provider-reported, gateway-reported, requested-only, or unknown.
6. Join delayed forge/quality outcomes by immutable decision ID through the
   evaluation system, not by asking the routed agent to grade itself.
7. Consume AISDLC-345's authoritative run/work-item cost evidence; do not
   create a competing cost-authority record. Treat AISDLC-282 cumulative spend
   enforcement only as an approved input/gate, not advisor-owned enforcement.
8. Define retention/redaction for input values and hashes.

Exit: every shadow decision and outcome is queryable without exposing content
or mislabeling requested model as verified served model.

## Phase 6: evaluation and promotion decision

1. Run the AISDLC-5 golden path repeatedly on identical pinned and candidate
   inputs.
2. Apply deterministic artifact checks first.
3. Compare completion/quality, reviewer changes, cost, latency, attempts,
   abstention, gate rejection, and escalation by task/security stratum with
   uncertainty.
4. Investigate every policy-rejected or quality-regressed class; do not average
   a security-relevant regression away with easy tasks.
5. Treat LangChain's 64% figure as context only. Set Fullsend's threshold before
   observing promotion results.
6. Retain failed experiments and exact versions.
7. Treat the applicable per-runtime gateway route-readiness artifact as a
   promotion prerequisite, not as evidence owned or generated by the advisor.

Exit: human promotion authority approves enforcement, or the advisor remains
shadow/offline.

## Phase 7: enforcement

Only after all gates:

1. Enable the exact application point accepted by the new ADR.
2. Re-run the pure gate, provider/capability checks, and security exact-set
   checks before mutating the effective selection.
3. Recompute alias/provider/credential/gateway planning from the accepted
   selection; never reuse a plan prepared for the static model.
4. For `gateway/`:
   - use the existing ADR 0137/0138 route;
   - keep its configured auth mode;
   - fail terminally on control-plane, credential, gateway-side classifier,
     gateway, or upstream failure; and
   - never fall back to direct provider credentials.
5. For direct routes, preserve current runtime-specific fallback behavior.
   Do not invent cross-provider fallback.
6. Keep advisor failure/abstention bounded to the approved static or named
   escalation action; never recursively re-query the advisor.
7. Provide a tested operator rollback that disables advisor enforcement and
   returns to pinned/static behavior without a product-code change.

Exit: opt-in pilot passes rollback, failure injection, security review, and
quality-equivalence checks.

## Test and verification plan

### Unit/package tests

- all 20 prototype cases, with table-driven shadow cases crossing every
  advisor outcome with `FailureAction=static` and `named_escalation`;
- a table-driven 3×4 cross-product of every mode and application point,
  including the accepted-ADR proof required by enforce;
- config merge/default/validation and provenance;
- canonical request digest stability without raw-secret leakage;
- race tests for any shared advisor client/cache;
- fuzz strict decoder, signal bounds, duplicate/unknown fields, and reason
  codes; and
- cancellation/deadline tests with fake clock/advisor.

### CLI/integration tests

- plan and `metrics.json` snapshots for pinned, shadow, accepted, abstained,
  rejected, operator override, direct, and gateway cases, including shadow
  timeout/malformed/rejection with both failure actions;
- no sandbox/provider creation on fatal configuration errors;
- advisor call precedes model-dependent provider planning;
- gateway failures never invoke direct-route setup;
- long-running OIDC gateway pilots are rejected without per-runtime live proof
  of `iat`/`exp` scheduling, generation settle/re-seed, runtime re-read, and
  same-route/auth continuity; refresh/re-seed/expiry negatives fail closed;
- API-key gateway pilots separately test upstream-key active/revoked state and
  contract-defined scope/expiry, run-scoped OpenShell provider lifetime, the
  fact that provider expiry/deletion does not revoke the key, and no OIDC re-seed;
- ADR 0112 trace optional/conditional according to accepted composition; and
- no raw untrusted signal in log, span, status comment, or error.

### Behaviour/evaluation tests

- dummy/dummy-playback scenarios for selection/failure/rollback;
- production-adjacent opt-in shadow;
- repeated golden path with fixed inputs/config;
- independent quality grader and delayed forge outcome join; and
- workload-drift/model-version segmentation.

### Repository verification

For Go production changes, follow `docs/contributing/go-code.md`:

1. stage changes before `make lint`;
2. run focused tests, `make go-test`, and `make go-vet`;
3. verify approximate patch coverage reaches the 80% target (75% floor) with
   the `check-patch-coverage` process; and
4. run behavior/playback tests when the end-to-end path changes.

## Security review checklist

- [ ] Policy/config/advisor identity come from trusted/base ref.
- [ ] Input schema is closed, bounded, versioned, and trust-classified.
- [ ] Raw task text is excluded or separately approved/scanned/redacted.
- [ ] Live shadow destination/provider/credentials/egress/privacy are approved
      before any task signal is transmitted.
- [ ] No credential, environment dump, gateway URL, or secret enters request or telemetry.
- [ ] Advisor has no tools or authority-bearing actions.
- [ ] Policy gate is deterministic and independently tested.
- [ ] Every shadow outcome mechanically retains the static effective selection.
- [ ] Security-stage exact allowed set cannot be relaxed by untrusted signals.
- [ ] Explicit operator precedence matches the new ADR.
- [ ] ADR 0112 composition is explicit and its unimplemented state handled.
- [ ] Gateway route/auth never silently switches or falls back direct.
- [ ] Long-running OIDC pilot readiness is supported by production-adjacent
      proof for each selected runtime, not inferred from route code or unit
      fault injection; API-key upstream-key and run-scoped provider lifetimes
      are separately verified without treating provider expiry/deletion as key
      revocation.
- [ ] Requested and actual served model are not conflated.
- [ ] Stochastic audit records are not called deterministic replay.
- [ ] Evaluation is independent and cost cannot offset quality failure.
- [ ] Rollback to pinned/static is exercised.

## Deliverables

1. Accepted advisor ADR, or documented decision not to proceed.
2. Versioned offline dataset and experiment report.
3. Advisor/gate contract and test suite.
4. Shadow decision records and dashboard.
5. Golden-path comparison and human promotion decision.
6. If approved, opt-in enforcement plus rollback runbook.
7. User-reviewed Jira findings; Jira persistence remains a separate action.
