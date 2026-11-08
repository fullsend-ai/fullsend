---
title: "79. Bind entity-driven execution through named connections and sources"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - configuration
  - entities
  - dispatch
  - polling
---

# 79. Bind entity-driven execution through named connections and sources

Date: 2026-11-08

## Status

Accepted

Refines [ADR 0098](0098-entity-first-harness-evaluation.md): harnesses declare
named entity slots and their kinds; repository configuration binds those slots
to concrete sources.
Partially supersedes [ADR 0149](0149-coalesce-entity-source-discovery-queries.md):
source membership uses native provider semantics and may be checked remotely,
rather than requiring local reproduction over actor-filtered data. Its combined
search, current-membership, coverage, and retry requirements remain. The
lifecycles in [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)
remain unchanged.

## Context

The current `tracker` mapping selects one issue backend and its issue-creation
policy. [PR #9091](https://github.com/fullsend-ai/fullsend/pull/9091) added the
Jira connection and polling configuration contract. The
[review proposal](https://github.com/fullsend-ai/fullsend/pull/9091#discussion_r4465261777)
separates reusable connections, named entity sources, and agent bindings: one
connection can supply several entity kinds, and one agent can process equivalent
work from several systems.

ADRs 0098, 0144, and 0149 define entity evaluation, reusable run processes, and
combined discovery, but leave the configuration and CI integration incomplete.
This ADR aims to connect those decisions through one repository-owned binding
model, while keeping agent coordination in entity state and predicates.

## Options

### Keep the current `tracker` syntax

The existing syntax is compact and already understood by the issue commands,
Jira poller, and configuration tooling. For example:

```yaml
tracker:
  jira:
    base_url: https://example.atlassian.net
    project: PROJ1
    allowed_targets:
      projects: [PROJ2]
    poll:
      enabled: true
      query: 'statusCategory != Done ORDER BY updated DESC'
```

This selects Jira for issue commands, supplies one primary project and polling
query, and permits issue creation in the additional project. It works for a
single tracker, including Jira used from a repository hosted on GitHub or GitLab.
Keeping it avoids a configuration migration.

The mapping accepts at most one provider block. It has no names for multiple
instances of a provider, no per-agent source bindings, and no source/target
entity-kind declarations. The example cannot express a triage agent reading
both native and Jira work items while a code agent reads only Jira and produces
native change proposals. Those concepts would have to be added to or alongside
`tracker`; the current shape is insufficient as the shared entity-execution
configuration model. It remains useful as a compatibility input.

### Separate connections, sources, and harness bindings

Named connections describe service instances, sources describe what to select,
and harness bindings connect those sources to an agent's needs. This supports
multiple providers and instances without putting repository-specific names in
reusable harnesses. It introduces more configuration concepts and requires a
compatibility adapter, but each concept has one role and can be shared across
agents. This is the selected option.

## Decision

Adopt named connections and entity sources as the binding between portable
agent harnesses and the shared discovery, dispatch, and run path. The
[entity-source configuration v1 contract](../normative/entity-source-configuration/v1/README.md)
defines the fields, defaults, layering, destination policy, and compatibility
translation. The sections below describe the target design; existing
deployments use `tracker` until their consumers migrate.

### Configuration responsibilities

The model separates four concepts:

- A **connection** identifies a service instance, such as a Jira site or GitHub
  Enterprise host. Repository configuration defines it under `connections`.
- An **entity source** identifies a connection, repository or project scope, and
  selection query. Repository configuration defines it under `entity_sources`.
- A **slot** names an entity requirement of a harness. The harness's
  `entity_kinds` map declares each slot's kind, such as `work_item` (an issue)
  or `change_proposal` (a pull or merge request).
- A **binding** assigns a list of entity sources to a slot. Repository
  configuration defines it under `agents[].entities`.

Connections share the registry proposed for Vault in
[PR #9097](https://github.com/fullsend-ai/fullsend/pull/9097); a connection's
`kind` selects its service type. Sources do not have a `kind` field. Fullsend
checks that each source's provider and query support the consuming slot's kind.
Bindings select where an agent can look for entities; credentials and permission
checks remain separate.

### Harness slots and defaults

Only the `source` slot drives automatic discovery and scheduling. Its binding
list is a union of possible inputs: each run processes one source entity.
`target` gives the run its default destinations for new entities. Additional
slots give it other named scopes to query or use. Those slots do not start
polling loops, fetch every entity in advance, or create combinations of inputs.
Every slot accepts a list. When several output destinations are possible, the
operation must choose one explicitly; a list does not broadcast writes.

Apply defaults after all configuration layers have been combined:

- In the harness, the `source` kind defaults to `work_item`; the `target` kind
  defaults to the effective source kind. Additional slots require a kind.
- In `agents[].entities`, `source` defaults to `[native]`; an omitted `target`
  copies the effective source list. An explicit target, including an inherited
  one, takes precedence. Additional slots require explicit nonempty bindings.
- Omitted slot values inherit. An explicit binding list replaces that slot's
  inherited list. `source: []` disables source-based execution; `target: []`
  removes the default output scope.

Copying a source list to `target` copies names, not entities or kinds. For
example, a Jira work-item source cannot supply a `change_proposal` target; that
agent must bind its target to a compatible source, such as `[native]`. Unknown
slots and incompatible bindings fail validation. An old event-only harness
stays on its existing path until it is explicitly migrated; these defaults do
not opt it into entity execution.

### Repository and harness examples

The following YAML illustrates the target contract, not fields supported by the
current CLI. Repository configuration supplies connections, scopes, and bindings:

```yaml
# .fullsend/config.yaml
connections:
  jira1:
    kind: jira
    base_url: https://example.atlassian.net
    api_url: https://api.atlassian.com

entity_sources:
  github:
    # Customizes native selection when the configuration was cloned from GitHub.
    query: 'is:open -label:blocked'
  jira_work:
    connection: jira1
    projects: [PROJ1]
    query: 'statusCategory != Done'
    discovery: true
  jira_related:
    connection: jira1
    projects: [PROJ1, PROJ2]
    query: ''  # No additional filter within these projects.

agents:
  - name: code
    entities:
      source: [jira_work]
      target: [native]
      related_work: [jira_related]
  - name: triage
    entities:
      source: [native, jira_work]
      # target is omitted, so it defaults to [native, jira_work].
```

The same body can live in a preset installed as `.fullsend/config.base.yaml`,
or under `defaults.config`, `github.config`, `gitlab.config`, or a repository
entry's `config` in `repos.yaml`
([layered configuration](../guides/infrastructure/layered-config-reference.md)).
An overlay can replace one slot's list while inheriting the other slots.

The corresponding code harness declares the kinds of its slots and its
predicate over the primary source entity:

```yaml
# Harness YAML; other agent, policy, and runtime fields omitted.
entity_kinds:
  source: work_item
  target: change_proposal
  related_work: work_item
entity_filters:
  source:
    github: 'is:open label:"needs-code" no:assignee'
    gitlab: open
    jira: 'statusCategory != Done'
trigger: >-
  entity.kind == "work_item" &&
  entity.state == "open" &&
  "needs-code" in entity.labels
```

With the code binding above, this harness receives Jira work items, targets
native change proposals, and can query `related_work` when needed. `entity` in
CEL is the one source entity for the run. The example predicate works without
an event. After successful processing, the agent must remove the request signal
or use a predicate that checks whether it has already handled the work. A
predicate that reads event fields must first check `event != null`. Labels used
by CEL remain subject to the actor-authorization rules.

Harness `entity_filters` maps slot names to provider-specific queries. Each
filter narrows the bound source's query and trusted scope; it cannot widen them.
Missing provider entries add no restriction. Only source filters participate in
automatic discovery and dispatch; other slot filters constrain explicit searches.
Filters do not inherit from source to target. They filter searches, not creation
destinations or lookups by an explicit entity identity. Matching a filter is
required for source-based execution, but the full harness CEL predicate must
also pass.

For a work-item agent using the default kinds, the declaration can be:

```yaml
# Harness YAML; source defaults to work_item and target defaults to source.
entity_kinds: {}
```

Together with the triage binding above, this means native and Jira work-item
inputs and the same native/Jira choices for new work-item targets. Creating in
Jira still needs an independent connection policy grant. If both binding keys
are omitted, they resolve to `source: [native]` and `target: [native]`.
Additional named slots require explicit bindings. The agent names in these
examples illustrate roles; they do not define a fixed inventory.

### Resolving native sources

`native` is an entity-source selector. Resolve it in three steps:

1. The repository from which Fullsend configuration was cloned selects the
   provider: GitHub, GitLab, or Forgejo. The platform running Fullsend does not
   select it.
2. The corresponding reserved source name (`github`, `gitlab`, or `forgejo`)
   selects a connection. It defaults to the connection with the same name.
3. That connection selects the instance. The source selects repository paths
   on that instance. The clone host and path provide defaults; explicit
   connection and source settings override them after layering.

Reserved connection names imply their provider kind; an explicit `kind` must
agree. For example, a GitHub.com configuration clone can use a GitHub Enterprise
instance as its native forge:

```yaml
# Configuration cloned from https://github.com/example/project.
connections:
  github:
    kind: github
    base_url: https://github.example.com
entity_sources:
  github:
    query: 'is:open -label:blocked'
agents:
  - name: triage
    entities:
      source: [native]
```

Here `[native]` and `[github]` both select `example/project` on
`github.example.com`, including when Fullsend runs on standalone Kubernetes or
in GitLab CI. Set `entity_sources.github.repositories` if the repository path
differs. The override selects an instance; access still requires credentials and
permissions for that instance.

Provider source names can also be bound explicitly for external systems. A
GitHub-origin configuration can use `[gitlab]` when a GitLab connection and
repository scope are configured, without changing its native provider. The
contract includes a complete cross-provider example. A matching provider name
and `native` are aliases for the same effective source and do not duplicate
runs. Without a native-source override, selection includes all lifecycle states.
Defining `entity_sources.native` is forbidden. Additional names may be reserved
in future revisions, with validation of existing definitions.

### Candidate selection and CEL evaluation

Source queries and harness provider filters use the same language for each
provider:

- Jira uses native JQL. Fullsend combines selection conditions with `OR` inside
  the configured project scope and manages traversal order separately.
- GitHub uses the contract's subset of UI filters: state, labels,
  author/assignee, milestone, issue type, missing metadata, dates, and PR
  draft/branch fields, with Boolean combinations and exclusions. Full-text and
  history/review search filters are outside the initial subset. Lifecycle
  filters use `is:open` or `is:closed`; an empty query adds no restriction.
- GitLab initially uses `open`, `closed`, and `all` presets. Adapter support may
  expand without requiring the same query language on every provider.

For each source binding, require both the repository's source query and the
harness filter, within the configured repository or project scope. Combine
those effective selections across enabled harnesses so discovery retrieves
candidates needed by any of them. A provider adapter may retrieve a broader
shared set if it can then check each harness's selection exactly. It must still
account for pagination, provider limits, and missed or incomplete results.

Source membership means that an entity currently matches this effective
selection. Use the provider's semantics. Reuse fetched data only when a local
check can prove the same answer; otherwise batch the remaining membership
checks through the provider, including Jira's
[JQL match API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-search/#api-rest-api-3-jql-match-post).
Equivalent queries and repeated references to the same entity share work when
their credential and authorization scopes are compatible. Unknown membership
remains unresolved and retryable; it is not evidence that no work exists.

For GitHub, prefer GraphQL advanced search and request fields based on the
enabled harnesses' compiled CEL expressions. Fetch inexpensive shared fields
first, then additional fields and history only where evaluation remains
unresolved. Include the authorship and coverage evidence needed for
authorization, even when CEL does not reference those fields directly.
[ADR 0147](0147-evaluate-entity-cel-over-selectively-acquired-data.md) governs
these partial-data checks. Selecting fewer fields does not remove search caps
or pagination requirements.

An entity matching a provider query is only a candidate. Before execution,
Fullsend must establish authorized invocation, a current source match, and a
true harness predicate over actor-filtered data. It must also prepare filtered
model context under [ADR 0141](0141-deterministic-filtered-entity-context-staging.md).
Provider-native fields may include changes that CEL is not allowed to use, so
query membership can select or suppress candidates but never proves an actor's
authority. This is the explicit change to ADR 0149's source-membership rule.

### CI coordination and run modes

Webhooks and polling supply candidates to the same Fullsend selection and
authorization path. Each dispatch identifies the agent, target repository,
canonical source entity, and resolved configuration revision. A resolved plan
contains the connections, source queries, slot bindings, and harness requirements
for that revision. Source aliases are not entity identities.

Fullsend decides whether an agent should act. CI provides trusted invocation,
capacity, job deadlines, execution serialization, and result publication.
Existing jobs may remain as execution wrappers during migration; migrated jobs
use Fullsend's decisions rather than separate stage-routing conditions.

Agents coordinate by changing entity state and recording completed work, which
other agents' predicates can observe. For example, triage can make a work item
eligible for a code agent, and a resulting change proposal can become eligible
for review. This introduces no workflow graph or automatic next-agent step.

This binding model supplies the entities and eligibility checks needed by
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md). Its run modes remain:

- `once`: one discovery cycle for `fullsend poll`, or one eligibility check and
  at most one sandbox cycle for an entity-bound `fullsend run`, then exit.
- `poll`: repeat checks while waiting for work. An entity-bound run exits after
  a finite idle period; the discovery process may stay alive until stopped.
- `timed`: repeat checks within a hard deadline, with the same idle exit for an
  entity-bound run and enough budget reserved for each sandbox cycle it starts.

An entity-bound run stays associated with one canonical entity and rechecks its
current source membership and predicate before each sandbox cycle. Reusing the
host process does not reuse the sandbox or model session. `fullsend poll` still
defaults to `once`. Bound runs default to `once` outside CI; the automatic CI
choice of `timed` also requires a usable deadline, sufficient initial-cycle
budget, permission for eventless checks, and working recurring discovery.

Installation sets up recurring discovery for enabled source bindings. Before a
run can repeat checks without events, Fullsend must verify that discovery is
operational for its current plan and can recover work after the run exits. An
installed workflow file alone is insufficient. Missing or stale discovery
evidence keeps the automatic `once` fallback or rejects an explicit request to
repeat eventless checks. A configuration change invalidates old readiness; an
existing process stops starting new cycles under the obsolete plan. ADR 0144's
pending-work, cancellation, and rediscovery rules continue to preserve unfinished
work.

### Destinations and standalone commands

Inside a run, new entity creation defaults to the `target` slot. Updates to an
existing entity use that entity's canonical origin; a native target does not
redirect comments on a Jira source issue. Connection-scoped
`allowed_targets` remains an independent policy for work-item creation. A source
binding or query match does not grant write access. The contract defines the
destination checks and explicit cross-project creation rules. No separate
`issue_commands` configuration block is introduced.

Standalone issue commands select one named source with the proposed
`--entity-source <name>` flag, or explicitly select `native` after resolving the
origin's provider and configured connection/source overrides. The command
supplies the `work_item` kind; no harness or agent binding is needed. Infer the
project or repository only when the selected scope has one choice; otherwise
require explicit selection. For example, using `jira_work` from the repository
example:

```sh
fullsend issues get --fullsend-dir .fullsend --entity-source jira_work --number 123
fullsend issues get --fullsend-dir .fullsend --entity-source native --number 123
```

The first command retrieves PROJ1-123 from Jira; the second retrieves issue 123
from the effective native source, including any instance override. Operations
with an explicit entity identity use the connection and lookup scope without
applying discovery filters, so the first command also works for a closed issue.
Creation remains subject to independent permissions. These flags describe the
target CLI, not current functionality. Omitting the selector is an error unless
the compatibility adapter can preserve legacy command selection. An explicit
source overrides legacy defaults; combining `--entity-source` and `--tracker`
is an error.

### Migration and activation

Accept deprecated `tracker` mappings by translating them in memory, with
actionable warnings and no automatic file rewrite. Preserve connection settings,
queries, polling enablement, command defaults, credentials, and creation
permissions. The old syntax remains usable while its consumers migrate.

Scripts that read raw tracker YAML must migrate to effective configuration before
new-only configurations are enabled. Syntax translation alone does not move an
agent from event reconstruction to entity-state execution.

First make configuration loading and existing consumers understand both forms.
Then deliver the provider adapters, CI integration, and entity-capable harnesses.
Switch each agent/source binding once; its old and new dispatch paths must not
run in parallel. Activation requires the normalized-entity contract, a provider
query protocol with coverage guarantees, the new major authorization contract
required by ADR 0149, and model-context support for the provider and entity kind.
That authorization contract must account for native source-membership semantics.
This configuration contract does not replace those prerequisites.
Implementation is tracked under
[#8418](https://github.com/fullsend-ai/fullsend/issues/8418), with entity and
discovery contracts in [#8420](https://github.com/fullsend-ai/fullsend/issues/8420)
and [#8421](https://github.com/fullsend-ai/fullsend/issues/8421).

## Forge scope

The initial entity-adapter targets are GitHub and GitLab work items and change
proposals, plus Jira work items. A Jira project remains distinct from the code
repository where the agent works. Forgejo and additional kinds such as
conversations use the same binding model, but remain disabled until the required
entity, discovery, context, and execution support is available and validated.

GitHub Actions and GitLab CI are the first CI integrations. Source providers are
independent of execution platforms; the same configuration-resolution rules
apply to standalone runtimes, including Kubernetes. Each combination activates
only when its adapters satisfy the relevant contracts. Existing event-only paths
remain available.

## Consequences

- Agents can process equivalent entities from several systems without knowing deployment-specific connection names.
- Explicit connections, sources, and bindings add configuration concepts that the single-tracker model did not require.
- Provider queries remain expressive, and shared discovery and selective reads can reduce API work, subject to exact membership and coverage checks.
- Existing tracker configurations continue working, but raw-YAML consumers and CI require a coordinated migration.
- Coverage readiness and adapter conformance become explicit activation requirements for resident runs.
