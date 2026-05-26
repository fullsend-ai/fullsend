# Retro Agent Design

**Issue:** [#131 — Story 8: Feedback Loop into Harness](https://github.com/fullsend-ai/fullsend/issues/131)
**Date:** 2026-05-04

## Overview

The retro agent performs retrospectives on agent workflows — whether they ended in a merged PR, a rejected PR, or are still in progress. It reconstructs the timeline of agent and human interactions, evaluates what happened against configurable optimization goals, and proposes improvements. It files its proposals as GitHub issues in the appropriate repo and comments back on the originating PR or issue with a summary.

The retro agent is an analyst, not a fixer. It produces well-contextualized proposals with validation criteria, then hands off to existing agent and human workflows to implement and verify the changes.

## Triggers

### Automatic: PR closed

When a PR is closed (merged or rejected), the dispatch shim (`fullsend.yaml`) triggers a `dispatch-retro` job via the `pull_request_target` event with `closed` action. This dispatches to the `.fullsend` repo's `retro.yml` workflow, passing the PR URL as input.

### On-demand: `/fs-retro` command

A human posts `/fs-retro` as a comment on an issue or PR, optionally with additional context explaining what they think is wrong and why. The shim handles this as an `issue_comment` event and dispatches to `retro.yml`, passing the originating URL and the full comment text.

The human's comment is high-signal context. For example:

> `/fs-retro` this triage output is wrong — I would never prioritize a cosmetic label change over a broken test. Go figure out why and propose a fix.

The retro agent treats this as a starting point for its investigation.

## Harness Configuration

The retro agent follows the standard harness structure (ADR 0024).

### `.fullsend/harness/retro.yaml`

| Field | Value |
|---|---|
| `agent` | `agents/retro.md` |
| `model` | Configurable (same options as other agents) |
| `policy` | Read-only sandbox with network access for GitHub API |
| `skills` | `finding-agent-runs`, future improvement pattern library |
| `pre_script` | Assembles trigger context into `agent_input` |
| `post_script` | Files issues and posts summary comment |
| `agent_input` | Directory with trigger context files |
| `timeout_minutes` | Generous (retro explores deeply with subagents) |

### Sandbox policy

Read-only. The retro agent needs:

- Network access for `gh` CLI calls (GitHub API)
- No filesystem write capability
- No push/commit capability

The sandbox and post-script share a single minted token with `issues:write` and `pull_requests:read`. The sandbox uses it for read operations (`gh run view`, `gh pr view`); the post-script uses it to file issues and post comments.

## Input Assembly

### Pre-script (deterministic, minimal)

The pre-script collects only the trigger context and writes it to the `agent_input` directory:

- **Originating URL:** The PR or issue that triggered the retro
- **Comment text:** The `/fs-retro` comment, if on-demand (empty for automatic triggers)
- **Repo metadata:** Org name, repo name, `.fullsend` repo location

The pre-script does not attempt to gather logs, traces, or workflow history. That is the agent's job.

### Agent runtime (LLM-driven exploration)

The retro agent explores the full context at runtime, using the `finding-agent-runs` skill and `gh` CLI to:

1. Trace from the originating PR/issue to all related shim runs and dispatch runs (triage, code, review)
2. Download and read JSONL reasoning traces from workflow artifacts
3. Read PR review comments, verdicts, and human interventions
4. Read CI check results and logs
5. Read the harness configs that were used for each agent in the workflow
6. Search for patterns across other PRs in the same repo
7. Check for prior retro proposals to avoid duplicates and build on existing findings

The agent dispatches subagents liberally for read-heavy operations. Each investigation thread (e.g., "read the triage agent's JSONL trace and summarize its decisions", "find all review comments and categorize them", "search the last 10 retro proposals for this repo") runs as a subagent. The main context window stays reserved for synthesis and hypothesis formation.

## Agent Behavior

### System prompt (`agents/retro.md`)

The system prompt defines:

**Role:** Retrospective analyst. Examines agent workflows and proposes improvements.

**Optimization goals:** Defined directly in the system prompt for now. Examples:
- Minimize rework rate without increasing token cost
- Decrease token cost without degrading review quality
- Reduce time-to-merge without weakening security checks

These goals are the lens through which the agent evaluates what it finds. Customizable goal configuration is deferred to a future iteration.

**Exploration instructions:**
- Start from the originating PR/issue
- Use `finding-agent-runs` skill to trace the workflow graph
- Dispatch subagents for all read-heavy operations to protect the main context window
- If triggered by `/fs-retro` with a human comment, treat that comment as the primary signal — the human is telling you where to look
- Go deep: follow threads, check related PRs, look for recurring patterns

**Analysis instructions:**
- Reconstruct the timeline of events
- Evaluate each step against the optimization goals
- Look for patterns across other PRs in the same repo
- Check for prior retro proposals — avoid duplicates, build on existing findings
- Assess your own uncertainty honestly — if you're not sure, say so

**Localization guidance:**
- Prefer upstream first. If the improvement would benefit all fullsend users, propose it in `fullsend-ai/fullsend`
- If it's org-specific, propose it in the `.fullsend` repo
- Only propose repo-level changes when the fix is truly specific to that repo (e.g., a test command, a repo-specific linter config)
- Don't push repo-specific details upstream — that bloats the platform

## Output

### Proposal format

The retro agent writes proposals as structured files to an output directory. Each proposal is a markdown file with YAML frontmatter:

```yaml
---
target_repo: "org/repo-name"  # full owner/repo form
title: "Concise proposal title"
---
```

The body contains four sections:

#### What happened

A timeline of events with links to specific points in logs, PR comments, and agent runs. Tells the story of how the workflow unfolded. Links to specific lines in JSONL traces, specific review comments, specific CI log output.

#### What could go better

The retro agent's assessment of improvement opportunities. Includes an honest uncertainty assessment — how confident the agent is in its analysis and why.

#### Proposed change

What to do differently and where. Specific enough for an implementer (human or agent) to act on. Names the file, config, skill, or prompt that should change and describes the change.

#### Validation criteria

How to know the change had the desired effect. Measurable or observable outcomes with a timeframe. For example:
- "The next 3 code agent runs on this repo should not trigger the same review rejection about missing test coverage"
- "Token cost for triage runs on this repo should decrease by ~20% over the next 10 runs"
- "The review agent should stop flagging Go error wrapping style in repos that use the bare-error convention"

### Post-script behavior

The post-script reads the proposal files from the output directory and:

1. **Files a GitHub issue** for each proposal in the `target_repo` specified in the frontmatter, using `gh issue create`
2. **Posts a summary comment** on the originating PR or issue using the REST Issues API (`POST /repos/{owner}/{repo}/issues/{number}/comments` via `gh api`), linking to all filed issues. This endpoint only requires `issues:write` and works for both PRs and issues since GitHub treats PRs as issues in the REST API.

## Dispatch Integration

### Shim changes (`fullsend.yaml`)

Add a `dispatch-retro` job with two trigger paths:

**PR close:**
```yaml
dispatch-retro:
  if: github.event_name == 'pull_request_target' && github.event.action == 'closed'
  # dispatch to .fullsend repo's retro.yml
```

**`/fs-retro` command:**
```yaml
dispatch-retro:
  if: github.event_name == 'issue_comment' && contains(github.event.comment.body, '/fs-retro')
  # dispatch to .fullsend repo's retro.yml with comment text
```

### `.fullsend` workflow (`retro.yml`)

A standard dispatch workflow that runs `fullsend run retro` with the provided inputs, following the same pattern as `triage.yml`, `code.yml`, and `review.yml`.

## Security Considerations

- **Read-only sandbox:** The retro agent cannot modify code, push branches, or alter harness configs. It only proposes changes via issues.
- **Credential isolation (ADR 0017):** Write credentials (`gh` token with issue-create and comment permissions) are held only by the post-script, outside the sandbox.
- **Unidirectional control flow (ADR 0016):** The retro agent proposes changes via issues. Changes go through standard review (CODEOWNERS, human approval) and take effect in future invocations, never the current one.
- **Adversarial feedback risk:** A malicious reviewer could post `/fs-retro` with misleading context to bias the retro agent's proposals. The mitigation is the same human approval gate on the resulting issues — a proposal only takes effect if a maintainer approves and merges the change.
- **Per-role GitHub App (ADR 0007):** The retro agent gets its own GitHub App with scoped permissions: read access to repos, PRs, workflow runs, and artifacts; write access to issues only. The post-script uses the REST Issues API (`POST /repos/{owner}/{repo}/issues/{number}/comments`) to comment on PRs, which requires only `issues: write` — avoiding `pull_requests: write` and the broader capabilities it grants.

## Architectural Constraints

- The retro agent fits the existing agent model — no new architectural concepts are introduced
- It follows the same harness → sandbox → runtime → post-script sequence as all other agents
- Proposals are filed as issues and go through normal review, preserving the "repo is the coordinator" principle
- The `finding-agent-runs` skill ([PR #568](https://github.com/fullsend-ai/fullsend/pull/568)) provides the workflow tracing capability the retro agent depends on

## Future Extensions

- **Configurable optimization goals:** Move goals from the system prompt to a versioned config file (`.fullsend/retro/goals.yaml`) with per-invocation overrides
- **Improvement pattern library:** A set of skills teaching common improvement patterns (e.g., "when review agents repeatedly flag the same issue, the fix is usually a linter rule")
- **Self-improvement:** The retro agent can eventually analyze its own prior runs and propose improvements to its own pattern library and skills
- **Cross-repo pattern detection:** Identify improvements that recur across repos and auto-propose org-level or upstream changes
