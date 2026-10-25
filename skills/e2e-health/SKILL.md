---
name: e2e-health
description: >
  Use when checking e2e test health or reviewing recent e2e failures on
  main, and when scanning behaviour runs for GitHub API rate-limit
  exhaustion evidence (the #6702 validation).
allowed-tools: Bash(bash skills/e2e-health/scripts/list-runs.sh:*), Bash(gh run view:*), Bash(bash skills/e2e-health/scripts/scan-ratelimit-evidence.sh:*), Bash(bash skills/e2e-health/scripts/scan-ratelimit-evidence_test.sh:*), Bash(gh repo view:*), Bash(gh run list:*), Bash(gh api repos/*/actions/runs/*/attempts/*:*), Bash(gh api repos/*/actions/jobs/*/logs:*), Bash(jq:*)
---

# E2E Health

Check the health of the E2E Tests workflow on `main` over the last 2 days,
summarize results in a table, and explain any failures. When failures look
like GitHub API rate limits, follow up with the
[rate-limit evidence scan](#rate-limit-evidence-scan).

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
- **Rate limits** — GitHub API exhaustion during harness waits; run the
  [rate-limit evidence scan](#rate-limit-evidence-scan) for per-run evidence
- **Real regression** — a code change broke e2e behavior

### 4. Overall assessment

End with a one-line verdict: whether `main` is healthy, degraded, or broken based on the pattern of results.

## Rate-limit evidence scan

`scripts/scan-ratelimit-evidence.sh` is the evidence tool for GitHub API
rate-limit exhaustion in behaviour-test runs (#6702's validation program).
Over a window of completed `e2e.yml` runs on `main` it walks every attempt
of each run (retries can mask a rate-limited first attempt), fetches the
`behaviour` job's logs, and reports the blind-wait diagnostic markers that
harness-wait timeouts emit — `run listing failed:` and
`(same error on N of M polls during the wait)`, polls and lookups variants
both. Run conclusions come from run and attempt metadata, never from log
parsing. The scan is read-only (gh GETs only) and runs from any checkout
with ordinary `gh` auth.

### When to run it

- Investigating behaviour-suite flakiness or timeouts suspected to be
  rate-limit related — timeout errors carrying the marker strings above
  are the tell.
- Executing #6702's post-merge validation: zero `run listing failed` /
  same-error diagnostics on runs that pass on re-run, over the 20 most
  recent completed behaviour runs on `main`.

### Invocation

Run from the repository root (the `allowed-tools` grants match that form):

```bash
bash skills/e2e-health/scripts/scan-ratelimit-evidence.sh                   # default: 20 runs
bash skills/e2e-health/scripts/scan-ratelimit-evidence.sh --runs 2          # window size, 1-100
bash skills/e2e-health/scripts/scan-ratelimit-evidence.sh --repo OWNER/NAME # explicit repository
```

The repository defaults to the one `gh` resolves from this checkout — the
upstream remote before the fork's `origin`. Runs whose `behaviour` job
skipped in every attempt contribute no evidence and do not count toward
the window; a thinned window shows as `window` vs `runs_scanned` in the
report, never silent.

stdout carries only the JSON report, so `script > report.json` captures it
cleanly; stderr carries progress, warnings, and the summary table
(`2> summary.txt` saves it). Exit 0 means the scan completed; exit 1 means
it failed or the report is incomplete — a behaviour-job log could not be
fetched, the verdict may undercount, and the affected attempts carry
`log_error` entries.

### Reading the two signal levels

The verdict is rendered at both levels from one scan, so either reading
of the closure criterion applies to the same report without re-scanning.
The summary table (stderr) ends with both lines; from a saved report,
`jq '.verdict' report.json` prints the counts.

- **Strict — `runs_with_markers`**: "N run(s) with markers". Any marker
  occurrence at all, on any attempt of any scanned run. Zero here means
  the criterion holds under the strictest reading.
- **Lenient — `runs_with_markers_that_passed`**: "N of those ultimately
  passed". Marker-bearing runs whose final conclusion is success —
  retry-masked occurrences, since infrastructure-classified failures
  retry once and a passing later attempt can mask a rate-limited first
  one. The input to the looser reading that counts only failures that
  never recover.

Which level counts as a violation is #6702's closure-rule decision,
recorded with the report rather than by the scanner. Check `incomplete`
before weighing either level: `true` means a behaviour-job log could not
be fetched and the verdict may undercount.

### Verifying the scanner contract

```bash
bash skills/e2e-health/scripts/scan-ratelimit-evidence_test.sh
```

The paired test drives the scanner's analysis layer from hermetic fixture
logs — marker detection (including the near-misses that must not match),
retry masking, window bounding, and the two-level verdict — with no
network and no `gh` calls. It is also registered in the Makefile
`script-test` target. Run it whenever the scanner or its marker contract
changes.
