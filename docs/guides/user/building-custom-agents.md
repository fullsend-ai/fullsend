# Building custom agents

This guide walks through creating a new custom agent from scratch on a per-repo fullsend installation.

For customizing existing agents (overriding harnesses, skills, or policies), see [Customizing agents](customizing-agents.md).

## Prerequisites

- A GitHub repository with fullsend [installed](../getting-started/configuring-github.md).

## Architecture overview

A custom agent is composed of six parts:

```
.fullsend/customized/
  agents/          # Agent prompt (Markdown with YAML frontmatter)
  harness/         # Execution config (sandbox image, host files, env vars)
  policies/        # Network and filesystem sandbox policies
  schemas/         # JSON Schema for validating agent output
  scripts/         # Pre/post scripts that run OUTSIDE the sandbox
  skills/          # Knowledge documents mounted into the sandbox
```

At build time, the workflow layers these customized files on top of the upstream fullsend defaults. Your files override the defaults — anything you don't customize uses the standard fullsend configuration. See [Customizing agents — Layered Configuration Resolution](customizing-agents.md#layered-configuration-resolution) for details on how the layering works.

The key security invariant: agents run inside an untrusted [sandbox](../../glossary.md#sandbox) with no credentials. Pre-scripts fetch data *before* the sandbox starts; post-scripts act on agent output *after* the sandbox exits. Agents never have direct write access to external systems. See the [security threat model](../../problems/security-threat-model.md) for the full trust model.

## Step 1: Write the agent prompt

Create `.fullsend/customized/agents/my-agent.md`:

````markdown
---
name: my-agent
description: >-
  One-line description of what this agent does.
tools: Bash(gh,jq,curl,python3,find,ls,cat,head,grep,wc,tree)
model: opus
skills:
  - my-skill
disallowedTools: >-
  Bash(git push *), Bash(git push),
  Bash(gh issue create *), Bash(gh issue edit *)
---

# My Agent

You are a [role description]. Your job is to [purpose].

## Inputs

Environment variables set by the pre-script:

- `MY_INPUT_FILE` — path to input data JSON
- `TARGET_REPO_DIR` — path to target repository checkout
- `FULLSEND_OUTPUT_DIR` — where to write your result

## Process

### Phase 1: Understand the input

```bash
echo "::notice::PHASE 1: Parse input"
cat "$MY_INPUT_FILE" | jq .
```

[Describe what the agent should extract and how to reason about it]

### Phase 2: Do work

[Describe the agent's main work — analysis, research, generation, etc.]

### Phase 3: Write result

Write to `$FULLSEND_OUTPUT_DIR/agent-result.json`:

```json
{
  "status": "complete",
  "result": { ... }
}
```

## Constraints

- You do NOT write code, create issues, or modify anything.
  Your only output is the JSON result file.
- The JSON must be valid and parseable. No markdown fences.
````

### Key frontmatter fields

| Field | Purpose |
|-------|---------|
| `name` | Must match the filename (without `.md`) |
| `tools` | Bash commands the agent can run. Restrict to what's needed. |
| `model` | LLM model (`opus`, `sonnet`, etc.) |
| `skills` | [Skill](../../glossary.md#skill) directories to mount (relative to `customized/skills/`) |
| `disallowedTools` | Bash patterns the agent is forbidden from running |

### Design principles

1. **Agent writes JSON, scripts do actions.** The agent's only output is a structured JSON file. All side effects (creating issues, posting comments, calling APIs) happen in post-scripts.

2. **Name specific things.** Don't say "add caching." Say "use `casbin` v2.82.0 from `go.mod` with the RBAC model adapter in `pkg/api/middleware/`."

3. **Confidence model.** Have the agent assess its own confidence and branch: act when confident, ask when uncertain.

## Step 2: Define the harness

Create `.fullsend/customized/harness/my-agent.yaml`:

```yaml
agent: customized/agents/my-agent.md
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
policy: customized/policies/my-agent.yaml

host_files:
  # GCP credentials for Vertex AI (required for model access)
  - src: env/gcp-vertex.env
    dest: /tmp/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/workspace/.gcp-credentials.json
  - src: ${GCP_OIDC_TOKEN_FILE}
    dest: /tmp/workspace/.gcp-oidc-token
    optional: true
  # Your custom input files (written by pre-script)
  - src: /tmp/workspace/my-input.json
    dest: /tmp/workspace/my-input.json
    optional: true

skills:
  - customized/skills/my-skill

pre_script: customized/scripts/pre-my-agent.sh

validation_loop:
  script: scripts/validate-output-schema.sh
  max_iterations: 2

post_script: customized/scripts/post-my-agent.sh

runner_env:
  MY_VAR: "${MY_VAR}"
  ISSUE_KEY: "${ISSUE_KEY}"
  GH_TOKEN: "${GH_TOKEN}"  # auto-minted in CI when --mint-url is provided
  FULLSEND_OUTPUT_SCHEMA: ${FULLSEND_DIR}/customized/schemas/my-agent-result.schema.json

timeout_minutes: 20

# Optional: enable runtime skill fetching (ADR-0038 Phase 4)
# allowed_remote_resources:
#   - https://github.com/org/skills/
# allow_runtime_fetch: true
# max_runtime_fetches: 10
```

See [Customizing agents — Harness YAML Structure](customizing-agents.md#harness-yaml-structure) for the full field reference (including optional `security`, `providers`, `plugins`, and runtime fetch blocks).

The key pattern to understand is how data flows into the sandbox through `host_files`:

1. **Pre-script** runs on the runner and writes files to `/tmp/workspace/`.
2. **Harness** copies those files into the sandbox via `host_files`.
3. **Agent** reads them inside the sandbox.

The agent never has direct access to credentials. The pre-script uses credentials to fetch data, writes it to a file, and the harness copies the file (not the credentials) into the sandbox.

## Step 3: Define the sandbox policy

Create `.fullsend/customized/policies/my-agent.yaml`:

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
  # Required: Vertex AI for model access
  vertex_ai:
    name: vertex-ai
    endpoints:
      - host: "*.googleapis.com"
        port: 443
        protocol: rest
        enforcement: enforce
        access: read-write
      - host: "api.anthropic.com"
        port: 443
        protocol: rest
        enforcement: enforce
        access: read-write
    binaries:
      - path: "**/claude"
      - path: "**/node"
  # Optional: GitHub API access (if agent needs it)
  github_api:
    name: github-api
    endpoints:
      - host: "api.github.com"
        port: 443
        protocol: rest
        enforcement: enforce
        access: read-only
    binaries:
      - path: "**/gh"
      - path: "**/curl"
```

### Policy design principles

- **Vertex AI is always required** — the agent needs it to talk to the LLM.
- **Add network access only for what the agent needs.** If the agent doesn't need web search, don't allow it.
- **Use `binaries` to restrict which programs can access each endpoint.** This prevents the agent from using unexpected tools to exfiltrate data.
- **Never allow Jira/internal APIs from the sandbox.** All Jira reads happen in pre-scripts; all Jira writes happen in post-scripts.

## Step 4: Define the output schema

Create `.fullsend/customized/schemas/my-agent-result.schema.json`:

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "My Agent Result",
  "type": "object",
  "required": ["status"],
  "properties": {
    "status": {
      "type": "string",
      "enum": ["complete", "needs_input", "error"]
    },
    "result": {
      "type": "object"
    },
    "comment": {
      "type": "string",
      "maxLength": 4000
    }
  }
}
```

The schema is enforced by `validation_loop` in the harness. If the agent's output doesn't match, it's re-invoked with the validation error and asked to fix it.

## Step 5: Write pre and post scripts

### Pre-script (data fetching)

`.fullsend/customized/scripts/pre-my-agent.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

WORKSPACE="/tmp/workspace"
mkdir -p "$WORKSPACE"

if [[ "${ISSUE_SOURCE}" == "jira" ]]; then
  AUTH=$(printf '%s:%s' "$JIRA_EMAIL" "$JIRA_API_TOKEN" | base64 -w0)
  curl -sSf -H "Authorization: Basic $AUTH" \
    "https://${JIRA_HOST}/rest/api/3/issue/${ISSUE_KEY}" \
    > "$WORKSPACE/my-input.json"

elif [[ "${ISSUE_SOURCE}" == "github" ]]; then
  gh issue view "$ISSUE_KEY" --repo "$REPO_FULL_NAME" \
    --json number,title,body,labels,comments \
    > "$WORKSPACE/my-input.json"
fi

echo "Pre-script complete."
```

The pre-script has full credentials on the trusted runner. It fetches data from external systems and writes it to files that the harness copies into the sandbox. Credentials never enter the sandbox.

### Post-script (action execution)

`.fullsend/customized/scripts/post-my-agent.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

RESULT_FILE=""
for dir in iteration-*/output; do
  if [[ -f "${dir}/agent-result.json" ]]; then
    RESULT_FILE="${dir}/agent-result.json"
  fi
done

if [[ -z "${RESULT_FILE}" ]]; then
  echo "ERROR: agent-result.json not found"
  exit 1
fi

# Validate JSON structure before extracting fields.
# The agent runs in an untrusted sandbox — treat its output as untrusted input.
if ! jq empty "${RESULT_FILE}" 2>/dev/null; then
  echo "ERROR: agent-result.json is not valid JSON"
  exit 1
fi

STATUS=$(jq -r '.status // ""' "${RESULT_FILE}")
COMMENT=$(jq -r '.comment // ""' "${RESULT_FILE}")

# Validate status against known values before acting on it.
case "${STATUS}" in
  complete)
    echo "Agent completed successfully"
    ;;
  needs_input)
    echo "Agent needs more information"
    ;;
  *)
    echo "ERROR: Unknown or missing status '${STATUS}'"
    exit 1
    ;;
esac
```

### Post-script security considerations

The post-script runs on the trusted runner with full credentials, but reads output produced by the untrusted sandbox. Treat agent output as untrusted input:

- **Validate JSON structure** before extracting fields (`jq empty` catches malformed output).
- **Validate field values against an allowlist** (the `case` statement above) rather than passing them to shell commands or APIs unchecked.
- **Never interpolate agent output into shell commands** without quoting. Use `jq -r` to extract values into variables, then use `"${VAR}"` (double-quoted) everywhere.
- **Limit string lengths** in the JSON schema (`maxLength`) to prevent resource exhaustion when posting to external APIs.

## Step 6: Create skills (optional)

[Skills](../../glossary.md#skill) are Markdown documents mounted into the sandbox that provide domain knowledge the agent can reference. See [Customizing agents — Adding a Custom Skill](customizing-agents.md#adding-a-custom-skill) for how to create one.

Place your skill at `.fullsend/customized/skills/my-skill/SKILL.md`, then reference it in both the agent frontmatter (`skills: [my-skill]`) and the harness (`skills: [customized/skills/my-skill]`).

## Step 7: Create the GitHub Actions workflow

Create `.github/workflows/my-agent.yml`:

```yaml
name: fullsend-my-agent

permissions:
  contents: read
  id-token: write

on:
  workflow_dispatch:
    inputs:
      issue_key:
        description: 'Issue key'
        required: true
        type: string
      issue_source:
        description: 'Issue source: jira or github'
        required: true
        type: string
        default: 'github'

concurrency:
  group: my-agent-${{ inputs.issue_key || 'unknown' }}
  cancel-in-progress: true

jobs:
  run:
    runs-on: ubuntu-24.04
    steps:
      - name: Checkout repository for harness reading
        uses: actions/checkout@v6

      - name: Checkout target repo
        uses: actions/checkout@v6
        with:
          path: target-repo

      - name: Checkout upstream defaults
        uses: actions/checkout@v6
        with:
          repository: fullsend-ai/fullsend
          ref: v0
          path: .defaults
          sparse-checkout: |
            internal/scaffold/fullsend-repo/

      - name: Prepare workspace (upstream defaults + repo overrides)
        run: |
          set -euo pipefail
          SRC=".defaults/internal/scaffold/fullsend-repo"
          LAYERED_DIRS="agents skills schemas harness policies scripts env"
          for dir in ${LAYERED_DIRS}; do
            if [[ -d "${SRC}/${dir}" ]]; then
              mkdir -p ".fullsend/${dir}"
              cp -r "${SRC}/${dir}/." ".fullsend/${dir}/"
            fi
          done
          for dir in ${LAYERED_DIRS}; do
            if [[ -d ".fullsend/customized/${dir}" ]]; then
              find ".fullsend/customized/${dir}" -type f ! -name '.gitkeep' -print0 \
                | while IFS= read -r -d '' f; do
                    rel="${f#".fullsend/customized/"}"
                    mkdir -p ".fullsend/$(dirname "${rel}")"
                    cp "${f}" ".fullsend/${rel}"
                  done
            fi
          done
          rm -rf .defaults

      - name: Authenticate to GCP via WIF
        uses: google-github-actions/auth@v3
        with:
          workload_identity_provider: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}
          project_id: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}

      - name: Prepare sandbox credentials
        run: bash .fullsend/scripts/prepare-sandbox-credentials.sh

      - name: Install fullsend CLI
        uses: fullsend-ai/fullsend@v0
        with:
          agent: __install_only__

      - name: Run my-agent
        env:
          ISSUE_KEY: ${{ inputs.issue_key }}
          ISSUE_SOURCE: ${{ inputs.issue_source || 'github' }}
          MY_VAR: ABCD
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          ANTHROPIC_VERTEX_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}
          CLOUD_ML_REGION: ${{ vars.FULLSEND_GCP_REGION }}
        run: |
          set -euo pipefail
          mkdir -p "$GITHUB_WORKSPACE/output"
          fullsend run my-agent \
            --fullsend-dir "$GITHUB_WORKSPACE/.fullsend" \
            --target-repo "$GITHUB_WORKSPACE/target-repo" \
            --output-dir "$GITHUB_WORKSPACE/output"

      - name: Upload artifacts
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: fullsend-my-agent
          path: ${{ github.workspace }}/output
```

### Critical workflow steps

1. **Checkout target repo** — `fullsend run` requires `--target-repo` pointing to a separate checkout of the repository the agent will work on. Without this, fullsend may overwrite output files.

2. **Prepare workspace (upstream defaults + repo overrides)** — the fullsend CLI expects files in `.fullsend/harness/`, `.fullsend/agents/`, etc. (not `.fullsend/customized/`). The layering step copies upstream defaults first, then overlays your customizations on top.

3. **Authenticate to GCP via WIF** — provides short-lived credentials for Vertex AI. Uses Workload Identity Federation (no service account keys).

4. **Prepare sandbox credentials** — the WIF auth creates a credential config that references GitHub's OIDC endpoint, which isn't reachable from inside the sandbox. This script pre-fetches the OIDC token and rewrites the config to use a file-based source.

5. **`ANTHROPIC_VERTEX_PROJECT_ID` and `CLOUD_ML_REGION`** — must be in the workflow `env` block so the `gcp-vertex.env` file (copied into the sandbox with `expand: true`) resolves correctly.

6. **All `runner_env` variables** must appear in the workflow `env` block. If your harness references `MY_VAR: "${MY_VAR}"`, the workflow must set `MY_VAR`.

## Step 8: Trigger the agent

The workflow above uses `workflow_dispatch`, which means you trigger it manually:

- **From the GitHub UI:** Actions → fullsend-my-agent → Run workflow → fill in `issue_key` and `issue_source`.
- **From the CLI:** `gh workflow run my-agent.yml -f issue_key=123 -f issue_source=github`

### Slash-command dispatch (optional)

If you want slash-command triggers (e.g., `/my-command` on a GitHub issue), create a dispatch workflow. This requires adding `actions: write` and `issues: write` permissions:

```yaml
name: my-agent-dispatch

permissions:
  actions: write
  contents: read
  issues: write

on:
  issue_comment:
    types: [created]

jobs:
  dispatch:
    if: >-
      github.event.comment.user.type != 'Bot'
      && startsWith(github.event.comment.body, '/my-command')
    runs-on: ubuntu-24.04
    steps:
      - name: Dispatch
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          ISSUE_NUMBER: ${{ github.event.issue.number }}
          REPO: ${{ github.repository }}
        run: |
          gh workflow run my-agent.yml \
            --repo "$REPO" \
            -f issue_key="$ISSUE_NUMBER" \
            -f issue_source="github"

          gh api "repos/${REPO}/issues/comments/${{ github.event.comment.id }}/reactions" \
            -f content="rocket" --silent 2>/dev/null || true
```

## Quick troubleshooting

| Symptom | Likely cause |
|---------|-------------|
| Agent crashes immediately (0s runtime) | Sandbox can't authenticate to Vertex AI. Verify `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION`, and that `prepare-sandbox-credentials.sh` ran after the WIF auth step. |
| "Harness file not found" | The fullsend CLI looks for `.fullsend/harness/my-agent.yaml`, not `customized/`. Verify the "Prepare workspace" step is layering files correctly. |
| Agent can't find input files | Ensure pre-script output paths match `host_files` entries in the harness. |
| Network policy blocks requests | Check `openshell-sandbox.log` in artifacts for `BLOCKED` entries. Add the endpoint to the policy. |
| Schema validation fails twice | Check the agent transcript in artifacts to see what it produced vs. what the schema expected. |

## File checklist

When creating a new agent, you need these files:

```
.fullsend/customized/
  agents/my-agent.md                     # Agent prompt
  harness/my-agent.yaml                  # Execution config
  policies/my-agent.yaml                 # Sandbox policy
  schemas/my-agent-result.schema.json    # Output validation
  scripts/pre-my-agent.sh                # Data fetching
  scripts/post-my-agent.sh               # Action execution
  skills/my-skill/SKILL.md               # Domain knowledge (optional)

.github/workflows/
  my-agent.yml                           # GitHub Actions workflow
  my-agent-dispatch.yml                  # Slash command trigger (optional)
```

## Reference

- [Customizing agents](customizing-agents.md) — override existing agent harnesses, skills, and policies
- [Bugfix workflow](bugfix-workflow.md) — how the built-in agents work together end to end
- [Getting Started](../getting-started/README.md) — prerequisite: admin setup guide
- [Architecture overview](../../architecture.md) — component vocabulary and execution stack
- [Security threat model](../../problems/security-threat-model.md) — how fullsend thinks about security
- [ADR 0035: Layered Content Resolution](../../ADRs/0035-layered-content-resolution.md) — how customized files override upstream defaults
