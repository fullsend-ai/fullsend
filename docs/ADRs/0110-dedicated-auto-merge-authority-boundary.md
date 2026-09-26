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

Fullsend will provide one way to merge pull requests automatically: a dedicated
Auto-Merge stage, separate from Code and Review. The stage combines an
Auto-Merge agent with trusted Fullsend runtime code. The agent recommends
whether a pull request is ready. The trusted runtime—code outside the agent
sandbox that holds the GitHub credentials—then checks the latest GitHub state.
If the change is still allowed, that runtime sends it through the repository's
configured merge path. The legacy `CODE_AUTO_MERGE` path will be removed.

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
revision being merged is the one that was evaluated. This keeps merge
credentials and final enforcement outside the agent sandbox, consistent with
[ADR 0017](0017-credential-isolation-for-sandboxed-agents.md).

The initial deployment targets, `fullsend-ai/fullsend` and
`fullsend-ai/agents`, use GitHub merge queues. After considering the agent's
assessment, the trusted Fullsend runtime decides whether to authorize the pull
request. If authorized, that runtime must use the repository's configured merge
path: enter the merge queue when one is required, or merge directly only when
repository policy permits it. The implementation must never bypass a required
merge queue. GitHub's generic "merge when ready" feature is not a substitute for
Fullsend's authorization checks.

## Options considered

1. Keep auto-merge in Code or Review — **Rejected** because the same component
   would both evaluate and merge the change, while Fullsend would still have
   multiple ways to enable automatic merging.
2. Give the model a merge-capable credential — **Rejected** because untrusted
   pull request content or compromised model output could use it to merge.
3. Use a dedicated Auto-Merge agent within its own stage, while trusted Fullsend
   runtime code retains final merge control — **Selected** to separate the
   agent's recommendation from the final checks and GitHub action.

## Decision: dedicated Auto-Merge agent and stage

We choose Option 3. Fullsend will provide a dedicated Auto-Merge agent as the
advisory component of a new `auto-merge` stage. The agent assesses whether a
pull request is ready, while trusted Fullsend runtime code performs the final
checks and GitHub action.

The stage is opt-in per repository and disabled by default. The legacy Code
post-script path and its `CODE_AUTO_MERGE*` settings will be retired as the
dedicated stage is implemented
([agents#1219](https://github.com/fullsend-ai/agents/pull/1219)); they will not
remain as a compatibility path.

## Required properties

- Automatic triggers, such as reviews and CI becoming ready, and manual commands
  must enter the same Auto-Merge stage.
- Deterministic policy checks must pass, and the agent's assessment must
  recommend merging. The agent's recommendation alone is not enough.
- Auto-Merge must read the PR-level risk assessment produced by Review for the
  exact revision being evaluated
  ([ADR 0089](0089-pr-risk-assessment-scoring.md)). The assessment is a policy
  input, not authorization by itself. It remains informational unless repository
  policy explicitly makes it a gate. When risk gating is enabled, a missing,
  stale, or malformed assessment—or a risk score above the allowed threshold—
  must stop automatic merging and escalate to a human.
- Every assessment and authorization must identify the repository, pull request,
  target branch, exact revision, and policy that were evaluated.
- Immediately before taking action, the trusted Fullsend runtime must fetch the
  latest GitHub state again. It must stop if required checks or reviews no longer
  pass, a human has blocked the change, or any required state is stale or
  unknown.
- The runtime must use the repository's configured merge path. If the repository
  requires a merge queue, the runtime must enter that queue and must not merge
  directly. Direct merge is allowed only when repository policy permits it.
  Before GitHub merges a queue-generated revision, the runtime must repeat the
  authorization checks against that exact revision.
- The agent never receives merge credentials. A dedicated least-privilege
  identity performs the final GitHub action, and administrator bypass is not
  allowed.
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
- Removing the legacy path requires explicit migration to dedicated-stage
  policy and avoids split-brain enablement.
- Implementation needs separate security, conformance, and rollout work before
  any repository enables autonomous mutation.
