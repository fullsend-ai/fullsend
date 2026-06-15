---
name: e2e-health
description: >
  Use when checking e2e test health, reviewing recent e2e failures on main,
  or asking about the state of end-to-end tests. Summarizes recent E2E Tests
  workflow runs with pass/fail status and failure explanations.
allowed-tools: Bash(skills/e2e-health/list-runs.sh:*), Bash(gh run view:*)
---

# E2E Health

Check the health of the E2E Tests workflow on `main` over the last 2 days, summarize results in a table, and explain any failures.

## Procedure

### 1. Fetch recent runs

```bash
skills/e2e-health/list-runs.sh            # default: last 2 days
skills/e2e-health/list-runs.sh "7 days ago"  # custom lookback
```

The argument is any string `date -d` accepts. Returns JSON with fields: `databaseId`, `displayTitle`, `conclusion`, `status`, `createdAt`, `url`.

### 2. Present a summary table

Format the results as a markdown table with clickable links:

| Status | Run | Commit Title | When |
|--------|-----|--------------|------|
| pass/fail/in_progress | [run-id](url) | displayTitle | relative time |

Use a green checkmark for success, red X for failure, and a spinner for in-progress.

### 3. Diagnose failures

For each failed run, fetch the failed step logs:

```bash
gh run view <run-id> --log-failed 2>&1 | grep -E "(FAIL|--- FAIL|Error|panic|timeout)"
```

Read the matched lines and provide a brief explanation of why the run failed. Common failure categories:

- **Flaky test** — timing-dependent or non-deterministic failure
- **Session expired** — GitHub session token needs rotation
- **Infrastructure** — GCP auth, Playwright deps, runner issues
- **Real regression** — a code change broke e2e behavior

### 4. Overall assessment

End with a one-line verdict: whether `main` is healthy, degraded, or broken based on the pattern of results.
