---
title: "106. Skill loading policy: extend, don't override"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - skills
  - harness
  - security
  - configuration
---

# 106. Skill loading policy: extend, don't override

Date: 2026-09-08

## Status

Accepted

## Context

[ADR 0024](0024-harness-definitions.md) introduced a `skills:` list in the
harness YAML, but left the interaction between harness-declared skills,
org-level skills, and repo-level skills as an open question
([#237](https://github.com/fullsend-ai/fullsend/issues/237)). Two approaches
were discussed: explicit list with opt-in repo skills (conservative, secure by
default) vs. all skills available with scanning (prioritizing agent
effectiveness).

The codebase has settled on a hybrid. Built-in skills are uploaded to the
agent's personal-level config directory (`CLAUDE_CONFIG_DIR/skills/`) during
bootstrap. Repo skills in `.claude/skills/` (or `.agents/skills/` symlinked
to `.claude/skills/`) are discovered by the agent runtime from the working
directory at project level. Claude Code's precedence rule — personal > project
— means built-in skills win on name collisions, and repo skills with novel
names extend the agent's capabilities. Two scan paths guard skill content
before the agent starts: `scanRuntimeContent` runs `InputPipeline` on
harness-declared (built-in) skills, and `scanRepoContextFiles` runs the
same `InputPipeline` on repo-level context files including SKILL.md
(see [security-threat-model.md](../problems/security-threat-model.md)).

This ADR formalizes the implemented policy.

## Options

**Approach A — Explicit list + org skills, opt-in repo skills.** Harness
`skills:` is always loaded. Org skills included. Repo skills blocked by
default; opt in via `allow_repo_skills: true`. Conservative: secure by
default, opt-in to risk.

**Approach B — All skills with scanning.** All tiers available by default.
Repo skills scanned for injection before loading. Prioritizes effectiveness:
agents get domain knowledge without configuration.

## Decision

Adopt **Approach B with Approach A's precedence guard** — the "extend, don't
override" model:

1. **Built-in skills** (from `fullsend-ai/agents`) are uploaded to the
   personal-level config directory during bootstrap. They define the agent's
   core capabilities.

2. **Repo skills** (`.claude/skills/` in the target repo) are available by
   default. They extend the agent with domain-specific knowledge (repo
   conventions, deployment checklists, architecture constraints) without
   requiring any fullsend configuration change.

   Org-level skills (`.fullsend/skills/`) are not yet implemented. Issue #237
   raised them as a distinct tier, but the codebase currently has no loading
   path for org-level skills. When org-level skills are added, their
   precedence tier (e.g., personal > org > project) and scanning posture
   will need a follow-up decision.

3. **Precedence prevents override.** Personal > project precedence means a
   repo skill whose directory name matches a built-in skill is
   shadowed — the built-in version wins. The runner logs a warning naming
   the shadowed skill. This prevents untrusted repo content from replacing
   trusted agent behavior.

4. **Injection scanning guards repo skills.** Two scan paths run before the
   agent starts: `scanRuntimeContent` runs `InputPipeline` on harness-declared
   (built-in) skills, and `scanRepoContextFiles` runs the same `InputPipeline`
   on repo-level context files (CLAUDE.md, AGENTS.md, SKILL.md, etc.). Critical
   findings abort the agent launch in `fail_mode: closed` (default) or warn in
   `fail_mode: open`. This is the same pipeline applied to agent definitions
   and plugins.

5. **Intentional override uses `base:` composition.** To replace a built-in
   skill, the org registers the agent in `config.yaml` with a harness that
   uses `base:` to inherit from the upstream harness and includes the
   replacement skill in the `skills:` list
   ([ADR 0045](0045-forge-portable-harness-schema.md),
   [ADR 0064](0064-deprecate-customized-directory-overlay.md)). This is an
   org-sanctioned operation subject to CODEOWNERS review.

No `allow_repo_skills` / `disable_repo_skills` toggle exists. A per-harness
toggle was rejected because it would create two code paths for the injection
scanner and make the security posture configuration-dependent rather than
invariant. Repo skills are always available if they pass scanning. The
harness `skills:` list declares built-in skills to provision but does not
act as a whitelist that blocks project-level skill discovery.

## Consequences

- Repos can extend agent capabilities by adding skills with unique names to
  `.agents/skills/` (symlinked to `.claude/skills/`). No fullsend
  configuration change is required.
- Repos cannot override built-in skills via the project-level skill
  directory. Name collisions are resolved in favor of built-ins, and the
  runner warns about shadowed skills.
- Intentional override requires org-level action: `base:` harness composition
  with config-based agent registration
  ([ADR 0058](0058-agent-registration.md)). This keeps the override path
  auditable and CODEOWNERS-gated.
- Injection scanning applies uniformly to all skill sources. The security
  posture does not weaken when repo skills are present.
- The resolved open question in [ADR 0024](0024-harness-definitions.md) is
  annotated as decided.
