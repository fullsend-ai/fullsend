# Jira poll adapter (NormalizedEvent extension)

Poll input driver mapping for Jira work items, defined in
[ADR 0063](../../../ADRs/0063-polling-based-work-discovery.md). This document
extends [NormalizedEvent v1](README.md) for `jira-poll` input drivers.

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

When `source.system` is `jira`, `entity.key` MUST be present and match
`^[A-Z][A-Z0-9_]+-[1-9][0-9]*$` (e.g. `PROJ-123`), the Jira work item key
shape both work-item dispatch consumers require (`fullsend run`'s identity
resolution and the GitLab jira dispatch driver). Validating the shape here is
defence in depth: both consumers already refuse a non-matching key after
dispatch, so the validation gate skips such an event before dispatch instead,
whatever route served it — a custom `tracker.jira.poll.query`, or a
single-character project key such as `P-1` on a legacy Server/Data Center
deployment. The config gate's `jiraProjectKeyRe` accepts single-character
project keys, and the CLI always scopes the query to the project, so such a
project is polled on the default query too: every one of its events fails
validation and the all-invalid guard fails each cycle (see the Jira guide's
troubleshooting table). The project-key check inside `searchCandidates` runs
only when no JQL is set at all — direct API use such as the behaviour-test
step, not the CLI — so it never intercepts these keys.
One further consumer has no downstream key check: `fullsend dispatch
--input-driver json` (or `--route <agent>` with a JSON event) validates the
key at parse time, so it now rejects such a key instead of printing a route
decision that could only end in a `fullsend run` refusal (see the v1
versioning note in [README.md](README.md)).

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

> **ADR 0107 target contract — not yet implemented:** The bot-role and
> `role_verified` rows describe the post-migration adapter output. Current Jira
> polling remains on its existing compatibility path until the resolver is
> integrated.

| Field | Source |
|-------|--------|
| `actor.id` | Jira `accountId` (preferred) or `name` when accountId unavailable |
| `actor.kind` | For the target adapter contract, `bot` only when Jira's authoritative account metadata identifies an app or the adapter configuration identifies the authenticated poller as a verified service identity; otherwise `human`. A display-name automation pattern alone is insufficient. The current compatibility poller may retain its legacy heuristics until it adopts the ADR 0107 target implementation. |
| `actor.role` | `none` for target bots; for humans, from the source the `jira` authorization provider selects ([Authorization v1](../../authorization/v1/README.md#forge-native-permission-mapping), [ADR 0153](../../../ADRs/0153-authorize-jira-poll-actors-without-project-administration.md)): from its lists, `write` or `triage` for listed actors and `none` otherwise; from the Jira project role, `admin` for Administrators, `write` for Developers, `read` for other named project roles, `external` when the actor does not hold any project role; `none` when the role is not resolved (label additions under a `jira` entry's `labels: jira_editors`, and non-command events when a `jira` entry sets no lists and not `labels: project_roles`). With no `jira` entry, every event takes its role from the Jira project role. Cross-system identity resolution (Jira user → GitHub user → repo permission) is not performed; the Jira project is the authorization boundary for Jira-sourced events. |
| `actor.bot_role` | Provider lookup of the verified bot identity; the canonical fullsend role when recognized, otherwise `null` |
| `actor.role_verified` | `true` when bot-role or Jira project-role lookup completed, including a no-match result; `false` when resolution failed |
| `actor.is_entity_author` | `true` when actor is the issue reporter |
| `actor.is_entity_assignee` | `true` when actor is the issue's current assignee (`fields.assignee`, requested in the search fields), compared on the same identifier as `actor.id`: `accountId`, or `name` when `accountId` is unavailable; otherwise `false`, including when the issue is unassigned |

Authorization is enforced by `fullsend dispatch` per
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)
**after** normalization. The poll loop does not bypass the gate.

## Validation and skipping

The poll loop also validates each normalized event before matching triggers.
An event that fails validation (for example a non-numeric issue id, which maps
to `entity.id` 0, an `entity.key` that is not a Jira work item key (see
[Schema extensions](#schema-extensions)), or an empty `actor.id`) is skipped
with a warning rather than dispatched, because `fullsend run` cannot use an
invalid embedded event.
A skipped event is never dispatched. The issue's checkpoint advances past it
as for any other inspected event; while a failed dispatch of another of the
issue's events holds the checkpoint, the next cycle inspects it again and
repeats the warning. A cycle that skipped events and runs to completion also
logs how many of its events it skipped. A cycle in which *every* event was
dropped before dispatch for any reason — every event failed validation, every
event's trigger matching errored, or the two mixed — fails instead of
completing: the poller returns an error and advances no checkpoint, so those
events are retried next cycle and the
operator sees the failure rather than a green run that dropped them.

An issue served without a labels array (`fields.labels` null or missing) is
treated as unlabeled: the adapter maps a missing `fields.labels` to an empty
`state.labels` list. Jira does not guarantee one response shape across
deployments and field configurations, so the adapter tolerates both rather
than depending on either.

## State

- `state.labels` — label names from `fields.labels` at event time; see
  [Validation and skipping](#validation-and-skipping) for how an issue served
  without the field is treated. The
  dispatch door bounds them to a 1024-byte JSON prefix, like the
  [GitLab dispatch payload](README.md#gitlab-dispatch-payload), so user
  labels past the bound never reach the harness on this transport
  (fullsend's own control labels, `fullsend-fix` and `fullsend-no-fix`,
  are kept past the bound, like on the GitLab transport); an `/fs-*`
  command body that does not fit the dispatch budget fails the dispatch
  instead of arriving shortened. On the GitLab target a command body is
  carried whole through normalization so the door measures the complete
  body; the GitHub target's transport has no fail-closed budget door
  (its records travel whole through the dispatch file into the poll
  job's matrix output), so there every body, command or not, keeps the
  schema's 4096-code-point cap at normalization. A raw non-command body
  past that cap is cut at normalization on both targets, before the
  dispatch door sees it, so that guarantee covers what the schema
  carries. Body-keyed triggers see the
  adapter-truncated body, while the transported non-command body is
  shortened further (the shortening ladder on the GitLab target; the
  GitHub target carries the adapter-truncated body). On
  the GitLab target the same labels bound is
  applied before trigger matching, so a trigger keyed on a label past the
  bound does not match: the matcher and the transported payload agree on
  the work item's labels. The GitHub target matches on the full label
  set.
- `state.change_proposal` — omitted. Change-proposal stages (`fix`, PR-linked
  `review`, merge `retro`) are forge-scoped; harness CEL triggers for those
  stages MUST require `entity.kind == 'change_proposal'` or equivalent guards.

## Execution ref projection

When `source.system` is `jira`, projection supplements the GitHub-shaped
`event_payload` fields:

| Execution ref / env | Source |
|---------------------|--------|
| `FULLSEND_WORK_ITEM_URL` | `entity.url` |
| `FULLSEND_WORK_ITEM_SOURCE` | `jira` |
| `FULLSEND_WORK_ITEM_KEY` | `entity.key` |
| `ISSUE_NUMBER` | Omit or empty — a Jira key is not a GitHub issue number |
| `event_payload.comment` | `transition.comment` when present |
| `GITHUB_ISSUE_URL` | Omit or empty — not a GitHub issue |
| `status-number` | `entity.id` (numeric Jira id) |

Harnesses and pre-scripts that require forge-agnostic work item URLs SHOULD use
`FULLSEND_WORK_ITEM_*` rather than forge-specific variables like
`GITHUB_ISSUE_URL`. The dispatch workflow sets `FULLSEND_WORK_ITEM_URL` and
`FULLSEND_WORK_ITEM_KEY` from the normalized entity for Jira events. For
forge-native events, the generic key and `ISSUE_NUMBER` carry the same value.

`fullsend run` reads the normalized event embedded in the dispatch payload.
On GitLab, the agent job extracts it from the signed `jira_*` dispatch payload
and passes it with `--event-file`.
For a Jira event it sets `FULLSEND_WORK_ITEM_SOURCE`, `FULLSEND_WORK_ITEM_KEY`,
and `FULLSEND_WORK_ITEM_URL` from that event, replacing any inherited values.
It also clears `ISSUE_NUMBER` before pre-scripts, post-scripts, and harness
env expansion run. Neither the numeric Jira entity ID nor the numeric suffix
of the key reaches scripts as a forge issue number. The run fails before any
script runs in three cases:

- `entity.key` is missing or is not a Jira key such as `PROJ-123`. The key
  must match `^[A-Z][A-Z0-9_]+-[1-9][0-9]*$`, so single-character project keys
  such as `P-1` are rejected.
- `entity.url` is missing or is not an `https` URL.
- The payload declares `source.system: jira`, but its normalized event fails
  validation.

For forge-native events, `fullsend run` only fills in
`FULLSEND_WORK_ITEM_SOURCE` when the caller left it unset. It uses
`source.system`, or the detected forge when no normalized event is present.

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
    "is_entity_author": false,
    "is_entity_assignee": false
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
