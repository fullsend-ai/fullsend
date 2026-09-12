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

## Options

- **Merge from Review or Code.** Rejected: it couples judgment and mutation and
  makes the existing role's credential and lifecycle responsible for both.
- **Give the model merge authority.** Rejected: prompt-injected pull-request
  content or a compromised runtime could directly invoke an irreversible
  operation.
- **Use a dedicated stage with a host-side gate.** Chosen: the stage assesses
  eligibility, while a trusted driver revalidates and requests the forge's
  normal merge or queue mechanism.

## Decision

Fullsend will implement autonomous merge as a separate `auto-merge` stage,
opt-in per repository and disabled by default. Its model output is a structured,
non-authoritative eligibility assessment; it never grants merge authority.

Before any mutation, a host-side forge driver must re-fetch authoritative state
and fail closed unless the repository policy allows the stage, the pull request
is in an allowed cohort, the current head SHA equals the reviewed/attested head
SHA, current review and human gates pass, required checks pass or the configured
queue path explicitly owns their waiting and revalidation, and the target
branch's merge mechanism is known. The driver must bind its request to that
expected head, route through the normal merge queue or merge policy, and never
use an administrator bypass.

The sandbox receives read-only evidence and no merge-capable credential. Any
write-capable merge identity or short-lived capability remains host-side and
exposes only the constrained operation needed by the driver.

The dedicated stage is the sole Fullsend-owned path for autonomous merge. The
`CODE_AUTO_MERGE` and `CODE_AUTO_MERGE_METHOD` variables and their Code
post-script implementation will be removed from the agents repository,
including generated bundles, forge helpers, tests, and user documentation. They
will not be aliased or migrated as a compatibility fallback. Existing values
therefore have no effect; repositories that want autonomous merging must opt in
to the dedicated stage. Forge-native or third-party automation, such as
Renovate's own `automerge` setting, is outside this Fullsend-owned stage
contract and requires separate policy ownership and audit.

The implementation removal is tracked in [agents#1219](https://github.com/fullsend-ai/agents/pull/1219).

## Consequences

- Review and merge can evolve and be evaluated independently, with stale-head and human-intent changes invalidating a decision.
- The first implementation needs a host-side policy/forge driver, structured review attestation, and integration coverage for direct merges and merge queues.
- Repositories retain branch protection and queue enforcement as the final forge boundary; Fullsend cannot override failed requirements.
- Observe-only and explicit human-trigger modes can be deployed before automatic mutation, while unknown state waits for a human.
- The legacy Code auto-merge path is intentionally removed, so existing `CODE_AUTO_MERGE*` configuration must be replaced by dedicated-stage policy; this avoids split-brain enablement and makes the migration auditable.
