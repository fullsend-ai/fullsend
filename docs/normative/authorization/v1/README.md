# Authorization Contract v1

Normative rules governing which actors may trigger agent dispatch.

This document is the single living contract for authorization policy.
The historical decision and rationale are recorded in
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md).
The [NormalizedEvent v1](../../normalized-event/v1/) specification defines the
`actor.role`, `actor.kind`, `actor.role_verified`, and `actor.bot_role` fields consumed by this
contract. Bot identity and role resolution are specified by
[ADR 0107](../../../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md).

> **ADR 0107 target contract — not yet implemented:** The bot identity fields,
> resolver outcomes, and bot-specific fail-closed rows below describe the
> behavior to be implemented by the adapters and dispatch path. Until that
> migration lands, existing compatibility behavior remains authoritative.

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
| `external` | Actor outside the repository or project (currently Jira-only; GitHub/GitLab map non-collaborators to `none`) |

### Forge-native permission mapping

| Forge | Native level | Fullsend role |
|-------|-------------|---------------|
| **GitHub** | `admin` | `admin` |
| | `maintain` | `maintain` |
| | `write` | `write` |
| | `triage` | `triage` |
| | `read` | `read` |
| | _(no collaborator entry)_ | `none` |
| | _(fork/non-collaborator)_ | `none` |
| **GitLab** | Owner | `admin` |
| | Maintainer | `maintain` |
| | Developer | `write` |
| | Reporter | `triage` |
| | Guest | `read` |
| **Jira** | Administrators | `admin` |
| | Developers | `write` |
| | _(other named role)_ | `read` |
| | _(no project membership)_ | `external` |

On GitHub the mapping source is the collaborator permission API
(`GET /repos/{owner}/{repo}/collaborators/{username}/permission`), which
returns the user's **effective** role including inherited org grants
regardless of membership visibility. Fork authors and non-collaborators
whose `role_name` is unrecognized by `MapGitHubPermission` are mapped to
`none` (not `external`); the `external` role is currently produced only
by the Jira adapter for actors without project membership. Both `none`
and `external` are denied by the default thresholds, so the
authorization outcome is identical. The `author_association` field is
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

On Jira the mapping source is the project's role membership roster,
resolved once per poll cycle for the configured `--jira-project`. An
actor is matched by Jira account ID. Role names are matched
case-insensitively against the names above; unrecognized role names fall
through to `read`. Actors with no membership in the configured project —
or whose issue belongs to a different project — are mapped to `external`
(fail-closed). See the
[Jira integration guide](../../../guides/user/jira-integration.md) for
details on the name-based matching limitation.

### Bot identity fields

The adapter/provider MUST classify the verified actor using authoritative
source-system metadata before authorization. This may include a provider actor
name when that forge gives it bot-specific semantics, such as GitHub's `[bot]`
logins; labels, review types, and arbitrary event-content strings are not bot
identity signals. For `actor.kind: bot`, `actor.role` MUST be `none`, and
`actor.role_verified` is true exactly when the provider completes the bot-role
lookup, including a successful no-match result; it is false when resolution
fails. For `actor.kind: human`,
`actor.bot_role` MUST be absent or `null`, and `actor.role` contains the forge
permission role when `actor.role_verified` is true. Only a non-null,
provider-resolved `actor.bot_role` can pass the bot dispatch gate; CEL may
further restrict it but cannot create or broaden it. Once that gate succeeds,
the actor.role-keyed observation and mutation thresholds below do not apply to
the bot; the bot is authorized by role recognition plus the selected harness's
generic transition/target policy and any further CEL restrictions.

For humans, `role_verified: false` denies the event regardless of the role
string. A missing `role_verified` on a trusted pre-migration event is treated
as legacy input and retains the current human authorization behavior; new
adapters MUST emit the field, and their false value MUST fail closed.

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

> **Implementation note:** the Go dispatch path (`IsAuthorized()` in
> `harnessdispatch`) currently uses a coarse `write+` filter for all
> non-exception transitions — it does not yet distinguish observation
> from mutation thresholds. Poll-based dispatch (GitLab/Jira) uses the
> Go path, so a `triage`-role user triggering observation through that
> path is denied today. The bash path (GitHub webhook dispatch)
> implements the full parameterized threshold.

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
| Collaborator API returns an unrecognized `role_name` | Mapped to `none`; denied |
| Collaborator API returns an error or times out | Denied (function returns failure) |
| Bot-role lookup returns no registered identity | `actor.role` remains `none`; `actor.role_verified` is true; `actor.bot_role` is absent/null; denied |
| Bot-role lookup fails or is unverifiable | `actor.role` remains `none`; `actor.role_verified` is false; `actor.bot_role` is absent/null; denied, with the failure retained in resolver/audit diagnostics |
| Custom repository roles (GitHub) | Mapped to `none`; denied until custom roles are handled platform-wide |
| `actor.role` is empty or missing | Event fails `NormalizedEvent` validation; never reaches dispatch |
| `actor.role_verified` is false for a human | Denied regardless of `actor.role` |
| Legacy bot event has an `actor.role` other than `none` | Valid in the v1 compatibility schema; authorization follows current compatibility behavior during migration and is not an ADR 0107 bot-role authorization result |
| `actor.role_verified` is false but `actor.bot_role` is non-null for a bot | Event fails `NormalizedEvent` validation; never reaches dispatch |
| `actor.bot_role` is non-null for a human | Event fails `NormalizedEvent` validation; never reaches dispatch |
| Username is empty | Denied |
| `OWNERS` is missing or malformed, or `OWNERS_ALIASES` is present but malformed (`owners_file` enabled) | OWNERS check skipped; the collaborator API decides |
| Username contains characters outside `[A-Za-z0-9-]` (`owners_file` enabled) | OWNERS check skipped; the collaborator API decides |
| Fullsend poll invocation provenance is missing or unverifiable | Entity discovery denied |
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

### Label application (GitHub)

When `source.system` is `github`, `transition.kind` is `label_changed`,
and `label.action` is `added`, the event is authorized regardless of
`actor.role`. This exception applies only when `source.system` is
`github`. GitHub's own permission model requires at least `triage`
access to apply a label, so label application is an **implicit
authorization gate**. Bot accounts that apply labels as part of
agent-to-agent handoff (e.g., adding `ready-to-code` after triage
completes) rely on this path because the collaborator API often returns
404 for `[bot]` accounts even when the GitHub App has write access via
its installation token.

> **Current compatibility behavior:** Until ADR 0107 is implemented, this
> exception remains in force and is not replaced by the bot-role gate.

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
mutate repository state. This exception is currently implemented in
the bash dispatch path; the Go `IsAuthorized()` path has no special
handling for closed transitions and applies the standard `write+` gate.

### Schedule and manual dispatch

When `source.system` is `schedule` or `manual`, the actor is the
configured service identity (GitHub App bot or workflow `GITHUB_ACTOR`).
Adapters set `actor.kind` to `bot`, resolve the configured identity through the
provider, set `actor.bot_role` when recognized, and leave `actor.role` as
`none`. The standard identity authorization gate applies; an unrecognized or
unresolved service identity is denied.

**Current compatibility behavior:** Until provider-backed bot-role resolution
is wired into the adapters and dispatch path, adapters set `actor.role` to the
service identity's effective repository permission (typically `write`), so
the standard permission gate applies.

> **Target contract, not yet implemented:** Existing schedule/manual
> compatibility handling remains authoritative until provider-backed bot-role
> resolution is wired into the adapters and dispatch path.

### Fullsend-originated entity discovery

This is the future path adopted by
[ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md); it is not yet
implemented by `fullsend dispatch` or `fullsend poll`, and entity-first
execution MUST NOT be enabled until the versioned normalized-entity contract
defines CEL-eligible and prompt-eligible fields. That contract is a platform
allowlist; a harness may request fewer fields but cannot expand it.

`fullsend poll` may perform scheduled entity discovery without synthesizing a
`NormalizedEvent` or event actor. This path is authorized only when its caller
has trusted Fullsend-controlled invocation provenance: a verified,
non-user-assertable platform execution identity bound to the invocation and its
target, such as an attested workflow/job identity or installation credential.
A CLI flag, request header, or other caller-supplied claim is insufficient. A
caller that cannot establish that provenance MUST be denied. Wildcard
eligibility (`*` or `all`) requires the same explicit platform-level
justification as any other wildcard allowlist in this contract.

Authorization of the poll origin does not authorize entity content. For each
candidate harness, before evaluating its CEL predicate, Fullsend MUST enumerate
a platform-defined closed superset of action-indicating elements for the entity
kind. These are actor-originated entity-history elements whose content or state
could be treated as a request for a stage to run; the superset includes issue or
change-proposal bodies, comments, reviews, and label applications wherever the
entity kind supports them. A harness cannot exclude a supported category from
classification. If enumeration fails, is unavailable, or does not cover the
entity kind, evaluation of that entity MUST be denied; only successful
enumeration may return an empty set for a state-only predicate.

Fullsend MUST resolve the current forge permission level for every enumerated
element's actor and remove the element unless that permission meets the
candidate harness's observation or mutation threshold. Historical or cached
actor relationships are insufficient. A harness CEL predicate MAY further
restrict selection using retained elements and their current actor-permission
fields, but cannot weaken the platform gate or be relied upon to identify an
element for later authorization.

Fullsend MUST omit or minimize other unneeded untrusted fields where the
normalized entity contract permits, while retaining the state, content, and
actor provenance required by the harness. Further trust or injection filtering
MAY run after CEL routing and before the harness pre-script. Retained content
remains untrusted throughout. Any resulting agent run uses the harness's
configured identity and permissions.

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
relationships. Fullsend-originated entity discovery must instead be authorized
from trusted Fullsend-controlled invocation provenance for enumeration and
evaluation. Action-indicating elements are separately gated by current actor
permission; historical actors and relationship fields must not serve as
authorization evidence.

## Enforcement point

Authorization is enforced as a **platform-level gate** before CEL trigger
evaluation. Event-backed dispatch uses the normalized event actor;
Fullsend-originated entity discovery uses trusted invocation provenance to
authorize enumeration and evaluation, then independently filters
action-indicating elements by current actor permission before CEL.

```
Forge event
  --> NormalizedEvent (adapter)
  --> Event-actor authorization gate        <-- enforced here
  --> CEL trigger evaluation (harness routing)
  --> Execution

Fullsend poll invocation
  --> Trusted-origin authorization gate     <-- enforced here
  --> Entity enumeration and resolution
  --> For each candidate harness:
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
`IsAuthorized()` runs before `MatchHarnesses()` in the dispatch core. On the
entity-discovery path, the trusted-origin gate runs before enumeration and CEL
evaluation. For each candidate harness, the platform removes unauthorized
action-indicating elements using that harness's stage threshold before its CEL
predicate runs, and `event` remains null. Neither path lets a CEL expression
override or relax an authorization denial.

### Per-repo configurability

The authorization gate is a platform-level security boundary. Individual
repositories cannot disable it. Per-repo configuration (which stages are
enabled, which labels trigger automation) operates **within** the
authorization boundary. A repository can disable a stage entirely but
cannot make it available to unauthorized users.

A repository can add permission sources through the `authorization`
field (today only `owners_file`). These sources extend the
`has_repo_permission` helper and the Go `IsAuthorized` check; they do not
bypass the gate or change the thresholds.

## Versioning

This is a living normative document under
[ADR 0015](../../../ADRs/0015-normative-specifications-directory.md).
Breaking changes require `docs/normative/authorization/v2/`.

| Change | v1 impact |
|--------|-----------|
| **Breaking** (requires v2): remove a role from the hierarchy, raise a default threshold, remove a documented exception, change fail-closed to fail-open | Dispatch implementations must migrate |
| **Non-breaking** (allowed in v1): add a role, lower a default threshold, add a new exception, add forge mappings, clarify documentation | Existing dispatch behavior is preserved or relaxed |
