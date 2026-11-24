---
title: "176. Layered repository resolution"
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

# 176. Layered repository resolution

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
- Deterministic where possible — no LLM when a lookup can answer
- Explicit — resolved deterministically, or unresolved with a reason,
  candidates, and a hint; never silently guessed
- Generic — not hardcoded to any single workload's sources or rules

## Decision

### The resolver

A standalone resolution skill that any agent can call. It takes a Jira
component, the issue description, and the configured sources
(component mapping and other configured sources such as architecture
context). It returns a resolved
repository or an unresolved result. The resolver knows nothing about
work items, epics, PR grouping, or any consumer's pipeline.

**Input**: a Jira component, the issue description, and configured
sources (component mapping, architecture context, and other configured
sources).

**Output**: one of:
- Resolved: the repository URL with the layer that resolved it
- Unresolved: a reason, the candidate repositories from the mapping,
  and a hint from the LLM guess if layer 3 ran

### Three resolution layers

Resolution proceeds through three ordered layers, cheapest and most
certain first. Stop at the first definitive answer. Because each layer
records which layer resolved and why — a mapping hit, a cited
architecture doc, or an LLM guess — the selection is verifiable by both
agents (downstream validation scripts) and humans (the reviewer sees
the evidence in the epic comment).

**Layer 1 — Component mapping (deterministic).** Look up the Jira
component in a maintained mapping. If the component maps to exactly one
repository, return it. No LLM needed. If the component maps to multiple
repositories, pass the narrowed candidate set to layer 2. If the
component is not in the mapping, the resolver returns unresolved — the
mapping is the source of truth for which components have approved
repositories.

**Layer 2 — Architecture context (open question).** Given the candidate
repositories from layer 1 and the architecture docs, try to narrow the
candidates. How this layer works is an open design question:

- **Deterministic**: structured metadata in the architecture context
  maps directly to repos. No LLM, but requires maintaining a crosswalk
  between architecture-context names and Jira component names — high
  operational overhead at scale (Ella's concern from the Nov 17 review).
- **LLM with evidence**: the LLM reads the architecture docs and
  selects a repo, citing specific text as proof. Lower maintenance,
  but no longer deterministic.

If this layer identifies exactly one repository, return it. If it
cannot narrow the candidates, pass them to layer 3.

**Layer 3 — LLM guess (hint only).** The LLM receives the candidate
repositories from the mapping, the Jira issue description, and the
architecture context. If Layer 2 is deterministic, this is the first
time an LLM reads the architecture context — combined with the Jira
description, it has enough to make a reasonable guess. The LLM picks
the most likely repository from the candidates. The guess is recorded
as a hint for a human reviewer — it is never published as the
repository. This is a signal to the human, not a decision by the
system.

When no layer resolves, the resolver returns unresolved with:
- The reason resolution failed
- The candidate repositories from the mapping
- The hint from layer 3, if one exists

What the consumer does with an unresolved result — leave the field
empty, add a warning comment, block publication — is up to the
consumer.

### Component mapping

One data structure per workload: Jira component → candidate
repositories, each with its URL.

The mapping records:
- Which repositories are associated with each Jira component
- Their canonical URLs

For RHAI, this data already exists. The Component Mapping Results
document maps every Jira component to its repos with URLs.
Examples from the existing data:

| Jira component | Repos |
| --- | --- |
| AI Core Dashboard | odh-dashboard, odh-dashboard-sync, red-hat-odh-dashboard |
| AI Evaluations | eval-hub, trustyai-explainability, trustyai-service-operator |
| AI Pipelines | argo-workflows, data-science-pipelines, data-science-pipelines-operator, ... (9 repos) |
| Feature Store | feast |
| Model Serving | kserve, odh-model-controller, modelmesh, vllm-orchestrator-gateway, ... (9 repos) |

Layer 1 uses the mapping to identify candidate repositories for a
given Jira component.

The mapping is workload data — RHAI owns its mapping, another workload
owns its own. It is a declared source fetched through the same mechanism
as architecture context.

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
    skills:
      - discover-components
      - repo-to-architecture-summary
```

The source declaration tells the platform where the data lives. The
`skills` field tells it which skills from that source to load for
interpretation — these provide the guidance on how to read the data,
what the structure means, and how to map component names to repos.
Different workloads load different skills for the same resolver.

A generic loader fetches the source, snapshots it, and records a
content hash. Refs are resolved to a commit SHA at load time. If the
content changes between resolution and verification, the integrity
check rejects the result.

For RHAI, the architecture-context repository already contains a
`component-map.json` with full structured details for each component:
repository organization, name, URL, release ref, component type
(operator, ui, asset, …), tier (core_platform, …), cross-references
to other components, and discovery confidence. This is the data that
Layer 2 would use — it already has the structured metadata needed to
narrow candidates when Layer 1 returns multiple repos.

The component mapping, architecture context, and other sources are
declared sources with the same structure: `id`, `url`, `ref`, `paths`,
`role`, `skills`. The platform owns loading, snapshotting, hashing, and
verification. Each workload owns the declaration of where its sources
live and which skills to load for interpreting them. Any workload points
at its own architecture repository and skills — the resolution logic
does not change. RHAI is one configuration of sources fed into generic
resolution logic. Other workloads declare their own sources without code
changes in the resolver.

#### Architecture context skills (from Nov 17 tech review)

Ralph proposed that agents should not reference the architecture context
repository directly. Instead, each workload declares which skills to
load from its architecture source. The architecture context is loaded
and snapshotted upfront — sources are fetched, pinned to a commit SHA,
and content-hashed before the resolver runs. The skills do not fetch
data at resolution time; they provide the interpretation guidance: which
files to read, what the structure means, how to map component names to
repos.

For RHAI, the skills live in the architecture-context repo itself (which
already has 12 skills including `discover-components`,
`repo-to-architecture-summary`, and `analyze-platform-components`). They
instruct the agent how to read the RHAI docs — for example, how to use
`component-map.json` to match candidates by type and tier. For another
workload with different data sources — for example, a team that queries
repos live instead of maintaining a static architecture-context repo —
the workload provides skills that get the same information from their
source.

Layer 2 reads the pre-loaded architecture context using the
interpretation guidance from the workload's declared skills, rather than
hardcoding how to read a specific source. This also addresses the
scaling concern from the Nov 17 review — workloads that cannot afford
to maintain a static architecture-context repo at scale can provide
skills that work differently.

### Verification

Layer 1 is deterministic — the result comes directly from the mapping,
no verification beyond mapping membership needed.

Layer 2 verification depends on its implementation: if deterministic,
same as Layer 1. If LLM-based, the cited evidence must exist in the
pinned source at the recorded commit SHA, and the selected repository
must be in the candidate set from Layer 1.

Layer 3 is a hint — it is never published as the answer, so it does
not require verification. The hint is presented to the human reviewer
alongside the candidates and the reason resolution failed.

### The resolution skill

The resolution logic lives in its own skill directory
(`skills/repository-resolution/`). The skill contains the resolution
instructions and scripts — the layered lookup, evidence citation rules,
and verification logic.

The skill owns:
- The three-layer resolution procedure
- The verification logic (mapping membership check)
- The output contract: resolved repo + resolving layer + evidence,
  or unresolved + reason + candidates + hint

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
strategy, call the resolution skill (see "Three resolution layers"
above). Each component gets a resolved repo with evidence, or an
unresolved result with candidates and a hint. The evidence is preserved
through the remaining steps.

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
resolved repo URL and the resolution evidence — which layer resolved it
and the cited sources. Unresolved work items still get an epic, but with
an empty repo field and a comment carrying the reason, candidates, hint,
and what evidence was available.

**Step 5 — Review and publish.** The agent self-reviews the drafted
epics for consistency (correct repo URLs, evidence present, no
unresolved items published without warning). Then a staff engineer
reviews the output before publication. The resolution evidence is
included so the reviewer can verify each repo assignment — which layer
resolved it, what text was cited, and what source versions were used.
The repo URL is written to the configured Jira URL field. If sources
have changed since resolution, the system forces re-resolution before
publication.

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
and a warning comment carrying the reason, candidates, and hint.

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

The three-layer design is a middle ground:

- Layer 1 handles the easy cases cheaply — a maintained mapping that
  covers the common path. For RHAI, this data already exists.
- Layer 2 handles the ambiguous cases — using architecture context
  (how is an open question) when the mapping returns multiple
  candidates.
- Layer 3 handles the hard cases honestly — the LLM guesses from the
  Jira description, but the guess is a hint, not a published answer.

The user sees: a resolved repo or an unresolved result with a hint.
They do not see the layers. The experience is seamless when the data
supports it, and explicit when it doesn't — the system never silently
guesses wrong.

The operational cost scales with how much of the resolution a workload
wants to be deterministic. A workload that invests in its mapping gets
more Layer 1 hits. A workload that cannot afford that investment still
gets Layer 2 and 3 coverage. The architecture context skill (see
above) allows workloads to provide their own implementation of how
context is fetched, so the operational model is not one-size-fits-all.

## Current implementation (PR #56) vs. this proposal

PR #56 implements repository resolution and is working (628 tests pass,
six real strategies evaluated). It already follows the fullsend agent
pattern: Python scripts handle deterministic work (source loading,
validation, evidence checking, finalization), the LLM handles judgment
(reading docs, selecting repos), and scripts validate the LLM's output.

PR #56 already implements the layers — they exist as instructions in
the guidance prompt that tell the LLM to use the mapping first, then
architecture context, then leave unresolved. The deterministic
validation scripts (`repository_plan.py finalize`,
`repository_context.py`) already check citations, inventory membership,
and URL format after the LLM proposes.

This ADR does not rewrite PR #56. It restructures it based on tech
review and owner feedback.

### What PR #56 already has

- `PLAN_REPOSITORIES` wave before `DECOMPOSE` — resolution happens
  before epic drafting
- Python scripts for deterministic work: source loading, snapshotting,
  content hashing, evidence citation validation, plan finalization
- The layered logic (mapping → arch context → unresolved) as LLM
  instructions in the guidance prompt
- One repo per planned PR, one epic per planned PR
- Unresolved work gets empty URL field + warning comment
- Source snapshots: changed sources invalidate prior results
- Feature flag (`repository_mode`) defaults to off

### What this ADR changes

The layers already exist in PR #56 as LLM instructions. This ADR
separates them into distinct execution steps and moves the deterministic
lookup out of the LLM:

- **Layer 1 becomes a script**: a Python script reads the mapping and
  returns candidates. If one repo, done — the LLM never runs. This is
  the same pattern as `fetch_components.py` and other fullsend scripts
  that handle deterministic work before the LLM.
- **Layers 2 and 3 become separate LLM steps** instead of one combined
  pass, each with different inputs.
- **The resolver becomes a standalone skill** that any agent can call,
  not embedded in the epic-creator wave prompt.

The core machinery — source loading, snapshotting, hashing, plan
finalization — carries forward from PR #56. The refactor is about
execution order and separation, not rewriting the logic.

### Summary of differences

| Aspect | PR #56 | This ADR |
| --- | --- | --- |
| Layer 1 | LLM reads mapping doc | Deterministic code lookup — no LLM for unambiguous components |
| Layer separation | All layers in one LLM pass | Three distinct steps: deterministic mapping → arch context → LLM guess |
| Resolver scope | Embedded in epic-creator's wave prompt | Standalone skill, any agent can call |
| Input | Strategy + all sources | Jira component + issue context |
| Output | Work items with resolutions + planned PRs | Resolved repo or unresolved per component |
| Splitting | LLM splits and resolves together | Resolve first, then split along repo boundaries |
| Epic boundary | Effectively by repo (via planned PRs) | Explicitly by repo (documented as the design) |
| Config | RHAI-specific file paths | Generic declared sources |

### What stays the same

- Source loading, snapshotting, hashing
- One epic per planned PR, one repo per planned PR
- Unresolved handling (warning comment, empty URL)
- Source snapshot invalidation
- Feature flag gating
- The `repository_plan.py finalize` deterministic validator

## Alternatives considered

- **Resolution private to one agent** — rejected: other agents need the
  same resolution. Duplicating the logic creates implementations that
  drift.

- **Assign repositories after drafting** (a post-hoc mapper) — rejected:
  the component-to-repo relationship is many-to-many, so post-hoc
  assignment would require merging and splitting already-drafted epics.
  Resolution must happen before epic creation.

- **Publish LLM guesses as repository selections** — rejected: an
  unverifiable pick published as if verified has asymmetric cost. It
  saves one human action but can send work to the wrong repository with
  no evidence trail. The mapping candidates are surfaced as hints
  instead.

- **Deterministic-only resolution** — not viable: some components map to
  multiple repositories and the mapping alone cannot disambiguate.
  Architecture context read by an LLM is needed for those cases.

- **Single LLM pass for all resolution** (the current PR #56 approach) —
  works but misses the cheap deterministic shortcut. When a component
  maps to exactly one repo, there is no reason to invoke an LLM. The
  layers also stay implicit in the prompt rather than being visible,
  testable execution steps.

- **Split by component, then assign repos** — rejected: the many-to-many
  relationship between Jira components and repos means component-scoped
  epics can span multiple repos or duplicate work in a shared repo.
  Splitting by repo from the start avoids this.

## Consequences

- The resolver is a standalone skill: component in, repo out. Any
  agent can use it without importing epic-creator pipeline concepts.
- The resolution order is visible and explainable: deterministic
  mapping → arch context → LLM guess as hint
- Layer 1 is deterministic — no LLM needed for unambiguous components
- Layer 3 (LLM guess) is only used as a last resort and its output
  is a hint, never a published answer
- Epic boundaries shift from Jira component to repository — each epic
  corresponds to exactly one PR in one repo
- Workloads declare their own sources and mapping; RHAI is
  configuration, not code
- Changed inputs invalidate prior results
- The skill stays in epic-creator for now; moving it is future work
- PR #56's core machinery (source loading, validation, finalization)
  carries forward; the refactor restructures, not rewrites

## Open questions

- What structured information in architecture context can Layer 2 use
  for deterministic matching — ownership metadata, component
  descriptions, dependency declarations, or something else
- Whether the approved repository inventory (PR #56's
  `repository-inventory.yaml`) is a separate source from the mapping,
  and how it relates to Layer 1
- Where workload mappings live long-term (the architecture-context
  repository is a candidate)
- Configuration experience: epic-creator uses files while other
  fullsend agents read environment variables; the two models need
  reconciling

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
