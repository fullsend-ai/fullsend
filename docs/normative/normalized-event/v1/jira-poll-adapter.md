# Jira poll adapter (NormalizedEvent extension)

Poll input driver mapping for Jira work items, defined in
[ADR 0063](../../../ADRs/0063-polling-based-work-discovery.md). This document
extends [NormalizedEvent v1](README.md) for `jira-poll` input drivers until the
changes are merged into `normalized-event.schema.json`.

## Scope

- **In scope:** Jira **issues** polled by `fullsend poll --input-driver jira-poll`.
  Events use `entity.kind: work_item` only.
- **Out of scope:** Jira webhooks, Service Desk, sprint/board events, and forge
  change-proposal routing (e.g. `/fs-fix`) — those stages require a
  `change_proposal` entity and are triggered from forge webhooks or poll drivers
  on the code repo, not from Jira issue comments alone.

## Schema extensions

The following fields are defined in
[`normalized-event.schema.json`](normalized-event.schema.json):

| Field | Definition |
|-------|------------|
| `source.system` | Enum includes `jira` |
| `entity.key` | Optional for GitHub; **required** when `source.system` is `jira` |

When `source.system` is `jira`, `entity.key` MUST be present (e.g. `PROJ-123`).

## Top-level fields

| Field | Jira poll value |
|-------|-----------------|
| `repo` | Target repo slug from poll context (`GITHUB_REPOSITORY` or config). |
| `source.system` | `jira` |
| `source.raw_type` | Native Jira object: `issue`, `comment`, `changelog` |
| `source.raw_action` | Native action when applicable: `created`, `updated`, `deleted` |
| `entity.kind` | `work_item` |
| `entity.id` | Jira numeric issue id (`fields` or search result `id`) |
| `entity.key` | Issue key (`PROJ-123`) |
| `entity.url` | Browse URL (`{base}/browse/{key}`) |
| `entity.linked_change_proposal` | Omitted — Jira poll emits work-item events only |
| `actor` | Comment/changelog author (see below) |
| `transition` | Mapped semantic transition (see below) |
| `state.labels` | Current Jira label names at event time |

## Transition mapping

| Jira signal | `transition.kind` | Sub-fields |
|-------------|---------------------|------------|
| Issue created | `opened` | — |
| Issue reopened | `reopened` | — |
| Summary/description/fields updated | `edited` | — |
| Label added | `label_changed` | `label.name`, `label.action: added` |
| Label removed | `label_changed` | `label.name`, `label.action: removed` |
| Comment added | `comment_added` | `comment.body`, optional `command` / `instruction` |
| Issue closed | `closed` | — |

Comment parsing matches the `gha-event` adapter: `command` is the first
whitespace-delimited token of the first line; `instruction` is the remainder of
the first line after the command (same rules as
[README — execution ref projection](README.md)).

## Actor mapping

| Field | Source |
|-------|--------|
| `actor.id` | Jira `accountId` (preferred) or `name` when accountId unavailable |
| `actor.kind` | `bot` when Jira account type is `app` or display name matches automation pattern; else `human` |
| `actor.role` | Effective permission on the **target repo** when the actor maps to a forge user with repo access; otherwise map Jira project role to closest ADR 0054 role (`write` for Developers, `read` for Reporter, `admin` for Administrators). When membership cannot be resolved, use `external`. |
| `actor.is_entity_author` | `true` when actor is the issue reporter |

Authorization is enforced by `fullsend dispatch` per
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)
**after** normalization. The poll loop does not bypass the gate.

## State

- `state.labels` — label names from `fields.labels` at event time, subject to
  the privacy gate below.
- `state.change_proposal` — omitted. Change-proposal stages (`fix`, PR-linked
  `review`, merge `retro`) are forge-scoped; harness CEL triggers for those
  stages MUST require `entity.kind == 'change_proposal'` or equivalent guards.
- `state.privacy_gate.source_hash` — SHA-256 of the pre-gate event payload,
  present when the privacy gate applied a transformation. See below.

## Privacy gate

Per [ADR 0068](../../../ADRs/0068-privacy-allowlist-for-poll-input-drivers.md),
`jira-poll` applies a configurable allowlist to Jira fields **before**
constructing `NormalizedEvent`. The gate is configured under
`poll.input_drivers[].privacy_gate` in `.fullsend/config.yaml`:

| Field | Effect |
|-------|--------|
| `allowed_fields` | Only listed fields are read into `NormalizedEvent`. Defaults to `[summary, issue_type, priority, labels]`. Fields not listed are omitted at construction, not filtered post hoc. |
| `comment_template` | When set, replaces `transition.comment.body` / `event_payload.comment` with a bounded, named-slot projection instead of the verbatim Jira comment. |

When `privacy_gate` is omitted entirely, the default allowlist above applies
— this is a change from the unfiltered mapping this document originally
described. Installations where the Jira project and target repo share a
trust boundary can widen `allowed_fields` or omit `comment_template` to
restore pass-through behavior.

## Execution ref projection

When `source.system` is `jira`, projection supplements the GitHub-shaped
`event_payload` fields:

| Execution ref / env | Source |
|---------------------|--------|
| `FULLSEND_WORK_ITEM_URL` | `entity.url` |
| `FULLSEND_WORK_ITEM_SOURCE` | `jira` |
| `FULLSEND_WORK_ITEM_KEY` | `entity.key` |
| `event_payload.comment` | `transition.comment` when present, subject to the privacy gate's `comment_template` |
| `GITHUB_ISSUE_URL` | Omit or empty — not a GitHub issue |
| `status-number` | `entity.id` (numeric Jira id) |

Harnesses and pre-scripts that require forge URLs MUST use `FULLSEND_WORK_ITEM_*`
for Jira-sourced runs.

## Example

Issue comment with slash command:

```json
{
  "repo": "acme/platform",
  "source": {
    "system": "jira",
    "raw_type": "comment",
    "raw_action": "created"
  },
  "entity": {
    "kind": "work_item",
    "id": 10042,
    "key": "PROJ-123",
    "url": "https://acme.atlassian.net/browse/PROJ-123"
  },
  "actor": {
    "id": "557058:abc123def456",
    "kind": "human",
    "role": "write",
    "is_entity_author": false
  },
  "transition": {
    "kind": "comment_added",
    "comment": {
      "command": "/fs-triage",
      "body": "/fs-triage check acceptance criteria",
      "instruction": "check acceptance criteria"
    }
  },
  "state": {
    "labels": ["needs-info", "bug"]
  }
}
```

## CEL triggers

Harness `trigger` expressions use the same `event` root variable as GitHub
events. Example:

```yaml
trigger: event.source.system == 'jira' && event.transition.kind == 'comment_added' && event.transition.comment.command == '/fs-triage'
```

Routing logic lives on harness files per
[ADR 0061](../../../ADRs/0061-harness-cel-dispatch.md), not in poll config.
