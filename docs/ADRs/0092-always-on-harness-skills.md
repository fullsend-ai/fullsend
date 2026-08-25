---
title: "92. Always-on harness skills via soft Skill directive"
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

# 92. Always-on harness skills via soft Skill directive

Date: 2026-08-19

## Status

Accepted

## Context

Putting a skill in the harness `skills:` list only **uploads** it. Claude Code
shows the skill name and a short description. The full text loads only if the
model calls the Skill tool. Default prompts name core skills (like
`retro-analysis`), so those get used. Team-specific add-ons show up in the list
too, but opening them is a model choice — listing alone is not enough.

We do not want team-specific skill names in default prompts, and we do not want
to paste `SKILL.md` bodies into the agent file (duplicates listing, skips the
Skill tool contract, and bloated always-on context). Related:
[ADR 0024](0024-harness-definitions.md),
[ADR 0038](0038-universal-harness-access.md),
[ADR 0045](0045-forge-portable-harness-schema.md),
[ADR 0088](0088-cel-guarded-overlays.md),
[codebase-context.md](../problems/codebase-context.md),
[#6380](https://github.com/fullsend-ai/fullsend/issues/6380).

## Options

### Option A: Soft-inject “always load every listed skill”

**Trade-offs:** Easy. Unreliable. Cannot tell “must always apply” from “use when
needed.” Mixes harness skills, optional extras, and built-ins.

### Option B: `metadata.apply: always` + name in prompt; open via Skill (chosen)

Pre-run bootstrap fills a placeholder with **names only** of skills that opt in.
The model must open each with the Skill tool before relying on them.

**Trade-offs:** Reliable in playground proof. No body paste. Needs Skill in
agent `tools:`. Soft (model can still skip).

### Option C: Paste `SKILL.md` body into the agent definition

**Trade-offs:** Guarantees text in context. Skips Skill tool. Inflates tokens.
Rejected after review and proof preference for Skill opens.

### Option D: Fork the agent prompt and hard-name the team skill

**Trade-offs:** Works today. Couples defaults to one team. Derived agent.

### Option E: Only enforce with schema / post-script

**Trade-offs:** Good safety net ([ADR 0022](0022-harness-level-output-schema-enforcement.md)).
Does not teach style or procedure.

## Decision

**We choose Option B.**

Harness `skills:` still **adds** a skill to a run. Load mode comes from skill
frontmatter (`metadata.apply`, with legacy top-level `apply` accepted):

1. **Default (omit, or `on-demand`)** — upload and list it. The model opens it
   with Skill when needed.
2. **`always`** — after upload, bootstrap replaces
   `__FULLSEND_ALWAYS_ON_SKILLS__` on the **copied** agent definition with a
   short directive that names those skills and tells the model to open each
   with the Skill tool before relying on them. Do not paste `SKILL.md` bodies.
   Do not write into the target repo’s `CLAUDE.md` / `AGENTS.md`.

If the placeholder is absent, bootstrap appends the same directive. Names are
allowlisted against uploaded harness skills. Companions
(`scripts/`, `references/`, …) stay on disk after upload.

Prerequisite: the agent’s `tools:` must include Skill (spaced from other tools,
e.g. `Bash(...), Skill`). Without Skill, always-on cannot open.

Schema / post-script checks stay a backstop, not a replacement for skill text.
This is orthogonal to ADR 0024’s open question on which skills are *included*
(#237).

## Evidence

Nine playground triage runs (3 setups × 3): harness listing alone never opened
the team skill (0/3); `metadata.apply: always` opened it and changed behavior
every time (3/3). Report (kept in the public playground, not this repo):
[skill activation matrix](https://github.com/fullsend-playground/python-app/blob/main/docs/adr-0091-skill-activation-matrix.html).

## Consequences

- Teams opt skills into always-on with `metadata.apply: always` on a thin
  harness wrapper — not by forking `agent:`
  ([default-vs-custom](../agents/topics/default-vs-custom.md)).
- Always-on costs little context (names + directive); full bodies load via Skill.
- Agent files that clamp `tools:` without Skill cannot use always-on until Skill
  is restored.
- Teach the flag in
  [customizing-with-skills.md](../guides/user/customizing-with-skills.md).
- Living docs (`docs/architecture.md`, related open questions) update in this PR.
