---
name: localizing-fixes
description: >-
  Use when you have a root cause hypothesis and need to determine where the
  fix should land — the target repo, the org's .fullsend config, or upstream
  in fullsend-ai/fullsend.
---

# Localizing Fixes

Determine which layer of the system should change to address an agent behavior
problem. The key question: would every fullsend adopter want this fix, or
just this org or repo? Scope the fix as broadly as the answer allows.

## The three layers

| Layer | Scope | Example fixes |
|-------|-------|---------------|
| **Downstream** (target repo) | This repo only | Add `CLAUDE.md` conventions, improve test fixtures, add linter config |
| **Org-level** (`.fullsend` repo) | All org repos (or per-repo override) | Modify skills, agent config, guardrails, improvement goals |
| **Upstream** (`fullsend-ai/fullsend`) | All adopting orgs | Fix default agent definitions, skills, dispatch workflows |

## Decision process

**Context vs behavior?**
- Agent didn't know something → **downstream** (or org-level if all repos need it)
- Agent knew the facts but acted wrong → **org-level** or **upstream**

**How broad?**
- One repo → downstream or per-repo override in `.fullsend/repos/`
- Multiple org repos → org-level
- Any fullsend adopter → upstream

**Would every adopter want this?** If yes, recommend upstream. If only this
org, org-level. If only this repo, downstream. When uncertain, start narrow
and note promotion is possible.

## Output

State: **layer**, **specific location** (file path), **rationale**, **blast
radius**, and **who reviews** (repo CODEOWNERS, org `.fullsend` CODEOWNERS,
or upstream maintainers).

## Constraints

- Don't recommend upstream fixes for org-specific problems or when
  the fix would leak local conventions into the framework.
- Don't recommend org-level fixes for genuinely single-repo problems.
- When uncertain, start narrow and note promotion is possible as
  evidence accumulates.
- `.fullsend` is human-governed (ADR 0003) — file an issue recommending
  the change; don't modify it directly.
