---
title: "68. Privacy allowlist for poll input drivers"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - poll
  - jira
  - privacy
  - dispatch
  - drivers
---

# 68. Privacy allowlist for poll input drivers

Date: 2026-07-06

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

[ADR 0063](0063-polling-based-work-discovery.md) defines `fullsend poll`,
including a `jira-poll` input driver that maps Jira issue fields into
`NormalizedEvent` per the
[Jira poll adapter](../normative/normalized-event/v1/jira-poll-adapter.md).
That mapping is unfiltered: issue title, comment bodies, and label state flow
directly into `NormalizedEvent` and are projected into agent-visible
`FULLSEND_WORK_ITEM_*` environment variables and `event_payload.comment`.

This is a gap for installations where the polled Jira project is internal but
the target repo is public. Jira issues can carry customer names, internal
priority rationale, or references to other internal tickets — content with no
equivalent in a GitHub-native dispatch, where the event source and the target
repo share the same trust boundary. `jira-poll` is the first driver where the
event source and the dispatch target do not.

The [security threat model](../problems/security-threat-model.md)'s "Indirect
information disclosure" section already treats forge content flowing through
an agent as a threat surface and mitigates it on the *output* side
(`SecretRedactor`, [ADR 0022](0022-harness-level-output-schema-enforcement.md)).
`jira-poll` introduces a new *input*-side question that ADR 0063 does not
answer: which Jira fields should reach `NormalizedEvent` at all. ADR 0063
scopes poll input drivers to discovery, change detection, and coordination
only ("Poll input drivers MUST NOT perform authorization policy — that is the
dispatch core's responsibility") and does not address content filtering.

## Decision

`jira-poll` (and future poll input drivers for non-forge-native sources)
apply a **privacy allowlist** to Jira fields before constructing
`NormalizedEvent`, inside the input driver, before the event reaches the
shared dispatch core.

Configuration extends the existing `poll.input_drivers` block:

```yaml
poll:
  input_drivers:
    - type: jira-poll
      connection: { ... }
      queries:
        - project = PROJ AND status != Done
      privacy_gate:
        allowed_fields: [summary, issue_type, priority, labels]
        comment_template: |
          Type: {issue_type}
          Priority: {priority}
          Summary: {summary}
```

- **Allowlist projection, default-deny.** Only fields listed in
  `allowed_fields` are read into `NormalizedEvent`. Fields not listed
  (description, comment body, custom fields, reporter identity beyond role
  mapping) are omitted at construction time, not merely hidden from display.
  Exception: `state.labels` is a required `NormalizedEvent` field
  ([schema](../normative/normalized-event/v1/normalized-event.schema.json))
  and is always emitted; when `labels` is absent from `allowed_fields`, the
  driver sets `state.labels` to an empty array rather than omitting the
  field, keeping the event schema-valid without leaking label content.
- **Template-projected free text.** `comment_template`, when set, replaces
  `transition.comment.body`/`event_payload.comment` with a bounded,
  named-slot projection instead of the verbatim Jira comment.
- **PII scan on allowlisted free text.** Fields allowed through by name (e.g.
  `summary`) are still scanned for PII patterns (email, phone, SSN-shaped
  strings) as defense in depth, consistent with the threat model's existing
  content-aware redaction guidance.
- **Provenance hash.** A SHA-256 hash of the pre-gate `NormalizedEvent`
  payload is attached as `state.privacy_gate.source_hash`, so sanitization
  can be verified for a given dispatched run without retaining the original
  Jira content anywhere downstream.

When `privacy_gate` is omitted, the driver defaults to the current allowlist
(`summary`, `issue_type`, `priority`, `labels`) rather than the unfiltered
behavior described in [the Jira poll adapter](../normative/normalized-event/v1/jira-poll-adapter.md) —
that document is updated alongside this ADR to reflect the gated mapping as
the default, with an explicit opt-out for installations where the Jira
project and target repo share the same trust boundary.

## Consequences

- Poll-sourced `NormalizedEvent`s carry less Jira content by default than the
  original adapter mapping; harnesses that need additional fields must
  request them explicitly via `allowed_fields`.
- `jira-poll-adapter.md` gains a new schema extension (`privacy_gate` block)
  that must be implemented before or alongside the `jira-poll` driver in the
  [#2263](https://github.com/fullsend-ai/fullsend/issues/2263) epic — this
  ADR should land before that implementation starts, not as a retrofit.
- Installations that want the pre-gate behavior (single-trust-boundary Jira +
  target repo) can set a broad `allowed_fields` list or omit `comment_template`
  to pass comment bodies through unmodified.
- Future poll input drivers for other internal sources should implement the
  same `privacy_gate` shape rather than inventing per-driver filtering.
- Does not change dispatch-core authorization ([ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)),
  CEL trigger evaluation, or output drivers — the gate applies strictly
  before `NormalizedEvent` construction.

## References

- [ADR 0063 — Polling-based work discovery via dispatch drivers](0063-polling-based-work-discovery.md)
- [ADR 0067 — GitLab cron-polling event dispatch](0067-gitlab-cron-polling-event-dispatch.md)
- [ADR 0054 — Require authorization on all agent dispatch paths](0054-require-authorization-on-all-agent-dispatch-paths.md)
- [ADR 0022 — Harness-level output schema enforcement](0022-harness-level-output-schema-enforcement.md)
- [Security threat model — Indirect information disclosure](../problems/security-threat-model.md)
- [Jira poll adapter (NormalizedEvent extension)](../normative/normalized-event/v1/jira-poll-adapter.md)
