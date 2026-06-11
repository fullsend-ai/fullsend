---
name: finding-agent-runs
description: >
  Use when an agent hasn't posted results, a workflow run failed, or you need
  to find the GitHub Actions run for a fullsend triage, code, or review agent
  given an issue number or PR number
---

# Finding Agent Runs

Given an issue or PR, find the fullsend agent workflow runs using `gh` CLI.

## Setup

```bash
ORG=$(echo "${REPO_FULL_NAME:-$(gh repo view --json owner -q .owner.login)}" | cut -d/ -f1)
DISPATCH_REPO="${ORG}/.fullsend"
```

The shim workflow (`fullsend.yaml`) runs in the source repo on `main`. It
dispatches to `${DISPATCH_REPO}` which runs the agent workflows
(`triage.yml`, `code.yml`, `review.yml`, `retro.yml`).

## Issue → Agent Runs

### Triage dispatch

Triage dispatches from `issue_comment` events (the `/fs-triage` command):

```bash
gh run list --workflow=fullsend.yaml \
  --json databaseId,status,conclusion,event,createdAt \
  -q '.[] | select(.event == "issue_comment")'
```

Match by timestamp against the `/fs-triage` comment (`gh issue view <N> --json comments`), then confirm `dispatch-triage` succeeded:

```bash
gh run view <RUN_ID> --json jobs \
  -q '.jobs[] | "\(.name) \(.status)/\(.conclusion)"'
```

### Code dispatch

Code dispatches from `issues` events when `ready-to-code` is applied:

```bash
gh run list --workflow=fullsend.yaml \
  --json databaseId,status,conclusion,event,createdAt \
  -q '.[] | select(.event == "issues")'
```

Confirm `dispatch-code completed/success` in the jobs list.

### Find the actual agent run

Match by timestamp in the dispatch repo (runs start within seconds):

```bash
gh run list --repo "${DISPATCH_REPO}" --workflow=triage.yml --limit 5 \
  --json databaseId,status,conclusion,createdAt

gh run list --repo "${DISPATCH_REPO}" --workflow=code.yml --limit 5 \
  --json databaseId,status,conclusion,createdAt
```

## PR → Agent Runs

### Code agent run

The PR branch follows `agent/{issue}-{slug}`. Extract the issue number and
use the issue recipe above to find the code dispatch.

### Review dispatch

Review dispatches from `pull_request_target` events. Match by `headBranch`:

```bash
gh run list --workflow=fullsend.yaml \
  --json databaseId,status,conclusion,event,headBranch,createdAt \
  -q '.[] | select(.event == "pull_request_target")'
```

Confirm `dispatch-review completed/success`, then find the run:

```bash
gh run list --repo "${DISPATCH_REPO}" --workflow=review.yml --limit 5 \
  --json databaseId,status,conclusion,createdAt
```

### Retro dispatch

Retro dispatches from `pull_request_target` (on PR close) and from
`issue_comment` events (the `/fs-retro` command):

```bash
gh run list --workflow=fullsend.yaml \
  --json databaseId,status,conclusion,event,createdAt \
  -q '.[] | select(.event == "pull_request_target" or .event == "issue_comment")'
```

Find the actual retro agent run:

```bash
gh run list --repo "${DISPATCH_REPO}" --workflow=retro.yml --limit 5 \
  --json databaseId,status,conclusion,createdAt
```

## Reference

### Logs and artifacts

```bash
# Search logs for errors
gh run view <RUN_ID> --repo "${DISPATCH_REPO}" --log 2>&1 \
  | grep -i "error\|fail\|exit code"

# Download session artifact
gh run download <RUN_ID> --repo "${DISPATCH_REPO}"
```

### Common failure signatures

| Log message | Meaning |
|-------------|---------|
| `Agent exit code: 0` + `Post-script failed` | Agent succeeded but post-script (push/commit) failed |
| `remote rejected ... without 'workflows' permission` | Agent modified `.github/workflows/` without permission |
| `Agent exit code: 1` | Agent failed — check session artifact |
