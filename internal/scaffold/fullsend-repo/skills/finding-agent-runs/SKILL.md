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
SOURCE_REPO="${REPO_FULL_NAME:-$(gh repo view --json nameWithOwner -q .nameWithOwner)}"
ORG=$(echo "${SOURCE_REPO}" | cut -d/ -f1)
CONFIG_REPO="${ORG}/.fullsend"
```

Per-org installs use synchronous `workflow_call`: the enrolled-repo shim
(`fullsend.yaml`) calls `${CONFIG_REPO}` `dispatch.yml`, which runs agent
stages as jobs in the **same** Actions run on `SOURCE_REPO`. There are no
separate `dispatch.yml` runs in `.fullsend` to look up.

## Issue → Agent Runs

### Triage dispatch

Triage dispatches from `issue_comment` events (the `/fs-triage` command):

```bash
gh run list --repo "${SOURCE_REPO}" --workflow=fullsend.yaml \
  --json databaseId,status,conclusion,event,createdAt \
  -q '.[] | select(.event == "issue_comment")'
```

Match by timestamp against the `/fs-triage` comment (`gh issue view <N> --json comments`), then confirm the **Triage** stage job succeeded:

```bash
gh run view <RUN_ID> --repo "${SOURCE_REPO}" --json jobs \
  -q '.jobs[] | "\(.name) \(.status)/\(.conclusion)"'
```

### Code dispatch

Code dispatches from `issues` events when `ready-to-code` is applied:

```bash
gh run list --repo "${SOURCE_REPO}" --workflow=fullsend.yaml \
  --json databaseId,status,conclusion,event,createdAt \
  -q '.[] | select(.event == "issues")'
```

Confirm the **Code** job completed successfully in that run's job list.

## PR → Agent Runs

### Code agent run

The PR branch follows `agent/{issue}-{slug}`. Extract the issue number and
use the issue recipe above to find the code dispatch run on `SOURCE_REPO`.

### Review dispatch

Review dispatches from `pull_request_target` events. Match by `headBranch`:

```bash
gh run list --repo "${SOURCE_REPO}" --workflow=fullsend.yaml \
  --json databaseId,status,conclusion,event,headBranch,createdAt \
  -q '.[] | select(.event == "pull_request_target")'
```

Confirm the **Review** job completed successfully:

```bash
gh run view <RUN_ID> --repo "${SOURCE_REPO}" --json jobs \
  -q '.jobs[] | "\(.name) \(.status)/\(.conclusion)"'
```

### Retro dispatch

Retro dispatches from `pull_request_target` (on PR close) and from
`issue_comment` events (the `/fs-retro` command):

```bash
gh run list --repo "${SOURCE_REPO}" --workflow=fullsend.yaml \
  --json databaseId,status,conclusion,event,createdAt \
  -q '.[] | select(.event == "pull_request_target" or .event == "issue_comment")'
```

Confirm the **Retro** job completed successfully in the same run.

## Reference

### Logs and artifacts

Use the shim run ID on `SOURCE_REPO` (not `${CONFIG_REPO}`):

```bash
# Search logs for errors
gh run view <RUN_ID> --repo "${SOURCE_REPO}" --log 2>&1 \
  | grep -i "error\|fail\|exit code"

# Download session artifact (uploaded by the stage job in this run)
gh run download <RUN_ID> --repo "${SOURCE_REPO}"
```

### Common failure signatures

| Log message | Meaning |
|-------------|---------|
| `Agent exit code: 0` + `Post-script failed` | Agent succeeded but post-script (push/commit) failed |
| `remote rejected ... without 'workflows' permission` | Agent modified `.github/workflows/` without permission |
| `Agent exit code: 1` | Agent failed — check session artifact |
