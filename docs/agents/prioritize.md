# Prioritize Agent

<img src="icons/prioritize.png" alt="Prioritize agent icon" width="80">

Scores a GitHub issue using the RICE framework (Reach, Impact, Confidence, Effort) and produces structured scores with reasoning for project board ranking.

## How the agent works

Triggered on a schedule (the prioritize scheduler polls the project board for unscored or stale issues) or on-demand via `/fs-prioritize`.

The prioritize agent fetches the issue and all its context, then evaluates it across the four RICE dimensions. It can invoke customer-research skills to gather additional signal about reach and impact. The output is a structured JSON result with per-dimension scores and written reasoning, which the post-script uses to update the project board.

## How it helps

- Issues are ranked consistently using the same framework, reducing bias from whoever happens to see them first.
- Scoring reasoning is transparent and auditable — anyone can read why an issue was ranked the way it was.
- Project boards stay sorted by value, so humans can focus on the highest-impact work first.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-prioritize` | Issue comment | Runs RICE scoring on the issue |

The `/fs-prioritize` command does not accept arguments. It scores the issue
using the current content, comments, and any available `customer-research`
skill data.

## Control labels

The prioritize agent does not apply or consume control labels. It reads the
issue content and produces a structured score — the post-script updates the
project board directly.

## Configuration and extension

### Skill: `customer-research`

The prioritize agent looks for a `customer-research` skill and, when available,
uses it to inform Reach and Impact scores. To provide it, create a skill directory
in your target repository at `.agents/skills/customer-research/` with a `SKILL.md` and
any helper scripts organized in a `scripts/` subdirectory. Then symlink `.claude/skills`
to `.agents/skills` so the skill is discoverable by both Fullsend and any local
agent tooling:

```
your-repo/
  .agents/skills/customer-research/
    SKILL.md
    scripts/
  .claude/skills -> ../.agents/skills
```

This gives the prioritize agent concrete data to distinguish between "one user
wants this" (Reach 0.25) and "three strategic accounts have filed support cases
about it" (Reach 2.0), instead of guessing from the issue text alone.

## Source

[`internal/scaffold/fullsend-repo/harness/prioritize.yaml`](https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/fullsend-repo/harness/prioritize.yaml)
