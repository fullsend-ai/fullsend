# Codebase Context

How do agents acquire sufficient understanding of a codebase to operate effectively, and how should the org structure information to enable that?

## The context problem

Agents are stateless — each session starts with zero codebase knowledge. The only guaranteed context is the agent instruction file (CLAUDE.md/AGENTS.md). Research from ETH Zürich (2026) found that auto-generated or verbose context files hurt agent performance while increasing costs by 20%+. Human-written, minimal files with specific non-obvious instructions showed consistent improvements.

The implication: context files are high-leverage but dangerous. Every bad line multiplies across every task. The question isn't whether to have them, but what goes in them and what doesn't.

## A layered context model

Context comes from multiple levels. Each layer has different properties:

**Layer 1 — the code itself.** Type signatures, module structure, test names, inline comments. Agents discover this independently. Do NOT duplicate it in context files (the research finding: duplication hurts).

**Layer 2 — per-repo context (CLAUDE.md + BOOKMARKS.md).** Repo-specific non-obvious instructions: build quirks, testing procedures, design philosophy. Must be human-written, minimal, and focused on what agents cannot discover from the code. BOOKMARKS.md provides progressive disclosure — a curated index of references the agent loads on demand, keeping CLAUDE.md small.

**Layer 3 — org-level context (the architecture repo).** Cross-repo architectural decisions, service boundaries, API contracts, invariants. No single repo contains this information. An agent working on build-service needs to know how it relates to integration-service, what ADRs constrain its design, what API conventions apply. This context lives in the architecture repo and is consumed via structured metadata.

**Layer 4 — external references.** Upstream docs, standards (like Kubernetes API conventions), research. Managed via BOOKMARKS.md entries.

## Per-repo context best practices

Based on the ETH research and practical experience:

- Keep CLAUDE.md under 60 lines, human-written, never auto-generated
- Only include what agents cannot discover from the codebase itself
- Use linters and CI for style enforcement, not prose instructions in CLAUDE.md
- Use [BOOKMARKS.md](https://github.com/ambient-code/workflows/blob/main/workflows/claude-md-generator/BEST_PRACTICES_CLAUDE.md) for progressive disclosure — references useful for some tasks but not all (internal docs like a release process, external standards like API conventions, design decisions). Each entry should include a description of what's in the target so agents can decide whether to load it. Keep it curated; if everything is bookmarked, the index loses its value.
- Prefer file:line pointers to pasted code snippets (snippets go stale)
- Don't list repository structure (agents can read the filesystem; listings drift)

## The architecture repo as org-level context

The konflux-ci/architecture repo already contains per-service docs and 60+ ADRs. To serve as effective org-level context for agents:

**1. Structured frontmatter on service docs.** Each service doc should have machine-parseable metadata: scope, related services, related ADRs, key CRDs. Use YAML lists, not comma-separated strings (agents shouldn't need to string-parse). Enforce the schema in CI so it doesn't drift.

**2. Structured frontmatter on ADRs.** Add status, applies_to (list of repos/services), superseded_by, and topics as frontmatter. This makes ADRs grep-searchable without a separate index file. A quick-reference summary is an acceptable generated artifact but should not be the source of truth — the ADR frontmatter is.

**3. Minimal CLAUDE.md in the architecture repo.** Should contain only: what this repo is (1 sentence), how to find things (grep frontmatter), and core architectural constraints that are genuinely cross-cutting (e.g., API model is Kubernetes CRDs, all operations async, Tekton-based execution). No service listings (discoverable from frontmatter). No structure descriptions (discoverable from the filesystem).

**4. API standards as documented context.** The emerging API Review SIG (CODEOWNERS on API files across repos) needs a documented rubric — starting from Kubernetes API conventions. This document lives in the architecture repo and is referenced from per-repo BOOKMARKS.md files. Agents reviewing CRD changes consult it the same way they consult ADRs.

The architecture repo shouldn't try to be a comprehensive reference that agents load wholesale. It should be a structured, grep-friendly source that agents query selectively. Verbose context hurts; structured metadata helps.

## Cross-repo context challenges

An agent modifying an API in one repo needs to know who consumes it. The architecture repo's service frontmatter (related_services) provides discovery, but the consuming repo's tests are what actually catch breakage. These are complementary — structured metadata tells the agent *where* to look, integration tests tell it *whether* things broke.

The emerging API Review SIG (CODEOWNERS on API files across repos) provides human enforcement for cross-repo concerns that agents can't fully evaluate from a single repo. Multi-repo changes — a CRD schema change affecting build-service and integration-service — need context from both repos simultaneously, which pushes the limits of single-repo agent operation.

## Relationship to other problem areas

- **Architectural invariants** — structured frontmatter enables the enforcement mechanisms discussed there (Option B)
- **Agent drift** — poor context leads to drift; stale context is worse than no context
- **Code review** — review agents need cross-repo context for API changes and architectural compliance
- **Repo readiness** — context file quality is a readiness criterion
- **Agent-compatible code** — type safety and context are complementary: types provide structural context, CLAUDE.md provides intent context
- **Governance** — who maintains org-level context files is a governance concern

## Open questions

- How do we validate that CLAUDE.md files remain under 60 lines and don't accumulate cruft? Should there be CI enforcement or periodic human review?
- What's the right balance between CLAUDE.md (always-loaded) and BOOKMARKS.md (on-demand)? How do agents know to check BOOKMARKS.md for a given task?
- How do we handle context for repos that aren't pure services (e.g., operator-toolkit, qe-tools, ci-helper-app)? The service-oriented frontmatter may not fit.
- Can agents contribute to BOOKMARKS.md when they discover useful references, or should that always be human-curated?
- What's the context loading strategy for multi-repo changes? Does the agent load context from all affected repos at once, or does it switch context as it moves between repos?
