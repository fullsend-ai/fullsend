---
name: retro-analysis
description: >
  Use when performing a retrospective on an agent workflow. Teaches how to
  trace workflow runs, explore context with subagents, and write structured
  improvement proposals.
---

# Retro Analysis

## Tracing the workflow graph

Given the originating PR or issue, reconstruct what agents ran and in what order.

### Setup

```bash
ORG=$(echo "$REPO_FULL_NAME" | cut -d/ -f1)
DISPATCH_REPO="${ORG}/.fullsend"
```

### From an issue

1. Find triage dispatches (triggered by `/fs-triage` command or `needs-info` label responses):

```bash
gh run list --repo "$REPO_FULL_NAME" --workflow=fullsend.yaml \
  --json databaseId,status,conclusion,event,createdAt \
  -q '.[] | select(.event == "issue_comment" or .event == "issues")'
```

2. Find the corresponding agent runs in the dispatch repo:

```bash
gh run list --repo "$DISPATCH_REPO" --workflow=triage.yml --limit 10 \
  --json databaseId,status,conclusion,createdAt
```

3. If the issue reached `ready-to-code`, find code dispatches:

```bash
gh run list --repo "$DISPATCH_REPO" --workflow=code.yml --limit 10 \
  --json databaseId,status,conclusion,createdAt
```

### From a PR

1. The PR branch follows `agent/{issue}-{slug}`. Extract the issue number to trace the full history.

2. Find review dispatches:

```bash
gh run list --repo "$DISPATCH_REPO" --workflow=review.yml --limit 10 \
  --json databaseId,status,conclusion,createdAt
```

3. Find fix dispatches (if review requested changes):

```bash
gh run list --repo "$DISPATCH_REPO" --workflow=fix.yml --limit 10 \
  --json databaseId,status,conclusion,createdAt
```

### Reading agent logs and artifacts

```bash
# View job outcomes
gh run view <RUN_ID> --repo "$DISPATCH_REPO" --json jobs \
  -q '.jobs[] | "\(.name) \(.status)/\(.conclusion)"'

# Search logs for errors
gh run view <RUN_ID> --repo "$DISPATCH_REPO" --log 2>&1 \
  | grep -i "error\|fail\|exit code"

# Download session artifacts (JSONL traces)
gh run download <RUN_ID> --repo "$DISPATCH_REPO"
```

## Exploration strategy

You have a large amount of context to cover. Use subagents to avoid overflowing your main context window.

### Dispatch subagents for each investigation thread

- **Workflow tracer:** "Find all agent workflow runs related to issue/PR #N. List each run with its stage, status, conclusion, and timestamp."
- **Trace reader:** "Download and read the JSONL reasoning trace for run <RUN_ID>. Summarize what decisions the agent made and why."
- **Comment analyzer:** "Read all comments on PR #N. Categorize them: agent review comments, human review comments, CI results, human interventions."
- **Pattern searcher:** "Search the last 10 retro agent issues in <REPO>. List any recurring themes or prior proposals related to <TOPIC>."
- **Harness inspector:** "Read the harness config at harness/<AGENT>.yaml and the agent definition at agents/<AGENT>.md in the .fullsend repo. Summarize the agent's configuration and constraints."

### Keep your main context for synthesis

After subagents return their findings, use your main context to:
1. Reconstruct the timeline
2. Identify where things could have gone better
3. Form hypotheses about root causes
4. Decide what changes to propose and where

## Localization guidance

When deciding where a proposed change belongs:

1. **Prefer upstream first.** If the improvement would benefit all fullsend users, target `fullsend-ai/fullsend`.
2. **Repo-level** for fixes truly specific to one repo (e.g., a test command, a repo-specific linter config): target the source repo itself.
3. **Org-level `.fullsend` repos — discouraged.** See below.

Do not push repo-specific details upstream.

<!-- TODO(#833): Remove this restriction once per-repo customization is
     stable. Depends on: #195, #179, #419, PR #792, PR #799. -->

**Avoid targeting `*/.fullsend` repos.** The per-repo customization model
for `.fullsend` repos is not yet defined. Issues filed there are hard for
users to discover and act on. Instead:

- Route platform/tooling improvements to `fullsend-ai/fullsend`.
- Route repo-specific fixes to the source repo.
- Only target a `.fullsend` repo when the change is genuinely org-level
  configuration with no alternative location. If you do, you **must**
  include explicit justification in the `proposed_change` field explaining
  why `.fullsend` is the only viable target.

## Output format

Write a single JSON file to `$FULLSEND_OUTPUT_DIR/agent-result.json` with this structure:

```json
{
  "summary": "Markdown summary for the originating PR/issue comment.",
  "proposals": [
    {
      "target_repo": "owner/repo-name",
      "title": "Concise proposal title",
      "what_happened": "Timeline with links...",
      "what_could_go_better": "Assessment with uncertainty...",
      "proposed_change": "Specific change description...",
      "validation_criteria": "How to verify the improvement..."
    }
  ]
}
```

**Schema is strict.** The top-level object allows ONLY `summary` and `proposals` — no additional properties. Each proposal object allows ONLY the six fields shown above. The harness validates against `$FULLSEND_OUTPUT_SCHEMA` with `"additionalProperties": false` at both levels. Do not add fields like `timeline`, `metadata`, `workflow_quality`, or `originating_url`.

### Writing good proposals

- **what_happened:** Tell the story chronologically. Link to specific workflow runs, log lines, PR comments, and review verdicts. Use markdown links.
- **what_could_go_better:** Be honest about your uncertainty. If you are confident, say so and why. If you are speculating, say that too. Explain your reasoning.
- **proposed_change:** Name the specific file, config, skill, or prompt that should change. Describe what the change looks like. Be specific enough for an implementer to act on it.
- **validation_criteria:** Define measurable or observable outcomes. Include a timeframe or sample size. For example: "The next 5 code agent runs on this repo should not trigger the same review comment about missing error handling."

### When to propose nothing

If the workflow went well and you cannot identify meaningful improvements, write a summary saying so and return an empty proposals array. A retro that finds nothing wrong is a valid outcome.

## Constraints

The agent definition (`agents/retro.md`) is the authoritative list of prohibitions. This skill does not restate them.
