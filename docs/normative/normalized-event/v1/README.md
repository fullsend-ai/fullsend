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

> **ADR 0107 target contract — partially implemented:** The bot-specific actor
> fields and lookup semantics described below are normative design. The Go
> event types (`bot_role`, `role_verified` on the actor) and the dispatch
> authorization gate that enforces them exist, but no production adapter emits
> the fields yet. Existing compatibility behavior remains authoritative until
> adapters are migrated.

## Scope (v1)

v1 adapters and examples target **GitHub** webhooks, **GitLab** cron-poll
and native-webhook input, and **Jira poll** input:

- `source.system` is `github`, `gitlab`, `jira`, `manual`, or `schedule`.
- `repo` is the target Fullsend repository (`owner/repo` for GitHub,
  `group/subgroup/project` for GitLab) for all systems — including Jira poll
  events (see [jira-poll-adapter.md](jira-poll-adapter.md)).
- The `gha-event` input driver is the production GitHub adapter; `gitlab-poll`
  is the production GitLab poll adapter ([ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md));
  `gitlab-webhook` is the shipped GitLab native-webhook fast-path adapter,
  provisioned at install time once its readiness gates hold
  ([ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)).
  Live issue-to-agent validation passed on GitLab.com Free and self-managed
  EE. The earlier self-managed CE issue-to-agent runs used a
  patched agents helper. In the latest CE rerun, the poller passed, but the
  sandbox SSRF guard stopped the agent runs before they read the issue. CE
  is non-gating for this validation (see
  [Validation status](#gitlab-webhook-validation-status));
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

Normalized-event and entity-context-snapshot v1 carry independently versioned
copies of the actor-role vocabulary. The entity-context-snapshot `bot` marker
is a non-authority kind marker, not a normalized-event repository permission,
and must not be projected into `event.actor.role`; adapters validate each
document against its own schema.

## Adapters

Input drivers map native forge events into this struct:

| Driver | Source | v1 status |
|--------|--------|-----------|
| `gha-event` | `GITHUB_EVENT_PATH` + `gh` snapshot for labels and change-proposal metadata | Production; Discussions → `entity.kind: conversation` planned ([ADR 0086](../../../ADRs/0086-conversation-surface-for-agent-participation.md)) |
| `gitlab-poll` | GitLab CI event payload (cron-polled; [ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md)) | Production (poll) |
| `gitlab-webhook` | `TRIGGER_PAYLOAD`, a file-type CI/CD variable whose value is a path to the webhook body delivered by a native "use a webhook" pipeline trigger pinned to the protected default branch: the file's contents are **untrusted hinting only** (resource IDs, event type). The adapter MUST accept only GitLab's own file-type `TRIGGER_PAYLOAD` variable — never a caller-supplied path override, and never following a symlink out of the runner-provided location — read the file, and re-fetch the referenced entities from the GitLab API. Provenance splits by field: `entity`, `state`, `actor`, and `state.labels` MUST come **solely** from the project-pinned API re-fetch, failing closed on any missing or mismatched resource. `transition.kind` (and `source.raw_type`/`raw_action`) has no API-snapshot equivalent — a resource `GET` cannot distinguish opened/labeled/synchronized/merged for an MR, or added/edited for a note — so it MAY be taken from the payload's object-kind/action hint **only after** the payload action is mapped onto the v1 transition vocabulary and validated by the fail-closed consistency checks enumerated in [GitLab webhook transition provenance](#gitlab-webhook-transition-provenance-gitlab-webhook) below. Any action that contradicts the snapshot, or that cannot be checked against it, MUST fail closed. Required transition sub-objects follow the same field-split: `transition.comment` and `transition.review` MUST be populated from the re-fetch, while `transition.label` (`name` and `action`, which a current-state snapshot cannot recover) is taken from the payload's `changes.labels` diff and admitted only after the present/absent snapshot check — see the subsection for the per-label fan-out. This is **not** a pure snapshot re-fetch — the payload action is untrusted until the check passes. The enumerated checks are *current-state* predicates and do not by themselves prove the action just occurred, so **snapshot-consistent replay** remains a named residual trigger-token risk (see the subsection); the adapter MUST use event-time evidence to bind the action **and the actor that performed it** to a recent occurrence and fail closed when no matching event links them. For transitions whose snapshot carries no actor (e.g. `label_changed`), GitLab's resource label/state events — which record user, action, and time — are a **required** part of the re-fetch; a users/members `GET` of the payload-named actor is **not** sufficient actor provenance. **Actor-to-transition attribution** is a distinct requirement, separate from replay (see the subsection). Any identifier read from the payload MUST be validated against its expected format before use in an API path; moreover every re-fetch URL MUST be constructed from the pinned API host (the `CI_JOB_TOKEN` job record's project, plus the pinned API root described below) combined with those validated identifiers, and the adapter MUST NOT follow any URL, host, or `path_with_namespace` taken from the payload (e.g. `project.web_url`, `object_attributes.url`, `repository.git_http_url`, `project.path_with_namespace`) — doing so would turn the payload into SSRF against the PAT-bearing client and would bypass the API-root pin. The payload's contents MUST NOT be logged or echoed. **Project identity** MUST come from the running job's `CI_JOB_TOKEN` job record (`GET /api/v4/job`) as the **sole** project-identity source — never from the trigger-overridable `CI_PROJECT_ID`, `CI_API_V4_URL`, `CI_SERVER_URL`, or `FULLSEND_GITLAB_URL` environment values, which the adapter MUST ignore for identity. An `id_tokens` JWT is **not** an independent or interchangeable identity source (it lands in an ordinary env var a same-named trigger variable can outrank); if a JWT is used at all it MUST be **bound** to the `CI_JOB_TOKEN`-authenticated job record by matching its `job_id`/`project_id` claims — in addition to JWKS signature verification — and MUST NOT stand alone. **Host/base-URL:** the adapter MUST target the API root from the same identity pin, never `CI_API_V4_URL`, `CI_SERVER_URL`, or `FULLSEND_GITLAB_URL` directly, and MUST fail closed when no pinned API root is available. The shipped dispatcher passes the validated https API root that bootstrapped the `CI_JOB_TOKEN` job lookup (`FULLSEND_PINNED_GITLAB_URL`). That bootstrap value is still in the overridable class, so its trust rests on the `ci_pipeline_variables_minimum_override_role=no_one_allowed` restriction, which install verifies before it provisions the webhook, and on the inherited CI/CD variable trust assumption in the [GitLab configuration guide](../../../guides/getting-started/configuring-gitlab.md#inherited-cicd-variable-trust). A base-URL pin independent of that restriction is not specified ([ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md), Consequences) | Shipped (webhook fast-path); live-validated on GitLab.com Free and self-managed EE; on self-managed CE, earlier runs used a patched agents helper and the latest rerun's agent runs were stopped by the sandbox SSRF guard (CE is non-gating) — see [Validation status](#gitlab-webhook-validation-status) |
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

This applies to the GitHub projection. For GitLab dispatch,
`_normalized_event.transition.comment` carries only `body`; the dispatcher
populates neither `command` nor `instruction`, and `run-agent-job.sh` extracts
the instruction downstream from the body (see
[GitLab dispatch payload](#gitlab-dispatch-payload)).

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

This is the ADR 0107 target representation. The Go `normevent.Actor` type
carries both fields (omitted when unset), `internal/botidentity`
(`ApplyBotRoleResult`) projects a lookup outcome onto them, and the dispatch
gate denies a bot with `role_verified` set but no recognized `bot_role` on
non-label paths. The production adapters do not emit the fields yet, so
triggers that read them must guard with `has()`.

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

For a guarded overlay selecting the authorization-v2 `human_min_role`, raw
`actor.role` is excluded even though it is listed as trusted here. A role
guard must use the [v2 verified effective-role input](../../authorization/v2/README.md#platform-floor-and-per-harness-human-minimum),
and a narrow label grant is not a role. A trusted-field dependency check also
does not establish which truth value is safe: a change author can make
`is_fork` true, while only a writer can make it false.

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
| `event_payload._normalized_event` | Complete `NormalizedEvent` embedded by `buildEventPayload` for CEL overlay resolution during `fullsend run` (#6748); includes `actor.bot_role` and `actor.role_verified` only when the adapter set them (ADR 0107) |
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
the GitLab input adapter follows the same mapping when it converts events.
`source.raw_type` carries the GitLab object type (e.g. `merge_request`, `note`),
and `event_payload.pull_request` uses the same GitHub-shaped structure for
backward compatibility — the GitLab adapter maps MR fields into this shape so
downstream workflows do not need forge-specific handling. The payload that the
GitLab poller and webhook dispatcher put on the dispatch pipeline is a smaller,
separate shape, described next.

#### GitLab dispatch payload

`buildEventPayload` in `internal/poll/dispatch.go` builds the base64
`EVENT_PAYLOAD_B64` payload for GitLab dispatch. It does not embed a complete
NormalizedEvent and emits no `event_payload.comment` and no `note_body`. For a
note event it adds a **partial** `_normalized_event` containing only
`transition`, with `transition.kind` set to `comment_added` and
`transition.comment.body` set to the note body. The envelope fields (`source`,
`kind`, and the rest) are absent, and `transition.comment` omits `command` and
`instruction`. Consumers MUST NOT treat this key as a full NormalizedEvent;
they read `_normalized_event.transition.comment.body` and derive the command
and instruction from it. `run-agent-job.sh` does this for the `/fs-fix`
instruction and the retro comment.

A body that starts with `/fs-` is carried untruncated, and dispatch fails closed
when the payload exceeds the chunk budget. Any other body is bounded to 800
Unicode code points, with a truncation marker appended when it is cut.

## GitLab adapter notes

GitLab is a normative v1 source system ([gitlab-implementation.md](../../../problems/gitlab-implementation.md)):

| Concern | Mapping |
|---------|---------|
| Input driver | `gitlab-poll` (production, cron-polled; [ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md)) and `gitlab-webhook` (shipped fast-path, provisioned at install time behind readiness gates, live-validated on GitLab.com Free and self-managed EE, with CE non-gating — see [Validation status](#gitlab-webhook-validation-status); reads the file `TRIGGER_PAYLOAD` points to, re-fetched with a `CI_JOB_TOKEN`-pinned project identity and API root, and fail-closed on mismatch; see the Adapters table above; [ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)) |
| `source.system` | `gitlab` |
| `repo` slug | Nested group path (`group/subgroup/project`) — `repo_path` pattern supports multi-segment paths |
| MR events | Cron-polled MR or `gitlab-webhook` `merge_request` payload → `entity.kind: change_proposal`. The webhook fast path accepts only the `open`, `merge`, and `close` actions; see [GitLab webhook transition provenance](#gitlab-webhook-transition-provenance-gitlab-webhook) (native `merge_request_event` CI dispatch removed in [#7322](https://github.com/fullsend-ai/fullsend/issues/7322); see [ADR 0067](../../../ADRs/0067-gitlab-cron-polling-event-dispatch.md) and [ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)) |
| Issue opened | Cron poll (`created_at` > both the discovery watermark and a persisted installation cutoff, `state == "opened"`) → `entity.kind: work_item`, `transition.kind: opened`; backstop for a missed webhook delivery, keyed like the webhook's issue open so a delivered webhook is not dispatched twice. The cutoff is set when install/converge seeds the events branch, falling back to the start of the first events poll when none is persisted; it is persisted once and independent of the watermark, so an issue created before installation is never recovered. |
| MR opened | Cron poll (`created_at` > watermark) → `transition.kind: opened` (review) |
| MR pushed | Cron poll (`state == "opened"`, the version that introduced the current head replaced a different head, version `created_at` > watermark) → `transition.kind: synchronized` (review); keyed on the head SHA so a head is dispatched once; the actor is the MR author because diff versions record no pusher, so fork MRs are not dispatched (fork MR re-review is currently unsupported) |
| MR merge | Cron poll (`merged_at` > watermark) → `transition.kind: merged` (retro; GitLab merge and close are distinct events) |
| MR closed (unmerged) | Cron poll (`closed_at` > watermark, `merged_at` empty) → `transition.kind: closed` (retro) |
| Notes | `note` → `transition.kind: comment_added` |
| Role mapping | Guest→`read`, Reporter→`triage`, Developer→`write`, Maintainer→`maintain`, Owner→`admin` |
| Bot identity | GitLab provider metadata may classify project-token actors as bots, but bot classification alone does not provide the registered `bot_role` required for non-label bot dispatch; an equivalent provider lookup is required. |

The issue/MR/note rows above are the **cron-poll** watermark mappings. The webhook
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
| `note` | update | — (ignored, non-routable) | none; an edited comment claims no new transition → MUST be ignored once the payload identifiers validate: no re-fetch, no dispatch, no state write, job succeeds |
| `issue` | `open`/`reopen` | `opened`/`reopened` | issue exists and `state == "opened"` |
| `issue` | `close` | `closed` | issue exists and `state == "closed"` |
| `issue` | `update` + `changes.labels` | `label_changed` (one per label diffed) | each named label present (add) / absent (remove) as claimed |
| `issue` | `update` (other) | — (ignored, non-routable) | none; claims no transition → MUST be ignored: no re-fetch, no dispatch, no state write, job succeeds |

An **ignored** event and a **rejected** event are different outcomes. The
`issue`/`update` (other) row covers a bare issue update with no
`changes.labels` key, such as a title, description, or assignee edit. It
claims no transition, so there is nothing to validate and nothing to route.
The adapter MUST record it as ignored and MUST NOT dispatch, write poll
state, or fail the dispatcher job. It SHOULD log an explicit no-op message.
The `note`/`update` row is the same outcome for an edited issue or merge
request comment. GitLab fires note webhooks for edits as well as new
comments. An edit is not a new `comment_added`, so the adapter MUST ignore
it in the same way. The note ID, `noteable_type`, and issue or merge request
IID are still validated first, and a malformed payload fails closed. Any
other note action is rejected.
A **rejected** event names a transition that the re-fetch contradicts or
cannot check. This includes every `update` that carries `changes.labels`:
a diff that does not resolve to discrete add/remove pairs, a label entry
without a `title`, a claimed end state the re-fetched labels contradict, or
a failed actor or resource-label-event binding. A rejected event MUST fail
closed: the job fails and nothing is dispatched. The presence of
`changes.labels` decides which outcome applies, not whether its contents
are well formed.

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
That driver has shipped. The dispatcher job pins project, ref, and API root
from the `CI_JOB_TOKEN` job record and fails closed when any of them is
unset. `fullsend poll --input-driver gitlab-webhook` requires an explicit
`--project` and has no `CI_PROJECT_PATH` fallback. These requirements stay
normative: when a pin is unavailable the driver MUST fail closed, never fall
back to an overridable variable. Shipping did not close snapshot-consistent
replay, which remains a residual risk (see below). Current builder behavior,
all of it fail-closed except the ignored bare issue update and note edit:

- A bare issue `update` (no `changes.labels`) is ignored. The builder
  returns no events and no error, and the dispatcher job logs a no-op and
  succeeds. A bare `merge_request` `update` still fails closed.
- A note `update` (an edited comment) is ignored the same way once its
  identifiers validate.
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
fail closed. The `issue`/`update` (other) row resolves the bare issue update
as ignored, and the `note`/`update` row resolves a note edit as ignored.
Those are the only ignored rows, and an implementation MUST NOT extend them
to other actions or object kinds. Any future named sub-check (e.g.
draft→ready for `marked_ready`) MUST be added as an **explicit row in this
spec**, not invented at implementation time.
The `merge_request`/`close` row requires the current
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
the poller/dedup backstop narrows but does not fully close.
[ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)
listed it as a fast-path ship gate. The shipped builder narrows it with the
30-minute event-time window and the dedup keys it shares with the poller,
but it remains open: within the window, a token holder can still re-fire a
matching event. Maintainers have
[accepted this residual](https://github.com/fullsend-ai/fullsend/issues/8835#issuecomment-6452638835)
as bounded at-least-once replay; see
[ADR 0125 security-gate status](#adr-0125-security-gate-status).

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
change. This requirement is independent of replay: closing replay would not
satisfy it. [ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)
listed it as a fast-path ship gate. The shipped builder meets it for each row
it accepts. It requires the payload-named actor to equal the actor the
re-fetch records for that transition: the note author, the MR author, merger,
or closer, the issue author or closer, or the user on the latest matching
resource label event. It rejects rows with no event-time actor evidence (both
`reopen` rows). The requirement stays normative: any row added later MUST
bind its actor the same way or fail closed.

#### GitLab webhook validation status

**GitLab.com Free and self-managed EE.** The
[2026-10-20 run](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6301468961)
on `main` at `a87815b6c` passed the same flows on disposable projects in both
environments. A newly opened issue started a webhook-triggered dispatcher
within about two seconds, and its inference-backed triage agent succeeded.
With the webhook removed, the polling schedule dispatched a new issue to a
triage agent that succeeded, and replay found no further dispatches. Trigger
account rotation moved both active schedules to the replacement account
without a second install, and a post-rotation issue reached a successful
triage agent through the webhook. `repos status` reported no drift, and the
trigger deploy key was the only protected-branch push grant Fullsend added,
separate from the Poller identity. Uninstall left no managed hooks,
schedules, variables, trigger tokens, or deploy keys. Some webhook pipelines
triggered by agent-generated issue updates were rejected because the payload
had no `changes.labels`, so the run validates issue-open and polling
dispatch, not issue-update dispatch.

**Self-managed CE.** Runs on an isolated GitLab CE 19.4.1 instance exercised
the same real GitLab CI paths, with inference-backed triage using GitLab CI
OIDC workload identity federation:

- The
  [first CE run](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6307934978)
  on `main` at `6cd9caa58496` passed install and `repos status` with zero
  drift. The Poller, Analyst, Coder, and trigger account had Developer
  access. A newly opened issue reached the webhook dispatcher and a triage
  agent that succeeded. An interrupted trigger-account rotation was
  recovered by a normal `repos install`, after which `repos status` again
  reported zero drift. Uninstall left no managed variables, hooks, triggers,
  schedules, memberships, trigger deploy keys, or trigger push grants.
- The
  [second CE run](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6316757216)
  on `main` at `5740e70511d8` covered the scenarios the first left open.
  With the webhook absent, the event-poll schedule's natural run dispatched
  an issue to a triage agent that succeeded. After
  `--rotate-gitlab-trigger-account` replaced the trigger account, deploy key,
  protected-branch grant, and trigger token, a newly opened issue reached the
  dispatcher and a triage agent that succeeded. The Poller stayed a separate
  Developer identity, `repos status` reported zero drift, and uninstall again
  removed all managed resources.
- The
  [latest CE rerun](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6378158979)
  on 2026-10-24 used a temporary OpenShell 0.1.5 runner image. The poller
  pipeline passed and dispatched the pending triage notes. The two agent
  pipelines created their sandboxes, but OpenShell's SSRF guard denied each
  agent's issue API request because the CE endpoint resolved to a private
  address. The agents stopped before reading any issue data. This is a
  sandbox-network limitation, not a GitLab CE failure. The run neither
  verifies nor refutes stock agents on CE.

**Phase A pause scenarios.** The
[instrumented pause run](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6306072289)
on CE 19.4.1, using `main` at `a87815b6c`, recorded all four
scenarios. It supersedes an
[earlier partial run](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6302935339)
that covered only the first.

1. A paused old-Poller trigger-create request, resumed after the fresh
   account was elevated and the old PAT revoked, returned 403.
2. A paused old-Poller PAT self-rotation returned a new PAT, but that PAT
   belonged to the old Developer user and could not create a trigger.
3. When a test proxy dropped the successful response to a bootstrap
   trigger create, Fullsend quarantined that generation, and the next
   `repos install` did not repeat the create. A request paused *after*
   GitLab's final trigger-creation authorization check still completed
   after the bootstrap PAT was revoked and the account demoted. The trigger
   it created was owned by the replacement account, now at Developer level.
4. A paused bootstrap PAT self-rotation, resumed after revocation and
   demotion, returned a PAT for the Developer-level replacement user, which
   could not create a trigger.

**Custom-port CE qualification.** The CE fixture served GitLab on an HTTPS
port other than 443. The stock agents GitLab triage helper rejected that URL,
so the CE triage runs used an isolated agents patch (`371fdf36cef0`, never
pushed). The gap is tracked in
[fullsend-ai/agents#1732](https://github.com/fullsend-ai/agents/issues/1732).
The patch accepts an optional HTTPS port and sends API calls
to `CI_SERVER_URL`. The CE runs therefore verify the Fullsend install,
webhook, polling, rotation, and cleanup paths on CE with that patch. They do
not verify stock agents on a custom-port CE instance. The port fix merged in
[fullsend-ai/agents#1733](https://github.com/fullsend-ai/agents/pull/1733)
on 2026-10-21, and stock agents were retested on CE the same day. The
[stock-agents CE rerun](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6328485466)
on `main` at `7ac5c8b62` passed install, `repos status`, issue-open webhook
dispatch, natural scheduled-poll dispatch, trigger-account rotation, and
uninstall, and each dispatch launched stock triage. The triage agents could
not reach the custom-host CE instance, because the sandbox policy did not
attach the generated GitLab endpoint
([#8683](https://github.com/fullsend-ai/fullsend/issues/8683), fixed by
[PR #8687](https://github.com/fullsend-ai/fullsend/pull/8687)). On
2026-10-23, one retry
[timed out on an OpenShell DNS mapping expiry](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6374098471)
and another was
[blocked by the sandbox SSRF guard](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6374578361),
as was the 2026-10-24 rerun described above. No stock-agents CE
issue-to-agent run has passed, so stock agents on a custom-port CE instance
remain unverified. Another retest is not required for #8245, because CE is
non-gating (see Validation scope below).

**Validation scope.** CE-specific end-to-end validation is non-gating for
[#8245](https://github.com/fullsend-ai/fullsend/issues/8245). That issue
records the GitLab.com Free and self-managed EE runs as its passing
live-validation evidence, and accounts for the CE results above without
treating them as passed. Running agents against a private-address CE
instance would need an explicitly supported, narrowly scoped sandbox
network rule that preserves the SSRF guard.

Demonstrated dispatch does not relax any MUST in this section. The
fail-closed provenance, identity-pin, and actor-binding requirements above
still apply. Snapshot-consistent replay remains a residual risk.

#### ADR 0125 security-gate status

[ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)
named security gates for the webhook fast path. The fast path has shipped.
This table is the current status of each gate, checked against the code at
`a7f851df9848` and the
[#8245](https://github.com/fullsend-ai/fullsend/issues/8245) record. A gate
is **closed** only with the evidence cited. An **open** gate stays open until
it has evidence or a maintainer records a decision to accept or defer it.
Deleting a gate's text does not close it. A gate closed by an accepted risk
cites the maintainer decision and keeps its residual text. The remaining open
gate is tracked in [#8835](https://github.com/fullsend-ai/fullsend/issues/8835).

| Gate | Status | Evidence or remaining work |
|------|--------|----------------------------|
| Trigger-variable control (Owner-role PAT or `no_one_allowed`) | Closed: superseded | [ADR 0131](../../../ADRs/0131-gitlab-dispatch-pipeline-inputs-no-owner-role.md) adopted typed pipeline inputs with `no_one_allowed` and a Developer-role Poller. No Owner-role credential is used. ADR 0125's Caveats finding that `no_one_allowed` is unusable applied only to variable-based dispatch. |
| Native `TRIGGER_PAYLOAD` under the required restriction | Closed | GitLab supplies `TRIGGER_PAYLOAD` as a file-type variable, and `no_one_allowed` does not strip it. In the [2026-10-20 run](https://github.com/fullsend-ai/fullsend/issues/8245#issuecomment-6301468961) on GitLab.com Free and self-managed EE (`a87815b6c`), and in the CE runs, webhook dispatcher pipelines read the payload and dispatched agents. At those commits, install provisions the trigger and webhook only after it reads back `no_one_allowed` ([`gitlabTriggerSafetyCheck`](https://github.com/fullsend-ai/fullsend/blob/a7f851df9848eab7f76c0b4aadc9043ddcd6241d/internal/repos/gitlab_webhook.go#L747)). The dispatcher fails closed when `TRIGGER_PAYLOAD` is unset or unreadable ([`run-dispatcher-job.sh`](https://github.com/fullsend-ai/fullsend/blob/a7f851df9848eab7f76c0b4aadc9043ddcd6241d/internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/run-dispatcher-job.sh#L66-L80)). |
| `CI_JOB_TOKEN` running-job identity pin | Open: gated on the restriction | All three job scripts source [`pin-ci-job-identity.sh`](https://github.com/fullsend-ai/fullsend/blob/a7f851df9848eab7f76c0b4aadc9043ddcd6241d/internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/pin-ci-job-identity.sh). Under `no_one_allowed`, GitLab rejects caller-supplied pipeline variables: the [#7769](https://github.com/fullsend-ai/fullsend/issues/7769) spike on self-managed EE 19.2.7 recorded HTTP 400 for a `CI_JOB_TOKEN` override on `POST /pipeline` and for a variable on a trigger-token `POST /trigger/pipeline`. The pin's own behavior for an invalid override (expected to fail closed) and for a valid foreign job token (would mis-resolve) has not been tested on a target GitLab version. The [maintainer decision on #8871](https://github.com/fullsend-ai/fullsend/issues/8871#issuecomment-6453959180) retains the `no_one_allowed` restriction as the gate and makes those invalid and valid-foreign override tests non-blocking. The "sole project-identity source" claim therefore holds only while the restriction is verified before runnable jobs and the webhook are delivered, and the pin is defense in depth: the helper does not independently bind a supplied token to the running job. The restriction does not cover project, group, or instance CI/CD settings variables. The decision accepts that project Maintainers, group Owners, and GitLab instance administrators do not add or change a `CI_JOB_TOKEN` settings-variable override in scopes Fullsend cannot read or that change later. If those roles are adversaries, the acceptance does not apply, and the installer must fail closed on unreadable scopes or a separate identity-binding control is needed. |
| API base-URL trust | Closed: risk accepted | The identity pin bootstraps `GET /api/v4/job` from `CI_API_V4_URL`, rejects any value that is not an https API root or that contains globbing or userinfo characters, and passes that root on as `FULLSEND_PINNED_GITLAB_URL`. `CI_API_V4_URL` is in the overridable class. Its trust rests on `no_one_allowed`, which install verifies before it provisions the webhook, and on the [inherited CI/CD variable trust](../../../guides/getting-started/configuring-gitlab.md#inherited-cicd-variable-trust) assumption. The current deployment meets that restriction-based model. It does not provide the restriction-independent pin that ADR 0125 required before shipping. The [maintainer decision on #8871](https://github.com/fullsend-ai/fullsend/issues/8871#issuecomment-6453141351) accepts the restriction-based model with no independent host pin. Within the accepted trust boundary, project Maintainers, group Owners, and GitLab instance administrators are trusted to control project, group, and instance CI/CD variables, including inherited scopes Fullsend cannot read. If those roles must be treated as adversaries, the acceptance does not apply, and an independent API-host pin or fail-closed verification of unreadable scopes is required. |
| Snapshot-consistent replay | Closed: risk accepted | The builder rejects re-fetched event-time evidence older than 30 minutes ([`webhookMaxEventAge`](https://github.com/fullsend-ai/fullsend/blob/a7f851df9848eab7f76c0b4aadc9043ddcd6241d/internal/poll/webhook.go#L56)) and shares occurrence-aware dispatch keys with the poller ([ADR 0132](../../../ADRs/0132-occurrence-aware-shared-poller-state.md)). The [residual replay risk](#gitlab-webhook-transition-provenance-gitlab-webhook) above remains. Delivery is also at-least-once: a webhook and a poll cycle that both read state before either persists can each dispatch the same event ([`webhook_dispatch.go`](https://github.com/fullsend-ai/fullsend/blob/a7f851df9848eab7f76c0b4aadc9043ddcd6241d/internal/poll/webhook_dispatch.go#L74-L84)). The [maintainer decision on #8835](https://github.com/fullsend-ai/fullsend/issues/8835#issuecomment-6452638835) accepts bounded at-least-once replay, not exactly-once dispatch. The accepted residuals are a duplicate dispatch when concurrent webhook and poller runs both read state before either saves, a retry after pipeline creation succeeds but state persistence fails, and a partial stage fan-out that redispatches a stage that succeeded when another stage failed. A replayed event must still pass re-fetch, freshness, actor binding, and authorization, and replay is skipped once its dispatch key is saved. |
| Actor-to-transition attribution | Closed for the rows the builder accepts | [`bindActor`](https://github.com/fullsend-ai/fullsend/blob/a7f851df9848eab7f76c0b4aadc9043ddcd6241d/internal/poll/webhook.go#L607) requires the payload-named actor to equal the actor the re-fetch records for the transition: the note author; the MR author, merger, or closer; the issue author or closer; or the user on the latest matching resource label event. Both `reopen` rows have no event-time actor evidence and are rejected. MR label changes and other MR updates are rejected as well. Note edits and bare issue updates are ignored without dispatch. Any row added later MUST bind its actor the same way or fail closed. |
| Live validation ([#8245](https://github.com/fullsend-ai/fullsend/issues/8245)) | Closed for GitLab.com Free and self-managed EE | See [Validation status](#gitlab-webhook-validation-status). Self-managed CE is non-gating, and stock agents on custom-port CE remain unverified. |
