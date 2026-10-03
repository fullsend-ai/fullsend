---
title: "110. Dedicated auto-merge stage with trusted merge control"
status: Accepted
relates_to:
  - autonomy-spectrum
topics:
  - auto-merge
  - policy
  - least-privilege
  - forge
---

# 110. Dedicated auto-merge stage with trusted merge control

Date: 2026-09-09

## Status

Accepted

## Decision summary

Fullsend will provide one target authority boundary for autonomous merging: a
dedicated Auto-Merge stage, separate from Code and Review. The stage combines
an Auto-Merge agent with trusted Fullsend runtime code. The agent recommends
whether a pull request is ready. The trusted runtime—code outside the agent
sandbox that holds only the credential needed to request the native SCM
operation—then checks the latest GitHub state. If the change is still allowed,
that runtime sends it through the repository's configured merge path. The
legacy `CODE_AUTO_MERGE` path remains during migration and is planned for
retirement; it is not the target long-term authority boundary.

## Context

Fullsend has a legacy `CODE_AUTO_MERGE` path in the Code post-script, while the
product direction calls for a dedicated, opt-in auto-merge agent
([agents#1132](https://github.com/fullsend-ai/agents/issues/1132)). Keeping both
would create two Fullsend-owned ways to authorize autonomous merging and make
policy, revocation, and auditing ambiguous. Related Fullsend policy and evidence
work is tracked in [fullsend#3016](https://github.com/fullsend-ai/fullsend/issues/3016)
and [fullsend#6892](https://github.com/fullsend-ai/fullsend/issues/6892).

An agent can recommend that a pull request be merged, but it cannot authorize
the merge itself. Before acting, the trusted Fullsend runtime must confirm that
required checks and reviews still pass, no human has blocked the change, and the
evaluated revision is the one the runtime authorizes onto the repository's
merge path. For a merge-queue repository, GitHub may generate a different
queue revision; deterministic SCM-state checks are repeated against that
revision, while semantic reauthorization of it remains follow-up work. This
keeps merge credentials and final enforcement outside the agent sandbox, consistent with
[ADR 0017](0017-credential-isolation-for-sandboxed-agents.md).

The initial deployment targets, `fullsend-ai/fullsend` and
`fullsend-ai/agents`, use GitHub merge queues. After considering the agent's
assessment, the trusted Fullsend runtime decides whether to authorize the pull
request. If authorized, that runtime must use the repository's configured merge
path: enter the merge queue when one is required, or merge directly only when
repository policy permits it. The implementation must never bypass a required
merge queue. GitHub's generic "merge when ready" feature is not a substitute for
Fullsend's semantic authorization checks. It is the SCM execution handoff after
those checks, and GitHub remains authoritative for the repository's mechanical
merge policy.

## Options considered

1. Keep auto-merge in Code or Review — **Rejected** because the same component
   would both evaluate and merge the change, while Fullsend would still have
   multiple ways to enable automatic merging.
2. Give the model a merge-capable credential — **Rejected** because untrusted
   pull request content or compromised model output could use it to merge.
3. Use a dedicated Auto-Merge agent within its own stage, while trusted Fullsend
   runtime code retains final merge control — **Selected** to separate the
   agent's recommendation from the final checks and GitHub action.
4. Give a post-script a merge-capable token and have it call the forge merge
   API directly — **Rejected** because this would create a privileged second
   merge path that could override repository policy. The trusted runtime may
   request GitHub's native Auto-Merge or queue operation, but it may not use a
   token to bypass required reviews, checks, conversation resolution, branch
   policy, or queue admission.

## Decision: dedicated Auto-Merge agent and stage

We choose Option 3. Fullsend will provide a dedicated Auto-Merge agent as the
semantic authorization component of a new `auto-merge` stage. The agent
assesses whether a pull request fits the repository's configured unattended-
merge scope, while trusted Fullsend runtime code performs the final checks and
requests GitHub's native action. Fullsend Review is the default semantic
provider, not a hard dependency; a trusted adapter may normalize an equivalent
third-party attestation. Without a trusted provider and current attestation,
Auto-Merge fails closed and does not authorize unattended merging.

The stage is opt-in per repository and disabled by default. The legacy Code
post-script path and its `CODE_AUTO_MERGE*` settings remain a migration path
and will be retired as the dedicated stage is implemented
([agents#1219](https://github.com/fullsend-ai/agents/pull/1219)); they will not
remain as a compatibility path.

## Required properties

- Automatic triggers, such as reviews and CI becoming ready, and manual commands
  must enter the same Auto-Merge stage.
- Deterministic policy checks must pass, and the agent's assessment must
  recommend merging. The agent's recommendation alone is not enough.
- Auto-Merge must consume risk evidence bound by trusted integration to the
  exact revision being evaluated. A PR-level label or unbound risk object is
  not sufficient by itself. The assessment may come from the configured
  semantic provider or another trusted adapter, not necessarily Fullsend Review
  ([ADR 0089](0089-pr-risk-assessment-scoring.md)). It is a policy input, not
  authorization by itself. It remains informational unless repository policy
  explicitly makes it a gate. When risk gating is enabled, a missing, stale, or
  malformed assessment—or a risk score above the allowed threshold—must stop
  automatic merging and escalate to a human.
- Every assessment and authorization must identify the repository, pull request,
  target branch, exact revision, and policy that were evaluated.
- Immediately before taking action, the trusted Fullsend runtime must fetch the
  latest GitHub state again. It must stop if required checks or reviews no longer
  pass, a human has blocked the change, or any required state is stale or
  unknown.
- The runtime must use the repository's configured merge path. If the repository
  requires a merge queue, the runtime must enter that queue and must not merge
  directly. Direct merge is allowed only when repository policy permits it.
  Before GitHub merges a queue-generated revision, the runtime must repeat
  deterministic SCM-state checks against that exact revision. Reauthorizing
  semantic evidence for a queue-generated revision is follow-up design work,
  not a capability claimed by this ADR.
- The agent never receives merge credentials. A dedicated least-privilege
  identity may request the configured native GitHub action, but it cannot use
  that credential to bypass repository policy. Administrator bypass is not
  allowed. If the repository requires core-maintainer approval or another SCM
  condition remains unsatisfied, Auto-Merge waits or escalates; it does not
  merge around the condition.
- The system must keep a durable record of the authorization and its outcome.

Detailed schemas, trigger rules, queue protocols, storage, reconciliation,
rollout gates, and cohort definitions are follow-up implementation design. They
must preserve this authority boundary and land in reviewable increments before
live mutation is enabled.

## Consequences

- Review and Auto-Merge can evolve independently without coupling model
  judgment to merge authority.
- A prompt-injected or compromised model cannot directly merge because the
  credential and final gate remain outside its sandbox.
- Direct and merge-queue repositories use their normal forge enforcement;
  Fullsend does not weaken or bypass repository requirements.
- Native SCM execution is the selected merge mechanism. A repository whose
  existing policy blocks a pull request does not get an automatic merge from
  Auto-Merge; the request waits for the policy to be satisfied or escalates.
- Removing the legacy path requires explicit migration to dedicated-stage
  policy and avoids split-brain enablement.
- Implementation needs separate security, conformance, and rollout work before
  any repository enables autonomous mutation.
