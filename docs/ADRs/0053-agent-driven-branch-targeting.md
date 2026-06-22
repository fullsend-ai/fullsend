---
title: "53. Agent-driven branch targeting for the code agent"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - code-agent
  - post-script
  - branch-targeting
  - structured-output
---

# 53. Agent-driven branch targeting for the code agent

Date: 2026-06-17

## Status

Accepted

## Context

The code agent's `reusable-code.yml` hardcodes `TARGET_BRANCH: main` in the
workflow step env. The post-script (`post-code.sh`) uses this value for
`git merge-base` and `gh pr create --base`. For any repository whose default
branch is not `main`, PR creation fails silently.

The fix agent already handles this correctly by resolving the PR's base
branch dynamically from the existing PR metadata. The code agent has no
equivalent — it creates new PRs from issues, so there is no existing PR to
query.

Beyond the immediate bug, the current design has two deeper problems:

1. **The agent understands the target better than the workflow.** When an
   issue says "set up builds on the 3.18 branch," the agent reads and
   understands that context. Hardcoding the branch in an env var forces the
   workflow to guess what the agent already knows.

2. **Business logic in GitHub Actions workflows is not portable.** Fullsend
   aims to run agents in environments beyond GitHub Actions (e.g., Kubernetes
   pods). Branch-targeting logic embedded in workflow YAML becomes technical
   debt that must be disentangled during that migration.

The post-script is the right place for branch policy enforcement. It already
serves as the security boundary — it runs on the runner (not in the sandbox),
holds the write token, and performs secret scanning and pre-commit validation
before any push. Adding branch validation here follows the established
security model.

## Options

### Option A: Workflow-level auto-detection

Add a step in `reusable-code.yml` that queries the GitHub API for the repo's
default branch and replaces the hardcoded `main`. Optionally parse
`--branch <ref>` from the `/fs-code` comment.

**Pros:** Smallest change (one file). Fixes the immediate bug.
**Cons:** Business logic in the workflow. Not portable. The agent still
cannot choose a branch different from the default without comment-level
syntax.

### Option B: Agent-driven targeting with post-script policy gate (recommended)

The agent writes its chosen `target_branch` to a structured output file
(`code-result.json`). The post-script reads the agent's choice, validates it
against a `CODE_ALLOWED_TARGET_BRANCHES` env var, and falls back to the
auto-detected repo default branch when no output is provided.

**Pros:** Agent decides intent based on issue context. Post-script enforces
policy. No business logic in the workflow. Follows the existing structured
output pattern used by fix, triage, and review agents. Backward compatible.
**Cons:** Requires a new output schema for the code agent.

### Option C: Git commit trailer convention

The agent adds a `Target-Branch: <ref>` trailer to its commit message. The
post-script parses it from `git log`.

**Pros:** No new schema.
**Cons:** Fragile parsing. No validation tooling. Agent may forget the
trailer. Does not follow established structured output patterns.

## Decision

Adopt Option B: agent-driven targeting with post-script policy enforcement.

### Code agent output schema

Add `schemas/code-result.schema.json`:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Code Agent Result",
  "type": "object",
  "required": ["target_branch"],
  "additionalProperties": false,
  "properties": {
    "target_branch": {
      "type": "string",
      "description": "Branch the PR should target.",
      "pattern": "^[a-zA-Z0-9._/-]+$"
    }
  }
}
```

The agent determines the target branch from the issue context and writes
`code-result.json` to `$FULLSEND_OUTPUT_DIR`.

### Post-script policy gate

Replace the `TARGET_BRANCH="${TARGET_BRANCH:-main}"` line in `post-code.sh`
with branch resolution logic:

1. Read `target_branch` from `code-result.json` (agent's choice).
2. Auto-detect the repo's default branch via `gh api`.
3. If the agent specified a branch, validate it against
   `CODE_ALLOWED_TARGET_BRANCHES` (comma-separated list, or `*` for any).
   When unset, only the auto-detected default branch is allowed.
4. If the agent did not write `code-result.json`, use the auto-detected default.
5. Fall back to `main` if the API call fails.

### Harness changes

Update `harness/code.yaml`:

- Add `FULLSEND_OUTPUT_SCHEMA` and `FULLSEND_OUTPUT_FILE` to `runner_env`
  (wiring up structured output for the code agent).
- Add `CODE_ALLOWED_TARGET_BRANCHES` to `runner_env` (policy enforcement).
- Remove `TARGET_BRANCH` from `runner_env` (replaced by post-script logic).

### Workflow changes

Remove `TARGET_BRANCH: main` from the "Run code agent" step env in
`reusable-code.yml`. No replacement env var is needed in the workflow — the
post-script handles all branch logic. Repos that want to restrict allowed
branches configure `CODE_ALLOWED_TARGET_BRANCHES` via their harness override
(`runner_env`), not in the workflow YAML.

## Consequences

**What becomes easier:**

- Repos with non-`main` default branches work out of the box. No
  configuration required — the post-script auto-detects the default branch.
- Agents can target the correct branch based on issue context (e.g., "set up
  builds on redhat-3.18" results in a PR targeting `redhat-3.18`).
- Repos can restrict which branches agents may target by setting
  `CODE_ALLOWED_TARGET_BRANCHES` in their harness override.
- Branch-targeting logic lives in the portable post-script, not in
  GitHub-Actions-specific YAML.

**What becomes harder or changes:**

- The code agent now has a structured output contract. Agent definitions must
  be updated to instruct the agent to write `code-result.json`.
- Repos that override `harness/code.yaml` via `.fullsend/customized/` must
  update their override to include the new `runner_env` fields. Specifically,
  replace `TARGET_BRANCH: "${TARGET_BRANCH}"` with the new keys:
  ```yaml
  runner_env:
    CODE_ALLOWED_TARGET_BRANCHES: "${CODE_ALLOWED_TARGET_BRANCHES}"
    FULLSEND_OUTPUT_SCHEMA: ${FULLSEND_DIR}/schemas/code-result.schema.json
    FULLSEND_OUTPUT_FILE: code-result.json
  ```
  Customized harnesses that still reference `${TARGET_BRANCH}` will fail
  harness validation because the workflow no longer provides the variable.
- The `TARGET_BRANCH` env var is removed. Any tooling that reads it directly
  (outside the post-script) must be updated.

**Backward compatibility:**

- If the agent does not write `code-result.json` (e.g., older agent
  definitions), the post-script falls back to the auto-detected default
  branch. This is strictly better than the current `main` hardcode.
- If `CODE_ALLOWED_TARGET_BRANCHES` is not set, only the auto-detected
  default branch is allowed. Safe by default.
