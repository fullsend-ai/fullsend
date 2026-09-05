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
   `ConfigModelAliases`). `key: ~` tombstones an inherited entry, which
   means "no explicit entry for this persona": the key then resolves the
   way an unmentioned persona does — its frontmatter model, then
   `subagents.default`.

3. Resolution order per persona:
   `repo subagents.<persona>` > `frontmatter model:` (alias-resolved) >
   `repo subagents.default` > the parent's **live** model. `default` sits
   below the frontmatter on purpose: "default" has to mean "when nothing
   else says", or `default: haiku` on `review` would silently pull
   `correctness` and `security` off opus. It is a floor for personas that
   name no model and for anonymous children, never an override.
   Nothing writes a model into the manifest for that last case: the entry
   is left without one so the extension inherits the same live model an
   anonymous child does. Recording the agent definition's model instead
   would put a persona on a different model from an anonymous child
   whenever config or a CLI flag overrode the parent.

4. Each resolved spec is canonicalised (alias resolved, `@suffix` stripped,
   xai normalised, `translatePiModel`) and validated against a trusted
   closed set (the run's model table, provider models, and the parent's
   own spec) before it enters the manifest. The set is built before the
   personas are resolved and is never widened by them, so a persona cannot
   authorise its own model.

5. The orchestrator dispatches by `subagent_type` (persona name) and omits
   `model` — the runner's resolution is authoritative. A `model` argument
   passed anyway is logged and ignored on every persona path, including a
   persona that inherits the parent.

6. An unrecognised non-empty `subagent_type` is rejected, naming the
   registered personas — but only when at least one persona is registered.
   With none registered the existing lenient behaviour stands, because
   skills dispatch descriptive non-`Explore` types today and rejecting
   them before personas exist would break every one of those runs.

7. `subagents.default` also covers children that name no persona. `retro`
   dispatches nothing else, so without this the map would have no effect
   on the agent that most needs it; on `review`, where every persona
   names a model, it changes nothing unless a persona is tombstoned or
   loses its `model:`.

8. A persona's `tools:` are intersected with the parent's set, never
   unioned. A persona naming a tool this runtime cannot serve, or one
   whose declared set is empty, fails Bootstrap: silently dropping the
   unservable names leaves an empty set, and an empty set that falls back
   to the parent's turns a deliberately restricted persona into an
   unrestricted child.

## Consequences

- The orchestrating agent no longer carries model strings; a skill can
  say "dispatch with subagent_type `correctness`" without knowing or
  caring which model that resolves to.
- A repo can tune individual personas to different vendors or generations
  from config alone, reviewed and versioned.
- Cross-vendor dispatch is explicit per persona rather than hidden behind
  an alias rename (which is deprecated for cross-vendor use).
- The pi runtime implements this first; Claude Code follows in a separate
  PR. Consuming the same map on codex (`subagents.default`, OpenAI ids) is
  planned, not implemented here.

### Backward compatibility

A repo with no `subagents:` block behaves exactly as before this ADR:
the same dispatch, the same models, the same tool sets, the same cost.
Persona files under a skill's `sub-agents/` are discovered and
validated, but a file that fails validation is warned about and left out
of the manifest — never fatal on its own. It becomes fatal only when the
repo's own `subagents` config names it, or sets `default` and the persona
would have received it, because then the repo asked for something the
run cannot honour. The names Claude Code's pinned CLI lists as built-in
agent types (`claude`, `Explore`, `general-purpose`, `Plan`,
`statusline-setup`, 2.1.258) keep the lenient anonymous path even once
personas are registered, since orchestrators emit them today; only other
unrecognised names are rejected. The only new observables without config
are the persona table Bootstrap prints and those warnings.

The Claude Code runtime — the production default — is unchanged by this
ADR: `internal/runtime/claude.go` is untouched, persona files are not
discovered or registered there, and the `Agent` tool resolves
`subagent_type` and `model` exactly as before. Persona routing on Claude
Code arrives in the second PR, behind the same `subagents:` opt-in, so a
repo that never sets it keeps today's behaviour on both runtimes.

### Deferred, and what depends on it

- **Layer provenance.** An unmatched `subagents` key is a hard error
  wherever it came from. The design calls for keys inherited from a preset
  layer to warn instead, so a shared preset can carry entries for a fuller
  harness than a given repo runs, but the merge does not yet record which
  layer a key came from. Until it does, a shared-config preset (the
  inherited-layer work still in review) must not ship a `subagents` block:
  a key naming a persona the consuming repo's skills do not ship would
  fail that repo's runs.
- **Discovery lives in `internal/runtime`,** so `fullsend lock` neither
  runs the same validation nor records the persona set, and a fleet-side
  persona rename produces no lock diff. Moving it to a package both
  Bootstrap and `lock` call is a follow-up.
- **Per-child Bash allowlists.** A persona's `Bash(...)` allowlist would be
  recorded in the manifest but the child's hook adapter still reads the
  parent's list, so it is refused at validation rather than silently
  ignored. Honouring it means the adapter selecting the persona's list,
  which touches the hook contract.
- **Namespaced dispatch.** Registering personas as a plugin on Claude Code
  makes the dispatch form `<plugin>:<persona>`. Because an unrecognised
  type is now rejected, pi must strip that prefix *before* the skill
  starts emitting it, or every persona dispatch on pi fails at the first
  call. That ordering constraint is a precondition for the skill change,
  alongside the two Claude Code facts that already gate it.
