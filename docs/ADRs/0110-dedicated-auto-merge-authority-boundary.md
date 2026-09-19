---
title: "110. Dedicated auto-merge stage with host-side authorization"
status: Accepted
relates_to:
  - agent-architecture
  - autonomy-spectrum
  - security-threat-model
topics:
  - auto-merge
  - policy
  - least-privilege
  - forge
---

# 110. Dedicated auto-merge stage with host-side authorization

Date: 2026-09-09

## Status

Accepted

## Context

Detailed behavior and implementation requirements are defined in the
[Auto-Merge Contract v1](../normative/auto-merge/v1/).

Fullsend currently has a legacy `CODE_AUTO_MERGE` path in the Code post-script,
while the product direction calls for a dedicated, opt-in auto-merge agent
([agents#1132](https://github.com/fullsend-ai/agents/issues/1132)). Keeping both
paths would create two Fullsend-owned ways to authorize autonomous merging and
would make repository policy and operational auditing ambiguous.

Review determines whether a change is acceptable; merge authorization must also
account for current repository policy, required checks, human intent, and the
exact pull-request revision. Combining those responsibilities makes it harder
to invalidate stale decisions and to keep merge authority least-privileged.

The sandbox boundary already keeps write credentials in host-side scripts
([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)). The auto-merge
stage should extend that boundary to the irreversible merge transition rather
than trusting an agent response or a prompt to enforce it.

### The core insight

The model is advisory. It answers only the non-deterministic question — does
this bounded patch match the stated intent, remain routine and coherent, have
meaningful tests, and avoid risks that require human judgment? It does not
establish authoritative forge facts, hold a merge-capable credential, or decide when
conditions are met. We do not need to trust the AI with merge authority because
the host-side gate re-verifies everything independently before mutation.

This separation means prompt injection in pull-request content, a compromised
model output, or an adversarial commit message cannot cause a merge. The model
can return `eligible`, but the trusted host code decides whether the forge
state actually supports that claim.

### Binding tuple

Every semantic decision applies to exactly one immutable tuple:

```text
(repository, pull_request_number, head_sha, base_ref, base_sha, policy_fingerprint)
```

Changing any member invalidates the decision. A model approval for one tuple
does not authorize another head, base, repository, pull request, or policy
version. The binding is established during deterministic preflight, copied into
the model's structured output, and verified again during postflight before
mutation.

### Why the agent should not click "merge when ready"

The most obvious simplification is to have the agent enable GitHub's native
auto-merge button instead of building a custom forge driver. This fails for
three reasons.

**It grants standing future authorization.** When the agent enables "merge when
ready," it says "merge this whenever GitHub's configured rules are satisfied."
But the agent's semantic judgment was about one specific revision at one
specific moment. Between enabling the button and GitHub actually merging, the
base branch can advance, branch protection settings can be weakened by an
admin, a required check can be renamed or removed, and an approval can become
stale without being dismissed (stale-approval dismissal is an optional GitHub
setting, not a default). The button delegates authority to a future point in
time with no guarantee the conditions that informed the judgment still hold.

**It untethers the decision from the reviewed revision.** The agent evaluated
one exact head SHA against one exact base. The button merges whatever head
meets GitHub's structural rules later — if a new commit lands on the PR between
the agent's evaluation and GitHub's merge, the button merges code the agent
never reviewed. The dedicated architecture binds the decision to an immutable
tuple; the button does not.

**It creates a second enablement path.** Enabling the button is a GitHub-native
auto-merge alongside the dedicated stage — two Fullsend-owned paths to
autonomous merge, which is exactly what the single-path invariant prohibits.
Worse, clicking the button requires a write credential that can enable standing
merge authority, which is strictly more powerful than a constrained one-shot
merge-at-this-exact-head operation.

Native forge auto-merge was designed for humans who made a conscious decision
and want convenience. The human is the semantic authority; the button is a
shortcut. In an agentic flow the model is not the authority — it is advisory.
The host must independently re-verify before mutation. The button skips the
host. Tools like Renovate and Dependabot use native auto-merge because their
changes are structurally predictable (version bumps with lockfile updates) and
their assessment is a constraint match, not a model judgment — and even then,
teams regularly get burned by auto-merged dependency updates that pass CI but
break production.

## Options

- **Merge from Review or Code.** Rejected: it couples judgment and mutation and
  makes the existing role's credential and lifecycle responsible for both.
- **Give the model merge authority.** Rejected: prompt-injected pull-request
  content or a compromised runtime could directly invoke an irreversible
  operation. The model's judgment is valuable for semantic assessment but
  insufficient for authorization — it cannot independently verify forge state,
  and its outputs are untrusted data until the host validates them.
- **Use a dedicated stage with a host-side gate.** Chosen: the stage assesses
  eligibility, while a trusted driver revalidates and requests the forge's
  normal merge or queue mechanism. The model sandbox receives no merge-capable
  credential; trusted host code owns the entire authority transition.

## Decision

Fullsend will implement autonomous merge as a separate `auto-merge` stage,
opt-in per repository and disabled by default. Its model output is a structured,
non-authoritative eligibility assessment; it never grants merge authority.

### Authority boundary

The implementation splits responsibility across three trust zones:

1. A **trusted runner pre-script** reads live GitHub state, applies deterministic
   policy, and assembles bounded, secret-free evidence for the model.
2. A **credential-free model sandbox** evaluates the semantic evidence and
   returns a structured result (`eligible`, `ineligible`, or `needs_human`;
   see the [contract](../normative/auto-merge/v1/) for the full decision
   vocabulary). The sandbox receives no `GH_TOKEN`, no merge-capable credential,
   and no network authority
   to mutate the pull request.
3. A **trusted runner post-script** records the decision, re-fetches every
   mutable fact from the forge, and may merge only the exact approved head
   using the forge's compare-and-swap merge API.

The sandbox boundary is enforced by the harness configuration: `GH_TOKEN` and
GitHub provider profiles are present in `env.runner` but deliberately omitted
from `env.sandbox`. The model cannot query or mutate GitHub.

### Deterministic preflight

Before any model invocation, the trusted pre-script must establish all of the
following from authoritative forge state:

- The target is an open, non-draft pull request in the configured repository.
- The current head SHA and base ref/SHA are recorded from fresh forge state.
  The base SHA comes from the live base branch, not only the pull-request
  payload.
- The author and changed paths fit the configured low-risk cohort.
- No hold label, changes-requested review, unresolved review blocker, or
  policy-denied path is present.
- Required checks for the exact head SHA have completed successfully.
- Required review approval applies to the exact head SHA.
- Mergeability is known and the pull request is not conflicted.
- The repository's native standing auto-merge feature is not enabled for this
  pull request.

An ineligible result stops before model invocation, avoiding inference cost.
Unknown, missing, or API-error states fail closed.

### Write-ahead receipt and postflight

Before any mutation, a durable pending receipt containing the authorization
snapshot, binding tuple, context fingerprint, request identity, and idempotency key
must be persisted.
If the host crashes after the forge accepts the request but before completion is
recorded, startup reconciliation must complete the pending receipt by inspecting
current forge state.

After recording the receipt, the trusted post-script obtains fresh forge state
and repeats every mutable gate. It must verify exact-head equality, base-branch
and base-SHA equality, current reviews, required checks, mergeability, holds,
policy, and cohort membership. Any mismatch produces a stale-decision rejection
and no mutation.

### Exact-head merge

The mutation uses the forge's compare-and-swap equivalent, supplying the
expected head SHA. If the forge reports that the head changed between postflight
and mutation, the controller records a stale-decision rejection and waits for a
new reconciliation event. It does not enable native auto-merge or leave behind
standing authority.

### Single enablement path

The dedicated stage is the sole Fullsend-owned path for autonomous merge. The
`CODE_AUTO_MERGE` and `CODE_AUTO_MERGE_METHOD` variables and their Code
post-script implementation will be removed from the agents repository,
including generated bundles, forge helpers, tests, and user documentation. They
will not be aliased or migrated as a compatibility fallback. Once the removal
lands, existing values will have no effect; repositories that want autonomous
merging must opt in to the dedicated stage.

Forge-native or third-party automation, such as
Renovate's own `automerge` setting, is outside this Fullsend-owned stage
contract and requires separate policy ownership and audit.

The implementation removal is tracked in [agents#1219](https://github.com/fullsend-ai/agents/pull/1219).

## Lab validation

A private integration lab (`ascerra/auto-merge`) proved the complete path from
issue through triage, coding, review/fix, CI, semantic eligibility evaluation,
and exact-head merge. The lab exercised Fullsend in per-repository mode with
workload identity scoped to exactly the lab repository.

### What the lab proved

- **Authority boundary works.** The model sandbox received no merge-capable credential.
  The harness configuration (`env.sandbox`) omitted `GH_TOKEN` and GitHub
  provider profiles. The model could not query or mutate GitHub.
- **Binding tuple verification chain works.** The preflight established the
  binding tuple, the model copied it into its structured output, and the
  postflight re-verified every member before mutation. A stale or tampered
  binding produced a rejection.
- **Fail-closed behavior works.** The first Auto-Merge run returned `needs_human`
  because the model lacked the actual pull-request patch (the checkout was at
  the base revision). Insufficient evidence failed closed — no merge occurred.
- **Exact-head compare-and-swap works.** The successful merge used GitHub's
  merge API with the expected head SHA. A head change between postflight and
  mutation would have been rejected by the forge itself.
- **27 adversarial unit tests passed.** Cases included stale approval, pending
  and failed checks, hold labels, disallowed paths, missing and oversized
  patches, unknown mergeability, stale base, stale merge preview, native
  auto-merge enabled, unsigned commits, unresolved threads, changes-requested
  reviews, stale model bindings, invalid decisions, eligible with risk signals,
  receipt ordering, tampered repository, and tampered PR number.

### Lab discoveries that strengthened the design

- **Pull-request base metadata can be stale.** The pull-request payload and
  merge preview can remain based on an older base revision even while the UI
  describes the PR as clean. The controller must fetch the live base SHA from
  the branch and verify the synthetic merge preview's parent order
  `[live_base_sha, head_sha]`, not trust only the base SHA embedded in the
  pull-request payload.
- **Patch evidence must be explicit.** A trusted base checkout does not contain
  the pull-request patch. The semantic model needs bounded, API-sourced change
  evidence. Missing or oversized patches became deterministic failures.
- **GitHub's merge API provides head-SHA conditionality but not base-SHA
  atomicity.** The forge's compare-and-swap covers the head but not the base.
  Production needs a per-PR lease so final base validation and mutation are
  serialized.
- **Require() accumulation pattern.** The deterministic gate implementation
  appends every failing condition to a list rather than short-circuiting. This
  ensures the receipt captures all reasons for ineligibility, not just the
  first, making debugging and auditing substantially easier.
- **Two receipts are correct.** The write-ahead receipt (before mutation) and
  the outcome receipt (after attempt) represent different events and must
  remain distinct. The outcome receipt can be compact and reference the first.

### Known production hardening gaps

The lab proved the core controller and authority boundary. These gaps remain for
production:

- **Purpose-built merge identity.** The lab used the coder role; production
  needs a dedicated least-privilege identity whose only privileged
  responsibility is the authorized merge transition.
- **Per-PR lease.** GitHub does not provide atomic base-SHA conditionality;
  a serializing lease is needed to close the TOCTOU window between base
  verification and mutation. Required before `automatic` mode; without it,
  concurrent runs could both pass postflight and issue duplicate requests.
- **Idempotent receipt store.** GitHub comments were adequate for the lab;
  production needs an idempotent store with explicit forge-state reconciliation
  after ambiguous timeouts.
- **Branch protection validation.** The lab could not enable branch protection
  under a personal-account plan. Production must use branch protection or
  rulesets as defense in depth.
- **DCO and autonomous commit policy alignment.** Repository DCO requirements
  and Fullsend's autonomous commit policy must agree so changes can reach
  eligibility without manual history repair.
- **Review attestation integration.** The contract specifies
  `ReviewAttestation` but the lab used forge-native review state directly.
  Production must integrate structured Review evidence.
- **Cross-forge generalization.** Version 1 is GitHub-first. GitLab and other
  forges require compatible safeguards.

## Consequences

- Review and merge can evolve and be evaluated independently, with stale-head
  and human-intent changes invalidating a decision at the final gate.
- The model's semantic judgment is valuable but never sufficient: the host-side
  gate independently re-verifies every authoritative fact before mutation.
  Prompt injection, adversarial content, or a compromised model output cannot
  cause a merge.
- Every authority transition is bound to one exact tuple and recorded in a
  durable write-ahead receipt before mutation, making the system auditable and
  crash-recoverable.
- The first implementation needs a host-side policy/forge driver, structured
  review attestation, and integration coverage for direct merges and merge
  queues.
- Repositories retain branch protection and queue enforcement as the final forge
  boundary; Fullsend cannot override failed requirements.
- Observe-only and explicit human-trigger modes can be deployed before automatic
  mutation, while unknown state waits for a human.
- The legacy Code auto-merge path is intentionally removed, so existing
  `CODE_AUTO_MERGE*` configuration must be replaced by dedicated-stage policy;
  this avoids split-brain enablement and makes the migration auditable.
- The lab validated the authority boundary, binding tuple verification, and
  fail-closed behavior across 27 adversarial test cases; remaining work is
  production hardening, not architectural redesign.
