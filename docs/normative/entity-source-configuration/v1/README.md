# Entity-source configuration v1

This contract defines the configuration binding chosen by
[ADR 0079](../../../ADRs/0079-bind-entity-execution-through-named-sources.md).
It applies to effective per-repository configuration, including managed
`repos.yaml` layers. MUST, SHOULD, and MAY express requirements for conforming
implementations.

**Target contract, not implemented:** existing configuration continues to use
`tracker`. This document is not the complete discovery/query wire protocol,
normalized-entity specification, or authorization contract. Those contracts,
provider conformance, and context support are prerequisites for entity-driven
execution, as required by ADR 0079.

The `v1` label versions this contract; it does not add a version field to
`config.yaml`. Configuration readers remain strict and older binaries may reject
the new fields. Deploy updated readers and managed-config consumers before
writing entity-source fields; mixed-version readers are not supported during
that cutover, and diagnostics MUST identify the unsupported configuration key.

## Configuration shape

Repository configuration binds named harness slots to entity-source scopes:

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

The example code harness declares
`entity_kinds: {source: work_item, target: change_proposal, related_work: work_item}`.
A triage harness can declare `entity_kinds: {}` to use the default work-item
source and target kinds. Agent names are illustrative, not a fixed inventory.
Concrete connection names and repository/project scopes belong to repository
configuration. The `entities` map is configuration, not a map of already-fetched
entity values exposed to CEL.

Unknown fields, unknown names, incompatible provider/kind pairs, and empty
required scope lists MUST fail validation. Unsupported capabilities cannot be
treated as empty discovery results. Compatibility placeholders for existing
incomplete tracker declarations are limited to the migration rules below.

### Connections

`connections` is a map of names to service-specific settings. Each ordinary
entry MUST specify `kind`; arbitrary names do not imply a provider. The reserved
names `github`, `gitlab`, and `forgejo` are provider-default connections: their
kind is implied, and an explicit `kind` MUST agree. Initial enabled entity
connection kinds are `github`, `gitlab`, and `jira`; reserving `forgejo` does not
enable an unsupported adapter. Non-entity connection kinds,
such as Vault under [PR #9097](https://github.com/fullsend-ai/fullsend/pull/9097),
share the registry but MUST NOT be accepted by an entity source.

Record the repository from which this invocation's fullsend configuration was
cloned as trusted origin metadata: provider, host, and canonical repository
identity/path. This selects the native provider and supplies connection defaults;
it does not lock native entities to the clone host. For the origin's provider,
synthesize its named connection when absent, using the origin host. A declared
connection of that name replaces the default and MAY set a different `base_url`.
If it omits `base_url`, use the origin host; never fill it from the CI platform.
Other provider-default connections require an explicit endpoint when used.
Connection kinds and endpoints determine where credentials are sent. Only
configuration layers designated trusted by the repository's configuration
policy may set or change them; event data, harnesses, and untrusted overlays
MUST NOT. At minimum, trusted layers include managed `repos.yaml` values and
`.fullsend/` configuration read from the protected default branch under
[ADR 0033](../../../ADRs/0033-per-repo-installation-mode.md). PR-head config,
harness content, and event data cannot designate themselves as trusted endpoint
overrides. Credential bindings MUST be provisioned for the canonical provider
endpoint and tenant, and the resolver MUST refuse to send a credential to any
other endpoint even when the connection name and kind match. Changing an
endpoint requires a separately provisioned credential binding for that host;
the old host's credential, grants, and cached evidence do not transfer.

For example, a GitHub.com clone selects the `github` provider. Setting
`connections.github.base_url` to a GitHub Enterprise URL makes that instance
the default GitHub connection used by native entities. The same rule applies to
GitLab and Forgejo. The execution platform and imported layer/harness origins do
not change the selected provider, but trusted configuration layers can change the
provider-default connection's instance. Missing origin prevents native-provider
inference; it MUST NOT be guessed from the runner or current Git remote. There
is no separate fixed `repository` connection. Entity `native` is distinct from
the Vault proposal's `native` secret-source selector.

Forge connections identify their provider instance through `base_url`;
Jira uses `base_url` for the site and optional `api_url` for its API
gateway, preserving the current Jira gateway default. Provider validation MUST
reject endpoints containing embedded credentials. An inherited issue-creation
policy MUST NOT authorize a different instance unless trusted configuration
explicitly rebinds that policy to the new instance.

Connections identify where to connect, not an authorization principal. Host
credential resolution MUST bind each connection to a provisioned credential
scope for the exact canonical endpoint and tenant, and keep discovery
credentials separate from harness run identities. A connection name or kind
alone MUST NOT select or reuse a credential for another host. Changing an
endpoint invalidates the old binding until a credential for the new host is
separately provisioned.
Legacy translated connections retain existing credential lookup. Credentials
MUST NOT be written into this configuration or sent in dispatch descriptors.
Additional connections require independently provisioned host bindings; they
MUST NOT inherit credentials merely because their `kind` matches. Concrete
credential-reference formats remain part of the provider credential contracts.

### Sources and queries

`entity_sources` is a map of named entries. Each source MUST resolve a
connection and provider-valid `query`; it MUST NOT declare a `kind`. If a source
omits `connection`, use the connection with the same name as the source and
require that connection to exist when the source is active. An unbound reserved
preset may omit its connection as described below. Reserved provider entries
follow the stricter same-name rule below. The
adapter's supported kinds and the query's selection are checked against each
consuming harness slot's kind. The same source may serve differently typed slots.
The planner enumerates only the kinds needed by active primary-source bindings.
An active forge source's `repositories` list scopes it to explicit canonical
repository paths. Omission is permitted only for the reserved source whose
provider matches the configuration origin. In that case it means the clone's
repository path on the same-named provider-default connection, including when
that connection uses an explicit `base_url` for another instance. All other
active forge sources, including reserved names for non-origin providers, MUST
specify `repositories`. An unbound reserved entry for another provider is an
inert preset and may omit its connection and repository scope until it is bound.
An explicit list selects those paths; it does not claim they are the same
canonical repository as the clone. A Jira source MUST specify a nonempty
`projects` list.
Source scopes MUST resolve to canonical provider identities before use.
Configuration authorizes a source scope, not an arbitrary target code
repository.

GitHub accepts the bounded UI filter subset defined below; an explicitly empty
query adds no filter beyond the scope and kind guards. GitLab initially accepts
`open`, `closed`, and `all` presets for `work_item` and `change_proposal`. These
are fullsend lifecycle predicates translated into native API filters. For change
proposals, `closed` means not open and expands to the union of native
`state=closed` and `state=merged` results; these are separate GitLab API states.
CEL can distinguish merged from closed-unmerged entities. Jira work-item sources
accept native JQL; an explicitly empty string
adds no filter beyond the project guard. Their project guard is independent of
the query:
`OR` terms MUST NOT widen it. When scopes overlap, adapters plan per canonical
provider/tenant/project-or-repository/kind/credential scope, so each scope has
one combined logical enumeration under ADR 0149, not one search per alias.

GitLab's presets are an initial fullsend subset, not a claim that the provider
supports only lifecycle filters. Its [REST issue filters](https://docs.gitlab.com/api/issues/#list-all-project-issues)
also include labels, assignees, and dates. The
[merge request API](https://docs.gitlab.com/api/merge_requests/#list-project-merge-requests)
exposes `closed` and `merged` as separate `state` values; the fullsend `closed`
preset therefore combines both native queries. The
[GLQL operators](https://docs.gitlab.com/user/glql/#operators) support conjunction
and some same-field alternatives through `in`, but not arbitrary disjunctions
of whole queries. GitHub supports
[Boolean and nested issue filters](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/filtering-and-searching-issues-and-pull-requests#building-advanced-filters-for-issues)
and an advanced-search option in its
[search API](https://docs.github.com/en/rest/search/search#search-issues-and-pull-requests).
Query support requires adapter conformance for the deployed provider
version, exact membership, and complete discovery under provider limits.

For Jira, combine parenthesized selection predicates and use one adapter-owned
traversal order. Individual `ORDER BY` clauses do not alter set membership and
MUST be separated before combination. Any batching or partitioning required by
provider limits remains part of that combined logical enumeration and MUST
preserve coverage. Unsupported queries or capabilities MUST fail explicitly;
they cannot silently become unrestricted, disappear from the plan, or cause a full
search per agent. Time-dependent queries require periodic rediscovery even
without entity updates. Provider-side filter changes and other dependencies
must likewise be reconciled rather than assumed immutable.

Native provider semantics determine membership. Local evaluation MAY avoid a
provider membership request only when it proves the same result from current,
sufficiently complete data under the same provider scope, credential identity,
and relevant query context. The adapter MUST NOT substitute the actor-filtered
CEL view. For example, `currentUser()` cannot be reinterpreted using the agent's
run identity. Batch unresolved membership checks across entities and distinct
queries in one credential scope. Query errors or uncertain freshness produce
an unresolved candidate with retry/rediscovery, never a false result. A proven
false harness predicate MAY avoid membership reads for that harness; a true
predicate cannot bypass membership. A prior discovery result does not prove
membership at a later sandbox cycle.

`discovery` is a boolean, defaulting to `true`, that permits broad enumeration
and eventless rechecks when this scope is bound to the primary `source` slot.
Binding it only to another slot adds no scheduled work. It does not authorize execution or
set the clock frequency. `false` permits event-backed candidates subject to
current membership and admission, but excludes this source from broad polling
and eventless execution. Effective platform eligibility and operational polling
coverage remain required even when it is `true`.

### Native sources and reserved names

`native` is an entity binding selector, not a definable source name.
`entity_sources.native` MUST fail validation in every configuration layer.
Resolve `native` in order: the configuration origin selects the provider's
reserved source name (`github`, `gitlab`, or `forgejo`); that source selects its
connection; the effective connection selects the instance. Resolve the source's
repository scope and consuming slot's kind on that instance. The clone host and
path are defaults, not immutable native destinations. Execution platform never
selects the provider or overrides these configured values.

The reserved provider names customize native selection. A declared reserved
entry MUST contain a provider-valid `query`. The reserved source name is also
its connection name: `entity_sources.github` uses `connections.github`,
`entity_sources.gitlab` uses `connections.gitlab`, and `entity_sources.forgejo`
uses `connections.forgejo`. The source may omit `connection`, which defaults to
that same name; if specified, `connection` MUST equal the reserved source name.
A reserved source MUST NOT use a differently named connection, even when its
kind matches. To select a different instance for the native provider, configure
the same-named provider-default connection's `base_url`. If the origin's
matching source entry is absent, synthesize it on that provider-default
connection with an all-state query and `discovery: true`: an empty query for
GitHub, or `all` for GitLab. If present, its `query` and optional `discovery`
override those defaults under ordinary whole-entry layering. Its repository
scope defaults to the clone's path on the same-named connection, including when
that connection's endpoint is overridden; an explicit `repositories` list
selects another path. Other named sources can select additional instances or
scopes.

Provider names MAY also appear explicitly in `agents[].entities` lists and in
standalone source selection. For the origin's provider, `[github]` (for example)
and `[native]` resolve to the same customized source even if its definition was
omitted. For a different provider, such as `[gitlab]` with a GitHub origin, an
explicit binding is allowed when the same-named provider-default connection and
an explicit repository scope are configured. Do not infer that external
connection from the execution platform. Such a source does not change native
resolution or acquire credentials or creation grants.

An unbound reserved entry for another provider may contain just its query and
discovery settings so a preset can customize several native providers. It does
not activate that provider. Binding it explicitly on a nonmatching origin
requires the missing external connection and scope to be supplied; otherwise
validation fails. Reservation alone does not enable a Forgejo adapter: provider,
kind, query, and admission support are still required before a source is used.
Additional names may be reserved in future contract revisions; adding a
reservation must diagnose existing incompatible definitions rather than silently
reinterpret them.

```yaml
# Configuration cloned from GitHub.com; runtime may be Kubernetes or GitLab CI.
# Native entities use the configured GitHub Enterprise instance.
connections:
  github:
    kind: github
    base_url: https://github.example.com
  gitlab:
    kind: gitlab
    base_url: https://gitlab.example.com
entity_sources:
  github:
    query: 'is:open -label:blocked'
  gitlab:
    repositories: [example/related-work]
    query: open
agents:
  - name: triage
    entities:
      source: [native, gitlab]
      target: [github]
```

Here `native` and `github` select `example/project` on `github.example.com`,
even though the configuration was cloned from GitHub.com. The `github` source
omits both `connection` and `repositories`: the same-named connection is used,
and the clone's owner/repository path carries over to that connection's
enterprise endpoint. `gitlab` selects the configured external scope.
Binding both `native` and `github`
does not duplicate discovery or executions. Native customization applies to all
uses of that native source, including omitted source bindings and targets that
inherit them; query filters still do not constrain creation or explicit identity
operations. Source and target kind defaults are unchanged.

### GitHub query subset

GitHub source `query` values and harness `entity_filters.<slot>.github` accept
the same subset of native issue/PR search syntax. The initial supported
qualifiers are:

- Lifecycle: `is:open`, `is:closed`, and their `state:` equivalents; for change
  proposals, also `is:merged` and `is:unmerged`.
- Metadata: `label:`, `author:`, `assignee:`, and `milestone:` with literal
  values; `type:` for issue types on work items. GitHub's `type:issue` and
  `type:pr` aliases are kind assertions, like `is:issue` and `is:pr`.
- Missing metadata: `no:label`, `no:assignee`, and `no:milestone`.
- Dates: `created:`, `updated:`, and `closed:`, plus `merged:` for change
  proposals. Accept absolute dates or timestamps, comparisons, and inclusive
  ranges using GitHub's [date syntax](https://docs.github.com/en/search-github/getting-started-with-searching-on-github/understanding-the-search-syntax#query-for-dates).
- Change-proposal fields: `draft:true`, `draft:false`, `base:`, and `head:`.

Use native [qualifier semantics](https://docs.github.com/en/search-github/searching-on-github/searching-issues-and-pull-requests),
including quoted values, case handling, and branch matching. Accept explicit
`AND`, whitespace conjunction, `OR`, and parentheses with GitHub's precedence.
Accept `-` exclusion of a single supported qualifier where GitHub permits it;
`-no:...` and negation of groups are not supported. Positive label comma lists
are native alternatives; repeated label terms are conjunctive. Express
exclusions with individual terms, such as `-label:blocked -label:wip`.
Use `is:open` or `is:closed` for lifecycle selection and `query: ''` for no
additional restriction. Bare `open`, `closed`, and `all` are unsupported free
text, not query shorthands. The adapter always retains the scope and kind guards.

For example, a GitHub repository can provide a named source while a portable
harness further narrows it:

```yaml
# .fullsend/config.yaml on a GitHub repository
entity_sources:
  actionable_work:
    connection: github
    query: 'is:open (label:bug OR label:enhancement) -label:blocked'
agents:
  - name: code
    entities:
      source: [actionable_work]
      target: [native]
```

```yaml
# Harness YAML; other fields omitted.
entity_kinds:
  source: work_item
  target: change_proposal
entity_filters:
  source:
    github: 'is:open label:"needs-code" no:assignee'
```

The effective query requires both expressions inside the configured repository
and work-item guard. A review harness could instead use a change-proposal
source with `is:open draft:false base:main -label:blocked`.

Parse and validate this subset before provider submission; do not pass unknown
syntax through as free text. Reject bare text, `in:`, general `NOT`, relative
dates, viewer-relative identities such as `@me`, and qualifiers outside the
list above. This excludes review/CI status, involvement or comment-history
filters, interaction counts, and project-board membership in v1. Richer agent
conditions remain in CEL until their native query membership and acquisition
contracts are supported. Sorting and ranking are adapter-owned; user `sort:`
is not accepted. Reject `repo:`, `org:`, and `user:` with a diagnostic pointing
to source scope configuration. Kind assertions MUST agree with the slot and
cannot replace the adapter's independent kind guard.

Combine parsed expressions, then serialize with explicit grouping and escaped
values; never concatenate unchecked strings. Preserve every condition when
provider query-length or nesting limits require partitioning or broader shared
enumeration. Exact local checks use current provider-view metadata, including
complete relevant collections, and follow the native membership rules above.
Unknown membership remains unresolved; negative filters cannot treat unfetched
labels, assignees, or other metadata as absent.

### Harness slots and repository bindings

`entity_kinds` is a harness map from slot names to single normalized kind names.
The reserved slots are `source` and `target`; other names declare additional
requirements. Compose explicit harness declarations first, preserving which
keys were omitted, then apply these defaults:

- Missing `source` means `work_item`.
- Missing `target` means the effective `source` kind.
- Every additional slot must explicitly name its kind.

An explicit inherited target kind remains explicit when a child changes the
source kind. A defaulted target follows the final source kind. These defaults
apply to the entity-capable contract; they MUST NOT silently activate an old
event-only harness. `entity_kinds: {}` is an explicit declaration of the default
slots. A legacy harness without the declaration stays on its compatibility
path until coordinated entity-mode cutover.

`agents[].entities` maps the same slot names to lists of scope names. Every
value MUST be a list, including when it contains one name. Every referenced
scope MUST exist and support the slot's effective kind. Compose bindings by
slot name: omission inherits, and an explicit list replaces that slot's list
rather than extending it. After composition, a missing `source` becomes
`[native]`; a missing `target` copies the effective source list, including an
explicit empty list. Explicit target lists, including inherited lists, win.
Copy scope names, not resolved entities or their kinds: resolve each copied
name against the target kind and reject incompatibility. For example, a Jira
source with a change-proposal target needs an explicit `target: [native]`.
Additional harness slots require explicit, nonempty lists, possibly inherited
from an explicit configuration layer. Unknown slot keys and missing required
bindings are configuration errors. Do not materialize default target lists in
parent layers before child source overrides have been applied.

An explicit `source: []` disables source-based scheduling, including
source-backed event candidates, without restoring native defaults or inventing
an event-only fallback. An explicit `target: []` provides no default output
scopes; operations needing that slot fail until a valid target is bound.
Use ordinary agent enablement to disable an agent completely.

Resolve `native` and the reserved provider names using the native-source rules
above, after configuration composition. The same source can resolve differently
for a work-item slot and a change-proposal slot. Missing native origin or
provider/kind support MUST be diagnosed, not replaced by an external connection
or treated as empty. External sources always require explicit bindings.

Only the `source` slot drives automatic candidate discovery, event routing,
and scheduling. Its list is a union of alternatives; one run binds one
canonical source entity, exposed as `entity` to the existing CEL contract.
`target` and additional slots provide typed scopes available to the run. They
MUST NOT create automatic joins, Cartesian products, eager acquisition, or
independent polling/dispatch merely by being bound. Reading from such a scope
still requires the appropriate acquisition, actor-admission, and context
filtering contracts. A configured scope is not admitted model context.

Every slot accepts multiple scopes. Output lists are choices, not automatic
broadcast. An operation identifies the slot and, when necessary, a specific
scope and repository/project within it. A single unambiguous destination may
be selected automatically; otherwise the operation MUST require an explicit
selection rather than use list order. Each selected destination must support
the slot's kind and pass independent run and creation policy checks.

Deduplicate primary-source candidates by target code repository, agent, and
canonical entity identity, independently of aliases. Do not combine evidence
from incompatible credential/admission scopes to manufacture a successful
check. Matching several source aliases does not create several executions.
Replies and status updates on an existing entity use its canonical origin;
`target: [native]` does not redirect a Jira source issue's update to a forge
issue with the same display number.

### Harness provider filters

Harnesses MAY declare `entity_filters`, a map from effective entity slot names
to maps of provider kinds and query strings. Filters do not change the shape or
defaults of `entity_kinds`. For example:

```yaml
entity_kinds:
  source: work_item
  target: change_proposal
entity_filters:
  source:
    github: 'is:open label:"needs-code" no:assignee'
    gitlab: open
    jira: 'statusCategory != Done'
trigger: >-
  entity.state == "open" &&
  "needs-code" in entity.labels
```

Choose the provider entry from each bound connection's kind, including the
resolved native provider, not its connection alias. Query values use the same
provider-specific language and kind validation as source queries. Unknown
slots, unknown provider keys, and explicitly unsupported filters MUST fail
validation. An omitted provider entry adds no restriction. Compose filters by
slot and provider: omission inherits, while an explicit query replaces that
slot/provider's inherited query. Use an empty query for GitHub or Jira, or
`all` for GitLab, to explicitly remove an inherited restriction. Target filters
MUST NOT default to source filters, even when kinds and bindings do.

For each binding, the effective selection is the intersection of the trusted
source scope, source query, and harness filter for that provider. The source's
scope and query remain enforced; a harness cannot broaden either. Combine the
effective selections of enabled primary-source bindings with logical OR in each
compatible discovery scope, deduplicating equivalent expressions. This is a
logical union requirement, not a requirement to serialize a native OR operator.
If the provider cannot express the union, the adapter MAY enumerate a shared
superset within the trusted scope and check the effective selections exactly.
It MUST preserve coverage, provider limits, and retry semantics; it MUST NOT
drop a filter or fall back to separate full searches per harness. When no
conforming plan is possible, report an unsupported combination.

For `source`, the filter is a required selection condition, not an optional
performance hint. It applies to polling, event-backed candidates, and every
further bound-run cycle. Match the combined source query and harness filter
using native semantics, the exact local fast path, and batched unresolved
provider checks described above. Cheap filter mismatches can skip full CEL
evaluation; execution still requires both a current selection match and full
actor-filtered harness CEL, along with independent authorization and context
admission. A broader discovery result alone proves neither condition.

Only `source` drives automatic discovery and scheduling. Filters on `target`
or additional named slots constrain explicit searches in those scopes without
adding background enumeration or eager acquisition. As with source queries,
they do not restrict creation destinations or explicit identity operations.
Standalone issue commands do not load a harness's filters.

### GitHub search and selective acquisition

The GitHub adapter SHOULD use GraphQL where it can combine discovery with
selective field acquisition. GitHub documents
[advanced search through GraphQL](https://github.blog/changelog/2025-03-06-github-issues-projects-api-support-for-issues-advanced-search-and-more/)
using `search(type: ISSUE_ADVANCED, query: ...)`; the current
[search schema](https://docs.github.com/en/graphql/reference/search) exposes that
type and typed issue/pull-request results. Validate the supported mode for the
deployed host. An unsupported mode MUST NOT silently fall back to interpreting
the same Boolean text with legacy search semantics. Preserve the per-kind
discovery scopes, using `is:issue` or `is:pr` explicitly. Search remains lexical
and set-based; semantic relevance and top-N ranking cannot establish coverage.

Build two coordinated plans: a candidate-selection query from the effective
source/filter expressions, and a field-acquisition plan from enabled harness
CEL dependencies under [ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md).
Mapping CEL field dependencies to GraphQL projections does not authorize
translating arbitrary actor-filtered CEL conditions into provider search terms.
The initial projection SHOULD share identity, scope, membership-routing facts,
and inexpensive decision-relevant fields across compatible harnesses. After
selection and partial evaluation eliminate harnesses or candidates, batch
further node reads for the dependencies still needed. Do not eagerly project
the union of every harness's complete history requirements onto every result.
Share acquired facts only across compatible credential and admission scopes.

Projections MUST include or subsequently acquire the provenance, authorization
evidence, and coverage needed to establish a decision, even when those fields
are not named directly in CEL. Derived fields and provider aggregates cannot
substitute for the actor-filtered view. Page nested connections as necessary;
a partial label, comment, review, or timeline page is not a complete collection.
Apply ADR 0147's proven true/false versus unresolved semantics throughout.

The search API exposes at most 1,000 results for one search, independently of
the selected fields. Partition or reconcile discovery as required by ADR 0149;
reaching that cap cannot establish coverage. GraphQL
[resource limits and partial errors](https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api)
also apply: selecting fewer fields can reduce work, but does not by itself
guarantee fewer billed points or complete results. Required data affected by an
error remains unresolved, never absent. Full model-context acquisition remains
a separate admitted step after selection and predicate evaluation.

### Destination policy and issue commands

There is no `issue_commands` configuration block. Inside an agent run, new
entity creation defaults to the `target` slot; an operation may explicitly
select another declared slot and a destination in its list. Existing-entity
operations use the resolved entity's origin. A slot binding selects a provider,
kind, and scope; it does not grant credentials or mutation authority. Source
queries are neither creation templates nor authorization policies. A new entity
need not already satisfy a discovery query before it can be created.

`connections.<name>.allowed_targets` carries the independent work-item creation
policy formerly under `tracker`: GitHub `orgs`/`repos`, GitLab `groups`/`repos`,
and Jira `projects`. Omission grants no additional destinations. The target
code repository remains implicitly allowed on its own forge and host, subject
to actual run permissions. For Jira, allowed creation projects must be listed
explicitly; the translator includes the legacy primary project to preserve its
implicit grant. Binding a Jira project as an input or output does not itself
grant creation rights there. Policies replace as whole values; grants cannot
move to another instance merely by reusing a connection alias.

Lookup scope supplies the ordinary destination choices. An explicit work-item
creation destination outside that lookup scope is allowed only when the
connection's separate `allowed_targets` policy covers it, the operation selects
a compatible bound slot/connection, and run permissions allow it. This retains
legacy explicit cross-project issue creation without turning a discovery query
into a write grant. Change-proposal creation retains the execution repository
boundary and the existing forge/run permission checks.

### Standalone issue commands

Without agent/entity context, `fullsend issues` commands select one scope using
`--entity-source <name>`, where the name is an effective `entity_sources` entry,
the reserved `native` selector, or its matching provider name. This is target
CLI syntax, not an implemented
flag. The command supplies the `work_item` kind, so the selected source MUST
support that kind. Resolve configuration through the normal layers, selected
with `--fullsend-dir` where needed; no agent or harness is required. The selector
is singular: standalone operations MUST NOT fan out across sources or inspect
an agent's `entities` bindings to choose a default.

For the configuration above:

```sh
# Resolve PROJ1-123 through jira1; jira_work has only one project.
fullsend issues get --fullsend-dir .fullsend --entity-source jira_work --number 123

# Select a project explicitly when the source contains several projects.
fullsend issues get --fullsend-dir .fullsend --entity-source jira_related --project PROJ2 --number 123

# Resolve issue 123 in the effective native source, including connection overrides.
fullsend issues get --fullsend-dir .fullsend --entity-source native --number 123
```

Infer an omitted `--project` only when the selected scope identifies exactly one
project or repository. Otherwise require an explicit project/repository, or an
unambiguous entity identity where the command accepts one. Resolve `native`
only when its provider and effective connection/scope can be resolved from the
configuration origin plus overrides; missing required context MUST produce an
error. Validate explicit entity origins against the selected
connection and lookup scope. Explicit creation destinations remain subject to
the independent creation policy above, including its cross-project allowance.

An explicit identity operation such as `get`, `post-comment`, or `link` MUST NOT
run the discovery query or require the identified entity to match it. For
example, `jira_work` can retrieve a closed PROJ1 issue by number even though its
discovery query excludes completed work. Queries select candidates for discovery;
they do not prohibit explicit scoped reads or updates. Authentication and
mutation checks still apply, and `create` still requires creation permission.

Without an explicit source, standalone commands MUST NOT silently select the
first configured source, the first agent, or a native default. The compatibility
adapter alone preserves existing tracker command selection, including the old
`--tracker` selector and default connection/project behavior, with deprecation
warnings. An explicit `--entity-source` overrides legacy command defaults;
supplying it together with `--tracker` MUST fail as conflicting selectors. With
neither a selected source nor a legacy selection, fail with a diagnostic that
requests `--entity-source`. No new repository-wide command-default block is
introduced. Removing legacy tracker configuration therefore requires updating
standalone invocations; agent-context operations obtain their slots from the
plan.

## Layering and legacy translation

Named `connections` and `entity_sources` entries replace whole entries by name;
they do not deep-merge. Provider-default connections and reserved provider source
overrides use the same whole-entry replacement. After composition, use the
recorded configuration origin to fill omitted native defaults, then resolve the
effective connection and source. Explicit connection endpoints and repository
scopes win over clone-derived defaults. Omitted maps
inherit; empty maps do not delete unrelated
inherited names. Harness `entity_kinds` and agent `entities` compose by slot
name, with defaults applied only after composition. Each explicit binding list
replaces the inherited list for that slot. Creation allowlists MUST NOT be
unioned across layers.

Resolve legacy `tracker` using its existing whole-block replacement semantics
across layers, then translate the effective result into a canonical fallback.
Explicit new entries override their translated equivalents, even if legacy
syntax occurs in a later layer; diagnostics MUST identify shadowed settings.
A new explicit slot binding replaces that translated slot's entire list. This
allows mixed-layer migration without changing legacy-only behavior or retaining
write grants from a replaced tracker provider.

For every present provider block, the translator creates the compatibility
connection `legacy_tracker` using the legacy endpoint and credential-resolution
rules. It attaches that block's `allowed_targets` to this connection, including
for GitHub and GitLab blocks that contain only creation policy; they MUST NOT be
copied to another provider-default connection. A provider-only block still does
not create a discovery scope. Explicit entries with the name `legacy_tracker`
replace the corresponding generated entries and undergo ordinary validation;
independent new names coexist. Effective-config inspection MUST distinguish translated entries, explicit
replacements, and compatibility-only command defaults.

Translate existing tracker configuration without guessing missing values:

- Preserve provider, endpoint, host credential binding, and effective command
  default connection/project. Jira's primary project becomes the lookup
  scope's `projects` element; native JQL and its legacy default are retained.
- Preserve `poll.enabled`: true permits discovery, while false or omission
  does not. A provider-only declaration creates no usable discovery scope;
  incomplete Jira declarations retain their existing use-time failure.
- Copy `allowed_targets` to the connection policy, including Jira's implicit
  primary-project grant and the existing native-repository grant. Do not add
  fresh-install defaults to an existing installation's permissions.
- Legacy drivers and commands consume this resolved configuration through an
  internal compatibility projection, including command defaults where there is
  no agent context. Translation alone MUST NOT add entity declarations, change
  poll checkpoints, switch predicate semantics, or replay historical work.
- At coordinated entity-mode cutover, established external routes become
  explicit `entities.source` bindings and established output destinations
  become typed slot bindings. Native defaults MUST NOT silently replace them.
  An unmappable route or destination prevents that binding's cutover and keeps
  its legacy path with a diagnostic.

Runtime loading and managed installation MUST share normalization and
validation. Warn once per loaded legacy definition per process, with its
location and replacement guidance, on diagnostics rather than machine-readable
stdout. Reads MUST NOT rewrite source files. Raw-YAML consumers must migrate
to effective configuration or a host-generated compatibility projection before
new-only configuration is enabled; the projection is never a second authority
merged back into configuration.

No release or removal date is set for `tracker` support. Keep the
compatibility translator until every supported reader, command, poller,
installer, and raw-YAML consumer has migrated and no supported execution
path depends on the legacy form. Removing it requires a later ADR and a
published major-release migration window. Until then, deprecation
warnings do not disable the old configuration.

## Execution readiness and cutover

The resolved execution plan records the agent/harness revision, trusted target
repository identity, configuration-origin provider/host/repository identity,
effective connection endpoints and canonical source repositories after overrides,
named slot kinds and binding lists, query/configuration revision,
credential-scope identity, and platform eligibility. Dispatch conveys canonical
entity identity and optional verified event context with a reference to this
trusted plan; event payloads MUST NOT supply connection endpoints, credentials,
or self-asserted eligibility. The receiving run revalidates the plan and current
admission before creating a sandbox. A discovery alias is not an execution lock.

Installers reconcile recurring discovery only for enabled effective `source`
bindings; target and auxiliary lists do not add scheduled work. For GitHub Actions
execution this is a managed workflow; for GitLab CI execution it is a managed
pipeline schedule/job. These are execution adapters, not native-provider
selection rules. A standalone scheduler uses the same readiness and coverage
contract. Each invokes the shared fullsend discovery path,
with the proper trusted credential scope, rather than reproducing agent routing
in workflow expressions. Event-driven GitHub Actions execution retains synchronous
dispatch correlation; GitLab CI uses its protected-default-branch delivery model.
Concurrency and result publication conform to ADR 0144, including preservation
of pending event-backed work and the latest selected cycle's result.

Resident eventless execution requires evidence that the discovery mechanism is
enabled, compatible, and making progress for the same effective plan, including
durable retry or reconciliation for uncovered work. A workflow filename or a
historical successful job alone is insufficient. The execution adapter MUST
define a bounded readiness age relative to its configured schedule, retry and
cycle budget, and store/check that evidence in trusted per-repository state.
No new centralized scheduler is required. Missing or stale evidence gives
automatic once behavior; an explicit resident request fails with a diagnostic.

Before each further cycle, verify current eligibility and plan readiness. A
changed configuration origin, binding, policy, credential scope, or execution
contract invalidates old readiness and cached membership. The resident process MUST stop admitting
new cycles and relinquish its claim through ADR 0144's pending-work protocol;
an already admitted sandbox retains the existing cancellation policy. New
invocations resolve the updated plan. Coverage invalidation MUST trigger a scan
for newly eligible work, not inherit a watermark that could skip it.

Activate by agent/source binding after its contracts, adapters, context handling,
and harness support are deployed. For that binding, only one of legacy event
reconstruction and entity-driven admission may own dispatch at a time. Keep
legacy paths for unmigrated agents. Generic execution jobs or existing stage
wrappers may deliver admitted work, but all migrated bindings use the same
fullsend admission logic. Job deadlines and lifecycle choices use ADR 0144;
source queries do not select lifecycle or define an agent sequence.

## Conformance requirements

Implementations MUST cover at least these observable cases:

- The v1 contract has no in-band configuration version field. Old strict readers
  reject entity-source keys with an actionable key-specific error; managed
  rollout does not write those keys until every supported reader is upgraded.
- Connection endpoints can be changed only by trusted layers. Discovery
  credentials are bound to the canonical endpoint and tenant; changing the host
  requires a separately provisioned binding and never sends the previous host's
  credential to the new endpoint.
- Kind defaults and per-slot inheritance resolve correctly; all binding values
  are lists, source defaults to `[native]`, and an omitted target copies the
  final source list. Explicit targets win, copied names must support the target
  kind, and additional named slots require explicit compatible nonempty bindings.
- A source that omits `connection` uses the connection with the same name;
  validation fails when that connection is absent. Reserved source names may use
  only their same-named provider connection. For a GitHub-origin clone,
  `entity_sources.github` may omit both `connection` and `repositories`; it uses
  `connections.github` and the clone path even when that connection points to
  GitHub Enterprise. `entity_sources.github.connection: enterprise` fails
  validation. A reserved source for another provider must specify its repository
  scope. Untrusted layers cannot change endpoints, and a new endpoint requires a
  separately provisioned credential binding.
- Native resolution uses the clone's provider independently of Kubernetes,
  GitHub Actions, or GitLab CI execution. Its provider-default connection can
  override the instance. Explicit matching-provider aliases deduplicate with
  `native`; external names do not change native resolution. Unbound
  cross-provider preset overrides start no discovery, and endpoint changes do
  not transfer grants or evidence.
- Only the primary source list schedules work; target/auxiliary lists create
  neither implicit joins nor broadcast writes, and ambiguous destinations need
  explicit selection. Empty source/target lists suppress their defaults.
- Harness filters resolve by slot and connection provider, inherit by
  slot/provider, and only narrow source selection. Missing filters add no
  restriction; targets do not inherit source filters. Shared enumeration covers
  the union of effective selections, with exact per-harness checks before
  admission even when enumeration must use a superset. Event-backed and resident
  cycles enforce the same filters. Other slots filter only explicit searches.
- GitHub subset parsing preserves Boolean grouping, quoted values, label
  alternatives and exclusions, kind assertions, and native metadata/date
  semantics. Unsupported terms and embedded scope/sort qualifiers fail before
  provider submission. Negative and missing-metadata tests require complete
  relevant facts; combining or partitioning cannot weaken a configured filter.
- GitHub discovery uses a verified advanced-search mode, with a projection
  derived from currently relevant CEL and admission dependencies. Irrelevant
  history is not eagerly fetched, partial nested collections remain explicit,
  and search caps or field errors cannot establish complete coverage or a
  negative predicate result.
- One canonical entity matching several aliases/queries admits at most one
  execution per agent and target repository, without mixing credential scopes.
- Jira query combination preserves scoped membership; the local fast path
  agrees with native evaluation, and unresolved checks batch without per-agent
  searches or changing the effective Jira principal. GitLab `closed` for a merge
  request combines native `state=closed` and `state=merged` results, while a work
  item uses `state=closed`; neither preset mistakes merged for open.
- A source match alone cannot authorize a sandbox or admit content; uncertain
  membership, actor authority, or coverage cannot masquerade as a non-match.
- Legacy translation preserves command defaults, polling enablement, credential
  resolution, and creation permissions, including whole-block tracker replacement
  and shadowing. For provider-only GitHub and GitLab blocks, `allowed_targets`
  attaches to the generated `legacy_tracker` connection; it is not lost or
  copied to a provider-default connection. Provider-only Jira entries follow the
  same rule. Standalone invocations without legacy defaults or agent/entity
  context require explicit scope selection.
- Standalone `--entity-source` selects one work-item scope independently of
  agent bindings. Unique projects are inferred; ambiguous projects require
  selection. Explicit identity operations can access entities outside a source's
  discovery filter while retaining scope and authorization checks. Conflicting
  new and legacy selectors fail, and legacy defaults cannot override an explicit
  new selection.
- A new-format configuration works through migrated script consumers without
  retaining `tracker` on disk; diagnostics do not corrupt machine-readable output.
- Lost coverage readiness and changed plans prevent new resident cycles while
  preserving pending events and rediscovery; closed/merged entities remain
  eligible for matching agents, including after webhook loss.

Provider query pagination/consistency proofs, detailed authorization evidence,
and normalized entity/context schemas are specified and tested in their owning
contracts. Passing configuration conformance alone MUST NOT enable execution.
