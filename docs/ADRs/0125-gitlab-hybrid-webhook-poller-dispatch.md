---
title: "125. Hybrid GitLab dispatch (webhook fast-path + poller backstop)"
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
  - webhook
  - polling
  - drivers
---

# 125. Hybrid GitLab dispatch (webhook fast-path + poller backstop)

Date: 2026-09-26

## Status

Accepted

Supersedes the GitLab **dispatch topology** in
[ADR 0067](0067-gitlab-cron-polling-event-dispatch.md) (native-CI two-path,
later pure cron-polling after #7322). ADR 0067's credential model, poller
internals, HMAC dispatch signing, security guardrails, and forge-interface
extensions remain current.

## Context

[ADR 0067](0067-gitlab-cron-polling-event-dispatch.md) chose cron-polling
for GitLab event dispatch to avoid an external webhook-to-trigger
translation bridge. [ADR 0028](0028-gitlab-support.md) had treated that
bridge as required because GitLab webhook JSON and the pipeline trigger
API were not wire-compatible. After #7322, GitLab dispatch is poll-only:
native `merge_request_event` pipelines run on unprotected MR refs, so
protected CI/CD variables (role tokens, `FULLSEND_DISPATCH_SECRET`) are
empty.

A September 2026 spike on the CEE fleet showed that GitLab's native
"use a webhook" pipeline trigger does not need a translation bridge.
`POST /api/v4/projects/:id/ref/:ref/trigger/pipeline?token=` pins
`:ref` in the URL; that ref overrides the payload ref. Pinning it to
the protected default branch makes the resulting `source=trigger`
pipeline a protected-ref job, so it receives protected and masked
CI/CD variables. `$TRIGGER_PAYLOAD` carries the webhook body.
Variable injection is server-side and ref-based, so it is
runner-independent.

Event-driven dispatch is already the primary path; polling is the
stated complement ([ADR 0063](0063-polling-based-work-discovery.md)).
A GitLab webhook driver should feed the same normalize → authorize →
CEL → dispatch spine as `gha-event` and `gitlab-poll`
([ADR 0061](0061-harness-cel-dispatch.md),
[ADR 0098](0098-entity-first-harness-evaluation.md)).

## Options

### Option A: Keep pure cron-polling

Retain ADR 0067 after #7322. No new trigger token or webhook
configuration.

**Rejected.** Poll-interval latency is an artifact of giving up native
events, not a GitLab limitation. The spike restored a native fast-path
without reintroducing a bridge.

### Option B: Restore native `merge_request_event`

Return MR pipelines to GitLab's `merge_request_event` source.

**Rejected.** Those pipelines still run on unprotected
`refs/merge-requests/N/head`, so protected variables stay empty. That
is the #7293 / #7322 failure mode.

### Option C: External webhook-to-trigger bridge

Deploy a Cloud Function or similar inbound receiver that translates
webhook JSON into trigger-API form parameters
([ADR 0028](0028-gitlab-support.md) Open Questions).

**Rejected.** The URL-pinned native trigger needs no intermediary and
no inbound endpoint we operate (GitLab → GitLab). A bridge reintroduces
the operational and attack-surface cost ADR 0067 avoided.

### Option D: Hybrid native webhook + poller backstop

Project webhook → native trigger on the protected default branch for
low-latency dispatch; keep the ADR 0067 cron-poller as the
authoritative reconciliation backstop.

**Accepted.**

## Decision

GitLab event dispatch is a **hybrid**:

1. **Webhook fast-path.** A project webhook fires GitLab's native
   "use a webhook" pipeline trigger with `:ref` pinned to the
   protected default branch. The resulting pipeline is
   `CI_PIPELINE_SOURCE=trigger` on a protected ref, so it receives
   protected and masked variables (role tokens,
   `FULLSEND_DISPATCH_SECRET`).
2. **Poller reconciliation backstop.** The existing `gitlab-poll`
   scheduled pipelines remain the source of truth and self-heal
   missed or auto-disabled deliveries. Native `merge_request_event`
   dispatch stays removed.

No translation bridge. No inbound endpoint we operate.

### Shared dispatch spine

A `gitlab-webhook` input driver is another driver into the shared CEL
dispatch core, not a parallel path. The webhook starts a lightweight
**dispatcher** pipeline that runs the existing
`normalize → authorize → CEL → CreatePipeline` spine over one event
from `$TRIGGER_PAYLOAD`. It reuses the router, dispatch HMAC keying,
and in-job security gate. Existing `dispatched_keys` dedup composes a
webhook-dispatched event with a later poll-detected one.

This matches [ADR 0098](0098-entity-first-harness-evaluation.md) /
[ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md):
the webhook is a low-latency **candidate**; each run reconciles current
entity state; the poller is the scheduled backstop.

### Guardrails retained from ADR 0067

- **Do not trust the payload.** The agent job re-verifies via the
  GitLab API (pipeline `.source` / identity, HMAC over dispatch
  variables). `$TRIGGER_PAYLOAD` is untrusted input.
- **Event authenticity** still comes from API reads and signed
  dispatch variables, not from the webhook body alone.
- **No event loss.** Webhook delivery can fail or auto-disable; the
  poller remains the backstop.

### Runner requirements

Poller and dispatcher jobs are API-only (`fullsend poll` / dispatch).
They need a container-capable runner on a protected ref with API
reach and CA trust (`fullsend poll` honors `CI_SERVER_TLS_CA_FILE`).
They do **not** need the sandbox agent fleet.

Prefer a dedicated `fullsend-api` runner tag over untagged shared
runners so API jobs do not contend with agent sandboxes. That split
requires replacing the single `__RUNNER_TAGS__` scaffold placeholder
with per-`FULLSEND_JOB_KIND` tags.

### Fleet capacity

A four-VM fleet with gitlab-runner `concurrent=1` is a four-slot
ceiling, shared across projects. That is an operational prerequisite
(tune `concurrent`, VM count, or a priority lane), not a limitation
of the webhook mechanism. Spike "failures" under load were queueing
behind that ceiling, plus a custom executor that requires a job
`image:`.

## Consequences

- GitLab moves toward GitHub-like event latency while keeping
  polling as the complement ([ADR 0063](0063-polling-based-work-discovery.md)).
- No hosted webhook receiver and no translation bridge; GitLab
  delivers to GitLab.
- Protected-variable access is restored for event-driven GitLab
  dispatch without reviving `merge_request_event` on unprotected MR
  refs.
- A trigger token and project webhook become install-time
  configuration; the poller, HMAC secret, and in-job gate stay
  required.
- Dispatcher and poller jobs should be tagged onto an API runner
  lane; under-provisioned `concurrent` still queues both webhook and
  poll work.

## References

- [ADR 0028 — GitLab Support Architecture](0028-gitlab-support.md)
- [ADR 0061 — Harness CEL triggers and fullsend dispatch drivers](0061-harness-cel-dispatch.md)
- [ADR 0063 — Polling-based work discovery via dispatch drivers](0063-polling-based-work-discovery.md)
- [ADR 0067 — GitLab cron-polling event dispatch](0067-gitlab-cron-polling-event-dispatch.md)
- [ADR 0098 — Evaluate harnesses against entities with optional event context](0098-entity-first-harness-evaluation.md)
- [ADR 0106 — Serialize agent runs and coalesce subsequent events](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
- [NormalizedEvent v1](../normative/normalized-event/v1/)
