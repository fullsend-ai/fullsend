---
title: "176. Repository-driven epic creation"
status: Proposed
relates_to:
  - agent-architecture
  - workloads
topics:
  - repositories
  - jira
  - resolution
  - skills
---

# 176. Repository-driven epic creation

Date: 2026-11-23
Ticket: [AISDLC-547](https://redhat.atlassian.net/browse/AISDLC-547)

## Status

Proposed

## Context

Epic Creator decomposes strategies into implementation epics. Today
it splits epics by Jira component and team boundary. Epics have no
assigned repository — a human knows which repo a component lives in,
but the system does not record or verify it.

The relationship between Jira components and repositories is
many-to-many: one Jira component can
map to multiple repos (e.g., AI Evaluations → eval-hub,
trustyai-explainability, trustyai-service-operator), and multiple
Jira components can share the same repo.

Resolution must be:

- Correct — the right repository for this work, not just any related
  one
- Verified — even a deterministic match is confirmed against
  architecture context, because both the component assigned to the Jira
  issue and the component mappings are human-maintained and can be wrong
- Explicit — resolved with cited evidence, or unresolved with a reason
  and candidates; never silently guessed
- Generic — not hardcoded to any single workload's sources or rules

## Decision

### The resolver

A standalone resolution skill that any agent can call. It takes a Jira
component, the issue description, and the configured sources
(architecture context, and optionally a component mapping). It returns
a resolved repository or an unresolved result. The resolver knows
nothing about work items, epics, PR grouping, or any consumer's
pipeline.

**Input**: a Jira component, the issue description, and configured
sources (architecture context required; component mapping optional).

**Output**: one of:
- Resolved: the repository (org and name) with cited evidence from the
  architecture context
- Unresolved: a reason, and the candidate repositories from the
  mapping if one is configured

### Two-phase resolution

Resolution has two phases: collect hints, then resolve with evidence.

**Phase 1 — Hints (optional).** Look up the Jira component in the
maintained mapping. If the component is in the mapping, its candidate
repositories are passed to Phase 2 as hints — even a single-repo match.
Both the component assigned to the Jira issue and the mappings
themselves are human-maintained and can be wrong or stale, so a mapping
hit is a strong signal but not a trusted final answer. If the
component is not in the mapping, Phase 2 runs without hints.

**Phase 2 — Resolution (always runs).** The LLM reads the architecture
context, the issue description, and any hints from Phase 1. It selects
a repository, citing specific text from the architecture context as
evidence. The cited evidence must exist in the pinned source at the
recorded commit SHA, and the selected repository must be in the
candidate set from the mapping (when one exists). If Phase 1 provided
a hint, Phase 2 confirms or overrides it — a confirmed hint resolves
faster than a cold resolution. If Phase 2 cannot identify a repository
with supporting evidence, the result is unresolved.

When resolution fails, the resolver returns unresolved with:
- The reason resolution failed
- The candidate repositories from the mapping, if any

What the consumer does with an unresolved result — leave the field
empty, add a warning comment, block publication — is up to the
consumer.

### Component mapping (optional)

Workloads may provide a mapping of Jira component → candidate
repositories. The mapping is optional — workloads without one skip
Phase 1 and go straight to Phase 2.

The mapping records:
- Which repositories are associated with each Jira component
- Repository details (organization, name) — the caller builds the
  SSH or HTTP URL from these as needed

For RHAI, this data already exists. The Component Mapping Results
document maps every Jira component to its repos.
Examples from the existing data:

| Jira component | Repos |
| --- | --- |
| AI Core Dashboard | odh-dashboard, odh-dashboard-sync, red-hat-odh-dashboard |
| AI Evaluations | eval-hub, trustyai-explainability, trustyai-service-operator |
| AI Pipelines | argo-workflows, data-science-pipelines, data-science-pipelines-operator, ... (9 repos) |
| Feature Store | feast |
| Model Serving | kserve, odh-model-controller, modelmesh, vllm-orchestrator-gateway, ... (9 repos) |

Phase 1 uses the mapping to identify candidate repositories for a
given Jira component. These candidates are hints for Phase 2, not
final answers.

The mapping is workload data — RHAI owns its mapping, another workload
may own its own. It is a declared source fetched through the same
mechanism as architecture context. Workloads that do not maintain a
mapping still get full resolution through Phase 2.

### Architecture context

Architecture context is a declared source entry in the workload
configuration:

```yaml
sources:
  - id: rhoai-arch
    url: https://github.com/opendatahub-io/architecture-context
    ref: rhoai-3.0
    paths:
      - docs/
    role: architecture
```

The source declaration tells the platform where the data lives. A
generic loader fetches the source, snapshots it, and records a content
hash. Refs are resolved to a commit SHA at load time. If the content
changes between resolution and verification, the integrity check
rejects the result.

For RHAI, the architecture-context repository already contains a
`component-map.json` with full structured details for each component:
repository organization, name, URL, release ref, component type
(operator, ui, asset, …), tier (core_platform, …), cross-references
to other components, and discovery confidence. This is the data that
Phase 2 uses alongside the mapping hints to resolve with evidence.

The component mapping, architecture context, and other sources are
declared sources with the same structure: `id`, `url`, `ref`, `paths`,
`role`. The platform owns loading, snapshotting, hashing, and
verification. Each workload owns the declaration of where its sources
live. Any workload points at its own architecture repository — the
resolution logic does not change. RHAI is one configuration of sources
fed into generic resolution logic. Other workloads declare their own
sources without code changes in the resolver.

#### Future: architecture context skills (from Nov 17 tech review)

Ralph proposed that agents should not reference the architecture context
repository directly. Instead, each workload would declare which skills
to load from its architecture source. The skills would provide
interpretation guidance: which files to read, what the structure means,
how to map component names to repos. This is future work — the current
implementation reads the architecture context directly.

The direction: Phase 2 would read the pre-loaded architecture context
using interpretation guidance from the workload's declared skills,
rather than hardcoding how to read a specific source. This would also
address the scaling concern from the Nov 17 review — workloads that
cannot afford to maintain a static architecture-context repo at scale
could provide skills that work differently.

### Verification

Phase 1 is deterministic — when a mapping is configured, it produces
hints, not final answers. When no mapping is configured, Phase 1 is
skipped entirely.

Phase 2 is LLM-based with evidence — the cited evidence must exist in
the pinned source at the recorded commit SHA. When a mapping is
configured, the selected repository must also be in the candidate set
from that mapping. This applies whether Phase 2 confirms a Phase 1
hint, overrides it, or resolves without hints.

### The resolution skill

The resolution logic lives in its own skill directory
(`skills/repository-resolution/`). The skill contains the resolution
instructions and scripts — the layered lookup, evidence citation rules,
and verification logic.

The skill owns:
- The two-phase resolution procedure (hints → resolution)
- The verification logic (evidence citation check, mapping membership
  when a mapping is configured)
- The output contract: resolved repo + cited evidence,
  or unresolved + reason + candidates

The skill does not own:
- Splitting strategies into work items
- Grouping resolved items into PRs
- Drafting epics
- Publication to Jira
- The workload's source data

The skill lives in the epic-creator repository for now. Where it moves
when more consumers need it is decided with the fullsend maintainers.

### Complete flow

A RHAISTRAT strategy arrives with one or more Jira components and a
description containing requirements and acceptance criteria.

**Step 1 — Resolve components.** For each Jira component in the
strategy, call the resolution skill (see "Two-phase resolution"
above). Each component gets a resolved repo with cited evidence, or an
unresolved result with candidates. The evidence is preserved through
the remaining steps.

**Step 2 — Split into work items.** Using the resolved component → repo
map, split the strategy's requirements into work items. Each work item
belongs to exactly one repo. A requirement that spans two repos becomes
two work items referencing the same requirement, with a dependency
between them.

**Step 3 — Group into planned PRs.** Work items targeting the same repo
are grouped into planned PRs. Work items that form one coherent
reviewable change share a planned PR. Work items in the same repo that
represent distinct changes — for example, an API change and a dependent
operator change, or code versus documentation — become separate planned
PRs. Each planned PR has exactly one repo.

**Step 4 — Draft epics.** One epic per planned PR. Each epic carries the
resolved repo URL and the resolution evidence — the cited architecture
context sources. Unresolved work items still get an epic, but with
an empty repo field and a comment carrying the reason, candidates,
and what evidence was available.

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
epic. The field is a URL type — the caller builds the URL from the
repo details (org and name) returned by the resolver.

Which field to use is workload configuration. For RHAI, the field
already exists in the Jira project. Other workloads configure their
own field. The field ID is not hardcoded in the resolver or the epic
creator — it is read from the workload definition.

Agents polling for work (e.g., code agents in specific repos) can
use JQL to query this field and find epics assigned to their repo.

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

Resolved epics carry the repository URL in the workload's configured
Jira URL field. Unresolved epics are created with an empty URL field
and a warning comment carrying the reason and candidates.

### Feature flag

Repository resolution is gated by the `repository_mode` configuration
flag. The flag controls whether the resolver runs before decomposition:

- **Off (the production default):** the resolver is skipped entirely.
  Epic Creator splits by Jira component and team boundary — the current
  main behavior. No repository is assigned to epics.
- **On:** the resolver runs first (Steps 1–2 in the complete flow),
  then decomposition splits along repo boundaries (Steps 3–5). Epics
  carry the resolved repository URL.

The flag is a CI variable in GitLab (Nov 17 tech review discussion).
It has a global default for all pipeline runs but can be overridden
per pipeline run — for example, to test resolution on a single
strategy without enabling it for all runs. It is not per-repo or
per-team. If a strategy touches multiple repos, resolution applies to
all of them in that run.

### Design tradeoff: seamless vs. maintainable

Resolution should be seamless to the end user — the system figures out
the right repo without the user knowing how. But fully seamless
resolution (no maintained mappings, the LLM figures everything out at
runtime) requires the LLM to scan potentially hundreds of repos to
make a decision — Ella raised this in the Nov 17 review: the OpenShift
organization alone has ~800 repos. That is expensive, slow, and less
reliable.

Fully deterministic resolution (maintained mappings and crosswalks for
every product, every release) is fast and reliable, but the
operational cost of maintaining that data at scale is high — for a
workload with hundreds of repos across multiple releases, keeping the
mappings current is a significant ongoing effort.

The two-phase design is a middle ground:

- Phase 1 narrows the search cheaply — a maintained mapping provides
  candidate hints. For RHAI, this data already exists.
- Phase 2 resolves with evidence — the LLM reads the architecture
  context, evaluates the hints, and selects a repo with cited evidence.
  If hints are wrong or missing, Phase 2 still resolves from the
  architecture context alone.

The user sees: a resolved repo with evidence, or an unresolved result
with candidates. They do not see the phases. The experience is seamless
when the data supports it, and explicit when it doesn't — the system
never silently guesses wrong.

Both the component assigned to the Jira issue and the mappings
themselves are human-maintained and can be wrong or stale. Phase 2
always verifies — a mapping hint that contradicts the architecture
context is overridden, not blindly trusted. This addresses the tech
review feedback that component assignments are error-prone.

The operational cost scales with how much of the resolution a workload
wants to accelerate. A workload that invests in its mapping — covering
more components with accurate hints — gets faster Phase 2 resolutions.
A workload without a mapping still gets full Phase 2 coverage. The
architecture context skill (see above) allows workloads to provide
their own implementation of how context is fetched, so the operational
model is not one-size-fits-all.


## Alternatives considered

- **Trust deterministic mapping without verification** — rejected: both
  the component assigned to the Jira issue and the mappings themselves
  are human-maintained and can be wrong or stale. Even a single-repo
  match needs verification against architecture context. The tech review
  confirmed this risk.

- **Three layers with LLM guess as fallback** — rejected: if the LLM
  with full architecture context (Phase 2) cannot resolve, a second
  LLM call without better information adds nothing. The guess either
  agrees with Phase 2 (redundant) or disagrees (confusing for the
  reviewer). Unresolved is a cleaner signal than a low-confidence hint.

- **Split by component, then assign repos** — rejected: the many-to-many
  relationship between Jira components and repos means component-scoped
  epics can span multiple repos or duplicate work in a shared repo.
  Splitting by repo from the start avoids this.

## Consequences

- The resolver is a standalone skill: component in, repo out. Any
  agent can use it without importing epic-creator pipeline concepts.
- Two-phase resolution: hints from mapping → LLM resolves with
  evidence. The mapping accelerates resolution but never short-circuits
  it — every result is verified against architecture context.
- Human-assigned component mappings are treated as hints, not truth.
  Wrong or stale assignments are caught by Phase 2 verification.
- Unresolved is an honest answer — no low-confidence guesses published
  as hints. The system resolves with evidence or says it cannot.
- Epic boundaries shift from Jira component to repository — each epic
  corresponds to exactly one PR in one repo
- Workloads declare their own sources and optionally a mapping; RHAI
  is configuration, not code
- Changed inputs invalidate prior results
- The skill stays in epic-creator for now; moving it is future work
- PR #56's core machinery (source loading, validation, finalization)
  carries forward; the refactor restructures, not rewrites

## Open questions

- Whether the approved repository inventory (PR #56's
  `repository-inventory.yaml`) is a separate source from the component
  mapping, or part of it
- Where workload mappings live long-term (the architecture-context
  repository is a candidate)

## References

- [AISDLC-547](https://redhat.atlassian.net/browse/AISDLC-547): RHAI
  repository mapping: evaluate and integrate Epic Creator resolution
- [AISDLC-90](https://redhat.atlassian.net/browse/AISDLC-90): Tag
  implementation epics with target repositories (parent)
- [AISDLC-546](https://redhat.atlassian.net/browse/AISDLC-546): Verify
  sources and build evaluation cases
- [PR #56](https://github.com/fullsend-ai/epic-creator/pull/56):
  Current implementation (merge held pending design approval)
- [PR #45](https://github.com/fullsend-ai/epic-creator/pull/45):
  Earlier ADR in epic-creator (replaced by this document)
