# Customizing Agents with Skills

Fullsend agents use [agent skills](https://agentskills.io/) — self-contained
markdown documents that teach an agent how to perform a specific task. Each
default agent ships with built-in skills, and you can extend or replace them by
committing your own skills to your repository.

For general project-wide instructions (code style, test conventions,
architecture rules), see [Customizing with AGENTS.md](customizing-with-agents-md.md).
This guide covers skills specifically.

## What is a skill?

A skill is a directory containing a `SKILL.md` file with YAML frontmatter and
structured instructions. The agent loads the skill by name and follows its
instructions during execution.

```
.agents/skills/my-skill/
  SKILL.md           # skill definition (required)
  scripts/           # supporting scripts (optional)
    helper-script.sh
  references/        # reference data (optional)
    data.json
```

For portability across agent runtimes, store skills in `.agents/skills/` and
symlink `.claude/skills` to it:

```bash
ln -s ../.agents/skills .claude/skills
```

This way, skills are discoverable by fullsend's agent runtime and by any local
agent tooling developers use when working on the repo directly.

The `SKILL.md` has frontmatter declaring the skill's name and description,
followed by step-by-step instructions:

```markdown
---
name: my-skill
description: >-
  One-line summary of what this skill does.
---

# My Skill

Instructions the agent follows when this skill is invoked.

## Step 1: Gather context

...

## Step 2: Produce output

...
```

Skills can reference companion scripts and data files in the same directory,
giving agents the ability to dynamically gather information at runtime.

## Adding skills to your repository

Place skills in `.agents/skills/` in your target repository and symlink
`.claude/skills` to `.agents/skills`. All agents operating on your repo will
discover them automatically:

```
your-repo/
  .agents/skills/
    customer-research/
      SKILL.md
      scripts/
        query-salesforce.sh
    deployment-checks/
      SKILL.md
  .claude/skills -> ../.agents/skills
```

## Extending agents with repo skills

Skills you add to your repository are available to all fullsend agents
alongside the built-in skills. This is the primary way to give agents
domain-specific capabilities — linting rules, deployment checklists,
architecture constraints — without modifying any fullsend configuration.

Repo skills **extend** the agent's skill set. They do not replace built-in
skills. If a repo skill has the same name as a built-in skill, the built-in
version takes precedence and the repo version is silently ignored. Use a
unique name to ensure your skill is discoverable.

### Skill precedence

Fullsend uploads built-in skills to the agent's personal-level config
directory (`CLAUDE_CONFIG_DIR/skills/`). Repo skills live in the project-level
`.claude/skills/` directory. Claude Code resolves name collisions using
precedence:

```
Personal (CLAUDE_CONFIG_DIR/skills/)  >  Project (.claude/skills/)
         fullsend built-in skills            repo skills
```

A repo skill with a novel name (no collision) is always available. A repo
skill with a name matching a built-in skill is shadowed — the agent never
sees it.

### Extension points

Some agents recognize skill names that do not ship with fullsend. Providing
these unlocks additional capabilities. See each agent's documentation for the
skills it supports — for example, the
[prioritize agent](../../agents/prioritize.md) uses a `customer-research` skill
when available.

## Overriding built-in skills

To intentionally **replace** a built-in skill with your own version, use the
`customized/` overlay ([ADR 0035](../../ADRs/0035-layered-content-resolution.md)).
This replaces the skill at the config layer before the agent starts — the
built-in version is never uploaded to the sandbox.

Create the override in your `.fullsend` config repo (per-org mode) or in
`.fullsend/customized/` in the target repo (per-repo mode). The directory
name must match the built-in skill name exactly:

```
customized/skills/code-review/SKILL.md    # replaces the built-in code-review
```

This is an org-sanctioned operation — it goes through the content overlay
engine, not through project-level skill discovery.

### Built-in skills

These skills ship with fullsend and can be overridden via `customized/skills/`:

| Agent | Skill | Purpose |
|-------|-------|---------|
| [Triage](../../agents/triage.md) | `issue-labels` | Label discovery and application during triage |
| [Code](../../agents/code.md) | `code-implementation` | Step-by-step implementation procedure |
| [Review](../../agents/review.md) | `code-review`, `pr-review`, `docs-review`, `issue-labels` | Review evaluation across dimensions |
| [Fix](../../agents/fix.md) | `fix-review` | Review feedback interpretation and fix strategy |
| [Prioritize](../../agents/prioritize.md) | `customer-research` | Customer data gathering for RICE scoring (extension point) |
| [Retro](../../agents/retro.md) | `retro-analysis`, `finding-agent-runs` | Workflow analysis and proposal generation |

## When to use skills vs. AGENTS.md

Use **skills** when you need to change how a specific agent performs a specific
task — especially when the customization involves domain knowledge, helper
scripts, or external data sources that only one agent needs.

Use **[AGENTS.md](customizing-with-agents-md.md)** for broad instructions that
apply to all agents and human contributors alike.

## What not to do

- **Don't duplicate AGENTS.md content in skills.** If an instruction applies
  to all agents, put it in `AGENTS.md`. Skills are for agent-specific behavior.
