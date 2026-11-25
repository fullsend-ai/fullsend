---
title: "176. Repository-driven epic creation"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - repositories
  - jira
  - resolution
  - skills
---

# 176. Repository-driven epic creation

Date: 2026-11-23

## Status

Accepted

## Context

Epic Creator decomposes strategies into implementation epics. Today
it splits epics by Jira component and team boundary. Epics have no
assigned repository — a human knows which repo a component lives in,
but the system does not record or verify it.

The relationship between Jira components and repositories is
many-to-many: one Jira component can map to multiple repos, and
multiple Jira components can share the same repo.

A **configuration group** is a named set of sources, component
mapping, and settings — such as the Jira field ID — shared by all
consumers that use it. For epic-creator, the RHAI configuration is
one group.

Resolution must be:

- Correct — the right repository for this work, not just any related
  one
- Verified — even a deterministic match is confirmed against
  architecture context, because both the component assigned to the Jira
  issue and the component mappings are human-maintained and can be wrong
- Explicit — resolved with cited evidence, or unresolved with a reason
  and candidates; never silently guessed
- Generic — not hardcoded to any single configuration group's sources
  or rules

This ADR aims to define how implementation epics are assigned to
repositories, replacing the current component-based epic boundary with
a repository-based one that produces one epic per planned PR.

## Options

- **Trust deterministic mapping without verification** — rejected: both
  the component assigned to the Jira issue and the mappings themselves
  are human-maintained and can be wrong or stale. Even a single-repo
  match needs verification against architecture context.

- **Three layers with LLM guess as fallback** — rejected: if the LLM
  with full architecture context (Phase 2) cannot resolve, a second
  LLM call without better information adds nothing. The guess either
  agrees with Phase 2 (redundant) or disagrees (confusing for the
  reviewer). Unresolved is a cleaner signal than a low-confidence hint.

- **Split by component, then assign repos** — rejected: the many-to-many
  relationship between Jira components and repos means component-scoped
  epics can span multiple repos or duplicate work in a shared repo.
  Splitting by repo from the start avoids this.

## Decision

We adopt a two-phase resolution skill — optional deterministic hints
followed by LLM-verified resolution against architecture context — to
resolve the repositories a strategy touches and assign each
implementation epic to exactly one repository.

### The resolver

A standalone resolution skill that any agent can call. It takes the
issue description, zero or more Jira components (optional hints), and
the configured sources (architecture context, and optionally a
component mapping). It returns the resolved repositories or an
unresolved result. The resolver knows nothing about work items, epics,
PR grouping, or any consumer's pipeline.

**Input**: the issue description, zero or more Jira components, and
configured sources (architecture context required; component mapping
optional). Many tickets — especially outside bugs and CVEs — have no
component set. The resolver handles this: when no component is
provided, Phase 1 is skipped and Phase 2 resolves from the issue
description and architecture context alone.

**Output**: one of:
- Resolved: one or more repositories (org and name), each with cited
  evidence from the architecture context. A narrow work item resolves
  to one repo; a strategy resolves to every repo its implementation
  touches.
- Unresolved: a reason, and the candidate repositories from the
  mapping if one is configured

### Repository details

Every repository the resolver returns carries full details —
organization and name — before the result is accepted: the canonical
URL is built from them. The details come from the architecture
context, either directly where a repository is mentioned with its
organization, or by resolving a repository key against the
structured repository data. This resolution always runs, with or
without a mapping. A repository mention that cannot be resolved to
full details is a hard failure before any result is accepted.

### Two-phase resolution

Resolution has two phases: collect hints, then resolve with evidence.

**Phase 1 — Hints (optional).** When Jira components are provided and
a mapping is configured, look up each component in the mapping. The
union of their candidate repositories is passed to Phase 2 as hints —
even a single-repo match. Both the components assigned to the Jira
issue and the mappings themselves are human-maintained and can be
wrong or stale, so a mapping hit is a strong signal but not a trusted
final answer. Phase 1 is skipped entirely when no component is
provided or no mapping is configured — Phase 2 runs without hints.

**Phase 2 — Resolution (always runs).** The LLM reads the architecture
context, the issue description, and any hints from Phase 1. It
identifies the repositories involved in the work, citing specific text
from the architecture context as evidence for each. The cited evidence
must exist in the pinned source at the recorded commit SHA. If Phase 1
provided hints, Phase 2 confirms or overrides them — confirmed hints
resolve faster than a cold resolution. If Phase 2 cannot identify the
repositories with supporting evidence, the result is unresolved.

**Trust boundary.** All resolver inputs are untrusted data, never
instructions. The issue description defines what the work is; any
repo-assignment claim inside it — like a Jira component or a mapping
entry — is a hint. Only the architecture context can justify an
assignment: Phase 2 must find its own supporting evidence there
regardless of what the description asserts. The citation check
verifies that the cited evidence exists in the pinned source; whether
it actually matches the work is a semantic judgment, made by human
review before publication. Consumers that call the resolver without a
human review step carry the risk of a plausible-but-wrong assignment.

When resolution fails, the resolver returns unresolved with:
- The reason resolution failed
- The candidate repositories from the mapping, if any

What the consumer does with an unresolved result — leave the field
empty, add a warning comment, block publication — is up to the
consumer. For epic-creator, the unresolved epic lifecycle is:

1. An unresolved item is not grouped by repo — it becomes its own
   epic with an empty repo field and a warning comment carrying the
   reason, candidates, and available evidence
2. A human completes the assignment: reads the comment, sets the repo
   URL field manually (or fixes the inputs and re-runs resolution)
3. Once the field is set, polling agents discover the epic like any
   other

### Component mapping (optional)

Configuration groups may provide a mapping of Jira component → candidate
repositories. The mapping is optional — groups without one skip
Phase 1 and go straight to Phase 2.

The mapping records:
- Which repositories are associated with each Jira component — a
  component may map to one repo or many. Entries carry repository
  keys, not full details.

Phase 1 uses the mapping to identify candidate repositories for a
given Jira component. These candidates are hints for Phase 2, not
final answers.

The mapping is configuration-group data — each group owns its own. It is a
declared source fetched through the same mechanism as architecture
context. Groups that do not maintain a mapping still get full
resolution through Phase 2.

### Repository inventory (optional)

A group may maintain an approved repository inventory — the list of
repositories it is allowed to touch. The inventory is a policy
decision, separate from the architecture context: describing a
repository there is documentation, not approval. When a group
maintains an inventory, every resolved repository must be in it,
checked by the deterministic layer in code before any Jira write — a
resolution outside it never reaches the epic. A group without an
inventory has no boundary: resolution relies on the evidence
requirement, the trust boundary, and the downstream gates.

A group that wants a boundary without maintaining a separate list
may declare the architecture context's repository set as its
inventory, by reference.

### Architecture context

Architecture context is a declared source entry in the group's
configuration:

```yaml
sources:
  - id: architecture
    url: https://github.com/<org>/architecture-context
    ref: main
    paths:
      - docs/
    role: architecture
```

The source declaration tells the loader where the data lives. The
loader fetches the source, snapshots it, and records a content
hash. Refs are resolved to a commit SHA at load time. If the content
changes between resolution and verification, the integrity check
rejects the result.

An architecture context may include structured component data:
repository organization, name, URL, release ref, component type,
tier, cross-references to other components, and discovery
confidence. This is the data that repository keys resolve against for
full details, and that Phase 2 uses alongside the mapping hints to
resolve with evidence.

The component mapping, architecture context, and other sources are
declared sources with the same structure: `id`, `url`, `ref`, `paths`,
`role`. Loading, snapshotting, hashing, and verification are
deterministic scripts shipped with the skill — not platform
machinery, and not model judgment. Each group owns the declaration
of where its sources live. Any configuration group points at its own
architecture repository — the resolution logic does not change. Each
group is a configuration of
sources fed into the same generic resolution logic, declared without
code changes in the resolver.

#### Future: architecture context skills

Agents should not reference the architecture context
repository directly. Instead, each group would declare which skills
to load from its architecture source. The skills would provide
interpretation guidance: which files to read, what the structure means,
how to map component names to repos. This is future work — the current
implementation reads the architecture context directly.

The direction: Phase 2 would read the pre-loaded architecture context
using interpretation guidance from the group's declared skills,
rather than hardcoding how to read a specific source. This would also
address the scaling concern — groups that
cannot afford to maintain a static architecture-context repo at scale
could provide skills that work differently.

This direction is already motivated beyond RHAI: tools that map out
an organization's repositories and their connections already exist,
and teams in the organization already maintain data about which
repositories map to which components and teams — shipped as skills,
in structures totally different from the architecture-context
repository. The resolver must treat architecture context as data
whose shape and producer vary — not assume a single hand-maintained
repository.

### Verification

Verification checks the resolver's result before it is accepted. The
checks are deterministic scripts shipped with the skill, run against
the resolver's output and the pinned source snapshots:

- **Citation check:** every repository in the result carries cited
  evidence, and each citation exists verbatim in the pinned source at
  the recorded commit SHA. This holds regardless of how the result was
  produced — a confirmed hint, an overridden hint, or a cold
  resolution.
- **Schema check:** the result matches the resolver output contract —
  resolved repositories with full details, or unresolved with reason
  and candidates.
- **Inventory check:** when the group maintains an inventory, every
  resolved repository is in it — checked before any Jira write.

Configuration and input validation happen before resolution starts
(see Inputs and delivery): malformed declarations, unfetchable
sources, and unresolvable repository mentions are hard failures, not
verification outcomes.

What remains LLM judgment is only identifying the repositories and
choosing the evidence to cite. A check only binds if it runs: the
consuming pipeline executes the verification scripts on the resolver
output before accepting a result. Platform-side enforcement of the
same checks is future work as more consumers adopt the skill.

### The resolution skill

The resolution logic lives in its own skill directory
(`skills/repository-resolution/`). The skill contains the resolution
instructions and scripts — the two-phase procedure, evidence citation
rules, and verification logic.

The skill owns:
- The two-phase resolution procedure (hints → resolution)
- The deterministic checks, as scripts: source loading, ref → SHA
  resolution, snapshotting, hashing, repository key → full-details
  resolution, input and output schema validation, and the
  citation-exists-at-SHA check
- The output contract: resolved repos + cited evidence,
  or unresolved + reason + candidates

The skill does not own:
- Splitting strategies into work items
- Grouping resolved items into PRs
- Drafting epics
- Publication to Jira
- The group's source data

The skill lives in the epic-creator repository for now. Where it moves
when more consumers need it is decided with the fullsend maintainers.

### Inputs and delivery

The resolver's configuration lives at a fixed path in the repository
of the consuming pipeline — for epic-creator, under `config/`:

```
config/
  resolver.yaml       # the resolver's configuration: declared sources
                      # (id, url, ref, paths, role) and consumer
                      # settings such as the Jira field ID
  _schema/            # JSON Schema files validating every config
                      # input
```

Declared sources are fetched, snapshotted, and hashed by the loader —
the config holds the declarations, not the data. Repo-local sources
use `url: self` pinned to the repository's own HEAD.

The config travels with the consuming repo's checkout into the agent
sandbox — no delivery mechanism is needed to locate it. These files
are the skill's own configuration; they are not keys in fullsend's
validated `config.yaml`, so no config schema change is required.

Configuration errors are hard failures before resolution starts: a
malformed declaration, a schema violation, a source that cannot be
fetched, or a missing Jira field ID stops the run with an error.
These are not "unresolved" results — unresolved is a resolution
outcome, not a configuration error.

Exact field-by-field schemas for the declarations and for the resolver
output are follow-up work, tracked in
[#9851](https://github.com/fullsend-ai/fullsend/issues/9851).

### Complete flow

A strategy arrives with a description containing requirements
and acceptance criteria, and zero or more Jira components.

**Step 1 — Resolve repositories.** Call the resolution skill once for
the strategy (see "Two-phase resolution" above): the strategy
description plus its Jira components, if any, as hints. When no
component is set, the resolver uses the strategy description and
architecture context alone. The result is the set of repositories
involved in the strategy's implementation, each with cited evidence,
or an unresolved result with candidates. The evidence is preserved
through the remaining steps.

**Step 2 — Split into work items.** Using the resolved repository set,
split the strategy's requirements into work items. Each work item
belongs to exactly one repo. A requirement that spans two repos becomes
two work items referencing the same requirement, with a dependency
between them. When splitting reveals a slice that belongs to a repo
outside the resolved set, that slice is resolved individually — one
extra resolver call with the slice's description — so every work item
carries its own evidence.

**Step 3 — Group into planned PRs.** Work items targeting the same repo
are grouped into planned PRs. Work items that form one coherent
reviewable change share a planned PR. Work items in the same repo that
represent distinct changes — for example, an API change and a dependent
operator change, or code versus documentation — become separate planned
PRs. Each planned PR has exactly one repo.

**Step 4 — Draft epics.** One epic per planned PR. Each epic carries the
resolved repo URL and the resolution evidence — the cited architecture
context sources. Unresolved work items skip repo grouping: each
becomes its own epic with an empty repo field and a comment carrying
the reason, candidates, and what evidence was available.

**Step 5 — Review and publish.** The agent self-reviews the drafted
epics for consistency (correct repo URLs, evidence present, no
unresolved items published without warning). Then a staff engineer
reviews the output before publication. The resolution evidence is
included so the reviewer can verify each repo assignment — what text
was cited and what source versions were used.
The repo URL is written to the configured Jira URL field. If sources
have changed since resolution, the system forces re-resolution before
publication.

### Jira field for resolved repository

The resolved repository is written to a configurable Jira field on the
epic. The field is a URL type. The written value uses one canonical
form — `https://<host>/<org>/<name>`, no `.git`, lowercase host, no
trailing slash — and the deterministic layer normalizes to it before
writing. Jira URL fields match on exact value only in JQL, so any
variation (SSH form, `.git` suffix, trailing slash) would make epics
invisible to the agents polling for them.

Which field to use is group configuration — a group's Jira
project may already have a suitable field, and others configure
their own. The field ID is not hardcoded in the resolver or the epic
creator — it is read from the group's configuration.

Agents polling for work (e.g., code agents in specific repos) query
this field with the canonical URL to find epics assigned to their
repo.

The repo field is a discovery signal, not a dispatch trigger. Code
agents start work only through their existing gate — a `/fs-code`
command from a human, or the `ready-to-code` label — never from the
repo field alone.

### Epic boundary shift: component → repo

Current main splits epics by Jira component and team boundary. This
ADR changes the epic boundary to repository.

This matters because the relationship between Jira components and repos
is many-to-many:
- One component → multiple repos: a component-scoped epic would need
  PRs in multiple repos, violating one-PR-per-epic
- Multiple components → one repo: separate component-scoped epics for
  work that belongs in a single PR

Repo-based boundaries produce epics that each correspond to exactly one
PR. One epic per planned PR, one repository per planned PR. Several
planned PRs may target the same repository when the changes are
distinct enough to warrant separate reviews. Cross-repository
requirements are split into separate work items with tracked
dependencies.

Resolved epics carry the repository URL in the group's configured
Jira URL field. Unresolved epics are created with an empty URL field
and a warning comment carrying the reason and candidates.

### Feature flag

Repository resolution is gated by the `repository_mode` environment
variable. The flag controls whether the resolver runs before
decomposition:

- **Off (the production default):** the resolver is skipped entirely.
  Epic Creator splits by Jira component and team boundary — the current
  main behavior. No repository is assigned to epics.
- **On:** the resolver runs first (Step 1 in the complete flow), then
  decomposition splits along repo boundaries (Steps 2–5). Epics
  carry the resolved repository URL.

The flag is an environment variable, delivered as a CI variable in
GitLab. Unset means off — the production default — so runs without
the variable keep legacy behavior regardless of forge. It can be
overridden per pipeline run — for example, to test resolution on a
single strategy without enabling it for all runs. It is not per-repo
or per-team. If a strategy touches multiple repos, resolution applies
to all of them in that run.

Existing component-based epics are untouched; with the flag on, new
runs produce one epic per planned PR.

The flag does not go on in production until the resolver passes a
backtest: run it over captured strategies whose implementation
destinations are already known from merged PRs, and measure how often
resolution agrees. Unresolved is an acceptable outcome; a wrong
assignment is not. The initial backtest set — real strategies with
observed merge destinations — already exists from the epic-creator
evaluation.

### Design tradeoff: seamless vs. maintainable

Resolution should be seamless to the end user — the system figures out
the right repo without the user knowing how. But fully seamless
resolution (no maintained mappings, the LLM figures everything out at
runtime) requires the LLM to scan potentially hundreds of repos to
make a decision. That is expensive, slow, and less reliable.

Fully deterministic resolution (maintained mappings and crosswalks for
every product, every release) is fast and reliable, but the
operational cost of maintaining that data at scale is high — for a
group with hundreds of repos across multiple releases, keeping the
mappings current is a significant ongoing effort.

The two-phase design is a middle ground:

- Phase 1 narrows the search cheaply — a maintained mapping provides
  candidate hints where they exist.
- Phase 2 resolves with evidence — the LLM reads the architecture
  context, evaluates the hints, and identifies the involved repos with
  cited evidence. If hints are wrong or missing, Phase 2 still resolves
  from the architecture context alone.

The user sees: resolved repos with evidence, or an unresolved result
with candidates. They do not see the phases. The experience is seamless
when the data supports it, and explicit when it doesn't — the system
never silently guesses wrong.

Both the component assigned to the Jira issue and the mappings
themselves are human-maintained and can be wrong or stale. Phase 2
always verifies — a mapping hint that contradicts the architecture
context is overridden, not blindly trusted.

What does the mapping buy, given Phase 2 always runs? Not a skipped
phase. If the group's components are reliable, candidate hints can
make Phase 2 cheaper; when resolution fails, the mapping supplies the
candidates offered to the human; and every Phase 2 override of a
mapping hint is a signal that the mapping needs maintenance. Whether
the components are reliable enough for this to pay off — signal or
noise — is the group owner's open question, not this ADR's decision.

Without a mapping, the search is bounded by the architecture context,
not the organization. The LLM never scans an org's repositories — it
reads the group's architecture context, one body of documents
describing which components exist and where they live. That is why
architecture context is the required source and the mapping is the
optional accelerator.

The operational cost scales with how much of the resolution a group
wants to accelerate. A group whose components are reliable can invest
in its mapping to accelerate Phase 2; a group without a mapping still
gets full Phase 2 coverage. The
architecture context skill (see above) allows groups to provide
their own implementation of how context is fetched, so the operational
model is not one-size-fits-all.

## Forge scope

The resolution skill is forge-neutral — it reads declared sources
(architecture context, optional component mapping) and returns
resolved repositories. Source loading is the same deterministic
scripts shipped with the skill (see Verification): a declared source
is fetched by its URL, with no forge-specific resolution logic. The
Jira URL field for the resolved repository is tracker configuration,
not forge-specific.

## Consequences

- The resolver is a standalone skill: description in, repositories
  out. Any agent can use it without importing epic-creator pipeline
  concepts.
- Two-phase resolution: hints from mapping → LLM resolves with
  evidence. The mapping accelerates resolution but never short-circuits
  it — every result is verified against architecture context.
- Human-assigned component mappings are treated as hints, not truth.
  Wrong or stale assignments are caught by Phase 2 verification.
- Unresolved is an honest answer — no low-confidence guesses published
  as hints. The system resolves with evidence or says it cannot.
- Epic boundaries shift from Jira component to repository — each epic
  corresponds to exactly one PR in one repo
- Groups declare their own sources and optionally a mapping; each
  group is configuration, not code
- Changed inputs invalidate prior results
- The skill stays in epic-creator for now; moving it is future work

## Open questions

- Where group mappings live long-term (the architecture-context
  repository is a candidate)
- Whether the Jira component is signal or noise for a given group —
  whether maintaining a component mapping pays off at all, or
  resolution should run from the description and architecture context
  alone. This is the group owner's decision, informed by the
  backtest's with-and-without-mapping comparison.
