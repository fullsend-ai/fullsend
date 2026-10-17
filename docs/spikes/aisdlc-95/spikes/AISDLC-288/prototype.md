# AISDLC-288 prototype

## Result: not applicable

AISDLC-288 refines acceptance criteria; it does not investigate a specific
implementation. A pseudocode router here would prematurely choose among the
external gateway, pre-run advisor, and offline deterministic-policy options
owned by AISDLC-289, AISDLC-291, and AISDLC-290.

The closest useful "prototype" is therefore the parameterized acceptance
contract in `research.md`:

- explicit criteria E1-E13 capture already stated requirements;
- implied candidates I1-I8 are labeled as unapproved implications;
- decisions D1-D12 expose every human choice; and
- the decision-ready templates contain bracketed values that cannot be filled
  until those choices are validated.

This avoids encoding rejected PR 7808 concepts—such as a second `routing:`
language or mandatory `shadow` mode—as if they were accepted architecture.
It also avoids fixing timing: a gateway may choose at request time, an advisor
may choose before sandbox creation, and an offline authoring system may produce
the production policy earlier. ADR 0112 is required only when overlays are
part of the approved composition, not as a presumed universal integration seam.

## Contract-validation walkthrough

The refinement result can be mechanically reviewed without pretending it is
runtime code:

```text
for each explicit AISDLC-95 criterion:
    require a local ID, observable evidence, and at least one pass/fail test

for each implied criterion:
    require source rationale
    require an owning human decision or an explicit deferral
    forbid marking it accepted before that decision

for each human decision:
    enumerate mutually distinct options
    identify affected acceptance criteria and architecture boundaries
    require the approved choice and approver/date before replacing placeholders

for each related-work claim:
    record observed Jira link type separately from description prose
    preserve AISDLC-40 as the live parent outcome boundary of AISDLC-95
    state that AISDLC-95 contributes governed model routing toward that outcome
        without claiming ownership of the whole AISDLC-40 outcome
    forbid deriving a new AISDLC-95 acceptance criterion from parentage alone
    require AISDLC-345 evidence for attributable routing cost
    leave cumulative per-agent/per-work-item enforcement to AISDLC-282
    treat AISDLC-144 as configuration/precedence context only
    preserve AISDLC-15 comments as consumer-interest and exploratory-integration
        evidence with architect approval pending
    forbid using that evidence to claim an available gateway, automatic-selector
        capability, or approved AISDLC-95 architecture
    forbid converting AISDLC-116's prose about AISDLC-24 into a blocker link

for each candidate placement:
    require explicit timing and enforcement ownership
    require explicit composition with ADR 0091, ADR 0112 when applicable,
        and ADR 0137 when applicable
    forbid treating a recorded stochastic output as deterministic replay
    if an external-gateway selector is eligible:
        require explicit ownership for alias-to-concrete-route resolution
        require every policy/candidate/control-plane path to use the configured
            gateway origin and the block's fixed ADR 0138 auth mode
        require runner-host credential acquisition and retention
        if auth mode is oidc:
            require JWKS-backed JWT signature, issuer, purpose-specific audience,
                token iat/exp, required claims, and exact repository claim
        if auth mode is api-key:
            securely validate opaque key correctness and active/revoked state
            require exact repository authorization
            require configured model/path scopes only when key contract defines them
            require expiry only when key contract defines it
            never require JWT signature, issuer, audience, or claims
            distinguish upstream gateway-key revocation/contract expiry from
                run-scoped OpenShell provider-credential expiry/deletion;
                provider expiry/deletion does not revoke the gateway key
        require caller credential headers stripped before every upstream and
            never reflected or logged
        require malformed, denied, timed-out, and unreachable calls to fail closed
        require control-plane, credential, classifier, gateway, and upstream
            failures to stay on the same gateway block/auth mode or fail closed,
            never preparing a direct-provider route or secret
        require a selector ADR for each new endpoint, host transport, egress
            rule, and configuration surface
        require review of concentrated prompt/completion, repository-policy,
            and provider-key risk
        test oidc separately:
            missing/bad JWT signature, issuer, audience, iat/exp, required
                claims, and repository claim all fail closed
        test api-key separately:
            missing, incorrect, revoked, wrong-repository, and contract-defined
                wrong model/path scope all fail closed
            expired fails closed only when that gateway key contract defines expiry

before AISDLC-95 acceptance criteria are finalized:
    require all placeholders resolved or explicitly deferred with owner
    require the authenticated 2026-10-12 Jira snapshot to be cited
    require the complete authenticated hierarchy:
        AISDLC-40 -> AISDLC-95 -> AISDLC-287 -> AISDLC-288..292
    re-read Jira immediately before mutation to detect later drift
    require current-main implementation check for ADR 0112
    require security review against ADR 0112 and ADR 0137
```

This is process pseudocode only. It is not a proposed production design.

## Refinement record shape

```text
JiraFact {
    observed_at: 2026-10-12
    issue: key, type, workflow_status, parent, assignee
    relationship: optional { type, direction, other_issue }
    source: authenticated Jira read
}

ScopeBoundary {
    source_issue: Jira key
    source_text: quoted or tightly paraphrased description scope
    acceptance_effect: proposed effect on AISDLC-95
    state: live_fact | proposal | unresolved_human_decision
}
```

The contract keeps a live link, a source description, and a proposed acceptance
effect as different fields. This prevents a `Related` link from becoming a
dependency, prevents description prose from becoming a Jira blocker, and
prevents a neighboring work item's scope from becoming accepted AISDLC-95
behavior without review.
