# Explore Agent

<img src="icons/explore.png" alt="Explore agent icon" width="80">

Investigates a GitHub or Jira issue to gather technical landscape, related work, architectural constraints, and competitive context before refinement begins.

## How the agent works

The explore agent is the first stage of the refinement pipeline, triggered when `/fs-refine` is invoked on an issue. It fetches the issue content — title, body, labels, comments, parent context — and then systematically researches the target codebase, related GitHub issues and PRs, Jira linked work, and the public web.

The agent runs in a read-only sandbox. It cannot modify issues, push code, create PRs, or interact with external services beyond read access. Its only output is a structured JSON exploration context consumed by the downstream [refine agent](refine.md).

The explore agent works with both GitHub Issues and Jira issues. When the issue source is Jira, the pre-script fetches issue data via the Jira REST API and normalizes it to `issue-context.json` before the agent starts.

## How it helps

- Gathers technical context that would take a human hours to assemble — project structure, dependencies, deployment targets, API surface, test infrastructure.
- Identifies related work (prior issues, PRs, abandoned attempts) so the refine agent doesn't propose work that already exists or was previously rejected.
- Surfaces architectural constraints and competitive approaches that inform decomposition decisions.
- Assesses confidence across five dimensions, flagging specific gaps so the refine agent knows where it lacks grounding.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-refine` | Issue comment | Triggers the refinement pipeline starting with explore |

The `/fs-refine` command kicks off the full pipeline. Explore runs first, then automatically chains to [refine](refine.md). There is no separate command to run explore in isolation.

For Jira issues, the pipeline can also be triggered by:
- A Jira Automation webhook sending a `repository_dispatch` event
- The Jira comment poller (cron job, every 5 minutes) detecting `/fs-refine` comments

## Pipeline flow

```
/fs-refine → explore → refine → critique → [approved | revise | needs_input]
```

Explore produces `exploration_context.json` as a GitHub Actions artifact. The post-script chains to the refine workflow via `workflow_dispatch`, passing the run ID for artifact correlation.

## Exploration dimensions

The agent assesses confidence (0–100) across these dimensions:

| Dimension | What it measures |
|-----------|-----------------|
| `technical_landscape` | Codebase, APIs, and patterns understood |
| `related_work` | Prior issues, PRs, and discussions found |
| `architectural_constraints` | Deployment targets, dependencies, and contracts |
| `competitive_context` | How alternatives handle this problem |
| `requirements_clarity` | Whether the work item is clear enough to decompose |

Dimensions scoring below 60 are flagged with specific gap descriptions in the output.

## Source

[`internal/scaffold/fullsend-repo/harness/explore.yaml`](../../internal/scaffold/fullsend-repo/harness/explore.yaml)
