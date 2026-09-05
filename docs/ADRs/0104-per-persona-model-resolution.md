---
title: "104. Sub-agent model resolution is a runner concern"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - config
  - runtime
  - sub-agents
---

# 104. Sub-agent model resolution is a runner concern

Date: 2026-09-05

## Status

Accepted

## Context

Sub-agent skills (pr-review, retro-analysis) dispatch children by passing a
`model` argument to the `Agent` tool.  The orchestrating agent therefore
carries a model string in its prompt, which it passes through unchanged.

This creates several problems:

- The model string is resolved by the runtime (pi, Claude Code) at dispatch
  time, and a wrong or unavailable id fails at the API, not at config time.
- A repo that wants cheaper or cross-vendor sub-agents — e.g. Gemini for
  style checks, Grok for adversarial challenge — has no supported lever:
  `agents[].model` tunes the parent, and changing one persona's model means
  forking the entire skill.
- On pi the alias table is a package-level map, and on Claude Code it is
  resolved by the CLI's internal Vertex alias table; neither path lets the
  repo control the generation or vendor per persona without editing the
  skill itself.

## Decision

**Personas name identities; repos map identities to models; the runner
resolves models at Bootstrap.**

1. Skills declare personas as `sub-agents/*.md` files under a skill root.
   Each file has frontmatter `name:` (required, must equal the file
   basename), optional `model:` (an alias or id, the persona's default)
   and optional `tools:` (restricting the child's tool set).

2. Repos configure per-persona models with `agents[].subagents` in
   `.fullsend/config.yaml` — a `map[string]*string` on the existing
   `AgentEntry` struct, with per-key layered merge (like
   `ConfigModelAliases`). `key: ~` tombstones an inherited entry.

3. Resolution order per persona:
   `repo subagents.<persona>` > `repo subagents.default` >
   `frontmatter model:` (alias-resolved) > parent's model.

4. Each resolved spec is canonicalised (alias resolved, `@suffix` stripped,
   xai normalised, `translatePiModel`) and validated against a trusted
   closed set (the run's model table, provider models, and the parent's
   own spec) before it enters the manifest.

5. The orchestrator dispatches by `subagent_type` (persona name) and omits
   `model` — the runner's resolution is authoritative.

## Consequences

- The orchestrating agent no longer carries model strings; a skill can
  say "dispatch with subagent_type `correctness`" without knowing or
  caring which model that resolves to.
- A repo can tune individual personas to different vendors or generations
  from config alone, reviewed and versioned.
- Cross-vendor dispatch is explicit per persona rather than hidden behind
  an alias rename (which is deprecated for cross-vendor use).
- The pi runtime implements this first; Claude Code follows in a separate
  PR. codex consumes `subagents.default` only (OpenAI ids).
