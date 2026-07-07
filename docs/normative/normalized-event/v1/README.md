# NormalizedEvent v1

Forge-neutral routing input for `fullsend dispatch` and harness CEL `trigger`
expressions ([ADR 0061](../../../ADRs/0061-harness-cel-dispatch.md)).

The field names and transition vocabulary are forge-neutral; **v1 normative
scope covers GitHub, GitLab, and Jira** (see [Scope](#scope-v1)).

## Contract

- **Schema:** [`normalized-event.schema.json`](normalized-event.schema.json)
- **CEL context:** harness `trigger` expressions receive a single root variable
  `event` bound to a `NormalizedEvent` object.
- **Authorization:** `fullsend dispatch` enforces
  [ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)
  as a platform-level gate after normalization and **before** CEL evaluation.
  Harness `trigger` expressions express routing only, not permission policy.

## Scope (v1)

v1 adapters and examples target **GitHub** webhooks, **GitLab** cron-poll
input, and **Jira poll** input:

- `source.system` is `github`, `gitlab`, `jira`, `manual`, or `schedule`.
- `repo` is the target Fullsend repository (`owner/repo` for GitHub,
  `group/subgroup/project` for GitLab) for all systems — including Jira poll
  events (see [jira-poll-adapter.md](jira-poll-adapter.md)).
- The `gha-event` input driver is the production GitHub adapter; `gitlab-poll`
  is the production GitLab adapter ([ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md));
  `jira-poll` is the production Jira poll adapter; `json` supports tests and
  replay.

## Versioning

Breaking changes require `docs/normative/normalized-event/v2/`.

| Change | v1 impact |
|--------|-----------|
| **Breaking** (requires v2): remove or rename fields, change field types, remove enum values, add new required top-level fields, tighten patterns that reject previously valid documents | Consumers must migrate |
| **Non-breaking** (allowed in v1.x schema/README): add optional fields, add new enum values, relax validation, clarify documentation | Existing fixtures and triggers keep working |

Adding a new `transition.kind` is **non-breaking** — CEL triggers use boolean
expressions, not exhaustive enum matching.

## Adapters

Input drivers map native forge events into this struct:

| Driver | Source | v1 status |
|--------|--------|-----------|
| `gha-event` | `GITHUB_EVENT_PATH` + `gh` snapshot for labels and change-proposal metadata | Production |
| `gitlab-poll` | GitLab CI event payload (cron-polled; [ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md)) | Production (poll) |
| `jira-poll` | Jira issue search + changelog/comments since `lastCheck` ([jira-poll-adapter.md](jira-poll-adapter.md), [ADR 0063](../../../ADRs/0063-polling-based-work-discovery.md)) | Production (poll) |
| `json` | stdin or `--input-file` | Tests, replay |

Adapters must populate:

- `state.labels` when routing guards or label-based triggers apply.
- `state.change_proposal` (including `head_ref`, `base_ref`, and `head_sha` when
  known) whenever a matched harness needs change-proposal execution context.
  Webhook payloads are often incomplete — adapters should fill gaps via GitHub
  API calls before dispatch.

### Schedule and manual sources

When `source.system` is `schedule` or `manual`, there is no native webhook
payload. The input driver **must** resolve and populate `entity` (and
`state.change_proposal` when the target is a change proposal) from the scheduled
or operator-specified work item before dispatch proceeds. Schedule drivers must
not emit events with a missing or synthetic entity.

**Authorization:** the platform authorization gate (ADR 0054) treats schedule and
manual dispatch as **trusted operator actions**, not end-user webhook events.
Adapters set `actor.id` to the configured service identity (e.g. the GitHub App
bot or workflow `GITHUB_ACTOR`), `actor.kind` to `bot`, and `actor.role` to the
effective permission of that identity on the target repo (typically `write` for
installed apps). `fullsend dispatch` applies the same permission check as
webhook paths; it does not default schedule/manual actors to `role: none`.

### Transition sub-objects

Transition-specific fields are present only when required by `transition.kind`:

| `transition.kind` | Required sub-object | Forbidden otherwise |
|-------------------|---------------------|---------------------|
| `label_changed` | `label` | `comment`, `review` |
| `comment_added` | `comment` | `label`, `review` |
| `review_submitted` | `review` | `label`, `comment` |
| all other kinds | none | `label`, `comment`, `review` |

The schema enforces presence/absence of transition sub-objects via conditional
`required` / `false` properties. Cross-field ID consistency (below) is
documented here and validated by adapter tests — JSON Schema cannot express
cross-field equality.

### Transition kind vocabulary

| Kind | Use |
|------|-----|
| `opened` | Entity created or first opened |
| `reopened` | Entity reopened after close; adapters MAY map to `opened` when the distinction is unnecessary |
| `edited` | Title/body/metadata edit without new commits |
| `synchronized` | Head branch received new commits (GitHub `synchronize`) |
| `updated` | Legacy umbrella; prefer `edited` or `synchronized` for new adapters |
| `merged` | Change proposal merged into target branch |
| `closed`, `marked_ready`, `label_changed`, `comment_added`, `review_submitted` | As named |

### Comment extraction

For `comment_added`, adapters extract `command` and `instruction` from the
**raw** comment body before applying the 4096-character truncation stored in
`comment.body` (JSON Schema `maxLength` counts Unicode code points). This keeps
slash-command routing and fix instructions intact even when the stored body is
truncated for transport.

This moves instruction extraction from downstream workflow steps (e.g.
`reusable-fix.yml`) into the input adapter — a behavioral change called out in
[ADR 0061 Consequences](../../../ADRs/0061-harness-cel-dispatch.md).

### Actor role mapping (GitHub)

`actor.role` uses permission levels aligned with
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)
and the GitHub collaborator permission API:

| `actor.role` | GitHub permission | Typical use in triggers |
|--------------|-------------------|-------------------------|
| `admin` | admin | Full repo control |
| `maintain` | maintain | Settings without destructive admin |
| `write` | write (member) | Push, label, comment |
| `triage` | triage | Label and moderate without write |
| `read` | read | Read-only collaborator |
| `none` | none | Authenticated user without explicit repo permission |
| `external` | — | Actor outside the repository (fork PR author, drive-by commenter) |

Adapters populate `role` from the GitHub collaborator permission API for human
actors. For **GitHub App bots**, use the installation's effective permission on
the repository (typically `write`), not `none` — the collaborator API often
returns 404 for `[bot]` accounts even when the app has write access via
installation token.

### Fork security (`state.change_proposal.is_fork`)

`is_fork` is `true` when `head_repo` differs from `base_repo` (fork-based
change proposal). Write-capable agents (code, fix) that push commits or open
follow-up PRs **must** gate on `!state.change_proposal.is_fork` in harness
`trigger` expressions or rely on dispatch-level authorization per ADR 0054.
Read-only agents (triage, review, retro) may run on fork PRs when policy allows.

## CEL trigger examples

Harness `trigger` expressions are CEL booleans over `event`:

```cel
// Triage on new issues
event.entity.kind == "work_item" && event.transition.kind == "opened"

// Code when ready-to-code label added
event.transition.kind == "label_changed"
  && event.transition.label.name == "ready-to-code"
  && event.transition.label.action == "added"

// Fix on review changes requested
event.transition.kind == "review_submitted"
  && event.transition.review.state == "changes_requested"

// Fix on /fs-fix slash command (non-fork PR)
event.transition.kind == "comment_added"
  && event.transition.comment.command == "/fs-fix"
  && !event.state.change_proposal.is_fork
```

See [`examples/`](examples/) for matching `NormalizedEvent` fixtures.

## Examples

See [`examples/`](examples/).

## Execution ref projection

`fullsend dispatch` projects each matched harness to the **execution ref**
consumed by existing agent workflows and `fullsend run` (unchanged CLI
contract):

| Execution ref field | Source in `NormalizedEvent` |
|---------------------|----------------------------|
| `source_repo` | `repo` |
| `event_type` | `source.raw_type` (native event name from the source forge; see notes below) |
| `event_action` | `source.raw_action` when present |
| `event_payload.issue` | `entity` when `entity.kind == "work_item"`: `{number: entity.id, html_url: entity.url}` |
| `event_payload.pull_request` | See below |
| `event_payload.comment` | `transition.comment` when present: `{body: transition.comment.body}` |
| `trigger_source` (fix agent only) | See below |
| `status-repo` | `repo` |
| `status-number` | `entity.id` |
| `project_number` | Not in `NormalizedEvent`; prioritize agent reads `PRIORITIZE_PROJECT_NUMBER` from workflow env |
| `run-url` | Runtime-only; set by the dispatch workflow, not projected from `NormalizedEvent` |

**`event_type` / `pull_request_target`:** v1 preserves the GitHub Actions event
name in `source.raw_type`. When the workflow runs on `pull_request_target`,
adapters emit `raw_type: "pull_request_target"` (not normalized to
`pull_request`) so downstream routing matches today's dispatch behavior.

**`trigger_source` (fix agent only):** this field is emitted only for the fix
harness execution ref. When `transition.kind == "review_submitted"`, set
`trigger_source` to `transition.review.reviewer_id` (the bot that requested
changes). When `transition.comment.command == "/fs-fix"`, set `trigger_source`
to `actor.id` (the human or bot that invoked the command). Omit
`trigger_source` for all other agents and transitions.

**`event_payload.pull_request`** (GitHub-shaped, for backward compatibility):

When `entity.kind == "change_proposal"`:

```json
{
  "number": 99,
  "html_url": "https://github.com/org/repo/pull/99",
  "head": {
    "ref": "feature-branch",
    "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "repo": { "full_name": "org/repo" }
  },
  "base": {
    "ref": "main",
    "repo": { "full_name": "org/repo" }
  }
}
```

(`number` is JSON integer; substitute from `entity.id` and related fields.)

When `entity.kind == "work_item"` and `entity.linked_change_proposal` is set
(e.g. GitHub `issue_comment` on a PR), emit **both** `issue` from `entity` and
`pull_request` from `linked_change_proposal` + `state.change_proposal` using
the same shape above (`number`/`html_url` from `linked_change_proposal`).

**Change-proposal identity:** when `state.change_proposal` is present,
`state.change_proposal.id` MUST equal `entity.id` if
`entity.kind == "change_proposal"`, or `entity.linked_change_proposal.id` if
the work item carries a linked change proposal. Adapters MUST NOT populate
conflicting IDs across these fields. When `entity.kind == "work_item"` and
`state.change_proposal` is present, `entity.linked_change_proposal` is required
(schema-enforced).

Omit `pull_request` when `state.change_proposal` is absent. Omit `issue` when
the event targets only a change proposal with no work-item carrier.

`head.sha` may be omitted in the projected payload when `head_sha` is unset;
downstream workflows may still resolve refs via GitHub API as a fallback.

No execution-ref field requires information outside this schema when adapters
have populated `state.change_proposal` for change-proposal workloads, except
`project_number` (prioritize env) and `run-url` (runtime).

**GitLab event projection:** the table above uses GitHub-oriented examples, but
the same projection logic applies to GitLab events. `source.raw_type` carries
the GitLab object type (e.g. `merge_request`, `note`), and
`event_payload.pull_request` uses the same GitHub-shaped structure for backward
compatibility — the GitLab adapter maps MR fields into this shape so downstream
workflows do not need forge-specific handling.

## GitLab adapter notes

GitLab is a normative v1 source system ([gitlab-implementation.md](../../../problems/gitlab-implementation.md)):

| Concern | Mapping |
|---------|---------|
| Input driver | `gitlab-poll` from GitLab CI event payload (cron-polled or `merge_request_event`; see [ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md)) |
| `source.system` | `gitlab` |
| `repo` slug | Nested group path (`group/subgroup/project`) — `repo_path` pattern supports multi-segment paths |
| MR events | `merge_request_event` → `entity.kind: change_proposal` |
| MR merge | `merge_request_event` (state=merged) → `transition.kind: merged` (primary path for retro-stage dispatch; GitLab merge and close are distinct events) |
| Notes | `note` → `transition.kind: comment_added` |
| Role mapping | Guest→`read`, Reporter→`triage`, Developer→`write`, Maintainer→`maintain`, Owner→`admin` |
