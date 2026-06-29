# Retro Agent

<img src="icons/retro.png" alt="Retro agent icon" width="80">

Performs retrospectives on agent workflows — analyzes what happened, identifies improvement opportunities, and proposes changes as structured GitHub issues.

## How the agent works

The retro agent is triggered after a workflow completes (PR merged or closed), or on-demand via `/fs-retro`. It reconstructs the full workflow graph — [triage](triage.md), [code](code.md), [review](review.md), [fix](fix.md), and human interactions — by fetching issue and PR timelines, agent run logs, and review threads.

1. **Pre-script** gathers metadata about the originating PR or issue.
2. **Sandbox** — the agent reads the full workflow history, identifies patterns (wasted cycles, missed context, repeated failures), and writes structured proposals. It uses the retro-analysis and finding-agent-runs skills. The agent cannot write files or edit code in the target repo.
3. **Validation loop** — output is checked against a schema, with up to 2 retries.
4. **Post-script** creates GitHub issues from the agent's proposals.

When triggered via `/fs-retro`, the human's comment is passed to the agent as high-signal direction about what to focus on.

## How it helps

- Every workflow gets a post-mortem, not just the ones that failed badly enough for someone to notice.
- Improvement proposals are filed as issues with context, so they enter the normal triage/prioritize pipeline.
- Patterns across multiple retros surface systemic issues (e.g., a skill that consistently underperforms).

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-retro` | PR or issue comment | Triggers a retrospective analysis |

Requires write-level repository permission (admin, maintain, or write).

The `/fs-retro` command accepts optional free-text instructions after the
command. The text is passed to the agent as high-signal direction about what
to focus on:

- `/fs-retro` — general retrospective on the workflow
- `/fs-retro figure out why the review agent approved this and make sure it never happens again`
- `/fs-retro the code agent spent 30 minutes on a 2-line fix, what went wrong`

The retro agent also runs automatically when a PR is closed (merged or not).

## Control labels

| Label | Meaning |
|-------|---------|
| `ready-for-triage` | Applied by the post-script to proposal issues so they enter the [triage](triage.md) pipeline automatically. |

## Configuration and extension

See [Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).

### Variables

None.

## Source

[`internal/scaffold/fullsend-repo/harness/retro.yaml`](../../internal/scaffold/fullsend-repo/harness/retro.yaml)
