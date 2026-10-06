---
title: "131. GitLab dispatch via pipeline inputs, not Owner-role pipeline variables"
status: Accepted
relates_to:
  - agent-infrastructure
  - gitlab-implementation
  - security-threat-model
topics:
  - gitlab
  - forge
  - ci-cd
  - dispatch
  - security
---

# 131. GitLab dispatch via pipeline inputs, not Owner-role pipeline variables

Date: 2026-10-01

## Status

Accepted

Supersedes the trigger-variable control direction left open in
[ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md), resolving #7769.
The webhook/poller topology, shared dispatch spine, identity pin, HMAC
verification, and protected-branch isolation remain unchanged.

## Context

GitLab can be configured so only project Owners may pass or override
pipeline variables when starting a pipeline. Fullsend's poller has
Developer-level access. Keeping variable-based dispatch under that policy
would require a much more powerful poller credential. This ADR chooses a
way to pass dispatch data without giving the poller that extra power.

GitLab's project setting `ci_pipeline_variables_minimum_override_role`
controls who may supply pipeline variables. Its `no_one_allowed` value
blocks that channel entirely, protecting `CI_JOB_TOKEN`, `CI_API_V4_URL`,
and `FULLSEND_DISPATCH_SECRET` from caller overrides. Fullsend reads this
GitLab setting during installation and updates; it is not a Fullsend
configuration field. Typed inputs replace the dispatch transport, not this
protection: HMAC verification cannot protect its own verification key.

GitLab limits an individual string input to less than 1 KB and the pipeline
contract to 20 inputs. Event payloads need bounded chunking; interpolating
caller-provided arrays into `script` or `before_script` would turn untrusted
data into executable shell statements, before any HMAC check.

## Decision

Dispatch fields travel through declared typed scalar inputs, while the
poller retains Developer-level access and GitLab blocks pipeline-variable
overrides with `ci_pipeline_variables_minimum_override_role=no_one_allowed`.

1. Stage, event type, resource key, fork status, originating URL,
   repository name, author/actor IDs, status IID, poll-job URL, and
   dispatch HMAC are conveyed as **scalar metadata inputs**.

2. The payload data is too large to fit in a scalar metadata input, so
   we break it into base64-encoded chunks. `event_payload_chunk_00`
   through `event_payload_chunk_08` each carry a base64-encoded payload
   chunk of at most 1000 characters.

3. When Fullsend first installs the new GitLab pipeline setup, and whenever
   it later updates that setup, it verifies that the project blocks
   pipeline-variable overrides before enabling typed-input dispatch. If
   the restriction is absent or cannot be verified, Fullsend does not
   deliver runnable typed jobs and reports the problem.

Typed dispatch also requires a compatible typed pipeline contract. Legacy
variable-based wrappers keep their existing project setting. Migration
and compatibility details belong in the
[GitLab operations guide](../guides/getting-started/configuring-gitlab.md#typed-input-dispatch-migration).

## Consequences

- No Owner-role dispatcher credential is introduced; the GitLab project
  restriction remains a prerequisite for delivering runnable typed jobs.
- The contract uses exactly 20 scalar inputs, leaving no room for extra
  root pipeline inputs. Repositories needing additional inputs must move
  those declarations to separate included configurations before enrolling.
- Payload transport is bounded data, never caller-supplied executable statements.
- Live GitLab validation of scalar interpolation, Developer-role dispatch,
  and job identity remains required before fleet-wide rollout; this ADR
  does not enable the unimplemented webhook fast path.

> **Update (#7772):** `repos install` now provisions the ADR 0125 webhook
> fast path (trigger token, webhook secret, project webhook) once the
> dispatcher is on the protected default branch and `no_one_allowed` is
> verified; see [ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md).
> Install-time Maintainer access is used only transiently to provision and
> revoke; the trigger token acts as its owner at runtime, so install
> rejects and revokes a token owned by a Maintainer or Owner (or whose
> owner cannot be verified) and leaves the fast path disabled rather than
> raise any runtime credential above Developer.

## References

- [ADR 0125 — Hybrid GitLab dispatch](0125-gitlab-hybrid-webhook-poller-dispatch.md)
- [ADR 0067 — GitLab cron-polling event dispatch](0067-gitlab-cron-polling-event-dispatch.md)
- [GitLab Role Credentials](../contributing/gitlab-role-credentials.md)
