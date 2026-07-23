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
That mapping is unfiltered: comment bodies and label state flow directly
into `NormalizedEvent` and `event_payload.comment` with no content
filtering, and nothing constrains what future extensions (e.g. surfacing
issue summary, type, or priority for richer triage context) could expose the
same way.

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

`jira-poll` (and future poll input drivers where the event source and
dispatch target do not share a trust boundary) apply a **privacy allowlist**
to Jira fields before constructing `NormalizedEvent`, inside the input
driver, before the event reaches the shared dispatch core.

`allowed_fields` names Jira **source** fields the driver may read — not
`NormalizedEvent` output fields directly. Today the only `NormalizedEvent`
field a Jira value populates directly is `state.labels`; every other
allowlisted field (`summary`, `issue_type`, `priority`) exists solely to be
interpolated into `comment_template`. `NormalizedEvent`'s structurally
required identifiers (`repo`, `entity.id`/`url`/`key`, `actor.id`/`kind`,
`source.system`/`raw_type`, `transition.kind`, `state.labels`) are always
emitted regardless of `allowed_fields` — the allowlist governs Jira
*content*, not the routing/identity fields the dispatch core depends on.

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
  `allowed_fields` are read from Jira at all. Fields not listed
  (description, comment body, custom fields, reporter identity beyond role
  mapping) are never read into driver memory, not merely omitted at
  construction or hidden from display.
- **`comment_template` slots MUST be a subset of `allowed_fields`.** The
  driver MUST reject configuration where a template placeholder references a
  field absent from `allowed_fields` — otherwise the template becomes a
  second, unconstrained path for excluded content to reach
  `event_payload.comment`, defeating the allowlist.
- **Substituted values MUST be sanitized before interpolation.** Newlines
  and characters that could be mistaken for template structure are stripped
  or escaped before a field value is written into a `comment_template` slot.
  Untreated substitution would let a crafted Jira field (e.g. a `summary`
  containing embedded newlines styled as additional "fields") inject content
  indistinguishable from the template's own structure — a template/prompt-
  injection surface, not merely a formatting concern.
- **Structural identifiers are always emitted, independent of
  `allowed_fields`.** `state.labels` is a required `NormalizedEvent` field
  ([schema](../normative/normalized-event/v1/normalized-event.schema.json));
  when `labels` is absent from `allowed_fields`, the driver sets
  `state.labels` to an empty array rather than omitting the field.
  `entity.url` (always required) and `entity.key` (required when
  `source.system` is `jira`) are likewise always emitted — see Consequences
  for the residual risk this carries and why it is accepted rather than
  gated.
- **PII scan on allowlisted free text.** Fields read by name (e.g.
  `summary`) are scanned for structured PII patterns (email, phone,
  SSN-shaped strings); a match causes the matched substring to be redacted
  in place before the field reaches `comment_template` or `NormalizedEvent`.
  This is new functionality — no existing fullsend component performs PII
  scanning today (`SecretRedactor` scans for secrets/credentials, not PII).
  Regex-based scanning does not catch unstructured leakage: customer names,
  internal priority rationale, or ticket cross-references embedded in
  `summary` are not detectable this way and remain a residual risk
  operators must manage via narrower `allowed_fields` or careful
  `comment_template` design.
- **Provenance fingerprint.** A SHA-256 hash of the pre-gate Jira source
  payload is attached as `state.privacy_gate.source_hash`, computed over the
  [RFC 8785](https://www.rfc-editor.org/rfc/rfc8785) JSON Canonicalization
  Scheme of the pre-gate field set so the hash is reproducible across
  implementations. This is a correlation/tamper-evidence fingerprint, not a
  verification mechanism — a one-way hash cannot itself confirm what was
  gated unless the pre-gate payload is independently available to recompute
  against (e.g. by re-fetching the still-live Jira issue during an audit).

When `privacy_gate` is omitted, the driver defaults to the allowlist above.
When `privacy_gate` is present but `allowed_fields` itself is omitted, the
same default applies. Either case is a change from the unfiltered behavior
[the Jira poll adapter](../normative/normalized-event/v1/jira-poll-adapter.md)
originally described — that document is updated alongside this ADR to
reflect the gated mapping as the default, with an explicit opt-out for
installations where the Jira project and target repo share the same trust
boundary.

## Consequences

- Poll-sourced `NormalizedEvent`s carry less Jira content by default than the
  original adapter mapping; harnesses that need additional fields must
  request them explicitly via `allowed_fields`.
- `entity.url` and `entity.key` are always emitted and are not subject to
  `allowed_fields` — they reveal the internal Jira instance hostname and
  ticket key even when all content fields are gated. This is an accepted,
  explicit gap rather than an oversight: both fields are load-bearing for
  `fullsend poll cancel` and runner lock-refresh
  ([ADR 0063](0063-polling-based-work-discovery.md)), which resolve a
  dispatched run back to its source issue by URL/key. An opaque-identifier
  scheme that preserves that resolvability while hiding the Jira hostname is
  future work, not blocking this ADR.
- `jira-poll-adapter.md` gains a new schema extension (`privacy_gate` block,
  including `required: ["source_hash"]`) that the `jira-poll` driver
  implementation — tracked from [ADR 0063](0063-polling-based-work-discovery.md)
  and originating in [#2263](https://github.com/fullsend-ai/fullsend/issues/2263) —
  must satisfy from the start; this ADR is intended to land before that
  implementation begins, not as a retrofit.
- Implementation MUST include conformance tests asserting: `state.labels` is
  always present and schema-valid regardless of `allowed_fields`; fields
  absent from `allowed_fields` are never read from Jira, not merely filtered
  post hoc; `comment_template` configuration is rejected at load time if a
  placeholder references a field outside `allowed_fields`; and
  `source_hash` matches `^[0-9a-fA-F]{64}$` whenever `privacy_gate` is
  present.
- Installations that want the pre-gate behavior (single-trust-boundary Jira +
  target repo) can set a broad `allowed_fields` list or omit `comment_template`
  to pass comment bodies through unmodified.
- Future poll input drivers for sources that don't share a trust boundary
  with their dispatch target should implement the same `privacy_gate` shape
  rather than inventing per-driver filtering.
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
