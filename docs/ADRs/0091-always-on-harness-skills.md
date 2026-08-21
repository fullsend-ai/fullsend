---
title: "91. Always-on harness skills via bootstrap context"
status: Accepted
relates_to:
  - agent-architecture
  - codebase-context
  - agent-infrastructure
  - security-threat-model
topics:
  - harness
  - skills
  - bootstrap
  - runtime
  - security
---

# 91. Always-on harness skills via bootstrap context

Date: 2026-08-19

## Status

Accepted

## Context

Putting a skill in the harness `skills:` list only **uploads** it. Claude Code
shows the skill name and a short description. The full text loads only if the
model calls the Skill tool. Default prompts name core skills (like
`retro-analysis`), so those get used. Team-specific add-ons (user / install
skills on a harness overlay) show up in the list too, but opening them is a
model choice — it sometimes works and often does not, because the default
prompt does not know their names.

We do not want to put team-specific skill names in the default prompt. Forking
the agent file to name them creates a derived agent. Saying "use whatever is in
the skill list" also fails: that list mixes harness skills, optional extras, and
built-ins.

Related: [ADR 0024](0024-harness-definitions.md),
[ADR 0038](0038-universal-harness-access.md),
[ADR 0045](0045-forge-portable-harness-schema.md),
[ADR 0088](0088-cel-guarded-overlays.md) (skill lists after overlay merge),
[codebase-context.md](../problems/codebase-context.md).

## Options

### Option A: Tell the prompt to use every listed skill

**Trade-offs:** Easy. Unreliable. Cannot tell "must always apply" from
"use when needed."

### Option B: Fork the agent prompt and name the team skill

**Trade-offs:** Works today. Couples defaults to one team. Derived agent.

### Option C: `apply: always` + load at bootstrap (chosen)

The skill says when it must always apply. Bootstrap pastes those skill bodies
into the agent context for that run.

**Trade-offs:** Reliable. No team-specific names in default prompts. Uses a bit
more context every run. Needs a small bootstrap change.

### Option D: Only enforce with schema / post-script

**Trade-offs:** Good safety net ([ADR 0022](0022-harness-level-output-schema-enforcement.md)).
Does not teach style, templates, or which field to keep short.

## Decision

**We choose Option C.**

Harness `skills:` is still how teams **add** a skill to a run. How it runs
depends on skill frontmatter:

1. **Default (no `apply`, or `apply: on-demand`)** — upload and list it.
   The model opens it with the Skill tool when needed. Same as today.
2. **`apply: always`** — after upload, bootstrap **pastes the `SKILL.md` body**
   (markdown after the frontmatter) onto the **end of the copied agent
   definition** for that run, under the section heading
   `## Always-on skills (harness)`. Do not write into the target repo’s
   `CLAUDE.md` / `AGENTS.md`. The model does not need to call Skill for those
   instructions to be in context. Default prompts do not name the skill.

### What gets copied where

Skills are directories: `SKILL.md` plus optional `scripts/`, `references/`,
`sub-agents/`, and other companions ([ADR 0038](0038-universal-harness-access.md)).
Option C does **not** paste those companions into the agent file.

| Piece | Always-on behavior |
|---|---|
| Whole skill directory | Still **uploaded** into the sandbox skill dir (today’s harness upload). |
| `SKILL.md` body | **Pasted** into the copied agent definition so it is always loaded. |
| `scripts/`, `references/`, `sub-agents/`, other companions | Stay on disk at the uploaded skill path. The pasted body (or a later Skill open) can tell the agent to run or read them by path. |
| Frontmatter | Not pasted as YAML into the agent; used by bootstrap (`apply`, name, …). |

So a large skill stays usable: hard rules in the short always-on body, heavy
helpers left as files. Authors should keep always-on bodies short; put long
procedure and tooling behind scripts/references the body points at.

Paste order follows the harness skill list order after `base:` / forge /
`overlays:` resolution ([ADR 0045](0045-forge-portable-harness-schema.md),
[ADR 0088](0088-cel-guarded-overlays.md)). Use the same integrity-checked files
already resolved for upload. Skills keep their Skill-tool listing unless we
later decide otherwise — authors should write always-on bodies so a second
open is harmless.

Teams add these skills on a thin `base:` harness wrapper (not by forking
`agent:`). `apply` is per skill, not “every skill on the child harness.” Optional
skills such as `customer-research` stay on-demand unless they set
`apply: always` themselves.

Harness skill `SKILL.md` files are already scanned before upload
(`scanRuntimeContent`). Always-on paste must use that same scanned body so
always-loaded content does not skip the injection scan.

Schema / post-script checks stay a backstop, not a replacement for the skill
text.

This is orthogonal to ADR 0024’s open question on which skills are *included*
(harness vs org vs repo discovery, #237). This ADR only decides load mode for
skills already on the harness list.

## Consequences

- Teams can change agent behavior without naming their add-on skills in default
  prompts. Harness-driven paste keeps a **configured default** agent; only
  replacing `agent:` in the harness makes a derived agent
  ([default-vs-custom](../agents/topics/default-vs-custom.md)).
- Always-on skills cost tokens on every run — keep the pasted body short. Prefer
  hard rules in the body; leave long references and scripts on disk.
- Authors must set `apply: always` on purpose. Teach the flag in
  [customizing-with-skills.md](../guides/user/customizing-with-skills.md).
- Implementation must extend `SkillMeta` and Bootstrap; this ADR records the
  contract, not the code yet.
- Living docs (`docs/architecture.md`, related problem-doc open questions) must
  be updated in the same PR that lands this Accepted ADR.
