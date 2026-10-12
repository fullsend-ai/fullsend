# AISDLC-292 prototype

## Implementation prototype status

No executable or model-routing pseudocode prototype is appropriate for this
spike. AISDLC-292 is a work-item ownership and dependency refinement, not an
investigation of a specific routing implementation. Producing a gateway,
advisor, or policy-authoring prototype here would pre-decide the sibling spikes
and violate the requirement not to assume a solution.

The useful prototype is instead a validation workflow that prevents Jira text
or relationships from asserting unsupported ownership.

## Boundary-validation pseudocode

```text
INPUTS:
  live AISDLC-40, AISDLC-95, and linked Jira items
  accepted Fullsend ADRs
  current Fullsend architecture docs
  merged/rejected PR evidence
  proposed AISDLC-95 description and link changes

ASSERT observed Jira baseline (captured 2026-10-12):
  AISDLC-40  = parent of 95; owns broader attributable/bounded agent-cost outcome
  AISDLC-95  = child Feature of 40, In Progress, start 2026-10-07
  AISDLC-287 = child Epic of 95, workflow In Progress, Epic Status To Do
  AISDLC-288..292 = child Spikes of 287, New, unassigned
  95 depends on 5
  95 Related: 15, 116, 282, 345
  116 Related: 24  # description prose says blocked; link does not
  144 = context only; no observed live 95 link

ASSERT hierarchy boundary:
  hierarchy = 40 -> 95 -> 287 -> 288..292
  95 contributes governed model routing to 40's broader outcome
  95 does not own the entire 40 outcome
  parentage alone creates no new 95 acceptance criterion

FOR each proposed boundary or dependency:
  identify owning source
  classify source as one of:
    CURRENT_CODE
    ACCEPTED_DECISION_NOT_IMPLEMENTED
    REJECTED_ALTERNATIVE
    OPEN_PROBLEM
    JIRA_SCOPE_STATEMENT
    PROPOSAL

  IF the source is unavailable or stale:
    mark NEEDS_FRESH_VALIDATION
    do not create or strengthen a Jira relationship

  distinguish ISSUE_DESCRIPTION_TEXT from LIVE_ISSUE_LINK
  never translate prose such as "blocked by" into a Blocks link

  IF proposal duplicates another work item's scope:
    replace duplicated requirement with an explicit ownership boundary

  IF proposal derives a criterion only from parentage:
    reject the derived criterion
    require explicit description or approved decision evidence

  IF proposal depends on a possible pilot or architecture choice:
    use RELATES_TO
    do not use BLOCKS until the choice is approved

  IF proposal describes an accepted but unimplemented ADR as live behavior:
    correct wording to "decided, not yet implemented"
    add prerequisite/companion-delivery question

  IF proposal reintroduces a rejected architecture:
    require an explicit new decision/ADR rather than treating it as baseline

  IF proposal selects a concrete gateway/ model:
    preserve ADR 0137 fail-closed routing
    never fall back to a direct-provider credential after control-plane,
      credential, gateway-side classifier, gateway, or upstream failure
    preserve ADR 0138's explicit oidc OR api-key choice
    never infer or silently switch credential mode
    record route-code status separately from live route-readiness evidence
    assert pi/Codex/Claude route wiring does not prove long-running OIDC refresh
    for each OIDC pilot runtime lasting over five minutes or beyond observed
      token lifetime:
      require production-adjacent proof that scheduling uses token iat/exp
      require new placeholder generation settle, re-seed, runtime re-read,
        and request continuity on the same gateway route and auth mode
      require failed refresh/re-seed and expired token to fail closed without
        direct-provider or api-key escape
    for api-key mode:
      require upstream gateway key active/not revoked and contract-defined
        scope/expiry, if any
      separately require run-scoped OpenShell provider-credential lifetime
      require provider expiry/deletion not to revoke the gateway key
      do not require OIDC re-seed

  IF proposal calls the implemented inference gateway an automatic router:
    reject the claim
    separate task-aware model selection from credential/serving route

OUTPUT:
  Jira-ready relationship section
  reviewed link-change list
  unresolved decision register
  source ledger for every claim
```

## Ownership contract

```text
AISDLC-40 owns: broader attributable/bounded agent-cost outcome
AISDLC-95 owns: governed model-routing contribution, routing-time attribution,
                quality equivalence, pinned rollback; not the entire AISDLC-40
                outcome, and no extra criterion follows from parentage alone
AISDLC-5 owns:  evaluation method/evidence discipline
AISDLC-116 owns: review-cost use case
AISDLC-24 owns: review-agent evaluation-suite context; its live link to
                AISDLC-116 is Related, not Blocks
AISDLC-15 owns: Hummingbird discovery/interest and an exploratory integration
                recommendation pending architect review/approval, not an
                approved gateway or selector
AISDLC-345 owns: authoritative attributable run/work-item cost evidence
                 consumed by AISDLC-95
AISDLC-282 owns: cumulative spend enforcement across repeated work
AISDLC-144 owns: layered configuration/precedence context
UNRESOLVED owns: persona/subagent routing until a human assigns it
ADR 0112 owns: deterministic harness-overlay routing decision
ADR 0137 owns: concrete gateway model's credential/serving route and
               fail-closed boundary; route code exists for pi/Codex/Claude,
               while its named live OIDC rotation proof remains outstanding
ADR 0138 owns: explicit gateway OIDC vs API-key mode, with no implicit fallback
new ADR owns:   any accepted LLM/learned automatic-routing architecture
```

This contract combines authenticated Jira facts captured 2026-10-12 with
explicitly labeled unresolved/proposed boundaries. It is a review aid, not a
Jira mutation or an architecture decision.
