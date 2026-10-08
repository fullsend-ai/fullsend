# NormalizedEvent v1

Forge-neutral routing input for `fullsend dispatch` and harness CEL `trigger`
expressions ([ADR 0061](../../../ADRs/0061-harness-cel-dispatch.md)).

The field names and transition vocabulary are forge-neutral; **v1 normative
scope covers GitHub, GitLab, and Jira** (see [Scope](#scope-v1)).

## Contract

- **Schema:** [`normalized-event.schema.json`](normalized-event.schema.json)
- **CEL context:** harness `trigger` expressions receive a single root variable
  `event` bound to a `NormalizedEvent` object. This describes the currently
  shipped event-backed path. [ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md)
  adopts a future entity-first context with required `entity` and nullable
  `event`; its field-level contract remains follow-up versioned work.
- **Authorization:** `fullsend dispatch` enforces the
  [Authorization Contract v1](../../authorization/v1/) as a platform-level gate
  after normalization and **before** CEL evaluation. Harness `trigger`
  expressions express routing only, not permission policy. This authorization
  statement applies to the event-backed path; Authorization Contract v1 also
  defines the trusted-origin gate for future entity discovery. The historical
  decision is recorded in
  [ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md).

> **ADR 0107 target contract — not yet implemented:** The bot-specific actor
> fields and lookup semantics described below are normative design, but current
> adapters and Go event types have not yet been migrated to emit or enforce
> them. Existing compatibility behavior remains authoritative until migration.

## Scope (v1)

v1 adapters and examples target **GitHub** webhooks, **GitLab** cron-poll
and native-webhook input, and **Jira poll** input:

- `source.system` is `github`, `gitlab`, `jira`, `manual`, or `schedule`.
- `repo` is the target Fullsend repository (`owner/repo` for GitHub,
  `group/subgroup/project` for GitLab) for all systems — including Jira poll
  events (see [jira-poll-adapter.md](jira-poll-adapter.md)).
- The `gha-event` input driver is the production GitHub adapter; `gitlab-poll`
  is the production GitLab poll adapter ([ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md));
  `gitlab-webhook` is the GitLab native-webhook adapter, provisioned at
  install time once its readiness gates hold and not yet live-validated
  ([ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md));
  `jira-poll` is the production Jira poll adapter; `json` supports tests and
  replay.
- `entity.kind: conversation` covers GitHub Discussions and future chat
  systems ([ADR 0086](../../../ADRs/0086-conversation-surface-for-agent-participation.md)).
  A conversation is the container (Discussion / Slack channel). Threading is
  expressed on the message via `transition.comment.id` and
  `transition.comment.parent_id` (always the thread-root id; equal to `id` for
  top-level messages). GitHub Discussion adapters are planned under
  `gha-event`; additional `source.system` values (e.g. Slack) are non-breaking
  enum additions when those drivers land.
- Conversations carry **`state.conversation.category`** (exactly one; 1:M
  category→conversation) separately from **`state.labels`** (M:M tags on the
  conversation). Threads and messages carry neither category nor labels;
  `comment_added` events still snapshot the parent conversation's category and
  labels and MUST include `transition.comment.id` and `parent_id`.

## Versioning

Breaking changes require `docs/normative/normalized-event/v2/`.

| Change | v1 impact |
|--------|-----------|
| **Breaking** (requires v2): remove or rename fields, change field types, remove enum values, add new required top-level fields, tighten patterns that reject previously valid documents | Consumers must migrate |
| **Non-breaking** (allowed in v1.x schema/README): add optional fields, add new enum values, relax validation, clarify documentation | Existing fixtures and triggers keep working |

The new optional actor fields are additive, but consumers that vendor this
schema with `additionalProperties: false` must refresh their copy before an
adapter emits `bot_role` or `role_verified`; otherwise their local validator
may reject an otherwise valid v1 event. Fullsend controls the supported
NormalizedEvent producers and consumers, so the bundled validator and other
supported consumers MUST be updated to this v1 schema before any adapter emits
the new fields. Older strict schema copies are not a supported
producer/consumer combination after that rollout. v1 compatibility means that
existing events without these fields remain valid; it does not require a
stale `additionalProperties: false` schema to accept newly emitted fields.
The JSON Schema's cross-field rules are currently stricter than the Go
`normevent.Validate()` implementation. The adapter migration MUST bring the Go
validator to parity before emitting the new fields; until then the schema
remains the normative stricter check.

Adding a new `transition.kind` is **non-breaking** — CEL triggers use boolean
expressions, not exhaustive enum matching.

## Adapters

Input drivers map native forge events into this struct:

| Driver | Source | v1 status |
|--------|--------|-----------|
| `gha-event` | `GITHUB_EVENT_PATH` + `gh` snapshot for labels and change-proposal metadata | Production; Discussions → `entity.kind: conversation` planned ([ADR 0086](../../../ADRs/0086-conversation-surface-for-agent-participation.md)) |
| `gitlab-poll` | GitLab CI event payload (cron-polled; [ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md)) | Production (poll) |
| `gitlab-webhook` | `TRIGGER_PAYLOAD`, a file-type CI/CD variable whose value is a path to the webhook body delivered by a native "use a webhook" pipeline trigger pinned to the protected default branch: the file's contents are **untrusted hinting only** (resource IDs, event type). The adapter MUST accept only GitLab's own file-type `TRIGGER_PAYLOAD` variable — never a caller-supplied path override, and never following a symlink out of the runner-provided location — read the file, and re-fetch the referenced entities from the GitLab API. Provenance splits by field: `entity`, `state`, `actor`, and `state.labels` MUST come **solely** from the project-pinned API re-fetch, failing closed on any missing or mismatched resource. `transition.kind` (and `source.raw_type`/`raw_action`) has no API-snapshot equivalent — a resource `GET` cannot distinguish opened/labeled/synchronized/merged for an MR, or added/edited for a note — so it MAY be taken from the payload's object-kind/action hint **only after** the payload action is mapped onto the v1 transition vocabulary and validated by the fail-closed consistency checks enumerated in [GitLab webhook transition provenance](#gitlab-webhook-transition-provenance-gitlab-webhook) below. Any action that contradicts the snapshot, or that cannot be checked against it, MUST fail closed. Required transition sub-objects follow the same field-split: `transition.comment` and `transition.review` MUST be populated from the re-fetch, while `transition.label` (`name` and `action`, which a current-state snapshot cannot recover) is taken from the payload's `changes.labels` diff and admitted only after the present/absent snapshot check — see the subsection for the per-label fan-out. This is **not** a pure snapshot re-fetch — the payload action is untrusted until the check passes. The enumerated checks are *current-state* predicates and do not by themselves prove the action just occurred, so **snapshot-consistent replay** remains a named residual trigger-token risk (see the subsection); the adapter MUST use event-time evidence to bind the action **and the actor that performed it** to a recent occurrence and fail closed when no matching event links them. For transitions whose snapshot carries no actor (e.g. `label_changed`), GitLab's resource label/state events — which record user, action, and time — are a **required** part of the re-fetch; a users/members `GET` of the payload-named actor is **not** sufficient actor provenance. **Actor-to-transition attribution** is a distinct ship-gating residual, separate from replay (see the subsection). Any identifier read from the payload MUST be validated against its expected format before use in an API path; moreover every re-fetch URL MUST be constructed from the pinned API host (the `CI_JOB_TOKEN` job record's project, plus the base URL once its pin is specified) combined with those validated identifiers, and the adapter MUST NOT follow any URL, host, or `path_with_namespace` taken from the payload (e.g. `project.web_url`, `object_attributes.url`, `repository.git_http_url`, `project.path_with_namespace`) — doing so would turn the payload into SSRF against the PAT-bearing client and would bypass even a future base-URL pin. The payload's contents MUST NOT be logged or echoed. **Project identity** MUST come from the running job's `CI_JOB_TOKEN` job record (`GET /api/v4/job`) as the **sole** project-identity source — never from the trigger-overridable `CI_PROJECT_ID`, `CI_API_V4_URL`, `CI_SERVER_URL`, or `FULLSEND_GITLAB_URL` environment values, which the adapter MUST ignore for identity. An `id_tokens` JWT is **not** an independent or interchangeable identity source (it lands in an ordinary env var a same-named trigger variable can outrank); if a JWT is used at all it MUST be **bound** to the `CI_JOB_TOKEN`-authenticated job record by matching its `job_id`/`project_id` claims — in addition to JWKS signature verification — and MUST NOT stand alone. **Host/base-URL** pinning is not yet specified — [ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md) tracks it as an open, ship-gating residual risk in its Consequences section, and this adapter MUST NOT be implemented until that gap is closed | Planned (webhook fast-path) |
| `jira-poll` | Jira issue search + changelog/comments since `lastCheck` ([jira-poll-adapter.md](jira-poll-adapter.md), [ADR 0063](../../../ADRs/0063-polling-based-work-discovery.md)) | Production (poll) |
| `json` | stdin or `--input-file` | Tests, replay |

Adapters must populate:

- `state.labels` when routing guards or label-based triggers apply.
- `state.conversation` (with `category.name` at minimum) whenever
  `entity.kind` is `conversation` — including message/`comment_added` events.
  Do not encode the category as a synthetic label. On comment transitions,
  also set `transition.comment.id` and `parent_id` (`parent_id == id` for
  thread-root messages; otherwise the root id).
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

**Authorization:** the platform authorization gate
([Authorization Contract v1](../../authorization/v1/);
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md))
treats schedule and manual dispatch as **trusted operator actions**, not
end-user webhook events.
Adapters set `actor.id` to the configured operator or service identity (e.g. a
GitHub App bot or workflow `GITHUB_ACTOR`) and classify `actor.kind` as either
`human` or `bot` using authoritative provider metadata. Under [ADR
0107](../../../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md),
human identities use verified forge permissions, while bot identities may
resolve `actor.bot_role` and retain `actor.role: none` for v1 compatibility.
Authorization therefore follows the identity kind: human permission thresholds
for humans, and recognized bot identity for bots.
`fullsend dispatch` applies the same identity lookup as webhook paths.
If that lookup returns no recognized role or fails/unverifiable, authorization
is denied. These lookup and denial rules are ADR 0107 target behavior and are
not yet enforced by the production runtime.

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

For human actors, `actor.role` uses permission levels aligned with
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
actors. Current adapters may also populate a GitHub App bot's legacy forge
permission in `actor.role`, and that representation remains valid in
NormalizedEvent v1 while the adapter has not adopted ADR 0107. Once an adapter
adopts ADR 0107, it MUST use the provider's authoritative bot
classification, emit `role: none` for bots, and use the provider-backed
optional `bot_role` lookup instead. Both representations are therefore
intentional v1 compatibility forms; the adapter implementation determines
which one it emits.

`actor.role_verified` is an optional additive field. For humans, it is true
only when `actor.role` is a trusted forge permission. For bots, it is true
when the bot-role lookup completed, whether it found a role or not. A
recognized bot has `role_verified: true` and a provider-resolved `bot_role`; an
unknown bot has `role_verified: true` and an absent or null `bot_role`; failed
resolution has `role_verified: false` and no `bot_role`.

For a label-added event, the permanent label authorization exception does not
require bot-role lookup. On that path, the provider's positive bot
classification and the forge-authorized label mutation are sufficient; an
absent `role_verified` field means that bot-role lookup was not applicable. At
the time of this ADR, GitHub is the only production adapter implementing this
exception; other source adapters must provide equivalent authoritative label
and actor-transition evidence before enabling the target behavior.
Whether an event uses the legacy representation is determined by the adapter's
implementation contract, not inferred from field omission alone.

This is the ADR 0107 target representation; it is not yet emitted by the
production adapters or available as a runtime CEL field.

### Fork security (`state.change_proposal.is_fork`)

`is_fork` is `true` when `head_repo` differs from `base_repo` (fork-based
change proposal). Write-capable agents (code, fix) that push commits or open
follow-up PRs **must** gate on `!state.change_proposal.is_fork` in harness
`trigger` expressions or rely on dispatch-level authorization per ADR 0054.
Read-only agents (triage, review, retro) may run on fork PRs when policy allows.

### Change facts (`state.change_proposal.diff`, `state.change_proposal.linked_work_items`)

Optional and added for
[ADR 0112](../../../ADRs/0112-overlays-may-set-any-harness-field.md)'s routing; no v1
adapter populates them yet. When known, adapters set
`state.change_proposal.diff` (file count, additions, deletions and changed
paths at `head_sha`, with `paths_complete` false when the forge truncated the
list, in which case a check for a path's absence should treat it as unknown)
and `linked_work_items` (each linked work item's id, optional key, state and
labels).
Harness `trigger` and `overlays:` expressions may read them, for example to
route `model` and `effort` per task; they are untrusted (see below)
([ADR 0112](../../../ADRs/0112-overlays-may-set-any-harness-field.md)). A
`trigger` that reads them lets the change's author decide whether a harness
runs, so security-relevant harnesses shouldn't trigger on them.

### Trusted fields

A field is **trusted** when neither the change's or entity's author nor an
actor below the permission level [ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)
requires can choose its value beyond what their permission already allows
(for example, only an author with write access can make `is_fork` false). An
overlay that sets a guarded harness field may read only these fields, `runtime.forge` and `config`
([ADR 0112](../../../ADRs/0112-overlays-may-set-any-harness-field.md)):

- `repo`
- `source.system`, and `entity.source.system` in the entity context of
  [ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md)
- `actor.role`
- `state.change_proposal.base_repo` and `is_fork`

Every other field, including a field added to this schema later, is untrusted
until it is listed here. For example `transition.comment` (its `body`,
`command` and `instruction` all come from the comment text), the action fields
(`transition.kind`, `source.raw_type`, `source.raw_action`, `entity.kind`),
`actor.kind` (some adapters infer it from a display name),
`actor.is_entity_author` (the author makes it true by acting), `state.labels`,
`base_ref` (the author picks the target branch), `head_*` and the change facts
are untrusted. A guarded overlay may test `has()` only on objects the schema
always requires, such as `source`; reading `is_fork` when there is no change
proposal is simply no match. `actor.role` says who triggered the event, not
who wrote the change, so an overlay that loosens a guarded value on a change
proposal should also require `!state.change_proposal.is_fork`.

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

// Conversation-native agent on Discussion slash command (ADR 0086)
event.entity.kind == "conversation"
  && event.transition.kind == "comment_added"
  && has(event.transition.comment.command)
  && event.transition.comment.command == "/fs-vouch"
  && has(event.state.conversation.category.slug)
  && event.state.conversation.category.slug == "vouch-request"
```

See [`examples/`](examples/) for matching `NormalizedEvent` fixtures. The
existing top-level fixtures retain the legacy v1 representation; target
fixtures for ADR 0107 are under
[`examples/adr-0107/`](examples/adr-0107/).

## Examples

See [`examples/`](examples/). The `examples/adr-0107/` subdirectory contains
target representations and is not emitted until an adapter adopts the ADR 0107
target implementation.

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
| `event_payload._normalized_event` | Complete `NormalizedEvent` embedded by `buildEventPayload` for CEL overlay resolution during `fullsend run` (#6748) |
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
| Input driver | `gitlab-poll` (production, cron-polled; [ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md)) and `gitlab-webhook` (fast-path, provisioned at install time behind readiness gates, with live-GitLab validation still outstanding, from the file `TRIGGER_PAYLOAD` points to, re-fetched with a `CI_JOB_TOKEN`-pinned project identity — host/base-URL pin still open — and fail-closed on mismatch; see the Adapters table above; [ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)) |
| `source.system` | `gitlab` |
| `repo` slug | Nested group path (`group/subgroup/project`) — `repo_path` pattern supports multi-segment paths |
| MR events | Cron-polled MR → `entity.kind: change_proposal` (native `merge_request_event` dispatch removed in [#7322](https://github.com/fullsend-ai/fullsend/issues/7322); see [ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md)) |
| MR opened | Cron poll (`created_at` > watermark) → `transition.kind: opened` (review) |
| MR merge | Cron poll (`merged_at` > watermark) → `transition.kind: merged` (retro; GitLab merge and close are distinct events) |
| MR closed (unmerged) | Cron poll (`closed_at` > watermark, `merged_at` empty) → `transition.kind: closed` (retro) |
| Notes | `note` → `transition.kind: comment_added` |
| Role mapping | Guest→`read`, Reporter→`triage`, Developer→`write`, Maintainer→`maintain`, Owner→`admin` |
| Bot identity | GitLab provider metadata may classify project-token actors as bots, but bot classification alone does not provide the registered `bot_role` required for non-label bot dispatch; an equivalent provider lookup is required. |

The MR/note rows above are the **cron-poll** watermark mappings. The webhook
fast-path derives the transition from the payload action instead of a
watermark — see the next subsection.

#### GitLab webhook transition provenance (`gitlab-webhook`)

For the webhook fast-path, `transition.kind` (and `source.raw_type`/
`raw_action`) is derived from the payload's `object_kind`/action hint mapped
onto the v1 transition vocabulary, then validated against the project-pinned
re-fetch. GitLab action names are **not** the v1 vocabulary and MUST be
mapped:

| `object_kind` | payload action | `transition.kind` | Snapshot check (else MUST fail closed) |
|---------------|----------------|-------------------|----------------------------------------|
| `merge_request` | `open` | `opened` | MR exists and `state == "opened"` |
| `merge_request` | `reopen` | `reopened` (or `opened`) | MR exists and `state == "opened"` |
| `merge_request` | `merge` | `merged` | `state == "merged"` and `merged_at` set |
| `merge_request` | `close` | `closed` | MR is currently closed (`state == "closed"`), `closed_at` set, and `merged_at` empty |
| `merge_request` | `update` + `changes.labels` | `label_changed` (one per label diffed) | each named label present (add) / absent (remove) as claimed |
| `merge_request` | `update` (other) | `synchronized`/`edited`/`marked_ready` | **uncheckable as a unit → MUST fail closed** |
| `note` | create | `comment_added` | referenced note exists in the re-fetch |
| `issue` | `open`/`reopen` | `opened`/`reopened` | issue exists and `state == "opened"` |
| `issue` | `close` | `closed` | issue exists and `state == "closed"` |
| `issue` | `update` + `changes.labels` | `label_changed` (one per label diffed) | each named label present (add) / absent (remove) as claimed |
| `issue` | `update` (other) | — | **uncheckable as a unit → MUST fail closed** |

**Dispatch stack and implementation status.** The webhook event builder
(`BuildWebhookEvents` in `internal/poll/webhook.go`, [#7770](https://github.com/fullsend-ai/fullsend/issues/7770))
reuses the **poll** stack (`RoutableEvent` → `toNormalizedEvent` →
`dispatch.HarnessRouter` → the ADR 0131 signed pipeline-input dispatch), not
the CEL spine over `normevent.Event`. That way the fast-path and the poller
backstop share one authorization gate, one `RoutableEvent.Key()`
deduplication scheme, and one launch transport. A `dispatch.NormalizedEvent`
→ `normevent.Event` bridge belongs to any later work that moves GitLab onto
the CEL spine. The builder only takes the project from its caller-pinned
client, so the base-URL and `CI_JOB_TOKEN` identity-pin requirements above
fall on the driver that wires it in ([#7773](https://github.com/fullsend-ai/fullsend/issues/7773)).
That driver MUST NOT ship until those residuals close. Current builder
behavior, all of it fail-closed:

- Rows whose actor has no event-time evidence in the re-fetch are rejected:
  `merge_request`/`reopen` and `issue`/`reopen`, which need resource state
  events.
- `merge_request` + `changes.labels` is rejected because the poll stack has
  no MR label transition.
- Label removals are validated and then dropped, because the poll stack
  routes only label additions.
- Every re-fetched event time must fall within a 30-minute window. This
  narrows replay but does not close it.

The `MUST fail closed` cells above have **no implementer-defined extension
point**: an `update` action that is not resolved by a row in this table MUST
fail closed. Any future named sub-check (e.g. draft→ready for `marked_ready`)
MUST be added as an **explicit row in this spec**, not invented at
implementation time. The `merge_request`/`close` row requires the current
`state == "closed"` in addition to the timestamps because GitLab can leave a
stale `closed_at` on a reopened MR; the production poller already guards on
`state == "closed"` for the same reason (`internal/poll/events.go`), and
timestamps alone would admit a `closed` transition against an open (reopened)
MR.

GitLab emits no dedicated `labeled` action; label changes arrive on both
merge requests and issues as an `update` with `changes.labels`, so the
adapter MUST fan that case out to `label_changed` and MUST NOT accept a bare
`update` (no `changes.labels`) as a routable transition. `changes.labels` is
a previous/current array that can add and remove several labels in one
webhook; the adapter MUST emit **one `NormalizedEvent` per label diffed**
(each carrying its own `transition.label`), or fail closed when the diff
cannot be resolved to discrete label add/remove pairs.

Transition sub-object provenance: `transition.comment` (`id`, `body`,
`command`, `instruction`) and `transition.review` MUST come from the
re-fetch, not the payload. `transition.label` is the exception — a
current-state snapshot cannot recover *which* label changed or *how* — so
its `name` **and** `action` (`added`/`removed`, both schema-required) are
derived from the payload's `changes.labels` diff, admitted only **after** the
present/absent snapshot check confirms the claimed end state (named label
present for `added`, absent for `removed`). The derivation MUST use GitLab's
real wire strings, not the v1 vocabulary:

- **`transition.label.name`** is the label object's **`title`** field.
  GitLab's webhook and REST label objects key the human label on `title`,
  not `name`; the adapter diffs `changes.labels` `previous`/`current` on
  `title` and maps the diffed `title` onto `transition.label.name`.
- **`transition.label.action`** (`added`/`removed`) is derived from the
  set difference of that diff: a `title` in `current` but not `previous`
  is `added`; one in `previous` but not `current` is `removed`.
- **Actor binding** (the actor-to-transition MUST above) matches against
  GitLab **resource label events**, whose fields are distinct again:
  `action` is **`add`**/**`remove`** (imperative, not the schema's
  `added`/`removed`) and the label is on **`label.name`**. The adapter MUST
  match a resource label event whose `action == "add"` (for an `added`
  transition) or `action == "remove"` (for `removed`) and whose
  `label.name` equals the diffed `title`, failing closed when none links the
  claimed actor to the change.

Copying `.name` straight off the webhook object, or equality-matching the
schema's `added`/`removed` against a resource label event's `add`/`remove`,
fail-closes every legitimate `label_changed`.

**Residual replay risk.** The snapshot checks above are *current-state*
predicates — they reject an action that contradicts the resource's state,
but they do not by themselves prove the action *just occurred*. Where the
`CI_JOB_TOKEN`-pinned re-fetch exposes event-time evidence (resource events,
system notes, or a timestamp comparable to a per-project watermark), the
adapter MUST use it to bind the claimed action to a recent occurrence and
MUST fail closed when the action is not uniquely determined. Absent such
evidence, a trigger-token holder can re-fire a snapshot-consistent
historical action (e.g. replay an old note as `comment_added`); this
**snapshot-consistent replay** is a named residual trigger-token risk that
the poller/dedup backstop narrows but does not fully close, and
[ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)
gates the fast-path on it.

**Residual actor-attribution risk.** Distinct from replay, the snapshot
checks bind the *action* to current state but do **not** establish that the
payload-named *actor* performed *this* transition. A `label_changed` whose
label is already present, or any transition confirmed only by a users/members
`GET` of the payload-named actor, lets a token holder attribute the change to
any write-access member and pass authorization. The adapter MUST bind the
actor to the specific transition via event-time evidence (GitLab's
[resource label events](https://docs.gitlab.com/api/resource_label_events/)
and resource state events, which record user, action, and time) and MUST fail
closed when no matching event uniquely links the named actor to the claimed
change. **Actor-to-transition attribution is a ship-gating residual** that
remains open even if replay is fully closed;
[ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md) gates
the fast-path on it.
