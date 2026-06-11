# Retro Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a retrospective agent that analyzes completed or in-progress agent workflows and proposes improvements by filing GitHub issues.

**Architecture:** The retro agent follows the same harness/sandbox/runtime/post-script pattern as all other agents (ADR 0024). It is triggered by PR close events (automatic) and `/fs-retro` commands (on-demand). The agent explores workflow history at runtime using `gh` CLI and the `finding-agent-runs` skill, then writes structured proposal files that the post-script converts into GitHub issues and summary comments.

**Tech Stack:** Bash (pre/post scripts), YAML (harness config, workflow, policy), JSON Schema (output validation), Markdown (agent definition), GitHub Actions (dispatch workflow)

---

## File Structure

### New files in `internal/scaffold/fullsend-repo/`

| File | Responsibility |
|------|----------------|
| `agents/retro.md` | Agent system prompt — role, optimization goals, exploration instructions, output format |
| `harness/retro.yaml` | Harness config — links agent, policy, scripts, skills, env |
| `policies/retro.yaml` | Sandbox policy — read-only filesystem, network access for GitHub API |
| `env/retro.env` | Environment variables injected into sandbox |
| `scripts/pre-retro.sh` | Minimal pre-script — validates inputs, writes trigger context to agent_input |
| `scripts/post-retro.sh` | Post-script — reads proposal files, files GitHub issues, posts summary comment |
| `schemas/retro-result.schema.json` | JSON Schema for retro agent output |
| `.github/workflows/retro.yml` | Dispatch target workflow — triggered by dispatcher for stage=retro |
| `skills/retro-analysis/SKILL.md` | Skill teaching the retro agent how to analyze workflows and write proposals |

### Modified files

| File | Change |
|------|--------|
| `internal/scaffold/fullsend-repo/templates/shim-workflow.yaml` | Add `dispatch-retro` and `dispatch-retro-command` jobs |
| `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml` | Update description to include "retro" in stage list |

---

### Task 1: Retro agent output schema

**Files:**
- Create: `internal/scaffold/fullsend-repo/schemas/retro-result.schema.json`

The output schema defines what the retro agent must produce. The post-script depends on this structure, so it comes first.

- [ ] **Step 1: Create the output schema**

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "retro-result.schema.json",
  "title": "Retro Agent Result",
  "description": "Structured output from the retro agent, validated by the harness before the post-script runs (ADR 0022).",
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "proposals"],
  "properties": {
    "summary": {
      "type": "string",
      "minLength": 1,
      "maxLength": 16384,
      "description": "Markdown summary to post as a comment on the originating PR/issue. Links to each filed proposal issue."
    },
    "proposals": {
      "type": "array",
      "items": { "$ref": "#/$defs/proposal" },
      "description": "List of improvement proposals. Each becomes a GitHub issue."
    }
  },
  "$defs": {
    "proposal": {
      "type": "object",
      "additionalProperties": false,
      "required": ["target_repo", "title", "what_happened", "what_could_go_better", "proposed_change", "validation_criteria"],
      "properties": {
        "target_repo": {
          "type": "string",
          "pattern": "^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$",
          "description": "Full owner/repo where the issue should be filed."
        },
        "title": {
          "type": "string",
          "minLength": 1,
          "maxLength": 256,
          "description": "Concise issue title."
        },
        "what_happened": {
          "type": "string",
          "minLength": 1,
          "description": "Timeline of events with links to logs, PR comments, and agent runs."
        },
        "what_could_go_better": {
          "type": "string",
          "minLength": 1,
          "description": "Improvement opportunities with honest uncertainty assessment."
        },
        "proposed_change": {
          "type": "string",
          "minLength": 1,
          "description": "What to change and where, specific enough for an implementer to act on."
        },
        "validation_criteria": {
          "type": "string",
          "minLength": 1,
          "description": "How to know the change had the desired effect. Measurable or observable outcomes."
        }
      }
    }
  }
}
```

Write this to `internal/scaffold/fullsend-repo/schemas/retro-result.schema.json`.

- [ ] **Step 2: Validate the schema is well-formed**

Run: `python3 -c "import json; json.load(open('internal/scaffold/fullsend-repo/schemas/retro-result.schema.json'))"`
Expected: No output (valid JSON)

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/fullsend-repo/schemas/retro-result.schema.json
git commit -m "feat(retro): add output schema for retro agent"
```

---

### Task 2: Sandbox policy

**Files:**
- Create: `internal/scaffold/fullsend-repo/policies/retro.yaml`

The retro agent needs read-only filesystem access and network access for `gh` CLI (GitHub API). Same pattern as `policies/triage.yaml`.

- [ ] **Step 1: Create the policy file**

```yaml
version: 1

filesystem_policy:
  include_workdir: true
  read_only: [/usr, /lib, /proc, /dev/urandom, /app, /etc, /var/log]
  read_write: [/sandbox, /tmp, /dev/null]
landlock:
  compatibility: best_effort
process:
  run_as_user: sandbox
  run_as_group: sandbox

network_policies:
  vertex_ai:
    name: vertex-ai
    endpoints:
      - host: "*.github.com"
        port: 443
        protocol: tcp
        enforcement: enforce
        access: allow
      - host: "*.googleapis.com"
        port: 443
        protocol: tcp
        enforcement: enforce
        access: allow
    binaries:
      - path: "**/curl"
      - path: "**/claude"
      - path: "**/node"
```

Write this to `internal/scaffold/fullsend-repo/policies/retro.yaml`.

- [ ] **Step 2: Commit**

```bash
git add internal/scaffold/fullsend-repo/policies/retro.yaml
git commit -m "feat(retro): add sandbox policy for retro agent"
```

---

### Task 3: Environment file

**Files:**
- Create: `internal/scaffold/fullsend-repo/env/retro.env`

The retro agent needs the originating URL, comment text (if `/fs-retro`), the repo name, and a GH_TOKEN for API access inside the sandbox.

- [ ] **Step 1: Create the env file**

```bash
export ORIGINATING_URL="${ORIGINATING_URL}"
export RETRO_COMMENT="${RETRO_COMMENT}"
export REPO_FULL_NAME="${REPO_FULL_NAME}"
export GH_TOKEN=${GH_TOKEN}
```

Write this to `internal/scaffold/fullsend-repo/env/retro.env`.

- [ ] **Step 2: Commit**

```bash
git add internal/scaffold/fullsend-repo/env/retro.env
git commit -m "feat(retro): add environment file for retro agent"
```

---

### Task 4: Pre-script

**Files:**
- Create: `internal/scaffold/fullsend-repo/scripts/pre-retro.sh`

Minimal — validates inputs and writes trigger context. Follows the same `set -euo pipefail` + validation pattern as `pre-triage.sh`.

- [ ] **Step 1: Create the pre-script**

```bash
#!/usr/bin/env bash
# pre-retro.sh — Validate inputs for the retro agent.
#
# Runs on the host via the harness pre_script mechanism. Validates the
# originating URL (PR or issue) and logs the trigger context.
#
# Required env vars:
#   ORIGINATING_URL — HTML URL of the PR or issue that triggered retro
#   GH_TOKEN        — GitHub token with read scope
#
# Optional env vars:
#   RETRO_COMMENT   — The /retro comment text (empty for automatic triggers)

set -euo pipefail

echo "::notice::🔗 Retro target: ${ORIGINATING_URL}"

# Accept both issue and PR URLs.
if [[ ! "${ORIGINATING_URL}" =~ ^https://github\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/(issues|pull)/[0-9]+$ ]]; then
  echo "ERROR: ORIGINATING_URL does not match expected pattern: ${ORIGINATING_URL}"
  exit 1
fi

if [[ -n "${RETRO_COMMENT:-}" ]]; then
  echo "Retro triggered on-demand with comment."
else
  echo "Retro triggered automatically (PR close)."
fi

echo "Pre-retro validation complete."
```

Write this to `internal/scaffold/fullsend-repo/scripts/pre-retro.sh` and make it executable.

- [ ] **Step 2: Make executable**

Run: `chmod +x internal/scaffold/fullsend-repo/scripts/pre-retro.sh`

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/fullsend-repo/scripts/pre-retro.sh
git commit -m "feat(retro): add pre-script for retro agent"
```

---

### Task 5: Post-script

**Files:**
- Create: `internal/scaffold/fullsend-repo/scripts/post-retro.sh`

Reads the retro agent's JSON output, files a GitHub issue for each proposal, and posts a summary comment on the originating PR/issue. Follows the same patterns as `post-triage.sh`.

- [ ] **Step 1: Create the post-script**

```bash
#!/usr/bin/env bash
# post-retro.sh — File GitHub issues from retro agent proposals and post summary.
#
# Runs on the host after sandbox cleanup. Working directory is the fullsend
# run output directory.
#
# Required env vars:
#   ORIGINATING_URL — HTML URL of the originating PR or issue
#   GH_TOKEN        — GitHub token with issues write scope
#
# The agent writes its result to output/agent-result.json (relative to
# the iteration directory). This script finds the most recent iteration's output.

set -euo pipefail

# Find the retro result JSON (same pattern as post-triage.sh).
RESULT_FILE=""
for dir in iteration-*/output; do
  if [[ -f "${dir}/agent-result.json" ]]; then
    RESULT_FILE="${dir}/agent-result.json"
  fi
done

if [[ -z "${RESULT_FILE}" ]]; then
  echo "ERROR: agent-result.json not found in any iteration output directory"
  exit 1
fi

echo "Reading retro result from: ${RESULT_FILE}"

# Validate JSON is parseable.
if ! jq empty "${RESULT_FILE}" 2>/dev/null; then
  echo "ERROR: ${RESULT_FILE} is not valid JSON"
  exit 1
fi

# Extract repo and number from ORIGINATING_URL.
# Accepts both /issues/N and /pull/N.
if [[ ! "${ORIGINATING_URL}" =~ ^https://github\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/(issues|pull)/[0-9]+$ ]]; then
  echo "ERROR: ORIGINATING_URL does not match expected pattern: ${ORIGINATING_URL}"
  exit 1
fi
ORIGINATING_REPO=$(echo "${ORIGINATING_URL}" | sed 's|https://github.com/||; s|/\(issues\|pull\)/.*||')
ORIGINATING_NUMBER=$(basename "${ORIGINATING_URL}")

echo "Originating: ${ORIGINATING_REPO}#${ORIGINATING_NUMBER}"

# File an issue for each proposal.
PROPOSAL_COUNT=$(jq '.proposals | length' "${RESULT_FILE}")
echo "Found ${PROPOSAL_COUNT} proposal(s)"

ISSUE_LINKS=""
for i in $(seq 0 $((PROPOSAL_COUNT - 1))); do
  TARGET_REPO=$(jq -r ".proposals[$i].target_repo" "${RESULT_FILE}")
  TITLE=$(jq -r ".proposals[$i].title" "${RESULT_FILE}")

  # Build the issue body from the four sections.
  BODY=$(jq -r "
    .proposals[$i] |
    \"## What happened\n\n\" + .what_happened +
    \"\n\n## What could go better\n\n\" + .what_could_go_better +
    \"\n\n## Proposed change\n\n\" + .proposed_change +
    \"\n\n## Validation criteria\n\n\" + .validation_criteria +
    \"\n\n---\n_Generated by retro agent from ${ORIGINATING_URL}_\"
  " "${RESULT_FILE}")

  echo "Filing issue in ${TARGET_REPO}: ${TITLE}"
  ISSUE_URL=$(gh issue create \
    --repo "${TARGET_REPO}" \
    --title "${TITLE}" \
    --body "${BODY}" \
    --label "" 2>&1 | tail -1)

  if [[ -z "${ISSUE_URL}" ]]; then
    echo "ERROR: failed to create issue in ${TARGET_REPO}"
    exit 1
  fi

  echo "Created: ${ISSUE_URL}"
  ISSUE_LINKS="${ISSUE_LINKS}- [${TITLE}](${ISSUE_URL}) (in \`${TARGET_REPO}\`)\n"
done

# Post summary comment on the originating PR/issue.
# gh issue comment works for both issues and PRs.
SUMMARY=$(jq -r '.summary' "${RESULT_FILE}")

if [[ "${PROPOSAL_COUNT}" -gt 0 ]]; then
  COMMENT=$(printf '%s\n\n### Proposals filed\n\n%b' "${SUMMARY}" "${ISSUE_LINKS}")
else
  COMMENT="${SUMMARY}"
fi

echo "Posting summary comment on ${ORIGINATING_REPO}#${ORIGINATING_NUMBER}"
printf '%s' "${COMMENT}" | gh issue comment "${ORIGINATING_NUMBER}" \
  --repo "${ORIGINATING_REPO}" \
  --body-file -

echo "Post-retro complete."
```

Write this to `internal/scaffold/fullsend-repo/scripts/post-retro.sh` and make it executable.

- [ ] **Step 2: Make executable**

Run: `chmod +x internal/scaffold/fullsend-repo/scripts/post-retro.sh`

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/fullsend-repo/scripts/post-retro.sh
git commit -m "feat(retro): add post-script to file issues and post summary"
```

---

### Task 6: Retro analysis skill

**Files:**
- Create: `internal/scaffold/fullsend-repo/skills/retro-analysis/SKILL.md`

This skill teaches the retro agent how to trace workflows, explore context, and structure its analysis. It references the `finding-agent-runs` skill patterns (from PR #568) inline so the retro agent can work even before that PR merges.

- [ ] **Step 1: Create the skill**

```markdown
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

1. Find triage dispatches (triggered by `/fs-triage` or `needs-info` comments):

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
- **Harness inspector:** "Read the harness config at .fullsend/harness/<AGENT>.yaml and the agent definition at .fullsend/agents/<AGENT>.md. Summarize the agent's configuration and constraints."

### Keep your main context for synthesis

After subagents return their findings, use your main context to:
1. Reconstruct the timeline
2. Identify where things could have gone better
3. Form hypotheses about root causes
4. Decide what changes to propose and where

## Localization guidance

When deciding where a proposed change belongs:

1. **Prefer upstream first.** If the improvement would benefit all fullsend users, target `fullsend-ai/fullsend`.
2. **Org-level** for org-specific configuration: target the `.fullsend` repo (e.g., `ORG/.fullsend`).
3. **Repo-level** only for fixes truly specific to one repo (e.g., a test command, a repo-specific linter config): target the source repo itself.

Do not push repo-specific details upstream.

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

### Writing good proposals

- **what_happened:** Tell the story chronologically. Link to specific workflow runs, log lines, PR comments, and review verdicts. Use markdown links.
- **what_could_go_better:** Be honest about your uncertainty. If you are confident, say so and why. If you are speculating, say that too. Explain your reasoning.
- **proposed_change:** Name the specific file, config, skill, or prompt that should change. Describe what the change looks like. Be specific enough for an implementer to act on it.
- **validation_criteria:** Define measurable or observable outcomes. Include a timeframe or sample size. For example: "The next 5 code agent runs on this repo should not trigger the same review comment about missing error handling."

### When to propose nothing

If the workflow went well and you cannot identify meaningful improvements, write a summary saying so and return an empty proposals array. A retro that finds nothing wrong is a valid outcome.
```

Write this to `internal/scaffold/fullsend-repo/skills/retro-analysis/SKILL.md`.

- [ ] **Step 2: Commit**

```bash
git add internal/scaffold/fullsend-repo/skills/retro-analysis/SKILL.md
git commit -m "feat(retro): add retro-analysis skill"
```

---

### Task 7: Agent definition

**Files:**
- Create: `internal/scaffold/fullsend-repo/agents/retro.md`

The system prompt defining the retro agent's role, goals, and behavior. References the `retro-analysis` skill for detailed exploration and output instructions.

- [ ] **Step 1: Create the agent definition**

```markdown
---
name: retro
description: >-
  Perform a retrospective on an agent workflow. Analyze what happened,
  identify improvement opportunities, and propose changes by writing
  structured proposals that become GitHub issues.
skills:
  - retro-analysis
  - finding-agent-runs
tools: Bash(gh,jq)
model: opus
---

You are a retrospective analyst. You examine agent workflows — completed, rejected, or in-progress — and propose improvements to the system.

## Inputs

- `ORIGINATING_URL` — HTML URL of the PR or issue that triggered this retro.
- `RETRO_COMMENT` — (optional) The human's `/fs-retro` comment, if this was triggered on-demand. This is high-signal context: the human is telling you what to focus on. Read it carefully.
- `REPO_FULL_NAME` — The source repository (owner/repo).

## Your role

You are an analyst, not a fixer. Your job is to:

1. **Explore** — Reconstruct what happened across the full workflow graph (triage, code, review, fix agents and human interactions).
2. **Analyze** — Evaluate what could go better, considering the optimization goals below.
3. **Propose** — Write structured improvement proposals with clear validation criteria.

You do NOT implement fixes, push code, or modify configuration. You propose changes and let existing agent and human workflows handle implementation.

## Optimization goals

Evaluate workflows through these lenses (in priority order):

1. **Review quality** — Are reviews catching real issues? Are they missing things? Are they flagging false positives that waste human time?
2. **Rework rate** — How many iterations did it take? Could the code agent have gotten it right the first time with better context or instructions?
3. **Token cost** — Are agents doing redundant work? Reading files they don't need? Exploring dead ends?
4. **Time to resolution** — Could the pipeline have moved faster without sacrificing quality?

These are defaults. If RETRO_COMMENT provides different focus areas, prioritize those instead.

## Exploration approach

Use the `retro-analysis` skill for detailed workflow tracing recipes.

**Dispatch subagents for every read-heavy operation.** Your main context window is for synthesis, not data gathering. Examples:

- "Read the JSONL trace for workflow run <ID> and summarize the agent's key decisions"
- "Gather all review comments on PR #N and categorize them by source (agent vs human) and type (approval, change request, comment)"
- "Check the last 10 retro proposals in this repo for recurring patterns"
- "Read the harness config and agent definition for the code agent and summarize its setup"

Go deep. Follow threads. If you notice a pattern, investigate whether it occurs on other PRs too.

## Analysis approach

After gathering findings from subagents:

1. **Reconstruct the timeline** — What happened, in what order, and why?
2. **Identify improvement opportunities** — What could go better next time?
3. **Check for patterns** — Is this a one-off or recurring issue?
4. **Assess uncertainty** — How confident are you? What evidence supports your hypothesis? What could you be wrong about?
5. **Localize the fix** — Where does the change belong? Prefer upstream (fullsend-ai/fullsend) if the improvement is universal. Use org config (.fullsend repo) for org-specific tuning. Use the source repo only for repo-specific fixes.

## Output

Write a single JSON file to `$FULLSEND_OUTPUT_DIR/agent-result.json`. See the `retro-analysis` skill for the exact schema and writing guidance.

## Output rules

- Write ONLY the JSON file. No other output files.
- The JSON must be valid and parseable. No markdown fences around it, no trailing text.
- Do NOT post comments, create issues, or perform any GitHub mutations. The post-script handles all writes.
- Do NOT echo untrusted content (issue bodies, PR descriptions, comment text) verbatim into your proposals. Summarize or paraphrase instead.
- If the workflow went well and you find no meaningful improvements, return an empty proposals array with a summary saying so.
```

Write this to `internal/scaffold/fullsend-repo/agents/retro.md`.

- [ ] **Step 2: Commit**

```bash
git add internal/scaffold/fullsend-repo/agents/retro.md
git commit -m "feat(retro): add agent definition for retro agent"
```

---

### Task 8: Harness configuration

**Files:**
- Create: `internal/scaffold/fullsend-repo/harness/retro.yaml`

Ties together the agent, policy, scripts, skills, and env. Follows the exact same structure as `harness/triage.yaml`.

- [ ] **Step 1: Create the harness config**

```yaml
agent: agents/retro.md
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
policy: policies/retro.yaml

host_files:
  - src: env/gcp-vertex.env
    dest: /tmp/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: env/retro.env
    dest: /tmp/workspace/.env.d/retro.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/workspace/.gcp-credentials.json
  - src: ${GCP_OIDC_TOKEN_FILE}
    dest: /tmp/workspace/.gcp-oidc-token
    optional: true

skills:
  - skills/retro-analysis

pre_script: scripts/pre-retro.sh

validation_loop:
  script: scripts/validate-output-schema.sh
  max_iterations: 2

post_script: scripts/post-retro.sh

runner_env:
  ORIGINATING_URL: ${ORIGINATING_URL}
  RETRO_COMMENT: ${RETRO_COMMENT}
  REPO_FULL_NAME: ${REPO_FULL_NAME}
  GH_TOKEN: ${GH_TOKEN}
  FULLSEND_OUTPUT_SCHEMA: ${FULLSEND_DIR}/schemas/retro-result.schema.json

timeout_minutes: 30
```

Write this to `internal/scaffold/fullsend-repo/harness/retro.yaml`.

- [ ] **Step 2: Verify harness loads**

Run: `go test ./internal/harness/... -run TestLoad -v`
Expected: Existing tests pass (new harness is loaded at runtime, not by tests).

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/fullsend-repo/harness/retro.yaml
git commit -m "feat(retro): add harness configuration for retro agent"
```

---

### Task 9: Dispatch target workflow

**Files:**
- Create: `internal/scaffold/fullsend-repo/.github/workflows/retro.yml`

The workflow in `.fullsend` that the dispatcher triggers for `stage=retro`. Follows the same pattern as `triage.yml` and `review.yml`.

- [ ] **Step 1: Create the workflow**

```yaml
# fullsend-stage: retro
name: Retro

on:
  workflow_dispatch:
    inputs:
      event_type:
        required: true
        type: string
      source_repo:
        required: true
        type: string
      event_payload:
        required: true
        type: string

concurrency:
  group: fullsend-retro-${{ fromJSON(inputs.event_payload).pull_request.number || fromJSON(inputs.event_payload).issue.number }}
  cancel-in-progress: true

jobs:
  retro:
    name: Retro
    runs-on: ubuntu-latest
    permissions:
      actions: write
      contents: read
      id-token: write
      issues: write

    steps:
      - name: Checkout .fullsend repository
        uses: actions/checkout@v6

      - name: Validate source repo is enrolled
        env:
          SOURCE_REPO: ${{ inputs.source_repo }}
        run: |
          # Format check — must be owner/repo, safe characters only
          if [[ ! "$SOURCE_REPO" =~ ^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$ ]]; then
            echo "::error::Invalid source_repo format: must be owner/repo"
            exit 1
          fi
          # Owner check — must match this org
          REPO_OWNER="${SOURCE_REPO%%/*}"
          if [[ "$REPO_OWNER" != "$GITHUB_REPOSITORY_OWNER" ]]; then
            echo "::error::source_repo owner does not match org"
            exit 1
          fi
          # Allowlist check — repo must be enabled in config.yaml
          REPO_NAME="${SOURCE_REPO#*/}"
          ENABLED=$(yq ".repos.\"$REPO_NAME\".enabled" config.yaml 2>/dev/null)
          if [[ "$ENABLED" != "true" ]]; then
            echo "::error::repo is not enabled in config.yaml"
            exit 1
          fi

      - name: Extract target repo name
        id: repo-parts
        env:
          SOURCE_REPO: ${{ inputs.source_repo }}
        run: echo "name=${SOURCE_REPO##*/}" >> "${GITHUB_OUTPUT}"

      - name: Generate sandbox token (read-only)
        id: sandbox-token
        uses: actions/create-github-app-token@v3
        with:
          client-id: ${{ vars.FULLSEND_RETRO_CLIENT_ID }}
          private-key: ${{ secrets.FULLSEND_RETRO_APP_PRIVATE_KEY }}
          owner: ${{ github.repository_owner }}
          repositories: ${{ steps.repo-parts.outputs.name }},.fullsend
          permission-contents: read
          permission-pull-requests: read
          permission-issues: read
          permission-actions: read

      - name: Generate write token (issues + comments)
        id: write-token
        uses: actions/create-github-app-token@v3
        with:
          client-id: ${{ vars.FULLSEND_RETRO_CLIENT_ID }}
          private-key: ${{ secrets.FULLSEND_RETRO_APP_PRIVATE_KEY }}
          owner: ${{ github.repository_owner }}
          repositories: ${{ steps.repo-parts.outputs.name }},.fullsend
          permission-issues: write

      - name: Pre-mask GCP credential file path
        run: echo "::add-mask::${GITHUB_WORKSPACE}/gha-creds-"

      - name: Authenticate to Google Cloud (WIF)
        if: vars.FULLSEND_GCP_AUTH_MODE == 'wif'
        uses: google-github-actions/auth@v3
        with:
          workload_identity_provider: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}

      - name: Authenticate to Google Cloud (SA key)
        if: vars.FULLSEND_GCP_AUTH_MODE != 'wif'
        uses: google-github-actions/auth@v3
        with:
          credentials_json: ${{ secrets.FULLSEND_GCP_SA_KEY_JSON }}

      - name: Set GCP_OIDC_TOKEN_FILE for non-WIF
        if: vars.FULLSEND_GCP_AUTH_MODE != 'wif'
        run: |
          touch "$RUNNER_TEMP/empty-oidc-token"
          echo "GCP_OIDC_TOKEN_FILE=$RUNNER_TEMP/empty-oidc-token" >> "${GITHUB_ENV}"

      - name: Mask GCP credential file paths
        run: |
          for var in GOOGLE_GHA_CREDS_PATH GOOGLE_APPLICATION_CREDENTIALS CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE; do
            val="${!var:-}"
            if [[ -n "${val}" ]]; then
              echo "::add-mask::${val}"
            fi
          done

      - name: Prepare sandbox credentials
        run: bash scripts/prepare-sandbox-credentials.sh

      - name: Setup agent environment
        env:
          AGENT_PREFIX: RETRO_
          RETRO_GH_TOKEN: ${{ steps.sandbox-token.outputs.token }}
          RETRO_ANTHROPIC_VERTEX_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}
          RETRO_CLOUD_ML_REGION: ${{ vars.FULLSEND_GCP_REGION }}
        run: bash .github/scripts/setup-agent-env.sh

      - name: Run retro agent
        uses: ./.github/actions/fullsend
        env:
          ORIGINATING_URL: ${{ fromJSON(inputs.event_payload).pull_request.html_url || fromJSON(inputs.event_payload).issue.html_url }}
          RETRO_COMMENT: ${{ fromJSON(inputs.event_payload).comment.body || '' }}
          REPO_FULL_NAME: ${{ inputs.source_repo }}
          GH_TOKEN: ${{ steps.write-token.outputs.token }}
        with:
          agent: retro
```

Write this to `internal/scaffold/fullsend-repo/.github/workflows/retro.yml`.

- [ ] **Step 2: Verify the stage marker is correct**

Run: `head -1 internal/scaffold/fullsend-repo/.github/workflows/retro.yml`
Expected: `# fullsend-stage: retro`

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/fullsend-repo/.github/workflows/retro.yml
git commit -m "feat(retro): add dispatch target workflow for retro stage"
```

---

### Task 10: Shim workflow changes

**Files:**
- Modify: `internal/scaffold/fullsend-repo/templates/shim-workflow.yaml`

Add two new dispatch jobs: `dispatch-retro` (automatic on PR close) and `dispatch-retro-command` (on-demand via `/fs-retro`).

- [ ] **Step 1: Read the current shim**

Read `internal/scaffold/fullsend-repo/templates/shim-workflow.yaml` to confirm current state.

- [ ] **Step 2: Add `closed` to `pull_request_target` event types**

The shim currently triggers on `[opened, synchronize, ready_for_review]`. Add `closed`:

Change:
```yaml
  pull_request_target:
    types: [opened, synchronize, ready_for_review]
```

To:
```yaml
  pull_request_target:
    types: [opened, synchronize, ready_for_review, closed]
```

- [ ] **Step 3: Add dispatch-retro job (automatic on PR close)**

Append after the `dispatch-fix-human` job:

```yaml
  dispatch-retro:
    runs-on: ubuntu-latest
    concurrency:
      group: retro-${{ github.event.pull_request.number }}
      cancel-in-progress: true
    if: >-
      github.event_name == 'pull_request_target'
        && github.event.action == 'closed'
    steps:
      - name: Build minimal payload
        id: payload
        env:
          PR_NUMBER: ${{ github.event.pull_request.number }}
          PR_HTML_URL: ${{ github.event.pull_request.html_url }}
        run: |
          set -euo pipefail
          PAYLOAD=$(jq -cn \
            --arg pn "${PR_NUMBER}" \
            --arg pu "${PR_HTML_URL}" \
            '{pull_request: {number: ($pn | tonumber), html_url: $pu}}')
          echo "json=$PAYLOAD" >> "$GITHUB_OUTPUT"
      - name: Dispatch retro stage
        env:
          GH_TOKEN: ${{ secrets.FULLSEND_DISPATCH_TOKEN }}
          EVENT_PAYLOAD: ${{ steps.payload.outputs.json }}
          EVENT_TYPE: ${{ github.event_name }}
          SOURCE_REPO: ${{ github.repository }}
          DISPATCH_REPO: ${{ github.repository_owner }}/.fullsend
        run: |
          gh workflow run dispatch.yml \
            --repo "$DISPATCH_REPO" \
            -f stage=retro \
            -f event_type="$EVENT_TYPE" \
            -f source_repo="$SOURCE_REPO" \
            -f event_payload="$EVENT_PAYLOAD"

  dispatch-retro-command:
    runs-on: ubuntu-latest
    concurrency:
      group: retro-${{ github.event.issue.number }}
      cancel-in-progress: true
    if: >-
      github.event_name == 'issue_comment'
        && (
          github.event.comment.body == '/retro'
          || startsWith(github.event.comment.body, '/retro ')
        )
        && (
          github.event.comment.author_association == 'OWNER'
          || github.event.comment.author_association == 'MEMBER'
          || github.event.comment.author_association == 'COLLABORATOR'
        )
    steps:
      - name: Build minimal payload
        id: payload
        env:
          ISSUE_NUMBER: ${{ github.event.issue.number }}
          ISSUE_HTML_URL: ${{ github.event.issue.html_url }}
          COMMENT_BODY: ${{ github.event.comment.body }}
        run: |
          set -euo pipefail
          # Determine if this is a PR or issue comment.
          # GitHub API: issue_comment events on PRs have issue.pull_request set.
          # We use gh api to get the PR HTML URL if needed.
          ORIGINATING_URL="${ISSUE_HTML_URL}"
          PAYLOAD=$(jq -cn \
            --arg in "${ISSUE_NUMBER}" \
            --arg iu "${ISSUE_HTML_URL}" \
            --arg ou "${ORIGINATING_URL}" \
            --arg cb "${COMMENT_BODY:-}" \
            '{issue: {number: ($in | tonumber), html_url: $iu},
              pull_request: {html_url: (if $ou != $iu then $ou else null end)},
              comment: {body: $cb}}')
          echo "json=$PAYLOAD" >> "$GITHUB_OUTPUT"
      - name: Dispatch retro stage
        env:
          GH_TOKEN: ${{ secrets.FULLSEND_DISPATCH_TOKEN }}
          EVENT_PAYLOAD: ${{ steps.payload.outputs.json }}
          EVENT_TYPE: ${{ github.event_name }}
          SOURCE_REPO: ${{ github.repository }}
          DISPATCH_REPO: ${{ github.repository_owner }}/.fullsend
        run: |
          gh workflow run dispatch.yml \
            --repo "$DISPATCH_REPO" \
            -f stage=retro \
            -f event_type="$EVENT_TYPE" \
            -f source_repo="$SOURCE_REPO" \
            -f event_payload="$EVENT_PAYLOAD"
```

- [ ] **Step 4: Commit**

```bash
git add internal/scaffold/fullsend-repo/templates/shim-workflow.yaml
git commit -m "feat(retro): add dispatch-retro jobs to shim workflow"
```

---

### Task 11: Update dispatch workflow description

**Files:**
- Modify: `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml`

- [ ] **Step 1: Update the stage description to include retro**

Change line 8:
```yaml
        description: 'Stage name (triage, code, review, fix)'
```

To:
```yaml
        description: 'Stage name (triage, code, review, fix, retro)'
```

- [ ] **Step 2: Commit**

```bash
git add internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml
git commit -m "feat(retro): add retro to dispatch stage list"
```

---

### Task 12: Run linter and verify

- [ ] **Step 1: Run make lint**

Run: `make lint`
Expected: All checks pass.

- [ ] **Step 2: Run Go tests**

Run: `make go-test`
Expected: All tests pass.

- [ ] **Step 3: Fix any failures and commit**

If lint or tests fail, fix the issues and commit the fixes.

---

### Task 13: Update architecture docs

**Files:**
- Modify: `docs/architecture.md` (add retro agent to the agent inventory)

Per the issue's acceptance criteria: "docs/architecture.md is updated to reflect any architectural changes."

- [ ] **Step 1: Read docs/architecture.md**

Find the section that lists agent roles or stages.

- [ ] **Step 2: Add retro agent to the agent inventory**

Add an entry for the retro agent alongside triage, code, review, and fix. Describe it as: "Retrospective analyst — examines completed or in-progress agent workflows, identifies improvement opportunities, and files proposals as GitHub issues."

- [ ] **Step 3: Commit**

```bash
git add docs/architecture.md
git commit -m "docs: add retro agent to architecture inventory"
```

---

### Task 14: Update bugfix workflow guide

**Files:**
- Modify: `docs/guides/user/bugfix-workflow.md`

The bugfix workflow guide has a "Planned" placeholder for the retro agent. Update it to reflect the actual design.

- [ ] **Step 1: Read docs/guides/user/bugfix-workflow.md**

Find the retro agent placeholder text.

- [ ] **Step 2: Update the retro agent section**

Replace the "Planned" placeholder with a description of what the retro agent does: runs automatically on PR close and on-demand via `/fs-retro`, analyzes the workflow graph, and files improvement proposals as GitHub issues.

- [ ] **Step 3: Commit**

```bash
git add docs/guides/user/bugfix-workflow.md
git commit -m "docs: update bugfix workflow with retro agent details"
```
