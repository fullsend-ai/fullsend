# AISDLC-288 refinement execution plan

Status: proposed; requires reporter decisions and approval

## Objective

Produce an approved, measurable AISDLC-95 acceptance-criteria section without
choosing an architecture by implication or treating accepted-but-unimplemented
ADR work as shipped code.

## Phase 0: preserve and revalidate authoritative inputs

1. Preserve the authenticated 2026-10-12 snapshot recorded in `research.md`:
   AISDLC-40 is the live parent of AISDLC-95; AISDLC-95 is an In Progress
   Feature (start 2026-10-07); AISDLC-287 is its In Progress child Epic even
   though legacy `Epic Status` says To Do; and AISDLC-288 through AISDLC-292
   are New, unassigned child Spikes. Preserve the complete hierarchy as
   `AISDLC-40 -> AISDLC-95 -> AISDLC-287 -> AISDLC-288..292`.
2. Preserve the observed AISDLC-95 links exactly: `depends on` AISDLC-5 and
   `Related` to AISDLC-15, AISDLC-116, AISDLC-282, and AISDLC-345. Do not create
   a link to AISDLC-144 by implication.
3. Keep AISDLC-116's description claim (“blocked by AISDLC-24”) separate from
   the actual Jira relationship, which is `Related`. Do not encode a blocker
   unless humans intentionally change the Jira relationship.
4. Preserve AISDLC-15's comment evidence accurately: Hummingbird expressed
   interest in standardizing on an ADLC model gateway and discovery material
   recommended exploratory component integration. Record that architect
   review/approval was still requested. Do not convert this evidence into a
   claim that a gateway or automatic selector is available, capable, or the
   approved AISDLC-95 architecture.
5. Preserve AISDLC-40 as AISDLC-95's parent outcome boundary. State the
   narrower boundary explicitly: AISDLC-95 contributes governed model routing
   toward AISDLC-40, but does not own the whole parent outcome. Do not derive
   any new AISDLC-95 acceptance criterion from parentage alone.
6. Immediately before any later Jira mutation, re-read the involved issues and
   diff them against the dated snapshot. Re-review drift rather than silently
   merging it.
7. Re-check Fullsend `main` for ADR 0112 implementation status before planning
   code. The baseline used here is commit `e37e3a0`.

Exit: dated live facts, description evidence, proposals, and unresolved human
decisions remain explicitly distinguishable.

## Phase 1: decision workshop

The product owner, Fullsend architect, security reviewer, evaluation owner, and
FinOps/cost owner review D1-D12 in order:

1. **Scope and architecture:** D1, D2, D3.
2. **Security:** D4 and the complete ADR 0137 route/operator boundary,
   including selector control-plane authentication, exact repository
   authorization, credential handling, route isolation, and concentrated
   gateway risk.
3. **Evidence and pilot:** D5, D6.
4. **Cost and operator contract:** D7, D8.
5. **Lifecycle:** D9, D10.
6. **Evidence gaps and data contract:** D11, D12, plus the AISDLC-345 evidence
   contract and AISDLC-282 enforcement boundary in D7.

For each decision, record option, rationale, approver, date, deferred work, and
whether a new Fullsend ADR is required. Do not use meeting silence as approval.

Exit: every D1-D12 row is approved or explicitly deferred with an owner and
target work item.

## Phase 2: write final AISDLC-95 criteria

1. Preserve E1-E13 unless a current authenticated Jira re-read shows an
   intentional change.
2. Replace the bracketed tokens in the decision-ready templates with approved
   values.
3. Move only validated implied items I1-I8 into acceptance criteria; keep
   rejected/deferred items in an explicit out-of-scope/deferred section.
4. Add a relationship table defining exactly what each dependency blocks.
   Preserve Jira's actual link type/direction; description prose is a separate
   source and cannot silently change the graph.
5. Add a glossary for:
   - pinned selection;
   - recommendation versus accepted selection versus actual served model;
   - routing unit;
   - fallback versus escalation;
   - quality equivalence/non-inferiority;
   - budget unit; and
   - policy/advisor version.
6. Link any required new ADR. Do not amend ADR 0112's substantive decision in
   place.
7. Describe the approved placement's timing without assuming a pre-sandbox
   decision: request-time gateway, pre-run advisor, and offline-authored policy
   are distinct contracts.

Exit: no ambiguous term, implicit architecture, or unresolved placeholder
remains in the proposed AISDLC-95 text.

## Phase 3: turn criteria into evidence cases

Create a traceability matrix from each final criterion to:

- configuration/schema validation;
- deterministic unit/behavior tests;
- security negative tests;
- fault injection for unavailable model/advisor/gateway and malformed output;
- repeated golden-path evaluation;
- policy promotion rejection tests;
- telemetry/decision-record completeness queries;
- reconciliation against AISDLC-345 authoritative cost evidence, including
  explicit unknown/incomplete cost rather than zero;
- separation tests proving AISDLC-95 does not implement AISDLC-282's cumulative
  per-agent/per-work-item enforcement;
- pinned rollback exercise; and
- operational dashboard/pilot evidence.

The minimum security cases are:

1. author-shaped facts cannot weaken an approved security floor;
2. unapproved provider/model is rejected before context transmission;
3. missing/unknown input fails to pinned behavior or an approved escalation;
4. after a gateway alias wins or selector control begins, control-plane,
   credential, gateway, classifier, and upstream failures stay on the same
   configured gateway block and fixed auth mode or fail closed; they cannot
   prepare or fall back to direct credentials;
5. every selector endpoint uses the configured origin and a host-held ADR 0138
   credential in its fixed mode without substitution; exact repository
   authorization, caller-header stripping before every upstream, no reflection/
   logging, and malformed/denied/timeout/unreachable fail-closed behavior are
   shared across modes;
6. OIDC conformance validates JWKS-backed JWT signature, issuer, purpose-
   specific audience, token `iat`/`exp`, required claims, and exact repository
   claim; negative cases cover every field plus missing credentials;
7. API-key conformance securely validates the opaque key's correctness, active/
   revoked state, exact repository authorization, configured model/path scopes
   when defined, and expiry only when the gateway key contract defines it;
   negatives cover missing, incorrect, revoked, wrong repository/scope, and
   contract-defined expiry without inventing JWT properties;
8. upstream gateway API-key revocation/contract expiry remains distinct from
   the run-scoped OpenShell provider credential lifetime: provider expiration
   or deletion limits sandbox use but does not revoke the gateway key;
9. every new policy/manifest endpoint, host transport, egress rule, and config
   surface is owned by the selector ADR, whose threat analysis covers the
   gateway's concentrated prompt/completion/repository-policy/provider-key risk;
10. one escalation cannot loop past its approved bound; and
11. cost improvement cannot promote a quality-inferior policy.

Exit: every criterion has observable pass/fail evidence.

## Phase 4: architecture and dependency gate

1. AISDLC-289/290/291 evaluate their candidate placements against the finalized
   solution-neutral criteria rather than redefining them.
2. If D2/D3 chooses learned/LLM live routing, create a separate Fullsend ADR
   covering placement and decision timing, trust boundary, precedence,
   abstention, fallback, telemetry, and explicit interaction with ADRs 0091,
   0112 when overlays participate, and 0137 when a gateway route participates.
3. If the approved composition uses overlays, plan the unimplemented ADR 0112
   seam or explicitly declare it a prerequisite. Do not impose that seam on a
   gateway-only or offline-policy option without a separate approved reason.
4. Confirm what milestone the live AISDLC-5 dependency blocks in D5. If D6
   selects the review pilot, separately decide whether AISDLC-24 is a required
   evidence gate; the current 116↔24 Jira link is `Related`, not a blocker.
5. Treat AISDLC-144 as configuration/precedence context only. Assign runtime or
   persona/subagent routing to explicit new work if D1 brings it into scope.

Exit: architecture work has an accepted decision record and executable
dependency order.

## Phase 5: Jira update and closeout

Only after human approval:

1. Attach or paste the approved decision register and criteria into AISDLC-95.
2. Add exact issue links and relationship meanings; do not rely on prose-only
   references.
3. Add the concise proposed result from `research.md` to AISDLC-288 with links
   to durable artifacts.
4. Mark AISDLC-288 complete only when all questions are resolved/deferred and
   AISDLC-95 contains the approved measurable text.

No Jira action is authorized by this plan itself.

## Review checklist

- [ ] Fresh Jira snapshot reconciled.
- [ ] Complete authenticated hierarchy is preserved exactly:
      `AISDLC-40 -> AISDLC-95 -> AISDLC-287 -> AISDLC-288..292`.
- [ ] AISDLC-40 remains the parent outcome boundary; AISDLC-95's narrower
      governed-model-routing contribution is stated without claiming the whole
      outcome or inferring new acceptance criteria from parentage.
- [ ] Live workflow status is not conflated with legacy `Epic Status`.
- [ ] Live Jira link type/direction is separate from description prose.
- [ ] AISDLC-345 is the authoritative cost-evidence source; no conflicting total.
- [ ] AISDLC-282 retains cumulative per-agent/per-work-item enforcement.
- [ ] AISDLC-144 is configuration/precedence context, not persona-routing ownership.
- [ ] AISDLC-15 comments are treated as verified consumer-interest and
      exploratory-integration evidence with architect approval still pending,
      not proof of an available gateway or automatic-selector capability.
- [ ] AISDLC-116/AISDLC-24 is not called a live blocker relationship.
- [ ] E1-E13 preserved or changes approved.
- [ ] D1-D12 each approved or explicitly deferred.
- [ ] I1-I8 not represented as accepted without validation.
- [ ] ADR 0112 accepted-versus-implemented distinction remains accurate.
- [ ] PR 7808 concepts labeled rejected/historical.
- [ ] Runtime and persona scope agrees with ADRs 0091/0104/0126.
- [ ] Gateway criteria cover explicit route ownership after alias selection,
      every selector/control-plane endpoint, runner-host credentials, fixed
      auth mode, exact-repository authz, header stripping/no reflection or
      logging, malformed/denied/timeout/unreachable fail-closed behavior, and
      no direct/auth escape across control-plane, credential, classifier,
      gateway, or upstream failures.
- [ ] OIDC conformance covers JWKS signature, issuer, purpose-specific audience,
      `iat`/`exp`, required claims, and exact repository claim; API-key
      conformance separately covers secure correctness, active/revoked state,
      exact repository authorization, contract-defined model/path scopes, and
      expiry only when the key contract defines it—never JWT properties.
- [ ] Gateway API-key revocation/contract expiry is distinct from OpenShell
      provider-credential lifetime; provider expiry/deletion is not described
      as revoking the upstream key.
- [ ] New selector endpoint/transport/egress/config surfaces are assigned to a
      new ADR, and concentrated gateway risk is explicitly reviewed.
- [ ] Placement and timing are explicit but not assumed before D2 is approved.
- [ ] Audit reconstruction is not mislabeled as deterministic classifier replay.
- [ ] Security threat order applied: external injection, insider, drift,
      supply chain.
- [ ] Per-repository scope only; no deprecated per-org extension.
- [ ] Every final criterion has objective evidence.
- [ ] No external write before final human approval.
