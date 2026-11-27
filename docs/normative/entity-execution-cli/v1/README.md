# Entity execution scope and CLI v1

This contract records two things. It names the first provider/kind scope for
entity-source harnesses. It also defines how `fullsend run` and `fullsend poll`
accept entity work without changing existing invocations. It implements the
planning step of [issue #9589](https://github.com/fullsend-ai/fullsend/issues/9589)
under [ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md) and
[ADR 0144](../../../ADRs/0144-reuse-poll-and-entity-bound-run-processes.md).

> **Target contract, not implemented:** No shipped command accepts the
> entity flags, the `entity-sources` poll driver, or the lifecycle flags below.
> Every existing invocation keeps its current behavior. Implementation follows
> in [#9598](https://github.com/fullsend-ai/fullsend/issues/9598),
> [#9585](https://github.com/fullsend-ai/fullsend/issues/9585),
> [#9599](https://github.com/fullsend-ai/fullsend/issues/9599), and
> [#9588](https://github.com/fullsend-ai/fullsend/issues/9588). User-facing CLI,
> configuration, and guide updates follow in
> [#9600](https://github.com/fullsend-ai/fullsend/issues/9600) after the
> behavior ships.

## Baseline contracts

This contract uses these contracts and does not redefine them:

- Source declarations, slot bindings, provider query languages, the `native`
  selector, execution readiness, and the cutover rule come from
  [ADR 0079](../../../ADRs/0079-bind-entity-execution-through-named-sources.md)
  and
  [Entity-source configuration v1](../../entity-source-configuration/v1/README.md).
- Discovery planning, pass outcomes, coverage, and retry come from
  [ADR 0149](../../../ADRs/0149-coalesce-entity-source-discovery-queries.md)
  and [Entity discovery v1](../../entity-discovery/v1/README.md).
- CEL-visible fields come from
  [Normalized entity v1](../../normalized-entity/v1/README.md). Model-facing
  fields come from
  [Entity-context-snapshot v2](../../entity-context-snapshot/v2/README.md).
- Admission gates come from
  [Authorization Contract v2](../../authorization/v2/README.md#entity-source-cel-evaluation),
  [ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md),
  and [ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md).
- Process lifecycles come from ADR 0144. Serialization of runs for one
  entity comes from
  [ADR 0106](../../../ADRs/0106-serialize-agent-runs-and-coalesce-subsequent-events.md).

An *entity-source harness* is a harness whose effective definition declares
`entity_kinds`, including the empty map `{}`. Every other harness is an
*event-only harness*. Event-only harnesses keep their current behavior under
every invocation in this contract.

## First enabled scope

The first enabled scope is:

| Provider | Kind | Native record | Enabled |
|----------|------|---------------|---------|
| `github` | `work_item` | Issue | Yes |
| `github` | `change_proposal` | Pull request | Yes |
| `github` | `conversation` | Discussion | No: deferred |
| `gitlab` | `work_item` | Issue | No: deferred |
| `gitlab` | `change_proposal` | Merge request | No: deferred |
| `jira` | `work_item` | Issue | No: deferred |

Any provider/kind pair outside Normalized entity v1, such as Forgejo pairs, is
not eligible. Configuration validation rejects it as entity-source
configuration v1 requires.

The enabled GitHub pairs apply only to a source that meets both conditions:

- The source resolves to the native GitHub connection. That connection can be
  github.com or a GitHub Enterprise endpoint set through
  `connections.github.base_url`.
- The source's repository scope is the target code repository, which is the
  repository whose configuration the run uses. This is the default scope of a
  source on the provider-default connection. An explicit `repositories` list
  that names another repository is outside the first scope.

The adapter MUST also verify that the deployed host supports the advanced
search mode that entity-source configuration v1 requires, and MUST NOT fall
back to legacy search semantics. A source with
another repository scope, another GitHub connection, or a host without that
search mode is outside the first scope.

### Capability assessment

A provider/kind pair becomes eligible when it has all four capabilities:

- **Normalized entity:** a mapping in Normalized entity v1.
- **Model-facing:** a field gate in Entity-context-snapshot v2.
- **Source query:** a source query language in entity-source configuration v1.
- **Adapter:** a provider adapter that can resolve the current entity, prove
  source membership, and stage the snapshot.

| Pair | Normalized entity | Model-facing | Source query | Adapter and execution path |
|------|-------------------|--------------|--------------|----------------------------|
| GitHub issue, pull request | Mapped | Covered | Bounded GitHub filter subset for both kinds, with GraphQL advanced search for combined discovery and selective acquisition | A selective-acquisition adapter exists (`internal/forge/github.EntityAdapter`), but source membership and snapshot staging remain pending under [#9598](https://github.com/fullsend-ai/fullsend/issues/9598), so the pair stays ineligible. The existing GitHub `forge.Client` already reads issues, pull requests, comments, label events, reviews, and review threads. No `fullsend poll` driver uses GitHub, so entity discovery competes with no driver |
| GitHub Discussion | Mapped | Covered | None. The GitHub subset guards only `is:issue` and `is:pr` | No Discussion ingress or conversation client under [ADR 0086](../../../ADRs/0086-conversation-surface-for-agent-participation.md) |
| GitLab issue, merge request | Mapped. Reviews, reactions, issue type, and close reason are `unsupported` | Covered | Only the `open`, `closed`, and `all` presets | No entity adapter. The `--forge gitlab` poller and the webhook fast path own dispatch through shared poll state ([ADR 0125](../../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md), [ADR 0132](../../../ADRs/0132-occurrence-aware-shared-poller-state.md)). Cutover needs a plan for that state |
| Jira issue | Mapped. Complete `label_applications` needs full changelog coverage | Covered | Native JQL within a project guard | No entity adapter. The `jira-poll` driver owns dispatch through checkpoints and lock properties. The source project is separate from the target code repository |

#### Why GitHub issues and pull requests come first

GitHub is the only provider whose source query language covers both
`work_item` and `change_proposal` and can narrow selection beyond lifecycle
state. Its issues and pull requests share one repository scope, one
authorization source, and one search mode. Entity-source configuration v1
defaults the source slot to `work_item`, and its example code harness targets
`change_proposal`, so one adapter serves both slots. No existing poll driver
owns GitHub dispatch, so entity discovery adds a new path and does not need to
take over an existing one. Limiting the first scope to
the target code repository avoids credential scopes and cross-repository
routing that a first adapter does not need.

Contract coverage alone does not enable a pair. The first adapter and the
Entity-context-snapshot v2 producer are part of #9598. Until they ship, the
enabled pairs report the unsupported result below.

#### Why the other pairs are deferred

- **GitHub Discussions** have no source query language, no runtime ingress,
  and no conversation client.
- **GitLab issues and merge requests** support only lifecycle presets, so a
  source cannot select work by label. Several normalized collections are
  `unsupported`. Entity admission must also coordinate with the shared state
  of the existing GitLab poller and webhook fast path, so that only one path
  owns dispatch for each binding.
- **Jira issues** need full changelog coverage to prove complete label
  applications. They need role resolution for eventless checks against the
  source project, which is separate from the target code repository. Entity
  admission must take over dispatch from the `jira-poll` driver for each
  migrated binding without losing its checkpoints.

### Jira unassessed-tracker scenario

The unassessed-tracker scenario (S5) from closed issue
[#8421](https://github.com/fullsend-ai/fullsend/issues/8421) is
**deferred** from the first scope. In S5, automation files a Jira
vulnerability ticket and no person changes it. The agent runs once and does
not run again after its result is on the ticket.

S5 depends on the Jira capabilities that are deferred above. Its predicate
reads the issue type and labels. It also reads handled-state evidence that the
agent's own Jira account writes. The current `jira-poll` driver classifies
changes made by its own account as bot events. An entity adapter must
therefore admit that account's records as entity history before S5 can work.
S5 also needs the same completeness guarantees as other state-only scenarios,
because a missed ticket means a vulnerability that nobody assessed.

S5 is the first named follow-up for the Jira issue pair. Until that follow-up
ships, each S5 ticket needs a person or a label added after creation.

### Single definition in code

The enabled scope MUST be defined once in code. Configuration validation,
`fullsend run`, `fullsend poll`, and dispatch MUST read that one definition. A
test MUST check that the scope table above matches it. The first implementing
issue, #9598, introduces the definition.

## Result classes

Every entity check and every discovered candidate ends in exactly one of these
classes. Commands MUST report the class and MUST NOT turn one class into
another:

| Class | Meaning | Sandbox | Retry |
|-------|---------|---------|-------|
| `unsupported` | The provider/kind pair, source scope, adapter, or flag combination is outside the enabled scope | Never starts | Only after a configuration or release change |
| `denied` | Event authorization, invocation provenance, or platform policy rejects the check | Never starts | Only after the evidence changes |
| `not_eligible` | Current membership or the CEL predicate is proven false | Never starts | On the next event or discovery |
| `unresolved` | Membership, entity data, authorization evidence, or coverage is incomplete, or a read failed | Never starts | Yes, through retry or rediscovery |
| `selected` | Membership and CEL are proven true and admission passes | At most one cycle per check | Follows ADR 0144 |

An `unsupported` result is never an empty discovery result or a false CEL
result. Entity discovery v1 pass outcomes map onto these classes without
change. A discovery plan the adapter cannot build is `unsupported`. An
`incomplete` or `failed` pass is never reported as complete-empty, and work it
did not cover stays `unresolved`. Absence from discovery results is never
`not_eligible`. A command reports it before it reads entity data or creates a sandbox.
Its diagnostic MUST name the provider, kind, source, and the reason, such as
"provider/kind not enabled", "repository scope outside the target repository",
or "adapter unavailable". A usage error for an incompatible flag combination
is also reported before any read.

## `fullsend run`

### Binding inputs

A run is bound when it has exactly one binding input:

- An **event input**: `--event-file`, or the `_normalized_event` that the
  dispatch payload carries today.
- An **explicit entity**, in one of two forms:
  - `--entity <id>` takes a canonical Normalized entity v1 ID.
  - `--entity-source <name> --entity-number <n>` takes an effective source
    name, `native`, or the matching provider name, plus the entity number.
    The selector resolves as it does for `fullsend issues --entity-source` in
    entity-source configuration v1. A direct lookup does not run the source's
    discovery query. The query still decides source membership in the check
    below.

The harness's effective `entity_kinds.source` supplies the kind. An `--entity`
ID with another kind is a usage error. A source whose scope contains several
repositories needs an explicit repository. The first scope has only one
repository per source, so v1 adds no repository flag.

The run resolves the binding to one canonical entity and reads its current
state. A supplied ID establishes the binding only. It is not current state.
Ambiguous or unresolvable bindings fail. They never fall back to an unbound
run.

With an explicit entity, the first CEL check uses `event == null`. Eventless
admission requires trusted invocation provenance and effective platform
policy. A one-cycle run for a known entity needs no discovery pass and no
discovery readiness evidence. It still rechecks current source membership
before the sandbox starts. With an event input, the run first authorizes the event. It then binds
the entity that the event identifies. The first check uses both `entity` and
`event`.

### Lifecycle flags

- `--lifecycle once|poll|timed` selects the ADR 0144 lifecycle for a bound run.
  When omitted, the run uses the ADR 0144 automatic default: once outside CI,
  and timed in CI only when every timed condition holds.
- `--deadline <duration>` sets the configured timed deadline, measured from
  command entry. It is valid only when the effective lifecycle is timed.
- The idle period is platform configuration, not a flag.

`fullsend run` has no `--watch` flag. `--watch` belongs to `fullsend poll`.

### Run matrix

| Invocation | Harness | Result |
|------------|---------|--------|
| No binding input | Any | Unchanged unbound once run |
| No binding input, `--lifecycle poll` or `timed` | Any | Usage error: a resident lifecycle requires a binding |
| Event input | Event-only | Unchanged event-only run |
| Event input, `--lifecycle poll` or `timed` | Event-only | `unsupported`: resident event-only runs need a wake path outside v1 |
| Event input for an enabled pair | Entity-source | Event-bound check, then at most one cycle when the effective lifecycle is once (#9598, #9585) |
| Event input for a pair outside the scope, such as a Jira or GitLab event | Entity-source | `unsupported` before any entity read |
| `--entity` or `--entity-source` for an enabled pair | Entity-source | Direct binding with `event == null`, then at most one cycle when the effective lifecycle is once (#9598) |
| `--entity` or `--entity-source` for a pair or scope outside the first scope | Entity-source | `unsupported` before any entity read |
| `--entity` or `--entity-source` | Event-only | Usage error: entity binding requires an entity-source harness |
| `--entity` or `--entity-source` together with `--event-file` | Any | Usage error: conflicting binding inputs |
| `--entity` or `--entity-source` while a dispatch payload carries a normalized event | Any | Usage error: conflicting binding inputs |
| `--entity` together with `--entity-source`, or `--entity-source` without `--entity-number`, or the reverse | Any | Usage error |
| Bound run, effective lifecycle poll or timed, whether explicit or the automatic timed default in CI | Entity-source | Resident checks on the bound entity only (#9599). Eventless checks need trusted provenance, policy, and `fullsend poll` coverage. Without coverage, an explicit choice fails closed and the automatic default stays once |
| `--deadline` with a lifecycle other than timed | Any | Usage error |

Existing flags such as `--target-repo`, `--status-number`, and the runtime
overrides keep their current meaning. They never select or change the entity
binding.

## `fullsend poll`

### Driver composition

Each `fullsend poll` invocation runs exactly one input driver, as it does
today. Entity discovery is a new driver, `--input-driver entity-sources`.
It enumerates the effective `source` bindings of enabled agents through the
ADR 0149 coordinator, following Entity discovery v1. A source with
`discovery: false` is not enumerated. It still admits event-backed candidates. It routes each `selected` candidate to the entity-bound
`fullsend run` path. It does not fabricate a normalized event. Each dispatch
descriptor carries the canonical entity ID and a reference to the trusted
execution plan, with no event.

The existing drivers do not enumerate entity sources. The `entity-sources`
driver does not read GitLab or Jira event streams. A scheduler that needs both
runs two invocations. For each binding, only one of legacy event
reconstruction and entity-driven admission owns dispatch at a time, as
entity-source configuration v1 requires. The first scope has no GitHub poll
driver, so no existing driver gives up dispatch in v1. GitHub events for an
entity-source harness become event-backed candidates through #9585. The
ADR 0106 key (harness, target repository, canonical entity) serializes runs
that the event path and the discovery path both start. Handled state belongs
in harness CEL or the pre-script, as ADR 0144 requires.

Each discovery pass emits the host-side pass report that Entity discovery v1
defines. An execution adapter MAY use that report as the discovery readiness
evidence that entity-source configuration v1 requires before a resident
eventless run. Reports carry canonical identities only, with no credentials
or entity content.

For the GitHub target, `entity-sources` uses `--output` and `--target-repo`
with the same meanings as the `jira-poll` GitHub target. #9588 defines the
exact descriptor fields.

When an active `source` binding is outside the first scope, the driver
reports that binding as `unsupported` and does not enumerate it. It still
processes the bindings in scope. After the cycle it exits nonzero, so the
scheduler shows the problem. A configuration with no active entity-source
bindings is an empty cycle and exits zero.

### `--mode` and lifecycle flags

`--mode` keeps one meaning: input discovery for the `--forge gitlab` poller,
`slash` or `events`. The `FULLSEND_POLL_MODE` variable keeps the same meaning
and scope. Lifecycle flags never change `--mode`, and `--mode` never selects a
lifecycle.

- `--lifecycle once|poll|timed` selects the ADR 0144 lifecycle. The default
  is once in every environment.
- `--watch` is the ADR 0063 shorthand for `--lifecycle poll`. Combining it
  with `--lifecycle once` or `--lifecycle timed` is a usage error.
- `--deadline <duration>` is valid only with timed.

### Poll matrix

| Invocation | Result |
|------------|--------|
| `--forge gitlab`, with or without `--mode slash` or `--mode events` | Unchanged |
| `--forge gitlab --print-dispatch-routes` | Unchanged |
| `--input-driver jira-poll`, either `--target-forge` | Unchanged. `--mode` stays ignored, as today |
| `--input-driver gitlab-webhook` | Unchanged |
| An existing driver with `--lifecycle once` | Same as omitting the flag |
| An existing driver with `--watch`, `--lifecycle poll`, or `--lifecycle timed` | `unsupported` in v1. `gitlab-webhook` handles one payload per invocation and always runs once |
| `--input-driver entity-sources` | One discovery cycle over in-scope bindings (#9588) |
| `--input-driver entity-sources` with `--watch`, `--lifecycle poll`, or `--lifecycle timed` | Resident or timed discovery under ADR 0144 (#9588) |
| `--input-driver entity-sources` with `--mode` | Usage error: `--mode` applies only to `--forge gitlab` |
| `--input-driver entity-sources` with `--forge` | Usage error |
| `--input-driver entity-sources` with `--target-forge gitlab`, `--project`, or `--gitlab-url` | `unsupported`: GitLab delivery of entity candidates is deferred |
| `--input-driver entity-sources` with an out-of-scope binding | `unsupported` for that binding, other bindings processed, nonzero exit |
| `--watch` with `--lifecycle once` or `timed` | Usage error |
| `--deadline` with a lifecycle other than timed | Usage error |

## Conformance cases

Implementations MUST cover at least these cases:

1. Each enabled pair is accepted and its entity is evaluated. Each other pair
   in Normalized entity v1 gives `unsupported` without an entity read or a
   sandbox.
2. An enabled pair whose source has another repository scope, another GitHub
   connection, or a host without advanced search gives `unsupported`.
3. An `unsupported` result never appears as an empty discovery result or a
   false CEL result.
4. Event-only harnesses behave the same under every existing `fullsend run`
   invocation, including `--event-file` and the dispatch-payload event.
5. Every existing `fullsend poll` invocation behaves the same, including
   `--mode`, `FULLSEND_POLL_MODE`, and `--print-dispatch-routes`.
6. Each cell of the run and poll matrices produces its listed result.
7. A direct entity binding evaluates CEL with `event == null`. An event
   binding evaluates the first check with that event, after event
   authorization.
8. The scope table in this document matches the single code definition.
