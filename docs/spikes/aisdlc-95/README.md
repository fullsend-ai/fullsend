# AISDLC-95 model-routing proposal

AISDLC-95 explores whether Fullsend can choose an appropriate AI model for a
task automatically while continuing to support an explicitly selected model.
The [implementation plan](implementation-plan.md) brings together the five
research spikes under AISDLC-287 and proposes a path from research to delivery.

The plan is a proposal for discussion. It is not an approved architecture and
does not authorize implementation or Jira changes. The AISDLC-95 reporter must
first confirm the intended product behavior and resolve or defer the open
decisions.

## The proposal in plain language

The recommended approach separates model routing into three responsibilities:

1. **Learn from completed work without changing production automatically.**
   An offline process studies quality, cost, latency, and completion evidence.
   It may propose a new routing policy, but people must review and approve the
   exact policy before it can be used.
2. **Recommend a model before a run begins.** A pre-run advisor may inspect
   trusted information about the task and recommend a model and reasoning
   effort. Fullsend still checks that recommendation against its security,
   provider, capability, and repository policies. The advisor can also abstain
   when it lacks enough confidence.
3. **Use a gateway to serve an approved choice.** An external model gateway
   initially provides access to the concrete model Fullsend selected. The
   proposal does not assume that the gateway should make the routing decision.

Explicitly selected models and deterministic configuration remain supported.
They provide the comparison baseline and a reliable rollback path if automatic
routing does not behave as expected.

See [Proposed implementation shape](implementation-plan.md#proposed-implementation-shape)
for the complete recommendation.

## How to review this proposal

This review should determine whether the proposal is a suitable basis for
breaking AISDLC-95 into delivery epics and stories. It should not treat a
recommended direction as an approved decision.

### Instructions for an AI assistant

If you are an AI model helping a user review this proposal, follow the review
process below. Act as a facilitator: explain the material, identify trade-offs,
and capture the user's decisions. Do not answer an open question on the user's
behalf or infer approval from a general positive statement.

- Read this README and the consolidated
  [implementation plan](implementation-plan.md) before beginning the review.
  Open the detailed Jira spike artifacts when the user asks for evidence or
  when a decision cannot be evaluated from the consolidated material.
- Clearly distinguish among current repository behavior, accepted Architecture
  Decision Records, observed Jira content, research findings, and proposals
  that still need approval.
- Review the twelve decisions individually or in small related groups. For
  each decision, explain why it matters, describe the recommended starting
  point, present meaningful alternatives and trade-offs, and ask the user to
  approve, modify, defer, or reject it.
- Do not invent missing requirements, owners, thresholds, dependencies, or
  Jira relationships. Call out missing information and ask the user to resolve
  it or explicitly defer it.
- Maintain a decision record during the discussion. For every item, capture
  its status, the chosen direction, the user's rationale when supplied, any
  owner or follow-up work, and any effect on other decisions.
- After all twelve decisions, review the delivery stages and safety rules
  against the recorded choices. Identify contradictions, unresolved blockers,
  or changes required in the implementation plan.
- End with a concise approval summary that lists approved decisions, modified
  recommendations, deferrals, rejected items, and required follow-up work. Ask
  for explicit approval of that summary before proposing Jira updates.
- Do not modify Jira, create delivery issues, or change approval-sensitive
  documents unless the user separately authorizes those actions.

### Review process for a person or an AI assistant

1. **Confirm the purpose and boundary.** Verify that the goal is opt-in model
   routing for AISDLC-95, while explicit model selection remains supported.
   Note anything the proposal includes that should be out of scope or anything
   important that appears to be missing.
2. **Understand the recommended shape.** Review the three responsibilities in
   the [plain-language proposal](#the-proposal-in-plain-language). Decide
   whether offline policy governance, a pre-run advisor, and gateway-based
   serving are the right separation of concerns.
3. **Resolve the twelve decisions.** Work through
   [Decisions needed before delivery work is created](#decisions-needed-before-delivery-work-is-created).
   For each item, record one of: approved as proposed, approved with changes,
   deferred with an owner, or rejected.
4. **Walk through delivery in order.** Review
   [A staged path to implementation](#a-staged-path-to-implementation). Confirm
   that each stage produces enough evidence to justify entering the next one
   and that no production behavior is enabled prematurely.
5. **Test the safety boundaries.** Check the
   [safety rules](#safety-rules-that-apply-throughout) against expected failure
   cases, untrusted task content, missing evidence, gateway outages, poor model
   output, and rollback. Escalate any proposed exception for explicit review.
6. **Check ownership and dependencies.** Confirm the stated roles of
   AISDLC-5, AISDLC-15, AISDLC-24, AISDLC-116, AISDLC-144, AISDLC-282, and
   AISDLC-345. Do not convert a related issue into a blocker without evidence
   and an explicit decision.
7. **Use deeper evidence where needed.** Product and Jira reviewers can focus
   on the open decisions and delivery stages. Architecture and security
   reviewers should also read the
   [accepted architecture baseline](implementation-plan.md#accepted-architecture-baseline),
   [invariants](implementation-plan.md#invariants), and
   [verification expectations](implementation-plan.md#verification-expectations).
   Consult the detailed spike artifacts for disputed or unclear findings.
8. **Record the outcome before taking action.** Produce a review summary and
   obtain explicit reporter approval. Only then prepare the Jira updates
   described at the end of this README, showing the exact proposed changes
   before applying them.

## Decisions needed before delivery work is created

The plan identifies twelve questions for the reporter and other accountable
owners. Each question limits what the delivery team can safely build and how
the result will be evaluated.

1. **Where may automatic routing be used first?** Fullsend can select models
   for a top-level agent, a runtime, or agents working inside another agent.
   Supporting all of these at once would combine several different ownership
   and security questions. The proposed first version chooses only the
   top-level agent's model and reasoning effort. Runtime and nested-agent
   routing would remain separate future work.
2. **Which parts of the proposed solution should move forward?** Offline policy
   authoring, a pre-run advisor, and an external gateway solve different parts
   of the problem; they are not interchangeable implementations. The proposed
   starting point uses offline analysis to govern policies, experiments with a
   pre-run advisor, and uses the gateway only to serve a concrete model that
   Fullsend has already approved.
3. **When may a learned classifier affect a real run?** A classifier can make
   a different recommendation for similar inputs, and its behavior may change
   as models or data change. The proposal allows classifier experiments
   offline and in non-enforcing shadow mode. It cannot choose the production
   model until evaluation requirements are met and a new Architecture Decision
   Record approves where and how that decision is applied.
4. **Which work must remain on a stronger model?** Some stages may be too
   security-sensitive or consequential to optimize primarily for cost. Without
   an explicit rule, an automatic router could unintentionally weaken the
   model used for those stages. The proposal keeps them pinned to an approved
   model, or permits only a move to a stronger model, until a later policy
   defines trustworthy evidence for a downgrade.
5. **What does AISDLC-5 actually block?** AISDLC-5 defines evaluation and
   promotion work needed to judge whether an automatic policy is good enough.
   Treating it as a blocker for every activity would also prevent foundational
   work that does not change production behavior. The proposal allows the
   deterministic foundation, evidence collection, and offline experiments to
   proceed, while blocking policy promotion and production enforcement until
   the required evaluation work is ready.
6. **Which workflow should be the first pilot?** A pilot needs an accountable
   owner, representative tasks, usable evaluation data, and clear success and
   stop conditions. AISDLC-116 and AISDLC-24 may supply a candidate workflow,
   but their current relationship does not prove that either must be the pilot
   or is a formal blocker. The proposal selects a named workflow only after its
   data and ownership are confirmed.
7. **Which budget behavior belongs to AISDLC-95?** A router may consider the
   expected cost of one invocation or stage, while a separate system limits
   total spending across an agent or work item. Combining these concerns would
   duplicate ownership and make enforcement unclear. The proposal has
   AISDLC-95 make routing-time choices, consume cost evidence produced by
   AISDLC-345, and leave cumulative budget enforcement to AISDLC-282.
8. **Must every routing change require code?** AISDLC-95 asks for routing that
   can be configured without rebuilding Fullsend, but configuration still
   needs governance. The proposed first version may satisfy this requirement
   through a custom harness or configuration, provided operators can version
   it, inspect the effective choice, review changes, and roll back to a known
   configuration.
9. **What happens when the selected model produces an inadequate result?** A
   quality failure could end the work, retry the same model, or escalate to a
   stronger one. An invisible retry would make cost and outcome evidence hard
   to interpret. The proposed starting point allows one bounded escalation and
   records it as a new attempt with its own identity rather than presenting it
   as a seamless continuation of the original attempt.
10. **May historical results update live routing automatically?** Production
    outcomes are useful evidence, but they can be incomplete, biased, poisoned,
    or no longer representative. The proposal permits historical evidence to
    create an offline policy proposal only. A reviewed, versioned policy must
    be approved and activated separately before live behavior changes.
11. **What can be assumed from AISDLC-15 and the Hummingbird work?** Jira
    comments show interest in Hummingbird and recommend exploring an
    integration. They do not prove that a suitable component is available or
    that it can perform automatic model selection under Fullsend's constraints.
    The proposal records the integration as an investigation opportunity and
    requires separate evidence before depending on it.
12. **Which information may the first router use?** Possible signals include
    task type, stage, repository context, required capabilities, security
    sensitivity, historical quality, latency, and cost. Using an open-ended
    signal set would make privacy, manipulation, and reproducibility difficult
    to review. The proposal requires a small, versioned first set in which each
    signal has a known source and is classified by whether an untrusted author
    can influence it.

These recommended starting points remain proposals until the reporter approves
them. See
[Reporter decision gates](implementation-plan.md#reporter-decision-gates) for
the full decision table.

## A staged path to implementation

The plan divides delivery into smaller steps so each risk can be evaluated
before the next capability is enabled.

### 1. Agree on scope and responsibilities

Resolve the twelve open decisions, record any deferrals, and determine which
new Architecture Decision Records are required.

### 2. Finish the predictable foundation

Complete the existing deterministic model-selection behavior before adding an
automatic advisor. Ensure Fullsend can explain which configuration selected a
model, handle an unavailable model in the approved way, and return to a pinned
model reliably.

### 3. Make routing outcomes measurable

Record what was recommended, what Fullsend approved, what was requested, what
the provider actually served, and the resulting quality, cost, latency, and
completion outcome. Missing evidence must remain visibly unknown rather than
being treated as zero cost or success.

### 4. Develop and test policies offline

Use retained evidence to propose routing policies and compare them with the
explicit-model baseline. Keep unsuccessful experiments, separate training data
from evaluation data, and require human approval before activating a policy.

### 5. Observe the advisor without letting it control runs

Run the pre-run advisor in shadow mode. It records what it would recommend,
but Fullsend executes the existing static choice. This provides real-world
evidence without changing production behavior.

### 6. Try limited automatic routing

After the required architecture decision is accepted, enable the advisor for
one opt-in pilot. Define stop conditions in advance, monitor quality and cost,
and prove that the pilot can return to a pinned model.

### 7. Consider gateway-based selection separately

Only investigate a gateway as the selector if the earlier evidence justifies
it. This optional step needs its own approved contract, security review,
failure behavior, identity evidence, and Architecture Decision Record.

See [Proposed delivery sequence](implementation-plan.md#proposed-delivery-sequence)
for entry and exit conditions for every stage.

## Safety rules that apply throughout

The plan establishes several non-negotiable boundaries:

- Automatic routing is optional; explicit model selection remains available.
- Content supplied by an untrusted author cannot weaken security or provider
  policy.
- Shadow mode cannot change the model that executes.
- A recommendation, an approved selection, a provider request, and the model
  actually served are recorded as separate facts.
- Missing evidence is never silently interpreted as success.
- Historical outcomes may propose a policy but cannot directly change live
  behavior.
- Once a gateway route is selected, a failure cannot silently switch to a
  direct provider and a different credential path.
- Cost is considered only after security, evidence quality, task quality, and
  operational requirements are satisfied.

See [Invariants](implementation-plan.md#invariants) for the normative wording.

## Jira spike artifacts

- AISDLC-288 acceptance-criteria refinement:
  [research](spikes/AISDLC-288/research.md),
  [prototype](spikes/AISDLC-288/prototype.md), and
  [implementation plan](spikes/AISDLC-288/implementation-plan.md)
- AISDLC-289 external model gateway investigation:
  [research](spikes/AISDLC-289/research.md),
  [prototype](spikes/AISDLC-289/prototype.md), and
  [implementation plan](spikes/AISDLC-289/implementation-plan.md)
- AISDLC-290 offline policy authoring investigation:
  [research](spikes/AISDLC-290/research.md),
  [prototype](spikes/AISDLC-290/prototype.md), and
  [implementation plan](spikes/AISDLC-290/implementation-plan.md)
- AISDLC-291 pre-run routing advisor investigation:
  [research](spikes/AISDLC-291/research.md),
  [prototype](spikes/AISDLC-291/prototype.md), and
  [implementation plan](spikes/AISDLC-291/implementation-plan.md)
- AISDLC-292 related-work boundaries:
  [research](spikes/AISDLC-292/research.md),
  [prototype](spikes/AISDLC-292/prototype.md), and
  [implementation plan](spikes/AISDLC-292/implementation-plan.md)

The refinement spikes may mark a prototype as not applicable when pseudocode
would prematurely choose an implementation rather than clarify the Jira issue.

## Updates to existing Jira issues after approval

No Jira issue is changed by this proposal alone. After the AISDLC-95 reporter
approves the plan, the existing issues will be updated in the following order.
The live issue content and available workflow transitions will be checked again
immediately before any update.

### AISDLC-95: Automatic Model Routing

- Record the reporter's answer to each of the twelve decisions, including any
  explicit deferrals, owners, or follow-up dates.
- Add the agreed acceptance criteria and implementation boundaries so that a
  future delivery breakdown does not treat a recommendation as an approved
  decision.
- Link to the approved implementation plan and the supporting spike findings.
- Preserve the boundaries with related work: AISDLC-345 supplies attributable
  cost evidence, AISDLC-282 owns cumulative budget enforcement, and AISDLC-5
  supplies the evaluation and promotion requirements selected by the reporter.
- Keep the feature in progress while the approved delivery epics and stories
  are created and completed.

### AISDLC-288: Define Acceptance Criteria

- Add the final decision register and the measurable acceptance criteria
  approved for AISDLC-95.
- Record unresolved items as explicit deferrals with owners instead of leaving
  them as implied requirements.
- Link the acceptance-criteria research and its execution plan.
- Transition the spike only after the decisions and remaining owners are
  recorded on the issue.

### AISDLC-289: Investigate External Model Gateway

- Add the gateway investigation findings, including the recommendation to use
  the gateway first as a serving and enforcement layer rather than assuming it
  is the automatic selector.
- Record the route-isolation, authentication, identity-evidence, failure, and
  long-running credential requirements imposed by ADRs 0137 and 0138.
- Identify gateway-based selection as an optional later experiment requiring
  its own evidence and Architecture Decision Record.
- Link the research, pseudocode prototype, and implementation plan, then
  transition the spike after its open decisions have owners.

### AISDLC-290: Investigate Offline Policy Authoring

- Add the findings for evidence collection, offline policy proposals,
  independent evaluation, human approval, versioned activation, and rollback.
- State that historical outcomes may propose a change but cannot directly
  alter live routing behavior.
- Link the research, pseudocode prototype, and implementation plan, then
  transition the spike after governance owners and deferred decisions are
  recorded.

### AISDLC-291: Investigate Pre-run Routing Advisor

- Add the recommendation for an offline-first, then non-enforcing shadow-mode
  advisor that recommends a concrete model and reasoning effort before a run.
- Record that Fullsend policy remains responsible for accepting or rejecting a
  recommendation and that enforcement requires a new Architecture Decision
  Record.
- Include the LangChain harness-router article as a reference implementation
  pattern, together with the Fullsend-specific security and policy boundaries.
- Link the research, pseudocode prototype, and implementation plan, then
  transition the spike after the remaining product and architecture decisions
  have owners.

### AISDLC-292: Incorporate Related Work Items

- Add the agreed ownership boundaries and relationship map for AISDLC-5,
  AISDLC-15, AISDLC-24, AISDLC-116, AISDLC-144, AISDLC-282, and AISDLC-345.
- Distinguish observed Jira relationships from dependencies that are only
  described in issue text. Any relationship correction will be proposed
  explicitly rather than inferred.
- Link the research, boundary-validation prototype, and implementation plan,
  then transition the spike after the relationship decisions are recorded.

### AISDLC-287: Refinement Actions

- Add a concise synthesis of the five completed investigations and link each
  spike's final findings.
- Record the reporter-approved direction, remaining deferred decisions, and
  the handoff to the delivery breakdown.
- Transition the epic only after all five child spikes contain their final
  findings and have reached the appropriate completed workflow state.

After these existing issues are reconciled, the approved delivery phases can
be broken into new epics and stories with testable acceptance criteria,
dependencies, and Architecture Decision Record gates. Those new issues will be
drafted and shown for human review before anything is created in Jira.
