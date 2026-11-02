# Authorization Contract v2

Normative rules for event actors, entity CEL facts, and model-bound records.

This is the target contract for CEL/entity routes and their v2 model-bound
admission after migration. Legacy non-CEL, non-entity workflow configurations
remain governed by Authorization v1 and their existing workflow guards; these
new per-harness and entity rules do not apply to them.

This version keeps a `triage` platform floor for humans, including the narrow
verified label-transition grant below, and applies the selected harness's
effective `human_min_role` to prompting events, actor-originated facts needed
for actionable CEL results, and Fullsend-controlled model-bound records.
Harness CEL may impose further
conditions but cannot replace these admission gates.
[ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md) establishes the
floor and harness minimum while preserving separate bot grants and fresh
model-bound checkpoints.
This version also adopts selective entity acquisition and decision-focused
audit evidence for the future entity-first path under
[ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md),
with bounded CEL actor-verdict reuse under
[ADR 0148](../../../ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md).
Existing dispatch continues to use [v1](../v1/README.md) until migrated.
The historical decision and rationale are recorded in
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md).
The [NormalizedEvent v1](../../normalized-event/v1/) specification defines the
`actor.role`, `actor.kind`, `actor.role_verified`, and `actor.bot_role` fields
consumed by this contract. Bot identity and role resolution are specified by
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
actors without project membership. The platform human floor denies `read`,
`none`, and `external` alike; a selected harness may require more than the
floor. The `author_association` field is **not** used because it does not
correctly reflect private org membership
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
is opt-in, exactly as specified in
[v1](../v1/README.md#forge-native-permission-mapping). With no `jira` entry,
every Jira event resolves through the project role roster. With an entry, the
mapping source for each event is the provider's `write` and `triage` lists
(authoritative once either list is set), the project role roster, or no
resolved role (`none`), per event type and the provider's `labels` rule. The
rest of this paragraph describes the project role roster, the source used for
every event when no `jira` entry exists (or when an entry sets
`labels: project_roles` and no list) and for slash commands when any other
entry sets no list.
The roster is the project's role membership for the
configured `--jira-project`, resolved once per poll cycle for event-backed
dispatch. Entity-source CEL may reuse a completed
provider-verified actor verdict across poll cycles and bound `run` checks only
under the bounded cache rules below; event admission does not use that cache.
An actor is matched by Jira account ID. Role names are matched
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
identity signals. For post-migration producers with `actor.kind: bot`,
`actor.role` MUST be `none`, and
`actor.role_verified` is true exactly when the provider completes the bot-role
lookup, including a successful no-match result; it is false when resolution
fails. For `actor.kind: human`, `actor.bot_role` MUST be absent or `null`, and
`actor.role` contains the forge permission role when `actor.role_verified` is
true. For non-label dispatch, only a non-null, provider-resolved
`actor.bot_role` can pass the bot gate. The human `triage` floor does not apply
to a recognized bot; platform authorization instead requires a valid
normalized event, an applicable source and target, and a transition supported
by the selected harness. CEL may further restrict routing but cannot create or
broaden bot identity authorization.

For humans on all event paths, including a verified label addition,
`role_verified: false` makes `actor.role` unusable as a forge-permission claim.
It does not invalidate an independently verified configured-source grant such
as OWNERS for the same verified actor and target. An exact provider-proven
human label addition may instead use its narrow `triage` grant for a harness
whose minimum is `triage`; stronger minima require a separately verified
effective role. The versioned CEL input MUST carry a verified effective role
and its source when present, and MUST distinguish a label grant from role
evidence without reinterpreting v1 `actor.role_verified`. Missing
`role_verified` on a trusted pre-migration event is treated as legacy
input and retains the current human authorization behavior; new adapters MUST
emit the field. Legacy status is determined by the adapter implementation
contract, not by guessing from this field's absence. An accepted forge label
mutation supplies only the transition-specific human `triage` grant, not a
verified repository role. For a bot using its separate narrow label grant,
omission means that bot-role lookup is not applicable, not that it failed.

Authorization-sensitive CEL role comparisons in v2 MUST use the versioned
verified effective-role input. The legacy `event.actor.role` value is not a
verified permission claim when `actor.role_verified` is false, even if a narrow
label grant admits the event. A harness that relies on a legacy role comparison
for a stronger condition MUST update its predicate for the versioned input
before using v2 routing; it cannot interpret the grant as that stronger role.
The same rule applies to an overlay `when:` that selects `human_min_role`.

## Platform floor and per-harness human minimum

Fullsend MUST require a verified effective normalized human role of at least
`triage` as a platform floor for ordinary human actors, with freshness
specified separately for each admission path below. The exact accepted human
label addition and its proven projection may instead use the
narrow, provider-proven `triage` grant below; it is not a general role claim.
For each selected harness, Fullsend MUST resolve and enforce that harness's
effective `human_min_role` before its CEL predicate runs. An effective role
comes from the source provider or a trusted configured permission source such
as OWNERS. An explicit `human_min_role` at the top level or in a guarded
overlay MUST be one of `triage`, `write`, `maintain`, or `admin`. Every explicit
value, including one in an overlay that does not match, MUST be validated. An
invalid value or unverifiable harness revision denies evaluation; it MUST NOT
use the default. An explicitly empty value is invalid rather than absent.
Within each base layer, matching overlays apply in order, with later matches
winning, before that layer merges into its child. An explicit child scalar
overrides the inherited value, then the child's matching overlays apply. When
no explicit value survives this resolution, the resolved harness `role`
supplies this default:

| Resolved harness `role` | Default `human_min_role` |
|-------------------------|--------------------------|
| `triage` or `review` | `triage` |
| Any other role, including a custom or unrecognized role | `write` |

An explicit value may lower a `write` default to `triage` or require
`maintain` or `admin`, but it cannot lower the platform floor. The minimum is
resolved under that trusted composition and overlay order before the
harness-specific actor gate or trigger CEL. An overlay that sets this field
MUST pass [ADR 0112](../../../ADRs/0112-overlays-may-set-any-harness-field.md)'s
guarded-field dependency validation: its `when:` may read only the normalized
event specification's trusted event or entity fields, platform-verified
`runtime.forge`, and `config` from the pinned trusted target-repository
revision, plus the versioned verified effective-role input. That input is
trusted for this guard only when its identity, target, source, and role are
independently verified. Guard inputs and their provenance MUST be available
without using actor-filtered entity facts, model content, or a previous
admission decision under the minimum being selected. A role-based guard MUST
use the versioned verified effective-role input. Raw `event.actor.role` is
excluded for this field even though v1's trusted-field list permits it, and
the narrow human label grant is not a role. The versioned CEL input schema
MUST specify the verified role's exact path and provenance before role-based
overlays can be enabled. Trigger CEL and deprecated `forge:` blocks cannot set
the field. Harness `role` selects the agent
credential identity (the mint role on GitHub); `privilege_levels` scopes tokens
for run stages.
Neither token permissions nor workflow stage names infer a different human
threshold. The default table is an authorization policy, not a claim about
the capabilities of every harness using a role.

ADR 0112's trusted-field check is a dependency check, not proof that the
matching value is safe. For example, a change author can choose a fork, but
only a writer can make `is_fork` false. A layer's non-overlay
`human_min_role` is its overlay baseline: use the layer's explicit scalar when
present, otherwise the inherited value, otherwise the role-derived default.
A matching overlay MAY preserve or lower that baseline only; it MUST NOT raise
it. To conditionally require a stronger minimum, set that stronger value as
the non-overlay baseline and let matching overlays lower it only when their
trusted guard proves the relaxation is intended. A known-absent guard is a
nonmatch, so the baseline remains at least as restrictive as every overlay
value. The final selected minimum applies to the prompting actor and to other
actors' decision-relevant facts and model-bound records for the entire
evaluation cycle.

A known absent event or optional key is a nonmatch under ADR 0112. If required
verified guard metadata is unavailable, or the CEL evaluation has another
error, Fullsend MUST fail the evaluation rather than treating the error as a
nonmatch and using a possibly weaker default. All processes that make an
admission decision MUST resolve overlays from one verified context within
that decision. A later bound `run` is a new decision: it MUST reload trusted
config and harness sources and recompute the guard context and ordered overlay
result against its current canonical entity. It MUST NOT reuse a dispatch or
poll allow as authority. For an event-bound run, the integrity-bound verified
event remains the event input; current entity and policy inputs are resolved
again. If a guard needs `entity.source.system`, Fullsend MUST obtain only that
provider-verified structural metadata from the event or candidate identity, or
with a platform discovery credential independent of the still-unpinned
harness run credential. It MUST NOT acquire actor-originated entity facts for
this purpose.

Policy inputs have separate trusted revision identities. The target-repository
checkout pins `.fullsend/config.yaml` and any `config` facts used by an overlay;
for a change-proposal event this is the provider-verified target/base commit,
never the proposed head. Local harness and `base` files in that checkout share
its revision. For other events, the platform selects and pins an equivalent
trusted target revision. An actor-supplied ref, CEL input, proposed head
checkout, or mutable target working tree MUST NOT select or replace these
policy files or their revision.

A remote registered harness or `base` layer MUST be selected through that
trusted registry and base chain, pass a trusted remote-resource allowlist, and
have its content verified against a declared hash before contributing `role`
or `human_min_role`. Its source revision and content hash are separate inputs
to the composed policy identity; they do not share the target-repository
revision. A hash or allowlist match alone does not authorize an actor-supplied
remote source. Configured actor-permission sources retain their separate
trusted-ref and freshness requirements below.

`human_min_role` constrains human actors and their records only. A
provider-classified bot using the exact verified label handoff or a bot with
an exact registered role uses its separate gate, even when the selected
harness has a `maintain` or `admin` human minimum. Harness CEL and bot policy
may narrow those bot paths; the human minimum does not serve as bot
authorization or deny an otherwise authorized bot.

The same effective minimum applies independently to a human prompting event,
actor-originated entity facts whose authority is needed to make CEL actionable
or prove an actionable absence or aggregate, and every Fullsend-controlled
model-bound record. Event admission uses fresh authority. Entity CEL may use a
live or eligible bounded actor verdict under the rules below; model-bound
records receive their own fresh checks at the required exposure checkpoints.
Raw state used only for conservative deferral follows the causal classification
below and does not require authorizing every historical updater. For ordinary
role-based admission, `read`, `none`, `external`, and unresolved human roles do
not pass; only the exact verified human label transition and its proven
projection may use the narrow `triage` grant. Admission of one event does not
authorize another actor's fact or model-bound record. Trusted Fullsend
invocation provenance, rather than a human event actor, authorizes
eventless entity discovery; decision-relevant actor-originated facts still
pass the selected harness's minimum.

Each evaluation cycle and any sandbox it starts MUST use one trusted composed
harness revision, resolved `role`, and effective `human_min_role`. Fullsend
MUST pin that policy within the cycle and retain it through that cycle's actor,
entity, and model-record checks. Its policy identity MUST bind
the trusted checkout revision, registry and composed harness content digest,
trusted remote-resource allowlist revision or digest, verified remote-layer
hashes, resolved `role`, ordered overlay matches and overrides, verified guard
inputs and provenance, effective minimum, and its top-level, overlay, or
default source.

For a separate bound `run`, the dispatch/poll handoff identifies the candidate
harness and event/entity binding; it is not an authorization decision. Before
model-context acquisition/staging or sandbox launch, `run` MUST independently
reload the trusted target config and selected harness/base chain, reevaluate
applicable `config.yaml` `when` flags and harness overlays against current
verified inputs, re-fetch the bound canonical entity, and recheck all current
authorization and launch conditions under
[ADR 0144](../../../ADRs/0144-reuse-poll-and-entity-bound-run-processes.md).
This includes the prompting-event gate when an event is present, trusted
Fullsend-origin gate when it is absent, actor/fact admission, and trigger CEL.
The run MUST then pin the policy identity it actually verified and use that
same identity throughout its own context-staging, sandbox, and model-record
checks.
Dispatch and run policy identities do not have to match: a change between
them is handled by this fresh run-time evaluation, not by inheriting the
earlier allow. If current inputs cannot be loaded or verified, or the candidate
no longer meets the current gates, `run` MUST stop before context exposure or
launch. If policy changes during a single run cycle, Fullsend MUST stop or
re-evaluate from the first gate under the newly loaded policy; it MUST NOT
silently mix minima or overlay outcomes. A changed guard result or an expired
guard claim requires the same stop or first-gate re-evaluation. The run's own
trusted source revisions and effective policy MUST be recorded for audit. A
new child cycle under
[ADR 0144](../../../ADRs/0144-reuse-poll-and-entity-bound-run-processes.md)
may resolve and record a new pin.

The platform MAY reject a below-floor human event before loading harnesses
unless the event has the verified label-transition grant. It MUST enforce each
selected harness's effective minimum before evaluating that harness's CEL
predicate, including for a verified human label addition. When admission uses
a role, the platform MUST expose that same verified effective human role to
CEL, including any valid configured OWNERS upgrade. When it uses the narrow
label grant, the versioned CEL input MUST expose grant provenance separately
and MUST NOT synthesize a verified `triage` role. Harness CEL MAY add stronger
or narrower conditions, but cannot admit an actor or fact that failed the
applicable gate. A false CEL predicate is a harness nonmatch, not a platform
authorization denial.

For each selected harness, the event-admission audit MUST bind the verified
actor identity, effective role and source or narrow label-grant evidence, the
policy identity above, and the gate outcome, including denial before CEL. For
separate dispatch/poll and run processes, the audit records both the policy
used for candidate selection and the independently loaded run policy, plus the
run-time recheck outcome. A dispatch policy identity forwarded in the handoff
is correlation evidence only; it is not permission and need not match the run
policy. A bot decision records its registered role or verified
label-transition evidence instead of a human role. An early platform-floor
denial records the actor evidence and floor outcome.

### Current dispatch compatibility

The existing GitHub bash dispatcher retains its route-specific human guards
and bot compatibility behavior, as recorded in
[Authorization v1](../v1/README.md). Those routes do not classify CEL
harnesses or set their role requirements. After provider-backed
`actor.bot_role` recognition, bots use the separate bot gate above.

> **Current Go implementation:** Since
> [#8727](https://github.com/fullsend-ai/fullsend/pull/8727),
> `fullsend dispatch` runs `IsEventAuthorized()` before harness enumeration,
> then `FilterAuthorizedHarnesses()` applies each candidate's `role`-derived
> `triage` or `write` default before `MatchHarnesses()`. Jira polling preloads
> candidates, then runs both authorization checks before CEL. This implements
> the v1 default for ordinary events.
> Migration must resolve any explicit or guarded-overlay minimum before that
> harness's CEL predicate and expose the same verified effective role to CEL.
> Today Go still lets human label-added events bypass the per-harness threshold;
> it applies an OWNERS upgrade to its gate but passes the original event role
> to CEL. The harness loader does not recognize `human_min_role` and can ignore
> an unknown YAML key. Adding the field today does not enforce it, including
> an intended `maintain` or `admin` minimum. A v2-conforming runner MUST reject
> a configured `human_min_role` it cannot parse, validate, or enforce rather
> than silently treat it as absent. The field cannot be deployed as effective
> policy while a selected runner still ignores it. The current execution
> matrix carries an agent name and credential role; these are candidate-routing
> metadata, not an authorization result. V2 requires an integrity-bound
> candidate and event/entity handoff plus an independent run-time policy
> recheck.
>
> The legacy stage workflow pins the target repository's `.fullsend/config.yaml`
> to the PR base commit. `run` first uses a configured local or URL source;
> remote config sources require a declared SHA-256. A source-less built-in
> entry is only an override and falls back to `fullsend-ai/agents`. Each `run`
> resolves that fallback's release tag (or `main` for a source build) to a
> commit SHA and fetches from the SHA-pinned URL, but does not verify a declared
> content hash. The legacy route resolves its harness in one `run`; CEL dispatch
> and its later `run` currently resolve independently. A v2 CEL/entity `run`
> MUST reload the trusted config and harness, recompute config `when` flags,
> harness overlays, and trigger CEL, and recheck launch gates against the
> current entity. The dispatch/poll result is a candidate only; the policy
> actually loaded by `run` is pinned for that run's own checks through context
> staging and sandbox launch. A changed source between dispatch and run
> triggers a new evaluation rather than a same-revision requirement. Current
> behavior is visible in
> [`reusable-dispatch.yml` at `c0695aaa`](https://github.com/fullsend-ai/fullsend/blob/c0695aaa5aee66fe500c4fb904ed4924e0b400da/.github/workflows/reusable-dispatch.yml#L135-L148),
> [`run.go` source selection at `c0695aaa`](https://github.com/fullsend-ai/fullsend/blob/c0695aaa5aee66fe500c4fb904ed4924e0b400da/internal/cli/run.go#L7200-L7249),
> [`config.go` URL pin validation at `c0695aaa`](https://github.com/fullsend-ai/fullsend/blob/c0695aaa5aee66fe500c4fb904ed4924e0b400da/internal/config/config.go#L601-L608),
> and [`run.go` fallback resolution at `c0695aaa`](https://github.com/fullsend-ai/fullsend/blob/c0695aaa5aee66fe500c4fb904ed4924e0b400da/internal/cli/run.go#L7262-L7361).

## Fail-closed behavior

The human role gate is **fail-closed**: when the effective role or a harness's
effective minimum cannot be determined, that harness's path is denied. The
exact verified human label addition may still satisfy a `triage` minimum with
its narrow grant; an unresolved role never satisfies a stronger minimum.
The registered-bot and separate bot-label gates use their own provider
evidence.

The entity-evaluation rows below specify the future ADR 0098 path as revised by
[ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md)
and [ADR 0148](../../../ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md)
and do not describe behavior currently implemented by `fullsend dispatch` or
`fullsend poll`.

The human role-lookup rows below apply to every target human event path. The
bot-specific rows are ADR 0107 target behavior; legacy events remain subject
to the compatibility rules until implementation migration is complete.

| Condition | Outcome |
|-----------|---------|
| Collaborator API returns a custom `role_name` with no effective permission signal | Forge role maps to `none`; a separate verified OWNERS grant may satisfy the selected harness minimum, or an exact verified label addition may satisfy `triage`; otherwise deny |
| Collaborator API returns an error or times out | Forge role is unusable; a separate verified OWNERS grant may satisfy the selected harness minimum, or an exact verified label addition may satisfy `triage`; otherwise deny |
| Bot-role lookup returns no registered identity on a non-label path | `actor.role` remains `none`; `actor.role_verified` is true; `actor.bot_role` is absent; denied |
| Bot-role lookup fails or is unverifiable on a non-label path | `actor.role` remains `none`; `actor.role_verified` is false; `actor.bot_role` is absent; denied, with the failure retained in resolver/audit diagnostics |
| Collaborator API response is not valid JSON or does not match the expected shape | Forge role is unusable; a separate verified OWNERS grant may satisfy the selected harness minimum, or an exact verified label addition may satisfy `triage`; otherwise deny |
| `actor.role` is empty or missing | Event fails `NormalizedEvent` validation; never reaches dispatch |
| `actor.role_verified` is false for a human, including a verified label addition | Ignore `actor.role`; require a separate verified configured-source role meeting the selected harness minimum, or the exact human label grant for a `triage` minimum; otherwise deny |
| Trusted harness `human_min_role` is invalid or the resolved harness role or revision is unverifiable | Deny that harness; do not silently use the default or evaluate its CEL predicate |
| An overlay setting `human_min_role` reads an untrusted or admission-dependent field | Reject that harness configuration before resolution |
| An overlay value is above its layer's non-overlay baseline | Reject that harness configuration before resolution; overlays may only preserve or lower the baseline |
| A `human_min_role` guard lacks required verified evidence or has a CEL evaluation error | Fail the evaluation; do not treat the overlay as a nonmatch or use a fallback minimum |
| A bound CEL/entity `run` cannot reload the trusted config/harness or re-evaluate current config `when` flags, overlays, and launch gates | Stop before context acquisition/staging or sandbox launch; the dispatch/poll decision is only a candidate selection |
| The selected harness policy changes within one evaluation or run cycle | Retain that cycle's verified policy identity, or stop and re-evaluate from the first gate under the new revision; never mix them |
| Human event actor meets the platform `triage` floor but not the selected harness minimum | Deny that harness before CEL; other harnesses are evaluated under their own minimums |
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
| A required entity field or history scope is not yet loaded, or pagination/coverage is incomplete | Its value is unknown, never false or empty; read more or defer to bound `run`; no sandbox starts from an unresolved result |
| A poll candidate remains inconclusive, cannot be handed off, or its bound run fails before completion or safe deferral | Retain a durable retry path or prove source rediscovery after checkpoint advancement; no sandbox starts from an unresolved decision |
| A required entity source is unsupported, unavailable, fails, or cannot establish complete coverage within allowed bounds | Evaluation fails closed; no sandbox starts and the failure is not recorded as a false CEL result |
| A conservative dependency or provider query cannot be shown to include every authorized fact that could change a definitive CEL result | Expand the plan or leave the result unknown; if it cannot be resolved, evaluation fails closed |
| Current attribution or required actor-role or exact label-grant evidence for a fact requiring the actor gate is missing or unverifiable | The fact cannot be admitted as authority; if it could affect an actionable result, evaluation remains unknown or fails closed and cannot start a sandbox |
| A versioned platform policy with an explicit maximum age no greater than five minutes for cross-check CEL actor-verdict reuse is missing or unverifiable | Reuse is disabled; obtain a live authoritative role verdict or leave dependent CEL unknown, with no sandbox on unknown |
| A needed CEL actor-verdict cache entry is expired, invalidated, or scoped to another source, actor, repository, policy, or revision | Refresh from the authoritative actor-role source; if that cannot complete, the dependent result remains unknown and cannot start a sandbox |
| The actor of a fact requiring the gate is human and below the selected harness minimum without the exact applicable human label grant for a `triage` minimum, or is a bot with no recognized role or narrow grant | The fact is excluded from that harness's authorized CEL view and cannot trigger execution |
| Versioned normalized-entity contract is missing or incomplete | Entity-first execution denied |

## Exceptions

The verified label-added transition below supplies an exact `triage` grant for
a human author and a separate narrow grant for a provider-classified bot
without a registered bot role. A human label author must meet the selected
harness's minimum through that `triage` grant or a separately verified role.
Registered-bot roles use a separate gate. Legacy compatibility paths do not
create additional v2 grants or authorize unrelated entity facts or model-bound
records.

### Label application

**Current compatibility behavior:** GitHub authorizes an added-label event
without applying the ordinary `actor.role` threshold. This existing behavior
preserves bot-to-bot handoffs such as adding `ready-to-code` after triage
completes, including handoffs from provider bots that are not Fullsend-
registered.

**V2 target behavior:** When `transition.kind` is `label_changed` and
`label.action` is `added`, the GitHub adapter MUST establish an accepted forge
label mutation and authoritative actor-to-transition provenance. For a human,
that evidence supplies a narrow `triage` grant for the exact accepted
transition and its proven entity projection. It satisfies a harness with
`human_min_role: triage` without claiming that the actor has a verified
repository role. A `write`, `maintain`, or `admin` minimum requires a separate
fresh, verified effective human role meeting it. A provider-classified bot
MAY use its own verified-transition grant without an `actor.bot_role` lookup,
preserving bot-to-bot handoffs. Neither grant authorizes unrelated history or
model-bound records. At the time of ADR 0107, GitHub is the only
implementation; another adapter MUST provide equivalent forge-authoritative
evidence before enabling either grant, and absent or unverifiable evidence
denies the event. The platform gate MUST NOT inspect the label name or
maintain an agent-role label allowlist; harness/CEL routing owns that mapping
and may further narrow the dispatch.

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

### Legacy lifecycle close (pull\_request\_target.closed)

**Current bash compatibility behavior:** The GitHub bash dispatcher routes
`pull_request_target.closed` to retro without a separate collaborator-role
check, so any closer may trigger it. This is not a read-only capability:
retro's token can write issues and pull requests, and its post-script can
file issues, comment, and apply labels. The Go CEL path has no close-event
exception and applies the selected harness's role-derived default; a `retro`
role requires `write`.

The ungated bash route is not a general v2 grant for CEL-based dispatch or
entity facts. Under this contract, a human close-event actor must meet the
selected harness's effective minimum before CEL. A `retro`-role harness
defaults to `write`; an explicit `human_min_role` may choose another allowed
value. The legacy `/fs-retro` slash command retains its bash `write` guard.

### Schedule and manual dispatch

When `source.system` is `schedule` or `manual`, the actor is the configured
operator or service identity (for example, a GitHub App bot or workflow
`GITHUB_ACTOR`). Adapters MUST classify that identity from authoritative
provider metadata. Human identities use verified forge permissions and the
selected harness's effective minimum; bot identities use provider-backed
`actor.bot_role` resolution and retain `actor.role` as `none`. An unrecognized
or unresolved bot identity is denied.

**Current compatibility behavior:** Until provider-backed bot-role resolution
is wired into the adapters and dispatch path, adapters set `actor.role` to the
service identity's effective repository permission (typically `write`), so
the standard permission gate applies.

> **Target contract, not yet implemented:** Existing schedule/manual
> compatibility handling remains authoritative until provider-backed bot-role
> resolution is wired into the adapters and dispatch path.

### Entity-source CEL evaluation

This future path was adopted by
[ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md), clarified by
[ADR 0144](../../../ADRs/0144-reuse-poll-and-entity-bound-run-processes.md),
and revised for selective acquisition and audit by
[ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md),
with bounded cross-check actor-verdict reuse under
[ADR 0148](../../../ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md).
[ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md) supplies the
platform floor and per-harness human minimum.
It applies to an entity-source harness's CEL predicate whether a verified
prompting event is present or `event == null`; legacy event-only dispatch is
unchanged. The entity-source path is not yet implemented by `fullsend dispatch`,
`fullsend poll`, or bound `fullsend run`. Entity-first execution MUST NOT be
enabled until the versioned normalized-entity contract defines CEL-eligible and
prompt-eligible fields and their coverage states. That contract is a platform
allowlist; a harness may request fewer fields but cannot expand it.

> **Planned:** Entity-source CEL is not implemented and remains disabled until
> the [normalized-entity contract tracked by issue #8420](https://github.com/fullsend-ai/fullsend/issues/8420)
> and the run-time authorization and launch checks described here are
> implemented. Legacy non-CEL, non-entity workflows remain on Authorization
> v1.

With a prompting event, the event-actor authorization gate for the selected
harness still runs before its CEL predicate; neither discovery nor an
entity-source query replaces it. The event actor authorizes that event, not
other actor-originated entity facts. A verified human label addition may use
its exact `triage` grant for a triage-minimum harness; a stronger minimum
requires a separately verified effective role. The separate verified GitHub
bot label grant may admit a provider-classified bot's exact accepted
transition and proven entity projection without a registered bot role.
Neither grant authorizes other label applications, the entity's current label
state without transition provenance, or other records from the same actor.
Current label state may use a grant only when the adapter proves it is the
projection of the same accepted transition, with no intervening replacement.
For a `triage`-minimum harness, this exact projection remains eligible while
that proof holds, even if the labeler later loses repository permission; it is
evidence of the accepted mutation, not a current role. A higher human minimum
still requires the labeler's current verified effective role, and model-bound
records still pass their separate fresh gate.
For an eventless check, the following Fullsend-origin and platform-eligibility
gate applies instead.

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

Each entity-source evaluation's audit record MUST identify its verified
initiator (the event actor for an event-bearing check, or the trusted Fullsend
invocation identity for an eventless check), target repository, effective
policy and pinned policy identity, canonical entity, acquisition-plan identity,
and decision outcome, including `UNKNOWN`, deferred, or denied outcomes. The
audit MUST record needed source scopes and bounded queries, their coverage and
completeness evidence, current attribution, live or eligible cached verified
role or exact label-grant provenance, and harness-gate outcome for individual
actor-originated records used as decisive witnesses. Complete
negative or aggregate claims require bounded query and source-coverage
evidence bound to the effective minimum, authorization policy, permission or
grant source, and check time or equivalent authorization epoch.
For a label-grant witness, the audit MUST bind the provider's accepted
transition proof, current source revision and proven projection, and check
time; the grant is not recorded as a verified actor role.
Summaries MAY use counts and digests of admitted, excluded, and unresolved actor
checks; they do not require
an audit roster of every record and actor examined to establish completeness.
When an actor verdict is reused for CEL, the audit MUST record bounded evidence
of its authoritative source, lookup start time or age measured from that start,
the completed verdict, the maximum age allowed by the effective platform
policy, effective authorization policy and source revision, and whether a live,
local-cached, or shared-cached verdict was used. This may be aggregated for
collection-wide checks rather than listing every actor.
If a cached actor verdict contributes to a false CEL result, the audit MUST
also identify the bounded recheck or rediscovery guarantee, or show that the
candidate remains retryable.
Unrelated history is not fetched or listed solely for audit. The audit does not
retain raw rejected content. An eventless child cycle attributes initiation to
the Fullsend identity, never to an earlier event actor or historical entity
actor.

Authorization of an event actor or Fullsend invocation origin does not
authorize other entity content. For each evaluated harness, Fullsend derives a
conservative acquisition plan from its CEL predicate and the normalized-entity
contract. The platform retains a closed classification of action-indicating
categories; it need not fetch every category for every predicate. The plan
MUST cover every field, history range, or derived fact that could change the
predicate's result, including actor-originated issue or
change-proposal bodies, comments, reviews, and label applications or current
label state when used as action-indicating evidence. Counts, existence tests,
and other aggregates over actor-originated records have the same dependency.
The evaluator MAY defer planned reads and stop once partial evaluation yields
a definitive result.
A source field is actor-neutral for a particular CEL use only when an
actor-originated change cannot make the predicate newly actionable. For a
service-clock predicate, raw authoritative `updated_at` means time since any
forge update; it is not recomputed from actor-filtered history. Where the
provider guarantees that it only advances, `updated_at < moving_cutoff` can
become true as time passes. An update, including one by an unauthorized actor,
can defer that staleness run but cannot make it newly eligible. A harness that
needs time since the last authorized update must acquire and authorize the
relevant history. By contrast, `updated_at >= cutoff` can become true because
of an update and requires provenance and authorization for that causal
occurrence, or remains unknown. Raw `comments_count`, current labels, or
newest-comment metadata cannot enter the authorized CEL view if an unauthorized
record or change could affect the result. Fullsend MUST recompute such a fact
from admitted records, prove the relevant causal occurrence or platform
exception, or leave it unknown. Presenting an action signal as current state
never bypasses the actor gate.
A provider query MAY narrow reads only when it is proven to include every
authorized fact that could affect the result; otherwise Fullsend expands the
query or leaves the dependent result unknown. A provider aggregate may replace
individual reads only if it preserves the actor-filtered meaning under the
eligible CEL verdicts for the current check and proves the coverage needed for
that result. Unanalyzable dynamic access requires a conservative broader plan
or a fail-closed unresolved outcome.
Cached fields or history may satisfy a planned read only after their source
revision and query coverage are validated for the current check. Accepted
events and ordinary entity wakeups invalidate affected entity facts under
[ADR 0144](../../../ADRs/0144-reuse-poll-and-entity-bound-run-processes.md).
They do not by themselves invalidate a separately scoped actor-verdict cache
entry; a trusted authorization change does.

Fullsend distinguishes not requested or not loaded, partially loaded,
successfully empty, absent or inapplicable, unsupported, and failed data.
Unloaded or incomplete data MUST NOT masquerade as an empty collection or a
false condition. For every actor-originated fact whose authority is needed to
make CEL actionable or establish an actionable absence or aggregate under the
causal classification above, Fullsend MUST re-establish its current
source-revision actor and obtain an eligible verified authorization verdict
before admitting the fact into the selected harness's CEL view. A human
ordinarily must have an effective role from the provider or a configured
permission source meeting that harness's `human_min_role`. Only the exact
verified label-added transition and its proven projection may instead use the
human `triage` grant when the harness minimum is `triage`; the grant is not an
actor-role verdict for other facts or CEL role comparisons. A bot needs a
provider-backed exact registered role under
[ADR 0107](../../../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md),
except for the exact transition covered by the verified bot label-added grant.
That grant requires accepted transition provenance and positive provider bot
classification and supplies authority only for that transition, not unrelated
entity history. A verified effective human role remains available to harness
CEL when present; the human label grant is exposed as separate provenance.
CEL may impose further conditions but cannot replace the gate. An
unauthorized fact is excluded from that harness's authorized view. Missing or
unverifiable attribution or authorization of a fact needed to prove an
actionable result
remains unresolved and cannot prove absence. Historical or inferred actor
relationships are insufficient; only the bounded provider-verified actor-role
cache defined below may replace a live actor-permission lookup. CEL may
further restrict the authorized view, but cannot create authority.

#### Bounded CEL actor-verdict cache

This exception to a fresh actor-permission lookup for each entity check is
adopted by
[ADR 0148](../../../ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md).
For entity-source CEL only, Fullsend MAY reuse a completed, authoritative
human permission or exact registered bot-role verdict across entity checks and
harnesses, poll cycles, separate `poll` and bound `run` loops, and the bound
run's final CEL decision. The cached value MUST be the verified effective
role or verified absence, never a platform-floor boolean, per-harness
authorization decision, or CEL result. Every check reapplies the selected
harness's effective human minimum or bot gate, as applicable, and its CEL
conditions. Both positive and verified negative results may be cached;
errors, timeouts, unresolved lookups, caller claims, and partially verified
roles MUST NOT be cached. A verified label grant is transition-specific and
MUST NOT be cached as an actor-role verdict. The key MUST bind the provider,
tenant, canonical repository or project, immutable actor ID, verified actor
kind, effective authorization policy and its revision or digest, and
permission source. When applicable, it also binds the selected ref and the
presence or absence and content or revision of both OWNERS and OWNERS_ALIASES,
or the bot-registry revision. A login or entity ID is not an actor cache key.
Where an authority does not expose a revision token, the authoritative
lookup's start time defines the
observed epoch; known changes to that source still invalidate its entries.

Within-check memoization under
[ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md)
avoids repeated lookups while the verdict remains eligible under an absolute
five-minute hard cap. Cross-check reuse is disabled until a versioned platform
policy defines and enforces an explicit maximum age no greater than five
minutes. Five minutes is an arbitrary initial cap, not a measured optimum.
Once entity polling is in production, revisit the cap against observed
authorization-freshness needs and SCM API rate-limit pressure. The platform MAY
choose a shorter age; repository or harness settings
MUST NOT widen the hard cap or the effective platform bound. Only a completed
verdict may enter the cache. Its absolute expiry is measured from the
authoritative lookup's start at the earlier of the hard cap and any shorter
platform bound. An entry expires at that fixed time even if it is read, handed
from `poll` to `run`, or retained by a shared host cache; no hit extends its
lifetime. An expired verdict must be refreshed before it can contribute to a
definitive CEL decision, including during one long progressive check. Known
role, membership, OWNERS,
OWNERS_ALIASES, bot-registry, or effective-policy changes MUST invalidate
affected entries without waiting for TTL expiry. An ordinary entity wakeup
invalidates affected entity facts, not an otherwise valid actor verdict,
unless it carries trusted evidence of an authorization change. Shared cache
readers MUST enforce expiry with a trusted clock or a conservative skew
allowance; uncertain age makes an entry unusable. An expired, invalidated, or
mismatched entry requires a new authoritative lookup. If that lookup cannot
complete, the dependent CEL result remains `UNKNOWN` or is denied
under the fail-closed rules above; the stale verdict is never used. A shared
host cache MUST remain inaccessible to the sandbox and agent.

A false CEL result dependent on any cached actor verdict MUST cause a recheck
after that verdict expires or is invalidated, and no later than the first of
those events plus a bounded, platform-controlled discovery or reconciliation
interval, even when the entity has not changed. The recheck obtains a new
verdict and may be scheduled directly or guaranteed by source rediscovery. The
platform MUST define that interval and either retain a durable candidate
through checkpoint or watermark advancement or prove rediscovery after it. If
timely re-evaluation cannot be guaranteed, the cached verdict MUST NOT close
the candidate: Fullsend uses a
live verdict for a final false decision or keeps the candidate inconclusive
and durably retryable under a bounded retry schedule. A cached actor result
alone cannot permanently close the candidate.

This deliberately accepts CEL actor-role evidence up to the platform
maximum age. A still-valid positive cache entry may admit a newly observed
actor-originated record after an unobserved revocation, and a negative entry
may exclude a newly promoted actor until expiry and the required bounded
recheck. A positive entry may likewise keep a negative
predicate false after an unobserved revocation. The bound run still checks the
current entity facts, source-revision actor, and coverage before its final CEL
decision; only the verified actor-role verdict may be reused within TTL.
The CEL cache MUST NOT satisfy event-actor admission or
[ADR 0133](../../../ADRs/0133-actor-attributed-data-security-boundary.md)'s
model-bound record admission and exposure checkpoints. Those gates continue to
resolve fresh authority under their separate rules. Fresh model-bound checks
may redact records or deny further execution, but they do not undo a sandbox
launch admitted using a positive CEL cache entry. The launch may already have
occurred after an unobserved revocation within the permitted age.

Fullsend MAY evaluate the predicate over a partially materialized authorized
view. It may return a definitive `true` or `false` only when that result is the
same for every possible completion of still-unloaded inputs admitted under the
actor-verdict rules above and consistent with established source coverage.
This quantifier includes still-valid cached CEL verdicts within the bounded
platform age, even if actor permission changed without a known invalidation.
Coverage MUST account for pagination and concurrent source changes; a required
collection remains incomplete unless a stable view or sound reconciliation
establishes its relevant range. For example, an authorized witness may settle
an existence test as true; false or a negative condition requires complete
coverage of its relevant authorized search space. An `UNKNOWN` result requires
further relevant field or history reads and re-evaluation in `poll` or bound
`run`, or retention
as retryable inconclusive work. A provider failure, unsupported source,
unresolved authorization, or inability to prove completeness in a remaining
decision dependency within allowed bounds fails closed: it cannot be converted
to false, empty, or a sandbox run.

`fullsend poll` MAY hand an unresolved candidate and its acquisition hints to
a bound `fullsend run` without claiming a CEL match. A definitive true result
in `poll` also selects only a candidate, not a sandbox launch. The bound run
MUST independently re-resolve current entity state and actor provenance, use a
live or eligible cached CEL actor verdict, and reach a definitive true result
before starting a sandbox. A definitive false result does not start one. Any
deciding verdict that expires or is invalidated before sandbox admission MUST
be refreshed and CEL re-evaluated; otherwise the decision remains inconclusive
and the sandbox does not start. Any handoff is provisional. Until the bound run
reports a definitive false decision with any cache-dependent recheck durably
arranged, completes actionable work, or safely defers it under
[ADR 0144](../../../ADRs/0144-reuse-poll-and-entity-bound-run-processes.md),
discovery MUST retain a durable retry record or prove that its source query
will rediscover the candidate after advancing a checkpoint or watermark.
Scheduling a run, delivering a wakeup, or receiving a process-start receipt is
not completion. If the bound run fails or remains inconclusive, the candidate
MUST remain retryable and cannot be recorded as false, empty, or handled.

Fullsend MUST omit or minimize unneeded untrusted fields where the normalized
entity contract permits. Further trust or injection filtering MAY run after CEL
routing and before the harness pre-script. Retained content remains untrusted
for dispatch and CEL evaluation throughout.
[ADR 0133](../../../ADRs/0133-actor-attributed-data-security-boundary.md)
defines the separate model-bound admission and filtering gate, as revised for
the per-harness human minimum by
[ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md): only filtered
content whose source record and actor pass the fresh gate below may be exposed
to the model under bounded harness scope. Any resulting agent run uses the
harness's configured identity and permissions.

## Model-bound actor-attributed data admission

The model-bound gate applies to every source-system record that Fullsend may
expose to a model, regardless of whether the record arrived in an event, was
found by polling or snapshotting, or was read through a proactive or
Fullsend-controlled API. Fullsend MUST establish the record's current actor
attribution through the source system and resolve that actor's current
authorization. A human MUST have a fresh, verified effective role meeting the
selected harness's `human_min_role` at each required checkpoint. A bot MUST
have fresh provider-backed recognition of its exact registered
`actor.bot_role` under ADR 0107; its `actor.role: none` is not compared with
the human minimum. Missing or unverifiable attribution, an unrecognized or
unresolved bot identity, and a human below the selected minimum MUST redact
the record; there is no data-only fallback. The platform derives the default
minimum from the resolved harness `role` as specified above, not from a
workflow stage or token capability. CEL conditions for selecting the run do
not authorize separate model records. A verified label transition, whether
human- or bot-authored, does not admit its source record to model input
without this separate fresh gate and ADR 0133 filtering.

The model-record admission evidence MUST bind each admitted or redacted
record's current-revision actor and gate result to the selected harness's
pinned policy identity and the fresh checkpoint. A CEL match or earlier event
admission is not this evidence.

Source-record attribution MAY come from a provider-verified event or be
resolved from the source system. Model-bound actor authorization MUST be
resolved from the current authoritative permission or registered-role source
under the effective policy after event or retrieval admission; it MAY be
co-fetched with a later record read or lazily resolved when first needed. A
resolved result MAY be cached only for the logical lifetime of the
containing event or entity structure and agent invocation, subject to the
fresh checkpoints below. Re-reading or re-fetching the data,
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
| Contribution history | Past contributions do not confer repository permissions. A former maintainer's contributions cannot substitute for a provider-verified role verdict; a valid CEL cache entry may still reflect a role from before revocation within its bounded age. |
| `actor.is_entity_author` | Being the author of an issue or PR does not grant repository permissions. This field supports routing decisions in CEL triggers, not authorization. |

**Principle:** relationship and contribution-history fields are not
evidence of current authority. Event-backed human authorization must be
derived from the source provider's verified permission model or a trusted
configured permission source at event time and meet the selected harness's
effective minimum, except that an exact provider-verified human label addition
may use its narrow `triage` grant. Neither path relies on cached or inferred
relationships.
Fullsend-originated eventless evaluation must instead be authorized from
trusted Fullsend-controlled invocation provenance for poll enumeration or
bound rechecks. Actor-originated entity facts needed for an actionable CEL
result are separately gated by current source-revision attribution and a live
or eligible bounded role verdict against that harness's minimum, except for
the exact human label transition and its proven projection under a `triage`
minimum. Deferral-only raw state follows the causal classification above.
Model-bound records receive separate fresh checks. Historical actors and
relationship fields must not serve as authorization evidence.

## Enforcement point

The `triage` platform floor and selected harness's effective human minimum are
**platform-enforced gates** that CEL cannot override. Event-backed dispatch
checks the normalized human event actor's fresh verified effective role against
both before evaluating that harness's CEL predicate. An exact verified human
label addition may instead satisfy a `triage` minimum with its narrow grant;
a stronger minimum requires the verified role. Bots use the registered-role or
separate narrow label-transition grants above. Fullsend-originated eventless
evaluation checks trusted invocation provenance and platform eligibility
before resolving the entity. An entity-source predicate may then be evaluated
progressively for each eligible harness. An actor-originated fact that could
make CEL actionable, or whose actor-filtered coverage is needed to prove an
actionable absence or
aggregate, passes that harness's minimum using live or eligible bounded role
evidence, or the exact human label grant when the minimum is `triage`. Its
current source-revision attribution and coverage are checked separately.
Deferral-only raw state follows the causal classification above.
Not-yet-loaded facts remain unknown rather than being treated as unauthorized,
absent, or empty.

```
Forge event
  --> NormalizedEvent (adapter)
  --> Human triage floor/label grant or bot grant  <-- enforced here
  --> Resolve trusted harness and verified event/target metadata
  --> If needed, resolve structural entity-source metadata only
      from the provider or platform discovery credential
  --> Resolve guarded minimum overlays from that metadata
      --> Human role or exact label-grant gate    <-- before this harness's CEL
  --> Event-only CEL routing or entity-source resolution
      --> Dependency-driven acquisition and per-harness actor gate
      --> CEL true / false / UNKNOWN
      --> Model-record gate and execution only on definitive true

Fullsend poll discovery / bound run eventless recheck
  --> Trusted-origin authorization gate     <-- enforced here
  --> Enumerate and resolve candidates (poll) / resolve canonical entity (run)
  --> Evaluate each candidate harness (poll) or the bound harness (run):
      --> Resolve guarded overlays and effective human minimum
          from verified pre-admission metadata
      --> Dependency-driven acquisition and per-harness actor gate
      --> CEL true / false / UNKNOWN (event is null)
      --> Unknown may continue in bound run after current entity re-resolution
      --> Model-record gate, filtering, and execution only on true
```

### CEL triggers: harness-specific conditions

Harness `trigger` expressions select runs and may require stronger human roles
or narrower bot roles, labels, and entity states. Such CEL conditions may
further narrow a run but never weaken or replace the platform floor or the
selected harness's admission minimum, and they do not create a bot or
transition grant. A CEL nonmatch is not a platform denial. On the event-backed
path, an event that fails the role or exact label-grant gate never reaches
that harness's CEL.

Current `fullsend dispatch` runs `IsEventAuthorized()` before harness loading
and `FilterAuthorizedHarnesses()` before `MatchHarnesses()`. Jira polling
preloads harnesses but applies both checks before CEL. In the target path, the
platform MAY reject a below-floor human event before loading harnesses unless
the exact verified label grant applies, but MUST resolve and enforce each
candidate harness's minimum before its CEL predicate. On the Fullsend-originated
eventless path, the trusted-origin gate runs before poll enumeration or
bound-entity resolution and CEL evaluation. For each eligible entity-source
harness, the platform admits actor-originated facts needed for
an actionable CEL result only after current source-revision attribution and a
live or eligible bounded role verdict meeting its effective minimum, or the
exact applicable human label grant for a `triage` minimum. CEL receives that
verified effective role when present and may apply further conditions. An
exact human label transition may instead carry separate `triage`-grant
provenance; CEL MUST NOT treat it as a verified role. The
verified bot label-event grant authorizes only its exact transition, as
described above. Eventless checks use `event == null`. Legacy event-only
harness routing does not acquire entity history under this section.

### Per-repo configurability

The `triage` floor and the narrow human-label and bot grants are
platform-level security boundaries. Individual repositories cannot disable or
lower them. A trusted harness author MAY set `human_min_role` at the top level
or in an ADR 0112 trusted-fact-guarded overlay to `triage`, `write`,
`maintain`, or `admin`. The top-level or inherited non-overlay value is the
baseline; an overlay MAY preserve or lower it, never raise it. This lets a
trusted condition relax a stronger default while a missing condition safely
retains that default. The selected value applies to human event, entity-fact,
and model-record admission alike. A repository may disable a harness or use
trigger CEL to require additional conditions, but cannot make it available to
a human rejected by either admission gate. A source event may select the field
only through the validated trusted facts above; it cannot supply policy text.

A repository can add permission sources through the `authorization`
field (today `owners_file` for GitHub and `jira` for Jira events). These sources may raise the verified
effective role used by the platform and visible to CEL; they do not change
either minimum or bypass the gate. The current bash
`has_repo_permission` helper and Go event and per-harness gates retain their
v1 compatibility behavior until migrated.

## Versioning

This is the v2 normative contract under
[ADR 0015](../../../ADRs/0015-normative-specifications-directory.md).
[ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md)
supersedes v1's future full-history entity-evaluation and audit requirements;
[ADR 0148](../../../ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md)
adds bounded cross-check reuse of provider-verified actor-role verdicts for
entity-source CEL only.
[ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md) sets the
immutable `triage` platform floor and the selected harness's effective
`human_min_role` for human event, entity-fact, and model-record admission. The
resolved harness `role` supplies the default map above; an explicit top-level
or trusted-fact-guarded overlay value may change it within the allowed range,
subject to the rule that overlays may only preserve or lower their non-overlay
baseline.
Trigger CEL may further narrow selection but does not replace the admission
minimum. V2 retains v1's role-derived defaults; a resolved explicit or
guarded-overlay value supersedes the fixed default for that harness across
event, entity-fact, and model-record admission. The separate fresh model-bound
release checkpoints remain.

Migration MUST teach harness loading and `base` composition to recognize and
validate `human_min_role` with key-presence awareness, including in overlay
entries, and reject an overlay value above its non-overlay baseline before the
value can affect any v2 decision. The versioned CEL schema
MUST name and classify the verified effective-role path before a role-based
minimum overlay can run. An explicit empty or invalid value MUST NOT silently
inherit or default during scalar merge; a
`forge:` value or overlay with untrusted or admission-dependent guard inputs
MUST be rejected before resolution. A runner that cannot interpret a
configured value MUST reject that harness, including a stronger `maintain` or
`admin` value; it MUST NOT silently use the v1 role-derived default. Migration
also requires versioned event and entity CEL inputs that carry verified
effective roles and distinguish exact label-grant
provenance. Existing authorization-sensitive CEL comparisons against an
unverified legacy role MUST be migrated before v2 routing. Migration also
requires the handoff to integrity-bind the candidate harness and event/entity
locator, and requires `run` to reload the trusted target config and harness/base
sources, recompute config `when` flags, harness overlays, and trigger CEL, and
recheck current authorization and launch gates against the canonical entity
under ADR 0144 before ADR 0141 context staging or sandbox launch. The run
records and pins the policy identity it actually verifies through its own
context, actor, and model-record checks.
The dispatch/poll policy identity may differ; it is not permission and need not
be verified as the same revision at run. Migration also requires per-harness
event and entity actor gates and an entity-context-snapshot v2 contract that
can represent all four allowed minimums and the changed record-admission rule.
[Entity-context-snapshot v1](../../entity-context-snapshot/v1/README.md)
retains its historical observation/mutation-derived `triage`/`write` rule until that
migration.
Authorization v1 remains authoritative for existing legacy non-CEL,
non-entity workflows during migration, including their bash thresholds and Go
role-derived defaults. An
explicit per-harness minimum change under v2 is a harness-policy change bound
to that harness's reviewed revision, not a normative contract version change.
Breaking changes to v2 require `docs/normative/authorization/v3/`.

**ADR 0107 migration exception:** The migration from legacy bot authorization
exceptions to provider-backed bot identity is also permitted within v2. It is a
policy tightening, not a new supported actor population: existing
compatibility behavior remains authoritative until an adapter implementation is
updated; once updated, registered bots use `actor.bot_role` and unregistered
non-label bots are denied. The adapter implementation itself is the migration
boundary; no per-event mode marker or separate deprecation window is required.
An updated adapter MUST enforce the target behavior and MUST NOT silently fall
back to legacy bot authorization when its resolver fails. The normalized-event
fields used to carry this distinction are additive. The verified label-added
bot handoff grant remains; human label additions meet the selected harness's
minimum only when the v2 human gate is implemented. This bot migration
exception does not alter the general v3 rule for unrelated authorization
changes.

| Change | v2 impact |
|--------|-----------|
| **Breaking** (requires v3): remove a role from the hierarchy, change the platform `triage` floor, change the harness-role default map or allowed `human_min_role` values, stop applying the minimum to human label additions or any of the three admission paths, alter a target label or bot grant, change fail-closed to fail-open, remove a fresh model-bound checkpoint, or weaken the actor and coverage gates beyond the bounded CEL cache adopted here | Dispatch implementations must migrate |
| **Adapter bot-identity migration** (allowed in v2): update an adapter to emit and enforce the ADR 0107 target representation | The adapter's existing compatibility behavior applies until that implementation update; afterward, registered bots use `actor.bot_role` and unregistered non-label bots are denied |
| **Non-breaking** (allowed in v2): add a role under the existing default map, change one reviewed harness's explicit minimum, add equivalent forge mappings, or clarify documentation | The platform floor and contract remain; a harness minimum change binds to its new revision |
