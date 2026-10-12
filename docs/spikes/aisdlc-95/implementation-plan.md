# SPIKE: AISDLC-95 model-routing implementation plan

**Feature:** [AISDLC-95](https://redhat.atlassian.net/browse/AISDLC-95)
**Refinement epic:** [AISDLC-287](https://redhat.atlassian.net/browse/AISDLC-287)
**Date:** 2026-10-12
**Status:** Proposed for reporter review; not approved for implementation

## Purpose

This document consolidates the research from the five AISDLC-287 spikes into
an implementation sequence that can be reviewed before AISDLC-95 is broken
down into delivery epics and stories.

It is not an accepted Fullsend architecture. The AISDLC-95 reporter must
approve the product direction and resolve or explicitly defer the decision
gates below before the delivery breakdown is created. Any learned or LLM-based
production routing decision also requires a new Fullsend ADR.

## Jira spike inputs

The implementation sequence is derived from the issue-specific research,
prototypes, and implementation plans:

- AISDLC-288 acceptance criteria:
  [research](spikes/AISDLC-288/research.md),
  [prototype](spikes/AISDLC-288/prototype.md) and
  [implementation plan](spikes/AISDLC-288/implementation-plan.md)
- AISDLC-289 external gateway:
  [research](spikes/AISDLC-289/research.md),
  [prototype](spikes/AISDLC-289/prototype.md) and
  [implementation plan](spikes/AISDLC-289/implementation-plan.md)
- AISDLC-290 offline policy authoring:
  [research](spikes/AISDLC-290/research.md),
  [prototype](spikes/AISDLC-290/prototype.md) and
  [implementation plan](spikes/AISDLC-290/implementation-plan.md)
- AISDLC-291 pre-run advisor:
  [research](spikes/AISDLC-291/research.md),
  [prototype](spikes/AISDLC-291/prototype.md) and
  [implementation plan](spikes/AISDLC-291/implementation-plan.md)
- AISDLC-292 related-work boundaries:
  [research](spikes/AISDLC-292/research.md),
  [prototype](spikes/AISDLC-292/prototype.md) and
  [implementation plan](spikes/AISDLC-292/implementation-plan.md)

## Accepted architecture baseline

The implementation must compose with these accepted decisions:

- [ADR 0112](../../ADRs/0112-overlays-may-set-any-harness-field.md) defines
  deterministic per-task `model` and `effort` selection through harness
  overlays, including source precedence, resolution traces and typed fallback.
  It explicitly defers LLM-based routing to AISDLC-95 and requires another ADR.
- [ADR 0137](../../ADRs/0137-inference-gateway-credential-route.md) defines an
  inference gateway as a credential route of its own. Once selected, gateway
  failures cannot escape to a direct-provider route.
- [ADR 0138](../../ADRs/0138-inference-gateway-api-key-credential-mode.md) adds an
  explicit `api-key` credential mode to the gateway route. It does not create
  fallback or precedence between OIDC and API-key modes.

ADR 0112 was accepted but its complete model/effort overlay behavior was not
implemented on the research baseline. That deterministic foundation should be
completed before automatic routing enforcement.

## Proposed implementation shape

The research supports a layered approach. These are recommendations for
reporter and architecture approval, not recorded decisions.

1. **Offline policy authoring is the governance layer.** Repeated evaluation
   and production evidence may propose a versioned routing policy. A proposal
   cannot activate itself. Human approval, signed artifacts, independent
   activation, promotion gates and exact rollback preserve deterministic
   production behavior.
2. **A pre-run advisor is the task-aware candidate.** A host-side advisor may
   recommend a concrete model and effort or abstain. Fullsend policy decides
   whether the recommendation is allowed. Shadow mode always executes the
   existing static selection. Enforcement requires a new ADR and an accepted
   application point.
3. **The external gateway begins as serving and enforcement.** The existing
   gateway route serves an approved concrete `gateway/<model>` choice. It is
   not presumed to be the automatic selector. A gateway selector would need a
   separately approved control contract, route-isolated failure behavior,
   authenticated repository-bound calls, conformance evidence and a new ADR.
4. **Pinned and deterministic overlay modes remain first-class.** Pinned mode
   is the safe baseline and rollback. Reviewed evidence can also produce a
   deterministic overlay policy without introducing a live classifier.

## Invariants

Every later epic and story must preserve these constraints:

- Automatic routing is opt-in. Pinned routing remains supported and is the
  rollback target.
- Author-controlled input cannot establish or relax the security floor,
  provider policy, credential route or egress policy.
- Shadow routing cannot change the model or effort that actually executes.
- Requested alias, advisor recommendation, accepted selection, runtime request
  and actual served model remain separate recorded identities.
- Missing cost, usage, served-model or quality evidence remains unknown. It is
  never converted to zero or success.
- Gateway response metadata, gateway telemetry and gateway exports share one
  gateway trust domain. Self-consistency is not independent proof of the
  upstream provider or model.
- Once the gateway route owns a selection or control call, failures stay on
  the same configured gateway block and ADR 0138 authentication mode. The only
  permitted recovery is a preapproved concrete gateway fallback or fail closed.
- OIDC and opaque API-key validation remain mode-specific. Upstream API-key
  revocation and optional contract expiry are distinct from the run-scoped
  OpenShell provider lifetime.
- A production-adjacent gateway pilot that may outlive the observed OIDC token
  lifetime requires per-runtime live proof of rotation, settle, re-seed,
  credential-file re-read and fail-closed behavior.
- Historical evidence cannot directly mutate live policy. Cost is evaluated
  only after evidence-integrity, security, deterministic, quality, completion
  and operational gates pass.

## Reporter decision gates

The reporter should approve an option or explicitly defer each item before the
delivery epic/story breakdown is created.

| ID | Decision required | Recommended starting point |
|---|---|---|
| D1 | Which surfaces may v1 route? | Top-level agent `model` and `effort`; leave runtime and persona/subagent routing out until separately assigned. |
| D2 | Which candidate placements proceed? | Offline governance plus a pre-run advisor experiment; gateway serves concrete choices and is not initially the selector. |
| D3 | May a classifier participate? | Yes in offline evaluation and shadow; enforcement only after evidence and a new ADR. |
| D4 | May a security-relevant stage be downgraded? | Keep it pinned or escalation-only unless a later approved policy defines trusted downgrade evidence. |
| D5 | What does AISDLC-5 block? | Block policy promotion and production enforcement, not deterministic foundation work or offline experimentation. |
| D6 | What is the first golden-path pilot? | Select one named workflow after confirming its evaluation data and ownership; treat AISDLC-116/AISDLC-24 as a candidate, not an encoded blocker. |
| D7 | What budget does AISDLC-95 own? | Routing-time per-invocation or per-stage policy; consume AISDLC-345 cost evidence and leave cumulative enforcement to AISDLC-282. |
| D8 | What satisfies configuration without code changes? | A reviewed custom harness/configuration is sufficient for v1 if operators can version, inspect and roll it back. |
| D9 | What happens after a quality failure? | Define one bounded escalation attempt with an explicit new attempt identity; do not imply transparent continuation. |
| D10 | May historical evidence change live policy? | Reviewed, versioned offline updates only for v1. |
| D11 | What may be claimed from AISDLC-15? | Hummingbird interest and an exploratory integration recommendation only; gateway availability and selector capability require separate proof. |
| D12 | Which signals may v1 use? | Close and version a minimal signal set before implementation; record provenance and author-control classification for every signal. |

## Proposed delivery sequence

### Phase 0: approval and architecture closure

- Obtain reporter approval for D1-D12.
- Record explicitly deferred decisions and owners.
- Confirm whether the approved direction needs one new ADR or separate ADRs
  for the advisor application point and gateway-selector control plane.
- Re-read current Jira and repository state before creating delivery work.

Exit gate: the reporter approves the planning basis and every unresolved item
has a decision, owner or explicit deferral.

### Phase 1: deterministic routing foundation

- Finish ADR 0112 model/effort overlay implementation.
- Preserve ADR 0091 source precedence.
- Emit the overlay-resolution trace and fallback reason.
- Implement and test the typed unserved-model retry without overlays.
- Keep pinned behavior as the comparison baseline and rollback.

Exit gate: deterministic conformance, fallback and resolution-trace tests pass.

### Phase 2: routing evidence contract

- Define stable decision, invocation, attempt, work-item and stage identities.
- Integrate authoritative cost evidence from AISDLC-345.
- Record recommendation, accepted selection, requested runtime model, actual
  served identity, fallback/escalation, latency, cost and outcome.
- Carry attester, source, trust domain, freshness and assurance on identity
  evidence.
- Define queryable evidence without duplicating AISDLC-282 cumulative spend
  enforcement.

Exit gate: a representative multi-attempt run can be reconstructed without
coercing missing evidence into a successful value.

### Phase 3: offline policy authoring and evaluation

- Create an immutable evaluation ledger with unsuccessful experiments retained.
- Define the pinned baseline, versioned golden path and repeated-trial method.
- Evaluate deterministic policies and advisor candidates against the same
  fixtures.
- Apply evidence-integrity, security, deterministic, quality, completion and
  operational gates before cost.
- Require human approval, signed policy artifacts, independent activation and
  exact rollback.

Exit gate: AISDLC-5 promotion requirements selected in D5 pass for the chosen
pilot, and the candidate is non-inferior within the approved tolerance.

### Phase 4: pre-run advisor shadow

- Refactor static resolution into one atomic host-side planning boundary before
  model-dependent credential, route and sandbox planning.
- Implement a strict versioned advisor input/output and abstention contract.
- Apply deterministic provider, model, security and capability policy after
  every recommendation.
- Run offline first. Permit live shadow only after destination, privacy,
  credential and egress review.
- Always execute the static choice in shadow mode.

Exit gate: shadow evidence is attributable, policy violations fail closed,
abstention is deterministic, and no shadow decision changes execution.

### Phase 5: controlled advisor enforcement

- Write and accept the required Fullsend ADR.
- Implement the approved application point and precedence matrix.
- Start one opt-in production-adjacent pilot.
- Monitor quality, completion, escalation, latency, cost, drift and evidence
  health with predefined stop rules.
- Exercise rollback to pinned mode.

Exit gate: the pilot passes its approved quality and operational thresholds and
demonstrates rollback.

### Phase 6: optional gateway-selector experiment

This phase is not required for the first advisor implementation.

- Define a provider-neutral authenticated selector control contract.
- Keep control and inference calls on the configured gateway route.
- Validate exact repository authorization and mode-specific credentials.
- Test malformed, denied, stale, conflicting, timed-out and unreachable
  responses; every case uses a trusted concrete gateway fallback or fails closed.
- Prove actual-served identity at the assurance required for each claim.
- Complete per-runtime long-run OIDC rotation proof before any pilot that may
  outlive the observed token lifetime.

Exit gate: a new ADR is accepted and the route, credential, conformance,
identity-evidence and operational-proof gates pass.

## Verification expectations

- Unit tests for policy ordering, abstention, fallback, precedence and
  mode-specific authentication.
- Behavior tests for pinned, deterministic-overlay, shadow and enforced modes.
- Adversarial tests for author-shaped inputs, forged gateway evidence,
  repository mismatch, credential leakage, route escape, malformed control
  responses and missing evidence.
- Repeated golden-path evaluation with uncertainty, exact configuration and
  artifact provenance.
- Go production changes meet the repository patch-coverage gate.
- User-facing configuration, CLI and operational behavior updates include the
  required documentation changes.

## Proposed Jira progression

No Jira mutation is authorized by this document. After reporter approval:

1. Record the approved D1-D12 decisions on AISDLC-95.
2. Attach or link the final research findings to AISDLC-287 and each child
   spike, preserving the distinction between findings and accepted decisions.
3. Transition the five research spikes and AISDLC-287 only after their findings
   and remaining owners are recorded.
4. Create delivery epics from the approved phases, then create stories with
   testable acceptance criteria, dependencies and ADR gates.
5. Link AISDLC-345 as the cost-evidence source and AISDLC-282 as cumulative
   enforcement ownership. Keep AISDLC-5's exact blocking milestone explicit.
6. Re-read every affected issue immediately before mutation and show the exact
   proposed Jira changes for human approval.

## References

- [AISDLC-95](https://redhat.atlassian.net/browse/AISDLC-95)
- [AISDLC-287](https://redhat.atlassian.net/browse/AISDLC-287)
- [ADR 0112](../../ADRs/0112-overlays-may-set-any-harness-field.md)
- [ADR 0137](../../ADRs/0137-inference-gateway-credential-route.md)
- [ADR 0138](../../ADRs/0138-inference-gateway-api-key-credential-mode.md)
- [Adaptive agent selection](../../problems/adaptive-agent-selection.md)
