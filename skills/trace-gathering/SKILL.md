---
name: trace-gathering
description: >-
  Use when investigating an agent-driven work item and you need the full
  history of what happened — label transitions, agent comments, review
  verdicts, CI results, and timing.
---

# Trace Gathering

Collect the evidence trail for an agent-driven work item from GitHub's API.
The output is a structured evidence bundle used by downstream analysis.

## Process

### 1. Identify the work item

Find the issue and linked PR(s):

```bash
# From a PR, find linked issues
gh pr view <number> --json closingIssuesReferences --jq '.closingIssuesReferences[].number'

# From an issue, find linked PRs
gh pr list --state all --search "closes #<issue>" --json number,state,headRefName
```

### 2. Collect the timeline

```bash
# Label transitions, comments, reviews — all in one timeline
gh api repos/{owner}/{repo}/issues/<number>/timeline --paginate

# PR reviews
gh api repos/{owner}/{repo}/pulls/<number>/reviews --paginate

# Check runs for the PR head
gh api repos/{owner}/{repo}/commits/<sha>/check-runs --paginate
```

### 3. Assemble the evidence bundle

From the raw timeline, extract and organize:

| Section | What to record |
|---------|----------------|
| **Lineage** | Issue, PR(s), branch(es), merge commit |
| **Label transitions** | Ordered list with timestamps and actors (bot vs human) |
| **Rework count** | Number of `ready-to-implement` ↔ `ready-for-review` cycles |
| **Agent outputs** | Key structured comments (triage-output, review verdicts) with links |
| **CI results** | Pass/fail per check, duration, retry count |
| **Human interventions** | Non-bot actions — reviews, comments, label changes |
| **Timing** | Wall time per stage, total elapsed, time in rework loops |

## Current limitations

- **No observability trace IDs yet** (Story 6). When available, correlate
  GitHub events with internal agent traces.
- **No token/cost data yet.** Add cost collection when the observability
  layer exposes it.
- **Bot detection** assumes GitHub App accounts are identifiable by actor
  type. May need refinement based on auth implementation.
