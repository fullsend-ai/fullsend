# NormalizedEvent v1

Forge-neutral routing input for `fullsend dispatch` and harness CEL `trigger`
expressions ([ADR 0054](../../../ADRs/0054-harness-cel-dispatch.md)).

## Contract

- **Schema:** [`normalized-event.schema.json`](normalized-event.schema.json)
- **CEL context:** harness `trigger` expressions receive a single root variable
  `event` bound to a `NormalizedEvent` object.
- **Versioning:** breaking changes require `docs/normative/normalized-event/v2/`.

## Adapters

Input drivers map native forge events into this struct:

| Driver | Source |
|--------|--------|
| `gha-event` | `GITHUB_EVENT_PATH` + `gh` snapshot for labels and change-proposal metadata |
| `json` | stdin or `--input-file` (tests, replay) |

Adapters must populate:

- `state.labels` when routing guards or label-based triggers apply.
- `state.change_proposal` (including `head_ref`, `base_ref`, and `head_sha` when
  known) whenever a matched harness needs change-proposal execution context.
  Webhook payloads are often incomplete — adapters should fill gaps via forge
  API calls before dispatch.

## Examples

See [`examples/`](examples/).

## Execution ref projection

`fullsend dispatch` projects each matched harness to the **execution ref**
consumed by existing agent workflows and `fullsend run` (unchanged CLI
contract):

| Execution ref field | Source in `NormalizedEvent` |
|---------------------|----------------------------|
| `source_repo` | `repo` |
| `event_type` | `source.raw_type` |
| `event_payload.issue` | `entity` when `entity.kind == "work_item"`: `{number: entity.id, html_url: entity.url}` |
| `event_payload.pull_request` | See below |
| `event_payload.comment` | `transition.comment` when present: `{body: transition.comment.body}` |
| `trigger_source` (fix only) | `transition.review.reviewer_id` when `transition.kind == "review_submitted"`; else `actor.id` when `transition.comment.command == "/fs-fix"` |

**`event_payload.pull_request`** (GitHub-shaped, for backward compatibility):

When `entity.kind == "change_proposal"`:

```json
{
  "number": "<entity.id>",
  "html_url": "<entity.url>",
  "head": {
    "ref": "<state.change_proposal.head_ref>",
    "sha": "<state.change_proposal.head_sha>",
    "repo": { "full_name": "<state.change_proposal.head_repo>" }
  },
  "base": {
    "ref": "<state.change_proposal.base_ref>",
    "repo": { "full_name": "<state.change_proposal.base_repo>" }
  }
}
```

When `entity.kind == "work_item"` and `entity.linked_change_proposal` is set
(e.g. GitHub `issue_comment` on a PR), emit **both** `issue` from `entity` and
`pull_request` from `linked_change_proposal` + `state.change_proposal` using
the same shape above (`number`/`html_url` from `linked_change_proposal`).

Omit `pull_request` when `state.change_proposal` is absent. Omit `issue` when
the event targets only a change proposal with no work-item carrier.

`head.sha` may be omitted in the projected payload when `head_sha` is unset;
downstream workflows may still resolve refs via forge API as a fallback.

No execution-ref field requires information outside this schema when adapters
have populated `state.change_proposal` for change-proposal workloads.
