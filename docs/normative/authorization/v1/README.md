# Authorization Contract v1

Normative rules governing which actors may trigger agent dispatch.

This document records the v1 contract for authorization policy.
The historical decision and rationale are recorded in
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md).
The [NormalizedEvent v1](../../normalized-event/v1/) specification defines the
`actor.role`, `actor.kind`, `actor.role_verified`, and `actor.bot_role` fields
consumed by this contract. Bot identity and role resolution are specified by
[ADR 0107](../../../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md).

> **Version note:** [Authorization Contract v2](../v2/README.md), adopted by
> [ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md),
> supersedes v1's future full-history entity-evaluation and audit requirements.
> [ADR 0148](../../../ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md)
> permits bounded actor-role reuse for entity-source CEL in v2.
> [ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md) keeps an
> immutable `triage` human floor and applies the selected harness's effective
> `human_min_role` to target event, entity-fact, and model-record admission.
> Human labelers must meet that minimum; bot label grants remain separate.
> Existing event-backed dispatch continues to use v1 until migrated; the v1
> requirements below remain the record of this version's contract.

> **ADR 0107 target contract — partially implemented:** The bot identity
> fields, resolver outcomes, and bot-specific fail-closed rows below describe
> the target behavior. The building blocks exist: the `actor.bot_role` and
> `actor.role_verified` fields in `internal/normevent`, the forge-neutral
> `BotRoleResolver` contract in `internal/botidentity`, the mint's GitHub
> `POST /v1/bot-role` lookup, and a non-label bot branch in `IsEventAuthorized()`.
> That branch applies only to events that carry a lookup result
> (`actor.role_verified` set). No production adapter performs the lookup yet,
> so existing compatibility behavior remains authoritative for current events.
> Non-GitHub lookup is not implemented.

## Role ordering

Fullsend uses a forge-neutral role hierarchy. Roles are listed from most
to least privileged:

```
admin > maintain > write > triage > read > none > external
```

| Role | Meaning |
|------|---------|
| `admin` | Full repository control |
| `maintain` | Repository settings without destructive admin |
| `write` | Push, label, comment, merge |
| `triage` | Label and moderate without push access |
| `read` | Read-only collaborator |
| `none` | Authenticated user without explicit repository permission |
| `external` | Actor outside the repository or project (currently Jira-only; GitHub maps non-collaborators to `read` on public repositories or `none`; GitLab maps them to `none`) |

### Forge-native permission mapping

| Forge | Native level | Fullsend role |
|-------|-------------|---------------|
| **GitHub** | `admin` | `admin` |
| | `maintain` | `maintain` |
| | `write` | `write` |
| | `triage` | `triage` |
| | `read` | `read` |
| | _(no collaborator entry)_ | `none` |
| | _(fork/non-collaborator)_ | `read` (public repository) or `none` |
| **GitLab** | Owner | `admin` |
| | Maintainer | `maintain` |
| | Developer | `write` |
| | Reporter | `triage` |
| | Guest | `read` |
| **Jira** (project roles) | Administrators | `admin` |
| | Developers | `write` |
| | _(other named role)_ | `read` |
| | _(no project membership)_ | `external` |
| **Jira** (`jira` provider lists) | listed under `write` | `write` |
| | listed only under `triage` | `triage` |
| | _(not listed)_ | `none` |
| **Jira** _(role not resolved)_ | — | `none` |

On GitHub the mapping source is the collaborator permission API
(`GET /repos/{owner}/{repo}/collaborators/{username}/permission`), which
returns the user's **effective** role including inherited org grants
regardless of membership visibility. A built-in `role_name` is used as is.
Custom repository roles are an organization-level GitHub Enterprise Cloud
feature, so most repositories only ever return built-in names; Fullsend
does not interpret a custom role's name or its fine-grained permissions.
A custom repository role resolves to the strongest base role set to `true`
in `user.permissions` (`admin`, `maintain`, `push` → `write`, `triage`,
`pull` → `read`), or `none` if no flag is set. Only when
`user.permissions` is absent or null does the legacy `permission` field
(`admin`, `write`, `read`) apply; anything else is `none`. Legacy `read`
stays `read`, because it cannot distinguish Read from Triage. Fork authors
and non-collaborators are mapped to `read` (public repositories) or `none`, never `external`.
The `external` role is currently produced only by the Jira adapter for
actors without project membership. The default thresholds deny `read`,
`none` and `external` alike. The `author_association` field is
**not** used because it does not correctly reflect private org membership
(see [Excluded fields](#excluded-fields)).

A GitHub repository that lists `owners_file` under `authorization` in
`.fullsend/config.yaml` consults its repo-root Prow `OWNERS` file before the
collaborator API:

| OWNERS list | Role granted |
|-------------|--------------|
| `approvers` | at least `write` |
| `reviewers` | at least `triage` |
| _(not listed)_ | none; the collaborator API decides |

OWNERS can only raise a role, never lower one. An entry that names an
`OWNERS_ALIASES` key stands for that alias's members. A login equal to any alias
key never matches, and nested aliases are not expanded. The files are read from the base branch for PR-scoped events and
from the default branch otherwise, so a PR cannot add its author. See the
[`authorization` field](../../../reference/config-reference.md#authorization).

On Jira the `jira` provider under `authorization` in `.fullsend/config.yaml`
([ADR 0153](../../../ADRs/0153-authorize-jira-poll-actors-without-project-administration.md))
is opt-in. It applies only to events with `source.system: jira`. With no
`jira` entry, every Jira event, label-added included, takes its role from the
project role roster (see **Without lists** below), and a label-added event
takes the ordinary per-harness threshold. With a `jira` entry, the mapping
source for each event is chosen by the provider:

- **Label-added events** follow the provider's `labels` rule. Under
  `jira_editors`, the default once the entry is present, the
  role is not resolved. The event is authorized by the
  [label exception](#label-application) instead. Under `allowlist` the role
  comes from the provider's lists, and under `project_roles` from the
  project role roster.
- **Every other event**, slash commands included, takes its role from the
  provider's `write` and `triage` lists when the provider sets either list
  (an empty list counts as set). The lists are then authoritative: the project role roster is not
  consulted. An actor named under `write` (by account ID, or by membership in
  a listed Jira group) is `write`. An actor named only under `triage` is
  `triage`. Anyone else is `none`. Group membership comes from
  `GET /rest/api/3/user/groups` and is read once per actor per poll cycle.
- **Without lists**, and every event when no `jira` entry exists, the role
  comes from the project's role roster, resolved once per poll cycle for the
  configured `--jira-project` and only when such an event is present. An
  actor is matched by Jira account ID. Role names are matched
  case-insensitively against the names above, and unrecognized role names
  fall through to `read`. Actors with no membership in the configured
  project, or whose issue belongs to a different project, are mapped to
  `external` (fail-closed). When a `jira` entry exists but sets no lists, only
  a slash-command comment uses the roster and any other event's role is not
  resolved, except that an entry with `labels: project_roles` and no lists
  resolves every event through the roster, exactly as with no `jira` entry.
  That entry is the explicit way to keep the no-entry behavior.

An event whose role is not resolved carries `actor.role: none`, which no
threshold admits. Reading the roster (`GET /rest/api/3/project/<KEY>/role`)
requires Jira's Administer Projects permission; a failed read fails the
poll cycle without advancing checkpoints. See the
[Jira integration guide](../../../guides/user/jira-integration.md) for
details on the name-based matching limitation.

### Bot identity fields

The adapter/provider MUST classify the verified actor using authoritative
source-system metadata before authorization. This may include a provider actor
name when that forge gives it bot-specific semantics, such as GitHub's `[bot]`
logins; labels, review types, and arbitrary event-content strings are not bot
identity signals. For post-migration producers with `actor.kind: bot`,
`actor.role` MUST be `none`, and
`actor.role_verified` is true exactly when the provider completes the bot-role
lookup, including a successful no-match result; it is false when resolution
fails. For `actor.kind: human`, `actor.bot_role` MUST be absent or `null`, and
`actor.role` contains the forge permission role when `actor.role_verified` is
true. For non-label dispatch, only a non-null, provider-resolved
`actor.bot_role` can pass the bot gate. Once that gate succeeds, the
`actor.role`-keyed observation and mutation thresholds below do not apply to the
bot; platform authorization instead requires a valid normalized event, an
applicable source and target, and a transition supported by the selected
harness. CEL may further restrict routing but cannot create or broaden bot
identity authorization.

For humans on non-label paths, `role_verified: false` denies the event
regardless of the role string. A missing `role_verified` on a trusted
pre-migration event is treated as legacy input and retains the current human
authorization behavior; new adapters MUST emit the field, and their false
value MUST fail closed. Legacy status is determined by the adapter
implementation contract, not by guessing from this field's absence. On the
permanent label exception, `role_verified` is not an
authorization input: an accepted forge label mutation authorizes a human, and
a bot additionally requires positive provider bot classification. For bots,
omission means that bot-role lookup is not applicable, not that it failed.

## Default thresholds

| Category | Minimum role | Rationale |
|----------|-------------|-----------|
| **Observation** (triage, review) | `triage` | Read-only analysis; lower barrier to reduce maintainer toil |
| **Mutation** (code, fix, retro slash command, prioritize) | `write` | Agents that push commits or alter state require push access |

A role satisfies a threshold when it is **at or above** the minimum in
the role ordering. For example, `admin` satisfies both `triage` and
`write` thresholds.

These thresholds apply to human actors and to legacy bot events that still
use forge permissions during migration. They do not apply to a bot after
successful `actor.bot_role` recognition, because `actor.role` remains the
compatibility value `none` for that target representation.

The bash dispatch implementation uses a parameterized
`has_repo_permission(username, min)` helper that encodes this comparison
(see [Enforcement point](#enforcement-point)).

> **Implementation note:** the Go CEL dispatch path (`harnessdispatch`) applies
> these thresholds per harness, keyed on the harness `role:` field. It serves
> GitHub CEL dispatch (`fullsend dispatch`) and Jira polling (`fullsend poll
> --input-driver jira-poll`). The event-level gate (`IsEventAuthorized()`)
> runs once before harness enumeration and applies the label-added, bot, and
> `role_verified` rules. For the remaining actors, the candidate harnesses are
> then filtered by `IsHarnessAuthorized()` before CEL evaluation: `role: triage`
> and `role: review` require `triage`; every other role, including an empty,
> unknown, or custom role, requires `write` (fail closed). GitLab polling and
> webhook routing do not use `harnessdispatch`: `dispatch.HarnessRouter`
> gates slash commands per stage (triage and review at `triage`, other stages
> at `write`) and label routes at `write`, and the agent job then requires
> Developer access for every stage except retro and prioritize. The one
> exception to that agent-job check is Jira work items dispatched to GitLab
> (`jira_*` events): the Jira poller authorizes the actor by Jira project role
> during polling, and the agent job skips the Developer-access check for these
> signed events. The bash path (GitHub webhook
> dispatch) applies the same thresholds per stage through
> `has_repo_permission`.
> Among the Go paths, only `fullsend dispatch --route retro` applies the
> lifecycle-close exception; the bash path applies it for every closer and
> CEL harness dispatch does not (see
> [Lifecycle close](#lifecycle-close-pull_request_targetclosed)).

## Fail-closed behavior

The authorization gate is **fail-closed**: when a role cannot be
determined, the actor is denied.

The entity-discovery rows below specify the future ADR 0098 path and do not
describe behavior currently implemented by `fullsend dispatch` or
`fullsend poll`.

The bot-specific rows in the following table are ADR 0107 target behavior;
legacy events remain subject to the compatibility rules until implementation
migration is complete.

| Condition | Outcome |
|-----------|---------|
| Collaborator API returns a custom `role_name` with no effective permission signal | Mapped to `none`; denied |
| Collaborator API returns an error or times out | Denied (function returns failure) |
| Bot-role lookup returns no registered identity on a non-label path | `actor.role` remains `none`; `actor.role_verified` is true; `actor.bot_role` is absent; denied |
| Bot-role lookup fails or is unverifiable on a non-label path | `actor.role` remains `none`; `actor.role_verified` is false; `actor.bot_role` is absent; denied, with the failure retained in resolver/audit diagnostics |
| Collaborator API response is not valid JSON or does not match the expected shape | Denied |
| `actor.role` is empty or missing | Event fails `NormalizedEvent` validation; never reaches dispatch |
| `actor.role_verified` is false for a human | Denied regardless of `actor.role` |
| Legacy bot event has an `actor.role` other than `none` | Valid in the v1 compatibility schema; authorization follows current compatibility behavior during migration and is not an ADR 0107 bot-role authorization result |
| `actor.role_verified` is false but `actor.bot_role` is non-null for a bot | Event fails `NormalizedEvent` validation; never reaches dispatch |
| `actor.bot_role` is non-null for a human | Event fails `NormalizedEvent` validation; never reaches dispatch |
| Username is empty | Denied |
| `OWNERS` is missing or malformed, or `OWNERS_ALIASES` is present but malformed (`owners_file` enabled) | OWNERS check skipped; the collaborator API decides |
| Username contains characters outside `[A-Za-z0-9-]` (`owners_file` enabled) | OWNERS check skipped; the collaborator API decides |
| Fullsend invocation provenance for poll discovery or a bound run's eventless check is missing or unverifiable | That eventless evaluation is denied |
| Harness entity sources are missing or malformed | That harness is skipped for scheduled evaluation |
| Effective platform eligibility policy is missing, malformed, or unverifiable | Scheduled evaluations governed by that policy are denied |
| Effective platform eligibility policy uses a wildcard without explicit platform-level justification | Scheduled evaluations governed by that policy are denied |
| Action-indicating enumeration fails, is unavailable, or does not cover the entity kind | Evaluation of that entity is denied |
| Current permission for an action-indicating element's actor is missing or unverifiable | That element cannot trigger execution |
| Current permission for an action-indicating element's actor is below the applicable stage threshold | That element cannot trigger execution |
| Versioned normalized-entity contract is missing or incomplete | Entity-first execution denied |

## Exceptions

Certain transitions are authorized without requiring a `write` or
`triage` role from the acting user. Each exception is documented with its
rationale.

### Label application

**Current compatibility behavior:** GitHub authorizes an added-label event
without applying the ordinary `actor.role` threshold. This existing behavior
preserves bot-to-bot handoffs such as adding `ready-to-code` after triage
completes, including handoffs from provider bots that are not Fullsend-
registered.

**ADR 0107 target behavior:** When
`transition.kind` is `label_changed` and `label.action` is `added`, GitHub
authorizes the event regardless of `actor.role` once the adapter has
established an accepted forge label mutation and authoritative actor-to-
transition provenance. This is a separate platform authorization grant, not an
application of the ordinary stage threshold. At the time of this ADR, GitHub
is the only implementation; another adapter MUST provide equivalent
forge-authoritative evidence before enabling the same grant, and absent or
unverifiable evidence denies the event. For a bot actor, the adapter MUST
positively classify it using provider-controlled metadata, but `actor.bot_role`
lookup is not required. The platform gate MUST NOT inspect the label name or
maintain an agent-role label allowlist; harness/CEL routing owns that mapping
and may further narrow the dispatch.

**Jira:** A Jira `label_changed` event with `label.action: added` is
authorized the same way, regardless of `actor.role`, because Jira requires
the Edit Issues permission to change an issue's labels. This applies only
when the repository has a `jira` provider entry whose `labels` rule is
`jira_editors`, the default once the entry is present
([ADR 0153](../../../ADRs/0153-authorize-jira-poll-actors-without-project-administration.md)).
It is often broader than Jira's Developers role. A repository with no `jira`
entry, or one that sets `labels: allowlist` or `labels: project_roles`, does
not get the exception: those label events take the ordinary per-harness
threshold against the role resolved from the project role roster or the
lists. The poller's own
account's events are classified as bot events and dropped before
authorization.

### Bot-submitted reviews (GitHub)

**Current compatibility behavior:** Until the resolver is implemented,
`fullsend dispatch` continues to authorize a GitHub `review_submitted` event
from a bot without requiring a collaborator permission or `actor.bot_role`.
This existing exception is not evidence that the bot identity was resolved.

**ADR 0107 target behavior:** After the resolver is implemented, a GitHub
review event is authorized only when the provider classifies the actor as a
bot and resolves its exact registered `actor.bot_role`; `actor.role` remains
`none`. The downstream harness CEL trigger may further constrain which bot
role and review state are accepted.

### Lifecycle close (pull\_request\_target.closed)

The `pull_request_target.closed` event that triggers the retro stage is
intentionally ungated: any closer may trigger read-only lifecycle
accounting. The retro agent performs only observation work and does not
mutate repository state. This exception is implemented in the bash
dispatch path and, on GitHub, by `fullsend dispatch --route retro`
(`IsLifecycleCloseAuthorized()` in `harnessdispatch`), which admits a
`pull_request_target` close, merged or not, whoever closed it, for the
retro role only. Any pull request author can close their own pull request,
so no other harness role gets the exception. The route applies it only when
routing retro, keyed on the resolved harness `role: retro`; when the harness
cannot be loaded, it is keyed on the routed agent's own role (the Retro job
mints the retro role either way). CEL harness dispatch has no
special handling for closed transitions: the retro harness role resolves to
the `write` threshold there.

### Schedule and manual dispatch

When `source.system` is `schedule` or `manual`, the actor is the configured
operator or service identity (for example, a GitHub App bot or workflow
`GITHUB_ACTOR`). Adapters MUST classify that identity from authoritative
provider metadata. Human identities use verified forge permissions and the
human thresholds; bot identities use provider-backed `actor.bot_role`
resolution and retain `actor.role` as `none`. An unrecognized or unresolved
bot identity is denied.

**Current compatibility behavior:** Until provider-backed bot-role resolution
is wired into the adapters and dispatch path, adapters set `actor.role` to the
service identity's effective repository permission (typically `write`), so
the standard permission gate applies.

> **Target contract, not yet implemented:** Existing schedule/manual
> compatibility handling remains authoritative until provider-backed bot-role
> resolution is wired into the adapters and dispatch path.

### Fullsend-originated eventless entity evaluation

This is the future path adopted by
[ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md) and clarified
by [ADR 0144](../../../ADRs/0144-reuse-poll-and-entity-bound-run-processes.md);
it is not yet implemented by `fullsend dispatch`, `fullsend poll`, or eventless
bound `fullsend run`. Entity-first execution MUST NOT be enabled until the
versioned normalized-entity contract defines CEL-eligible and prompt-eligible
fields. That contract is a platform allowlist; a harness may request fewer
fields but cannot expand it.

`fullsend poll` may enumerate candidates without synthesizing a
`NormalizedEvent` or event actor. A bound `fullsend run` may instead recheck
only its canonical entity with `event == null`, including after consuming an
initial event. Both paths require eligible entity sources and effective
platform policy permitting eventless evaluation. They are authorized only when
the caller has trusted Fullsend-controlled invocation provenance: a verified,
non-user-assertable platform execution identity bound to the invocation and its
target, such as an attested workflow/job identity or installation credential.
Neither the actor of an earlier event nor an authenticated wake peer supplies
this authority. A CLI flag, request header, or other caller-supplied claim is
insufficient. A caller that cannot establish that provenance MUST be denied.
Wildcard eligibility (`*` or `all`) requires the same explicit platform-level
justification as any other wildcard allowlist in this contract.

Each eventless entity evaluation's audit record MUST identify the verified
Fullsend invocation identity, target repository, effective policy, harness
revision, canonical entity, and every retained action-indicating element and
its actor. Those element actors describe evaluated entity history, not the
evaluation's initiator. An event-bearing child cycle identifies its verified
event actor; the actor of an earlier event is not attributed as the initiator
of a later eventless cycle in the same containing run.

Authorization of the Fullsend invocation origin does not authorize entity
content. For each evaluated harness, before evaluating its CEL predicate,
Fullsend MUST enumerate a platform-defined closed superset of
action-indicating elements for the entity kind. These are actor-originated
entity-history elements whose content or state could be treated as a request
for a stage to run; the superset includes issue or change-proposal bodies,
comments, reviews, and label applications wherever the entity kind supports
them. A harness cannot exclude a supported category from classification. If
enumeration fails, is unavailable, or does not cover the entity kind,
evaluation of that entity MUST be denied; only successful enumeration may
return an empty set for a state-only predicate.

Fullsend MUST resolve the current forge permission level for every enumerated
element's actor and remove the element unless that permission meets the
evaluated harness's observation or mutation threshold. Historical or cached
actor relationships are insufficient. A harness CEL predicate MAY further
restrict selection using retained elements and their current actor-permission
fields, but cannot weaken the platform gate or be relied upon to identify an
element for later authorization.

Fullsend MUST omit or minimize other unneeded untrusted fields where the
normalized entity contract permits, while retaining the state, content, and
actor provenance required by the harness. Further trust or injection filtering
MAY run after CEL routing and before the harness pre-script. Retained content
remains untrusted for dispatch and CEL evaluation throughout. [ADR 0133](../../../ADRs/0133-actor-attributed-data-security-boundary.md)
defines the separate model-bound admission gate: only filtered content whose
source record and current actor authorization satisfy that ADR may be exposed
to the model under bounded harness scope. Any resulting agent run uses the
harness's configured identity and permissions.

## Model-bound actor-attributed data admission

The model-bound gate applies to every source-system record that Fullsend may
expose to a model, regardless of whether the record arrived in an event, was
found by polling or snapshotting, or was read through a proactive or
Fullsend-controlled API. Fullsend MUST establish the record's current actor
attribution through the source system and resolve that actor's current
authorization against the applicable observation or mutation threshold. If
attribution or authorization cannot be established, or the actor does not meet
the threshold, the record MUST be redacted; there is no data-only fallback.

The actor authorization MAY be co-fetched with the record, supplied in the
same verified event payload, or lazily resolved when first needed. A lazy
result MAY be cached only for the logical lifetime of the containing event or
entity structure and agent invocation. Re-reading or re-fetching the data,
creating a new structure for retry or requeue, or terminating and re-invoking
the agent ends that lifetime and requires fresh resolution, even if the same
in-memory structure is reused. The cache lifetime is not extended by object
identity or by retaining the structure.

Event-carried authorization evidence MAY support event admission and routing,
but MUST NOT satisfy model-bound checks. Event-triggered runs MUST resolve
record-actor authorization after event admission and before model admission,
again immediately before sandbox initialization, and again immediately before
first model exposure. If sandbox initialization and first exposure are
contiguous, one fresh check MAY serve both. Non-event retrieval paths MUST
perform the equivalent checks after retrieval admission. A
Fullsend-controlled capability invoked after sandbox initialization MUST
resolve authorization immediately before releasing each result to the model.
That release check is the mid-run equivalent of the sandbox checkpoints.

A cache entry MUST NOT cross any checkpoint. Implementations MAY coalesce
contiguous checks and MAY batch or cache lookups for multiple records from the
same actor, provided the cache remains within this logical lifetime. Failure,
unavailability, or loss of authorization at any checkpoint MUST redact the
record and, if no authorized records remain, deny the run. Revocation after
first exposure cannot retract content already present in the model context,
but every later retrieval result requires its own release check.

## Excluded fields

The following fields are **not** authorization evidence and must not be
used for dispatch gating:

| Field | Why excluded |
|-------|-------------|
| `author_association` | Does not reflect private org membership; an org admin with private membership gets `CONTRIBUTOR` instead of `MEMBER` ([github/gh-aw-mcpg#2862](https://github.com/github/gh-aw-mcpg/issues/2862)). Also reflects contribution history, not current authority. |
| Contribution history | Past contributions do not confer current repository permissions. A former maintainer whose access was revoked should not pass authorization. |
| `actor.is_entity_author` | Being the author of an issue or PR does not grant repository permissions. This field supports routing decisions in CEL triggers, not authorization. |

**Principle:** relationship and contribution-history fields are not
evidence of current authority. Event-backed authorization must be derived from
the forge's permission model at event time, not from cached or inferred
relationships. Fullsend-originated eventless evaluation must instead be
authorized from trusted Fullsend-controlled invocation provenance for poll
enumeration or bound rechecks. Action-indicating elements are separately gated
by current actor permission; historical actors and relationship fields must not
serve as authorization evidence.

## Enforcement point

Authorization is enforced as a **platform-level gate** before CEL trigger
evaluation. Event-backed dispatch uses the normalized event actor;
Fullsend-originated eventless evaluation uses trusted invocation provenance to
authorize evaluation, then independently filters action-indicating elements by
current actor permission before CEL.

```
Forge event
  --> NormalizedEvent (adapter)
  --> Event-actor authorization gate        <-- enforced here
  --> CEL trigger evaluation (harness routing)
  --> Execution

Fullsend poll discovery / bound run eventless recheck
  --> Trusted-origin authorization gate     <-- enforced here
  --> Enumerate and resolve candidates (poll) / resolve canonical entity (run)
  --> Evaluate each candidate harness (poll) or the bound harness (run):
      --> Enumerate action-indicating elements
      --> Harness-stage actor gate           <-- enforced here
      --> CEL trigger evaluation (event is null)
      --> Further trust/injection filtering
      --> Execution
```

### CEL triggers: routing only

Harness `trigger` expressions express **routing and may tighten input
selection**, not platform permission policy.
A CEL expression may **tighten** dispatch conditions (e.g., require a
specific label, restrict to non-fork PRs, filter by bot identity) but
may **never weaken** the platform authorization gate. On the event-backed path,
an event that fails authorization never reaches CEL evaluation.

This separation is enforced architecturally: on the event-backed path,
`IsEventAuthorized()` runs before harness enumeration in the dispatch core, and
`FilterAuthorizedHarnesses()` removes each harness whose role threshold the
actor does not meet before `MatchHarnesses()` evaluates CEL. On the
Fullsend-originated eventless path, the trusted-origin gate runs before poll
enumeration or bound-entity resolution and CEL evaluation. For each eligible
harness, the platform removes unauthorized action-indicating elements using
that harness's stage threshold before its CEL predicate runs, and `event`
remains null. Neither path lets a CEL expression override or relax an
authorization denial.

### Per-repo configurability

The authorization gate is a platform-level security boundary. Individual
repositories cannot disable it. Per-repo configuration (which stages are
enabled, which labels trigger automation) operates **within** the
authorization boundary. A repository can disable a stage entirely but
cannot make it available to unauthorized users.

A repository can add permission sources through the `authorization`
field (`owners_file`, and `jira` for Jira events; see
[ADR 0153](../../../ADRs/0153-authorize-jira-poll-actors-without-project-administration.md)).
These sources extend the
`has_repo_permission` helper and the Go `IsEventAuthorized` gate and
per-harness filter; they do not
bypass the gate or change the thresholds.

## Versioning

This is a living normative document under
[ADR 0015](../../../ADRs/0015-normative-specifications-directory.md).
Breaking changes require `docs/normative/authorization/v2/`.

**ADR 0107 migration exception:** The migration from legacy bot authorization
exceptions to provider-backed bot identity is permitted within v1. It is a
policy tightening, not a new supported actor population: existing
compatibility behavior remains authoritative until an adapter implementation is
updated; once updated, registered bots use `actor.bot_role` and unregistered
non-label bots are denied. The adapter implementation itself is the migration
boundary; no per-event mode marker or separate deprecation window is required.
An updated adapter MUST enforce the target behavior and MUST NOT silently fall
back to legacy bot authorization when its resolver fails. The normalized-event
fields used to carry this distinction are additive, and the permanent
label-added exception is preserved. This exception does not alter the general
v2 rule for unrelated authorization changes.

| Change | v1 impact |
|--------|-----------|
| **Breaking** (requires v2): remove a role from the hierarchy, raise a default threshold, remove a documented exception, change fail-closed to fail-open | Dispatch implementations must migrate |
| **Adapter bot-identity migration** (allowed in v1): update an adapter to emit and enforce the ADR 0107 target representation | The adapter's existing compatibility behavior applies until that implementation update; afterward, registered bots use `actor.bot_role` and unregistered non-label bots are denied |
| **Non-breaking** (allowed in v1): add a role, lower a default threshold, add a new exception, add forge mappings, clarify documentation | Existing dispatch behavior is preserved or relaxed |
