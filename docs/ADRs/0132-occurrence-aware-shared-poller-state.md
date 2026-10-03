---
title: "132. Occurrence-aware shared poller state for the GitLab webhook driver"
status: Accepted
relates_to:
  - agent-infrastructure
  - gitlab-implementation
topics:
  - gitlab
  - dispatch
  - webhook
  - polling
  - drivers
---

# 132. Occurrence-aware shared poller state for the GitLab webhook driver

Date: 2026-10-03

## Status

Accepted

Builds on [ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md) (hybrid
webhook fast-path plus poller backstop) and [ADR 0067](0067-gitlab-cron-polling-event-dispatch.md)
(poller state and HMAC signing). Implemented in #7773.

## Context

The `gitlab-webhook` input driver makes the dispatcher a second writer into
the poller's dispatch state. Presence- and watermark-based deduplication
cannot tell two writers' label occurrences apart, and a poller from before
this change must keep reading and preserving the signed state. The maintainer
authorized changing the shared poller for this (#7773), so the decisions below
are intentionally part of the poller, not a webhook-only addition.

## Decision

- **Occurrence identity.** A label addition is keyed on its GitLab resource
  label event ID, which the webhook builder and poll discovery both read from
  the same API, so a remove-then-re-add is a distinct occurrence on both
  paths. Where no ID is available the key carries a millisecond timestamp.
  Keys persisted by earlier versions carry Unix seconds and stay recognised (a
  value below 1e11 is seconds, never read as milliseconds); their retry counts
  migrate onto the new key and their dispatch keys still suppress re-dispatch.
- **Signed state stays readable by older pollers.** The `state.json` schema
  and HMAC are unchanged. The pending-label handoff is carried inside
  `failed_keys_full` as `fullsend-pending-label:`-prefixed entries (count 1),
  so an older poller round-trips them as unknown failed keys, verifies the
  document, and does not drop a pending occurrence when it commits. The HMAC
  covers them, so removing or editing a handoff fails verification. A handoff
  carries the validated actor of its exact occurrence, so a retry is routed
  under that actor's current access and never under a later user's.
- **A watermark is not dispatch evidence.** Presence-based polling can advance
  its watermark past a remove-then-re-add it never observed, so the driver
  does not skip events behind the watermark. Dispatched keys are retained for
  the webhook freshness window plus clock skew behind the watermark. Because
  an older poller prunes `dispatched_keys_*` at the bare watermark, every
  dispatched key is also mirrored into the matching `failed_keys_{fast,full}`
  map as a `fullsend-replay-key:`-prefixed entry (count 1); the next current
  reader merges them back before deduplicating a webhook.
- **Failed-label retry handoff.** A webhook dispatch failure for a label
  addition is persisted as a pending occurrence in the events state. The
  events poll retries pending occurrences independently of `LabelState` and
  the `updated_at` watermark filter. Pending state merges under CAS by exact
  key, so a successful dispatch clears only the occurrence it dispatched and a
  stale in-flight poll cannot erase a newer handoff.
- **Revalidated label-state writes.** On every CAS attempt, a writer's label
  additions and removals derived from its own snapshot are revalidated against
  the forge: a stale removal cannot erase a dispatched re-addition, and a
  stale addition cannot restore a label already removed. A failed revalidation
  of a removal or an addition, and a stale removal applied while the issue
  carries an undispatched re-addition, hold the poll watermark so the next
  poll rederives the label state and dispatches what remains undispatched.
- **Coalesced fan-out.** An event satisfied by a successful dispatch of the
  same stage and entity in the same payload or poll cycle records its own
  dispatch key, so replaying the payload creates no second pipeline.

## Consequences

- Once a dispatch's deduplication state has been persisted, a
  remove-then-re-add is not dispatched again by whichever writer sees it
  next, and a webhook replay inside the freshness window creates no second
  pipeline. Delivery is not exactly-once: both writers create the pipeline
  before committing deduplication state, and CAS protects state updates, not
  an atomic dispatch reservation. Overlapping writers can therefore dispatch
  the same occurrence, and a successful pipeline creation followed by a
  persistence failure can allow a redispatch.
- Older pollers keep the signed state valid and preserve pending and replay
  evidence, so mixed-version operation does not lose retry state.
- An older poller does not recognise the new label keys: rolling back or
  running mixed versions can re-dispatch a rediscovered label addition or lose
  its retry count. Drain in-flight poll jobs or pin every poller to one
  version before rolling back.
- Poll persistence makes extra forge reads (issue and label events) per
  newly added or removed label on each CAS attempt.
