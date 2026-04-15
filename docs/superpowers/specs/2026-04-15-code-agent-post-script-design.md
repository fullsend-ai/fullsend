# Code Agent Post-Script and Provider

Date: 2026-04-15

## Problem

The code agent runs inside an OpenShell sandbox, reads an issue, implements a
fix, and commits to a feature branch. When the sandbox is destroyed, the commit
is lost. The automation layer that turns the agent's local commit into a pushed
branch and PR does not exist yet.

This is the post-script component of the code agent harness -- the credentialed
write-back step described in [ADR-0017][adr-0017]. It runs on the host after
sandbox cleanup, outside the agent's isolation boundary.

## Dependencies

- **Repo extraction** (Marta): Modified repo must be extracted from the sandbox
  into `runDir/repo/` before teardown. The post-script assumes this contract.
- **PR #231** (`fullsend run` CLI): The `Harness` struct and `run.go` execution
  flow already support `post_script`, `providers`, and `runner_env` fields. This
  work produces configuration that plugs into that machinery.
- **`code.yml` workflow**: Must export `ISSUE_NUMBER` and `REPO_FULL_NAME` as
  environment variables before calling `fullsend run`.

## Design

### File layout

```
.fullsend/
  harness/code.yaml          # harness definition for the code agent
  providers/github.yaml      # GH_TOKEN provider
  scripts/post-code.sh       # post-script: push branch + open PR
```

### Provider: `.fullsend/providers/github.yaml`

```yaml
name: github
type: generic
credentials:
  GH_TOKEN: "${FULLSEND_CODE_BOT_TOKEN}"
```

Maps the workflow-level `FULLSEND_CODE_BOT_TOKEN` (set by `setup-agent-env.sh`
after prefix-stripping) to `GH_TOKEN` for the post-script. The token is
available only on the host side; it never enters the sandbox (per ADR-0017).

### Harness: `.fullsend/harness/code.yaml`

```yaml
agent: agents/code.md
image: quay.io/manonru/fullsend-exp:latest
policy: policies/code.yaml

providers:
  - github

post_script: scripts/post-code.sh

runner_env:
  ISSUE_NUMBER: "${ISSUE_NUMBER}"
  REPO_FULL_NAME: "${REPO_FULL_NAME}"
```

- `agents/code.md` and `policies/code.yaml` are out of scope; the harness
  references them for completeness.
- `ISSUE_NUMBER` and `REPO_FULL_NAME` originate from the dispatch workflow's
  GitHub event payload. The workflow must export them before calling
  `fullsend run`.
- No `pre_script`, `skills`, `host_files`, or `validation_loop` -- those can be
  added as the code agent matures.

### Post-script: `.fullsend/scripts/post-code.sh`

```bash
#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="repo"

if [ ! -d "${REPO_DIR}" ]; then
  echo "error: extracted repo not found at ${REPO_DIR}" >&2
  exit 1
fi

cd "${REPO_DIR}"

BRANCH="$(git branch --show-current)"

if [ -z "${BRANCH}" ] || [ "${BRANCH}" = "main" ]; then
  echo "error: agent did not create a feature branch" >&2
  exit 1
fi

git remote set-url origin \
  "https://x-access-token:${GH_TOKEN}@github.com/${REPO_FULL_NAME}.git"

git push -u origin "${BRANCH}"

gh pr create \
  --repo "${REPO_FULL_NAME}" \
  --head "${BRANCH}" \
  --title "fix: resolve #${ISSUE_NUMBER}" \
  --body "Closes #${ISSUE_NUMBER}"
```

#### Behavior

1. Verifies the extracted repo exists at `repo/` under `runDir` (the working
   directory set by `run.go`).
2. Verifies the agent created a feature branch (not `main`, not detached HEAD).
3. Rewrites the git remote to use the GitHub App token for push auth.
4. Pushes the branch.
5. Creates a PR that references and auto-closes the originating issue.

#### Error cases

| Condition | Behavior |
|---|---|
| `repo/` missing | Exit 1 with message. Indicates extraction did not run. |
| No branch or on `main` | Exit 1. Agent did not do its job. |
| Push fails | `set -euo pipefail` exits immediately. |
| PR creation fails | Same -- exits with `gh` error. |

#### Environment contract

| Variable | Source | Purpose |
|---|---|---|
| `GH_TOKEN` | Provider (github.yaml) | Push auth and `gh` CLI auth |
| `REPO_FULL_NAME` | runner_env (code.yaml) | `owner/repo` for remote URL and PR target |
| `ISSUE_NUMBER` | runner_env (code.yaml) | Issue reference in PR title/body |

### Execution flow (in context of `run.go`)

```
fullsend run code
  -> load harness/code.yaml
  -> ensure provider "github" (GH_TOKEN available on host)
  -> create sandbox, bootstrap agent, copy repo
  -> run agent (agent commits to feature branch inside sandbox)
  -> extract output files
  -> [Marta] extract repo to runDir/repo/
  -> delete sandbox
  -> run post-script (scripts/post-code.sh) in runDir
     -> push branch, create PR
```

The post-script runs in the deferred cleanup in `run.go`, after sandbox
deletion but with access to the extracted repo and host environment.

## What this does NOT cover

- The `agents/code.md` agent definition
- The `policies/code.yaml` sandbox network policy
- Repo extraction from the sandbox (Marta's work)
- Review agent integration (separate harness + post-script)
- Retry/failure handling beyond immediate exit
- PR labels, assignees, or reviewer assignment

## Testing

End-to-end testing requires repo extraction (in-flight). The post-script can be
validated independently by:

1. Creating a mock `runDir/repo/` with a git repo on a feature branch
2. Setting `GH_TOKEN`, `REPO_FULL_NAME`, `ISSUE_NUMBER` in the environment
3. Running `post-code.sh` and verifying push + PR creation against a test repo

[adr-0017]: ../../ADRs/0017-credential-isolation-for-sandboxed-agents.md
