---
name: fix-review
description: >-
  Step-by-step procedure for addressing review feedback on an existing PR.
  Reads review comments, plans targeted fixes, implements, verifies with
  tests and linters, commits, and produces structured output for the
  post-script.
---

# Fix Review

A thorough fix reads every review comment, understands the reviewer's intent,
verifies the feedback against the actual code, and makes the smallest correct
change for each item. Jumping straight to edits without understanding context
produces fixes that introduce new issues or miss the reviewer's point.

## Tools reminder

You have the `Bash` tool for all CLI operations. **You must use it** for
verification and committing — do not skip these steps.

Commands you will need during this procedure:

- `gh pr view`, `gh pr diff` — reading PR state and diff
- `git add <file>`, `git diff`, `git commit` — staging and committing
- `make test`, `go test ./...`, `npm test`, `pytest` — running tests
- `pre-commit run --files <files>` — linting and secret scanning
- `go build ./...`, `go vet ./...` — compilation checks

Use `Read`/`Write`/`Grep`/`Glob` for file operations.

### Secret scanning

The `scan-secrets` helper is pre-installed in the sandbox image at
`/usr/local/bin/scan-secrets`. Before starting step 7, verify it exists:

```bash
command -v scan-secrets
```

If missing, **STOP**. Do not improvise a replacement or skip scanning.

## Progress markers

At the start of each major step, emit a progress marker:

```bash
echo "::notice::STEP <N>: <title>"
```

**Do this at steps 1, 2, 4, 7a, 7b, 7c, and 8.**

## Time budget

If the `TIMEOUT_SECONDS` environment variable is set, use it to manage time.

Capture the start time at the very beginning:

```bash
AGENT_START=$(date +%s)
```

Before starting pre-commit (7b), before each retry iteration (7c), and
before commit (8), check remaining time **only if `TIMEOUT_SECONDS` is set**:

```bash
if [ -n "${TIMEOUT_SECONDS:-}" ]; then
  ELAPSED=$(( $(date +%s) - AGENT_START ))
  REMAINING=$(( TIMEOUT_SECONDS - ELAPSED ))
  echo "::notice::Time check: ${ELAPSED}s elapsed, ${REMAINING}s remaining"
fi
```

Thresholds (fractions of budget):
- **Before 7b (pre-commit):** < 40% remaining → skip pre-commit
- **Before retry in 7c:** < 20% remaining → commit with disclosure
- **Before 8 (commit):** < 8% remaining → skip gitlint validation

## Process

Follow these steps in order. Do not skip steps.

### 1. Identify the PR and trigger

```bash
echo "::notice::STEP 1: Identify PR and trigger"
```

Read the environment:

```bash
echo "PR_NUMBER=${PR_NUMBER}"
echo "TRIGGER_SOURCE=${TRIGGER_SOURCE}"
echo "FIX_ITERATION=${FIX_ITERATION:-1}"
```

- `PR_NUMBER` — which PR to fix (required)
- `TRIGGER_SOURCE` — `bot` (review agent) or `human` (`/fix` command)
- `HUMAN_INSTRUCTION` — the human's instruction text (only when `TRIGGER_SOURCE=human`)
- `FIX_ITERATION` — which iteration of the review→fix loop this is

If `PR_NUMBER` is not set, stop.

Fetch the PR metadata:

```bash
gh pr view "${PR_NUMBER}" --json number,title,body,headRefName,baseRefName,state,files,labels
```

If the PR is closed or merged, stop.

### 2. Gather review feedback

```bash
echo "::notice::STEP 2: Gather review feedback"
```

First, fetch the current PR diff so you know exactly what code is on the branch:

```bash
gh pr diff "${PR_NUMBER}"
```

**If TRIGGER_SOURCE is `bot`:**

**Step 2a — Read the pre-fetched review body:**

The review agent posts all of its findings as a single `gh pr review --body`
comment. The workflow pre-fetches this review body before the sandbox starts
and places it at a known path. Read it:

```bash
REVIEW_BODY_FILE="/tmp/workspace/review-body.txt"
if [ ! -s "${REVIEW_BODY_FILE}" ]; then
  echo "::error::No review body found at ${REVIEW_BODY_FILE}"
  # Fallback: the file may not exist in local testing; check env.
fi
cat "${REVIEW_BODY_FILE}"
```

The file contains the complete review. This is your primary input. You do
NOT need to call `gh api` to fetch it — the workflow already did that on the
runner (where the API token has appropriate scope).

**Step 2b — Understand the review before acting:**

Read the entire review body carefully before planning any fixes. Write down:

1. **The reviewer's overall concern.** What is the high-level theme? Is the
   reviewer asking for a pattern change, a correctness fix, a style
   adjustment, or a rethinking of the approach? Summarize it in one sentence.
2. **Individual findings.** Parse the review body for distinct issues. The
   review agent typically structures findings with file paths, line references,
   and remediation suggestions. Extract each finding into your action list.
3. **Whether findings are independent or interconnected.** Multiple findings
   may be symptoms of one root-cause issue. If so, the correct fix addresses
   the root cause — not each symptom separately, which can produce
   contradictory or redundant changes.

**Step 2c — Build your action list:**

For each finding extracted from the review body, record:
- `finding` — a short label for the issue (e.g., "missing nil check in handler")
- `path` — file path referenced in the finding
- `description` — the reviewer's feedback text
- `related_findings` — other findings that share a root cause (if any)

Ignore any content wrapped in `<details>` blocks — these are collapsed
summaries from previous iterations and have already been addressed.

**Important:** The fix agent does not read or respond to inline PR comments.
Inline comments are not part of the review agent's output. If humans need to
direct the fix agent, they use the `/fix` command.

**If TRIGGER_SOURCE is `human`:**

The human instruction is in `HUMAN_INSTRUCTION`. This is your primary directive.
The PR diff you already fetched provides context. The human instruction
supersedes any prior bot review feedback. If the human's instruction is
vague, use the PR diff and file list to infer the most conservative
interpretation.

### 3. Discover repo conventions

Before writing any code, understand how this repository works:

1. Read `CLAUDE.md`, `CONTRIBUTING.md`, `AGENTS.md` if they exist.
2. Discover test and lint commands from `Makefile`, `package.json`, etc.
3. Check for linter config (`.golangci.yml`, `.pre-commit-config.yaml`, etc.).

Determine:
- Test command (e.g., `make test`, `go test ./...`)
- Lint command (e.g., `make lint`, `pre-commit run --files`)
- Commit conventions (message format, signing)

### 4. Plan fixes

```bash
echo "::notice::STEP 4: Plan fixes"
```

**Start from the whole-review theme**, not from individual findings. Your
plan should address the reviewer's overarching concern first, then confirm
that each finding is satisfied by that plan. This prevents the common
failure mode of making independent micro-fixes that individually address
each finding but collectively don't satisfy the reviewer's actual intent.

For related findings (from step 2c), plan a single coherent fix for the
group. For standalone findings, plan individually.

For each finding or group, determine:

1. **Is the feedback valid?** Read the code at the referenced path and line.
   Does the issue the reviewer describes actually exist?

2. **What is the minimal fix?** Identify the smallest change that addresses
   the feedback without side effects. For grouped findings, the minimal fix
   addresses the root cause — not each symptom separately.

3. **Should I disagree?** If the feedback is incorrect, out of scope for this
   PR, or would introduce a regression, prepare a reasoned disagreement.

**Strategy escalation:** If `FIX_ITERATION` is set and exceeds
`STRATEGY_ESCALATION_THRESHOLD` (default: 3), the same approach has failed
multiple times. Before planning, read the PR's commit history to understand
what was already tried:

```bash
git log --oneline "origin/${BASE_BRANCH}..HEAD" | head -20
```

Try a fundamentally different approach: different algorithm, different data
structure, different error handling strategy. Note the strategy change in
your structured output.

### 5. Read affected code

For each file referenced by review findings:

1. Read the full file (not just the reviewed lines) to understand context.
2. Read any related test files.
3. Read imports, types, and call sites affected by the planned changes.

### 6. Implement fixes

For each finding, in the order they appear in the file (top-down):

1. Make the code change that addresses the feedback.
2. Follow existing patterns. If the repo uses a specific error handling idiom,
   match it.
3. Do not introduce new dependencies unless the review explicitly asks for it.
4. Write or update tests if the fix changes behavior.

**Scope guardrail:** Your changes must be strictly limited to addressing
review feedback. Do not:
- Refactor code the reviewer did not mention
- Add features the reviewer did not request
- Fix bugs the reviewer did not flag
- Improve documentation unless the reviewer asked

### 7. Verify

**7a. Secret scan — MANDATORY FIRST STEP**

```bash
echo "::notice::STEP 7a: Secret scan"
```

```bash
scan-secrets <files-you-modified>
```

If secrets are detected: hard stop. Remove them, re-scan.

**7b. Pre-commit hooks — best-effort optimization**

```bash
echo "::notice::STEP 7b: Pre-commit hooks"
```

Same rules as the code agent:
- Maximum 2 pre-commit runs total across the entire session.
- Pre-format your code before running pre-commit.
- If the second run still fails, log the error and move on.

```bash
test -f .pre-commit-config.yaml && pre-commit run --files <all-changed-files>
```

**7c. Tests and linters — MANDATORY**

```bash
echo "::notice::STEP 7c: Tests and linters"
```

Run the test suite covering the code you changed:

```bash
make test        # or: go test ./..., npm test, pytest, etc.
make lint        # or: golangci-lint run, eslint, ruff, etc.
```

If tests fail due to your code:
1. Read the failure output carefully.
2. Fix the issue.
3. Re-run secret scan (7a) and then tests (7c).
4. Do NOT re-run pre-commit during retries.

The retry limit is read from `MAX_RETRIES` (default: 1).

**7d. Self-review**

```bash
git diff
```

Read every line. Check for:
- Changes that don't trace to a review comment
- Debug prints, commented-out code, TODO comments
- Secret material
- Protected-path files

### 8. Commit

```bash
echo "::notice::STEP 8: Commit"
```

**8a. Stage files**

```bash
git add path/to/file1 path/to/file2
```

Only include files you deliberately modified.

**8b. Scan staged content**

```bash
git diff --cached --stat
scan-secrets --staged
```

**8c. Commit**

The commit message must:
- Follow the repo's commit convention (discovered in step 3).
- Reference the PR number and summarize what was fixed.
- Note any disagreements with review feedback.

```bash
git commit -s -m "fix: address review feedback on PR #${PR_NUMBER}

<summary of changes per review comment>

Addresses review feedback on #${PR_NUMBER}"
```

Validate with gitlint if available:

```bash
which gitlint &>/dev/null && gitlint --commit HEAD
```

### 9. Produce structured output

**This step is MANDATORY.** The post-script cannot function without it.

Write a JSON file to `$FULLSEND_OUTPUT_DIR/fix-result.json`:

```json
{
  "pr_number": 42,
  "trigger_source": "bot",
  "iteration": 1,
  "actions": [
    {
      "type": "fix",
      "finding": "missing nil check in HandleRequest",
      "path": "pkg/handler.go",
      "description": "Added nil check for request parameter as requested"
    },
    {
      "type": "disagree",
      "finding": "refactor HandleRequest to use strategy pattern",
      "path": "pkg/handler.go",
      "reason": "The suggested refactor is out of scope for this PR and would change the public API"
    }
  ],
  "decision_points": [
    {
      "description": "Chose to use error wrapping instead of a new error type",
      "alternatives": ["Custom error type", "Sentinel error"],
      "rationale": "Matches existing error handling pattern in this package"
    }
  ],
  "summary": "Addressed 2 of 3 review findings. Disagreed with 1 (out-of-scope refactor).",
  "strategy_change": null,
  "tests_passed": true,
  "files_changed": ["pkg/handler.go", "pkg/handler_test.go"]
}
```

**Action types:**

- `fix` — You fixed the code per the reviewer's feedback. The post-script
  includes this in the summary comment.
- `disagree` — You determined the feedback is incorrect or out of scope. The
  post-script includes your reason in the summary. The reviewer can insist
  in the next review cycle.

**Required fields:** `pr_number`, `trigger_source`, `actions`, `summary`,
`tests_passed`, `files_changed`.

Write the file using `Bash`:

```bash
cat > "${FULLSEND_OUTPUT_DIR}/fix-result.json" << 'FIXEOF'
{ ... your JSON ... }
FIXEOF
```

Validate the JSON is well-formed:

```bash
python3 -c "import json; json.load(open('${FULLSEND_OUTPUT_DIR}/fix-result.json'))" \
  || jq . "${FULLSEND_OUTPUT_DIR}/fix-result.json" > /dev/null
```

## Partial work

If you hit a token limit before addressing all findings: commit what
you have and produce structured output documenting which findings were
addressed and which remain. The post-script will communicate this to the
reviewer, and the next fix iteration will pick up the remaining items.

## Constraints

The agent definition (`agents/fix.md`) is the authoritative list of
prohibitions. This skill does not restate them. If a step in this skill
appears to conflict with the agent definition, the agent definition wins.
