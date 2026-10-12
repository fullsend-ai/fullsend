# AISDLC-292 implementation plan

This is a refinement/execution plan. It makes no Jira changes and implements no
routing mechanism.

## Preconditions

* Use the authenticated 2026-10-12 snapshot in `research.md` as the current
  evidence baseline. Immediately before a later Jira mutation, refresh the
  summary, description, workflow status, parent, assignee, and relationship
  types for AISDLC-40, AISDLC-95, AISDLC-287, AISDLC-288 through AISDLC-292,
  AISDLC-5, AISDLC-116, AISDLC-24, AISDLC-15, AISDLC-282, AISDLC-345, and
  AISDLC-144.
* Reconfirm that PR 7842 remains merged, PR 7808 remains closed, ADR 0112 is
  Accepted, and the repository still labels its behavior unimplemented.
* Reconfirm that ADRs 0137 and 0138 remain Accepted, that route code remains
  implemented for pi, Codex, and Claude, and that ADR 0137's named live in-
  iteration OIDC placeholder-rotation proof is still outstanding or has new
  per-runtime evidence. Do not collapse code presence into pilot readiness.
* Ask the AISDLC-95 product owner to answer the unresolved questions in
  `research.md`; do not infer answers.

## Ordered execution

### 1. Validate the live relationship graph

Start from the authenticated hierarchy/status/link table in `research.md`.
Capture it again immediately before editing and flag changes since the
2026-10-12 snapshot. Keep workflow status distinct from the legacy Epic Status
field and keep description prose distinct from actual issue-link types.
Confirm the complete AISDLC-40 -> AISDLC-95 -> AISDLC-287 -> AISDLC-288..292
hierarchy. Preserve AISDLC-40's ownership of the broader attributable/bounded
agent-cost outcome and AISDLC-95's narrower governed-model-routing
contribution. Do not infer a new AISDLC-95 criterion from parentage alone.

Deliverable: current-state relationship table with capture date and source URL.

### 2. Resolve dependency semantics

Record explicit human decisions for:

* the milestone gated by the existing AISDLC-95 `depends on` AISDLC-5 link;
* whether any explicit AISDLC-40 criterion—not parentage alone—must be traced
  to AISDLC-95, while keeping the broader outcome owned by AISDLC-40;
* whether AISDLC-116/AISDLC-24 is the first pilot;
* the budget granularity AISDLC-95 consumes for a routing decision without
  owning cumulative-spend enforcement;
* who owns persona/subagent routing, which is not owned by AISDLC-144 today;
* whether a currently available Hummingbird integration can be evaluated,
  without treating AISDLC-15's discovery comments—whose exploratory
  recommendation still awaited architect review/approval—as approval;
* how AISDLC-95 consumes AISDLC-345 cost evidence without duplicating its
  evidence contract or AISDLC-282 enforcement;
* how ADR 0112 implementation is scheduled;
* whether any candidate automatic selector is required to select only models
  served through the implemented gateway route.

Deliverable: decision register containing decision, approver, date, rationale,
and affected Jira links/text.

### 3. Refine AISDLC-95 description

Start from the Jira-ready text in `research.md`, then update it only with the
approved decisions. Label architectural states precisely:

* ADR 0112: accepted, not yet implemented;
* PR 7808: rejected/closed alternative;
* learned/LLM routing: open proposal requiring a separate ADR;
* ADRs 0137 and 0138: accepted gateway route/security boundary with route code
  implemented for pi/Codex/Claude, not an automatic router; long-running OIDC
  readiness remains gated by the live per-runtime rotation proof named in ADR
  0137;
* Jira relationship statements: observed in the dated authenticated snapshot,
  with proposals and unresolved decisions labeled separately.
* AISDLC-40: live parent and owner of the broader attributable/bounded
  agent-cost outcome; AISDLC-95 contributes governed model routing and does
  not acquire new acceptance criteria from parentage alone.

Deliverable: proposed AISDLC-95 description diff for user review.

### 4. Propose relationship changes

Prepare an exact before/after list of Jira links. Default uncertain
cross-feature relationships to `relates to`; use `blocks` only when a specific
blocked milestone and exit condition have been approved.

Deliverable: link-change manifest listing source, target, old type, new type,
rationale, and approving decision.

### 5. Architecture and security review

Review the proposed refinement against:

* ADR 0112 precedence, trace, trust, and fallback rules;
* ADR 0137's fail-closed gateway route and prohibition on fallback to a
  direct-provider credential;
* ADR 0138's explicit OIDC-versus-API-key selection with no implicit fallback;
* the repository threat priority (external injection before insider, drift,
  then supply chain);
* the prohibition on describing author-shaped model selection as a security
  floor;
* the boundary between routing-time budget input and cumulative cost controls;
* provider approval and sensitive-context constraints in AISDLC-95;
* the requirement for a new ADR for learned/LLM routing.

Deliverable: review checklist with each finding resolved or explicitly deferred.

### 6. User approval, then Jira update

Present the complete description diff and link-change manifest to the user.
Only after explicit approval, update AISDLC-95 and its relationships. Re-read the
item afterward and compare it with the approved text.

Deliverable: verified Jira update record with item URLs and final link graph.

## Acceptance checks

* Every relationship states what it enables or blocks and at which milestone.
* The authenticated hierarchy is recorded as AISDLC-40 -> AISDLC-95 ->
  AISDLC-287 -> AISDLC-288..292.
* AISDLC-40 retains ownership of the broader attributable/bounded agent-cost
  outcome; AISDLC-95 is limited to its governed-model-routing contribution.
* No AISDLC-95 acceptance criterion is inferred from parentage alone.
* The hierarchy/status snapshot distinguishes AISDLC-287 workflow **In
  Progress** from legacy Epic Status **To Do**, and records all five child
  Spikes as **New** and unassigned.
* No related item is promoted to an implementation dependency without a human
  decision.
* AISDLC-95 consumes authoritative attributable run/work-item cost evidence
  from AISDLC-345 and does not duplicate AISDLC-282 cumulative-spend enforcement.
* The review pilot dependency chain is explicit if, and only if, that pilot is
  chosen.
* AISDLC-15 is described as discovery/interest and an exploratory integration
  recommendation pending architect review/approval, not gateway availability,
  approval, or automatic selection.
* Persona/subagent routing is explicitly included, deferred, or excluded
  without assigning it to AISDLC-144 by assumption.
* AISDLC-116/AISDLC-24 description prose is not represented as a live blocker;
  the observed link remains `Related` unless a user approves a change.
* ADR 0112 is described as accepted but unimplemented.
* ADRs 0137 and 0138 are described as the implemented credential/serving
  route code for a concrete gateway model, not as automatic model selection or
  proof that long-running OIDC refresh is production-ready.
* Every runtime selected for a production-adjacent OIDC pilot lasting over five
  minutes or beyond the observed token lifetime has live evidence for
  `iat`/`exp` scheduling,
  new-generation settle/re-seed, runtime re-read, same-route/auth continuity,
  and fail-closed expired/failed-refresh behavior. API-key mode is evaluated
  separately: upstream-key active/revoked state and contract-defined scope/
  expiry are distinct from the run-scoped OpenShell provider-credential
  lifetime; provider expiry/deletion does not revoke the key, and there is no
  re-seed loop.
* A gateway-based automatic-selection proposal preserves fail-closed routing
  across control-plane, credential, gateway-side classifier, gateway, and
  upstream failures and cannot silently switch to direct-provider credentials
  or another gateway auth mode.
* PR 7808 is described as rejected history, not current architecture.
* Any learned/LLM routing recommendation names the need for a new ADR.
* The user sees and approves all Jira mutations before they occur.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Jira content changed after the authenticated snapshot | Re-read all live fields before drafting a mutation |
| Parent scope is silently inherited by AISDLC-95 | Preserve AISDLC-40's broader outcome ownership; require explicit evidence for any AISDLC-95 criterion rather than deriving it from parentage |
| A `blocks` link overstates an evaluation relationship | Require blocked milestone and exit condition before adding it |
| Accepted ADR is mistaken for shipped code | Preserve the explicit "decided, not yet implemented" label |
| Implemented gateway route is mistaken for an automatic router | Separate task-aware selection from the concrete `gateway/<model>` credential/serving route |
| Automatic routing bypasses the gateway security boundary on failure | Preserve fail-closed behavior; prohibit direct-provider credential fallback and implicit auth-mode switching |
| Rejected PR concepts leak into baseline scope | Cite PR 7808 only as rejected history; require a new decision to revive any concept |
| Cost scope is duplicated | Consume attribution evidence from AISDLC-345; keep cumulative enforcement with AISDLC-282; define only AISDLC-95's routing decision semantics |
| Security is weakened by cost routing | Preserve pinned/security floors outside author-steerable model/effort selection; review against the threat model |

## Definition of done

AISDLC-292 is complete when the live relationship graph—including
AISDLC-40 -> AISDLC-95 -> AISDLC-287 -> AISDLC-288..292—is validated, every
listed ownership/dependency question is answered or explicitly deferred, the
AISDLC-95 description and link changes are user-approved, and the final Jira
state is verified against the approved change set. Research alone does not
authorize the Jira mutation.
