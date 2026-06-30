---
name: e2e-health
description: >
  Use when checking e2e test health or reviewing recent e2e failures on main.
allowed-tools: Bash(bash skills/e2e-health/scripts/list-runs.sh:*), Bash(gh run view:*)
---

# E2E Health

Check the health of the E2E Tests workflow on `main` over the last 2 days, summarize results in a table, and explain any failures.

## Procedure

### 1. Fetch recent runs

```bash
bash skills/e2e-health/scripts/list-runs.sh            # default: last 2 days
bash skills/e2e-health/scripts/list-runs.sh "7 days ago"  # custom lookback
```

The argument is any string `date -d` accepts. Returns JSON with fields: `databaseId`, `displayTitle`, `conclusion`, `status`, `createdAt`, `url`.

### 2. Present a summary table

Format the results as a markdown table with clickable links:

| Status | Run | Commit Title | When |
|--------|-----|--------------|------|
| pass/fail/in_progress | run-id (linked) | displayTitle | relative time |

Use a green checkmark for success, red X for failure, and a spinner for in-progress.

To determine the Status column: check `status` first — if it is not `completed`, the run is in-progress (conclusion will be null). If `status` is `completed`, use `conclusion` (`success` or `failure`).

### 3. Diagnose failures

For each failed run, fetch the failed step logs:

```bash
gh run view <run-id> --log-failed 2>&1 | grep -iE "(FAIL|--- FAIL|Error|panic|timeout)"
```

Read the matched lines and provide a brief explanation of why the run failed. Common failure categories:

- **Flaky test** — timing-dependent or non-deterministic failure
- **Mint / auth** — OIDC or mint token exchange failure, FOREIGN allowlist misconfiguration, or triage dispatch not firing (check `ready-for-triage` label path)
- **Infrastructure** — GCP auth, runner issues, pool org lock contention
- **Real regression** — a code change broke e2e behavior

### 4. Overall assessment

End with a one-line verdict: whether `main` is healthy, degraded, or broken based on the pattern of results.
