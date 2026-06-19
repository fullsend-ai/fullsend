# Review Agent Contextual Labels Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable the review agent to apply contextual labels (e.g., `area/api`, `priority/high`) to PRs using the same `issue-labels` skill as the triage agent.

**Architecture:** Generalize the existing `issue-labels` skill to be agent-agnostic, add it to the review agent's harness/definition, extend the review result schema with an optional `label_actions` field, and add label processing to the review post-script mirroring the triage post-script's implementation.

**Tech Stack:** Bash (post-scripts), JSON Schema, Markdown (agent definitions, skills, docs)

---

### Task 1: Generalize the issue-labels skill

**Files:**
- Modify: `internal/scaffold/fullsend-repo/skills/issue-labels/SKILL.md`

- [ ] **Step 1: Read the current skill file**

Read `internal/scaffold/fullsend-repo/skills/issue-labels/SKILL.md` to confirm current contents match expectations.

- [ ] **Step 2: Update the skill**

Replace the file with the generalized version. Changes:
- Description: "triaged issues" → "issues and pull requests"
- Remove the entire "Control labels (do NOT recommend these)" section (lines 14-24). Post-scripts enforce this server-side.
- Title area: "issue being triaged" → "issue or pull request"
- Step 2: add a note to skip for PRs

```markdown
---
name: issue-labels
description: >-
  Discover repository labels and recommend contextual labels to add or remove
  on issues and pull requests. Produces label_actions in the agent result JSON.
---

# Issue Labels

Recommend contextual labels for the issue or pull request being processed.
These are labels that describe the domain, area, priority, or other
team-specific dimensions -- NOT control labels used by agent pipelines.

Control labels are managed by each agent's post-script and will be refused
server-side if recommended. You do not need to track which labels are
control labels -- just recommend what fits and the pipeline will filter.

## Step 1: Discover available labels

```
gh label list --repo OWNER/REPO --json name,description --limit 100
```

If the repo has no labels beyond those used by agent pipelines, skip labeling
entirely -- do not emit `label_actions`.

## Step 2: Check for GitHub issue types

GitHub issue types (Bug, Feature, Task, etc.) classify issues at a higher level
than labels. **Skip this step when labeling a pull request** -- GitHub issue
types do not apply to PRs.

If the repo uses issue types, do **not** recommend labels that
duplicate the issue type -- e.g., do not add `bug` or `type/bug` when the issue
already has the Bug type.

Query the current issue to check for an issue type:
```
gh issue view NUMBER --repo OWNER/REPO --json type
```

If the `.type` field is non-null, the repo uses issue types. In that case:
- Do not recommend labels whose names match or overlap with the issue type
  (e.g., `bug`, `type/bug`, `enhancement`, `feature`, `type/feature`).
- Area, priority, component, and other non-type labels are still appropriate.

## Step 3: Research labeling conventions

Spawn a sub-agent to investigate how labels have been applied to recent issues.
The sub-agent should:

1. Query recent closed and open issues:
   ```
   gh issue list --repo OWNER/REPO --state all --json number,title,labels --limit 50
   ```
2. Analyze which labels appear together and in what contexts.
3. Return a short summary (under 500 characters) describing the labeling
   conventions observed -- which labels are commonly used and any patterns in
   how they are applied.

Do not dump raw issue data into the parent context. Only use the sub-agent's
summary to inform your recommendations.

## Step 4: Recommend labels

Based on the content, the available labels, and the observed conventions:

- Recommend labels to **add** if they clearly apply.
- Recommend labels to **remove** if stale labels from a prior run no longer
  apply.
- If no labels clearly apply, do not emit `label_actions` at all. Silence is
  better than noise.
- Only recommend labels that exist in `gh label list`. Do not invent labels.

## Output

Include your recommendations in the `label_actions` field of the agent result
JSON:

```json
"label_actions": {
  "reason": "Single sentence explaining the label choices for the whole batch.",
  "actions": [
    { "action": "add", "label": "area/api" },
    { "action": "remove", "label": "area/cli" }
  ]
}
```

Write one concise sentence for `reason` that justifies the batch. Do not
include label justifications in the `comment` field -- the pipeline appends the
reason automatically.
```

- [ ] **Step 3: Run the linter**

Run: `make lint`
Expected: PASS (no lint failures from the skill file change)

- [ ] **Step 4: Commit**

```bash
git add internal/scaffold/fullsend-repo/skills/issue-labels/SKILL.md
git commit -S -s -m "feat(skill): generalize issue-labels for issues and PRs (#1706)

Remove hardcoded control-label exclusion list (post-scripts enforce
this server-side) and reword triage-specific language to be
agent-agnostic. Add note to skip issue-type check for PRs.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```

---

### Task 2: Add label_actions to the review result schema

**Files:**
- Modify: `internal/scaffold/fullsend-repo/schemas/review-result.schema.json`

- [ ] **Step 1: Write a test to validate the schema accepts label_actions**

Create a quick validation script. This tests that the schema accepts a review result with `label_actions` and also one without.

Create file `internal/scaffold/fullsend-repo/schemas/review-result-label-actions-test.sh`:

```bash
#!/usr/bin/env bash
# Test that review-result.schema.json accepts label_actions correctly.
# Requires: ajv-cli (npx ajv) or python3 with jsonschema.
set -euo pipefail

SCHEMA="$(dirname "$0")/review-result.schema.json"
FAILURES=0

fail() {
  echo "FAIL: $1"
  FAILURES=$((FAILURES + 1))
}

# Use python3 jsonschema for validation (available in CI images).
validate() {
  local desc="$1"
  local json="$2"
  local expect_pass="$3"

  if echo "${json}" | python3 -c "
import sys, json
try:
    from jsonschema import validate, ValidationError, Draft202012Validator
    schema = json.load(open('${SCHEMA}'))
    instance = json.load(sys.stdin)
    Draft202012Validator(schema).validate(instance)
    sys.exit(0)
except ValidationError as e:
    print(str(e)[:200], file=sys.stderr)
    sys.exit(1)
" 2>/dev/null; then
    if [ "${expect_pass}" = "true" ]; then
      echo "PASS: ${desc}"
    else
      fail "${desc} (expected rejection but schema accepted it)"
    fi
  else
    if [ "${expect_pass}" = "false" ]; then
      echo "PASS: ${desc}"
    else
      fail "${desc} (expected acceptance but schema rejected it)"
    fi
  fi
}

# --- approve without label_actions (baseline) ---
validate "approve-without-label-actions" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM"
}' "true"

# --- approve with valid label_actions ---
validate "approve-with-label-actions" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM",
  "label_actions": {
    "reason": "PR modifies API surface",
    "actions": [
      { "action": "add", "label": "area/api" }
    ]
  }
}' "true"

# --- request-changes with label_actions ---
validate "request-changes-with-label-actions" '{
  "action": "request-changes",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "Found issues",
  "findings": [{"severity":"high","category":"bug","file":"main.go","description":"nil deref"}],
  "label_actions": {
    "reason": "Touches CI config",
    "actions": [
      { "action": "add", "label": "area/ci" },
      { "action": "remove", "label": "area/api" }
    ]
  }
}' "true"

# --- failure action with label_actions (should still be valid — optional field) ---
validate "failure-with-label-actions" '{
  "action": "failure",
  "pr_number": 42,
  "repo": "org/repo",
  "reason": "tool-failure",
  "label_actions": {
    "reason": "Would have labeled area/api",
    "actions": [{ "action": "add", "label": "area/api" }]
  }
}' "true"

# --- invalid: label_actions missing reason ---
validate "label-actions-missing-reason" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM",
  "label_actions": {
    "actions": [{ "action": "add", "label": "area/api" }]
  }
}' "false"

# --- invalid: label_actions with empty actions array ---
validate "label-actions-empty-actions" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM",
  "label_actions": {
    "reason": "No labels",
    "actions": []
  }
}' "false"

# --- invalid: label action with unknown action verb ---
validate "label-actions-invalid-verb" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM",
  "label_actions": {
    "reason": "Test",
    "actions": [{ "action": "replace", "label": "area/api" }]
  }
}' "false"

# --- invalid: extra property in label_actions ---
validate "label-actions-extra-property" '{
  "action": "approve",
  "pr_number": 42,
  "repo": "org/repo",
  "head_sha": "abc1234",
  "body": "LGTM",
  "label_actions": {
    "reason": "Test",
    "actions": [{ "action": "add", "label": "area/api" }],
    "extra": "should fail"
  }
}' "false"

echo ""
if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash internal/scaffold/fullsend-repo/schemas/review-result-label-actions-test.sh`
Expected: FAIL — the schema doesn't have `label_actions` yet, so the "approve-with-label-actions" test should fail (schema rejects the unknown property due to `additionalProperties: false`).

- [ ] **Step 3: Add label_actions to the schema**

Edit `internal/scaffold/fullsend-repo/schemas/review-result.schema.json`. Add the `label_actions` property to the `properties` object (after `reason`) and add the `$defs/label_actions` definition.

Add to `properties` (after line 26, the `reason` property):

```json
    "label_actions": {
      "$ref": "#/$defs/label_actions"
    }
```

Add to `$defs` (after the `finding` definition, before the closing `}`):

```json
    "label_actions": {
      "type": "object",
      "required": ["reason", "actions"],
      "properties": {
        "reason": {
          "type": "string",
          "minLength": 1,
          "description": "Single sentence explaining why these labels are being applied or removed"
        },
        "actions": {
          "type": "array",
          "minItems": 1,
          "maxItems": 20,
          "items": {
            "type": "object",
            "required": ["action", "label"],
            "properties": {
              "action": { "type": "string", "enum": ["add", "remove"] },
              "label": { "type": "string", "minLength": 1, "pattern": "^[a-zA-Z0-9._/: +-]+$" }
            },
            "additionalProperties": false
          }
        }
      },
      "additionalProperties": false
    }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bash internal/scaffold/fullsend-repo/schemas/review-result-label-actions-test.sh`
Expected: All tests passed

- [ ] **Step 5: Run make lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/scaffold/fullsend-repo/schemas/review-result.schema.json \
       internal/scaffold/fullsend-repo/schemas/review-result-label-actions-test.sh
git commit -S -s -m "feat(schema): add optional label_actions to review result (#1706)

Same shape as triage-result.schema.json. The field is optional --
when omitted the post-script skips label processing.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```

---

### Task 3: Add label_actions processing to the review post-script

**Files:**
- Modify: `internal/scaffold/fullsend-repo/scripts/post-review.sh`
- Modify: `internal/scaffold/fullsend-repo/scripts/post-review-test.sh`

The post-script flow requires label_actions to be processed in two phases:

1. **Before** `fullsend post-review` (line 139): validate label_actions and append the reason to the result JSON body (same pattern as the protected-path downgrade at lines 122-128).
2. **After** `fullsend post-review` (after line 218, alongside outcome labels): apply the validated label mutations via the GitHub labels API.

- [ ] **Step 1: Write failing tests for label_actions processing**

Edit `internal/scaffold/fullsend-repo/scripts/post-review-test.sh`. Add an `is_control_label` function and tests for it after the existing outcome-label tests.

Append before the `# --- Summary ---` section (before line 102):

```bash
# ---------------------------------------------------------------------------
# Control-label guard tests
# ---------------------------------------------------------------------------

REVIEW_CONTROL_LABELS=(
  "ready-for-merge" "requires-manual-review" "rejected"
  "ready-for-review" "fullsend-no-fix" "fullsend-fix"
)

is_control_label() {
  local label="$1"
  for cl in "${REVIEW_CONTROL_LABELS[@]}"; do
    if [[ "${cl}" == "${label}" ]]; then
      return 0
    fi
  done
  return 1
}

run_control_label_test() {
  local test_name="$1"
  local label="$2"
  local expected_control="$3"  # "true" or "false"

  if is_control_label "${label}"; then
    local actual="true"
  else
    local actual="false"
  fi

  if [ "${actual}" != "${expected_control}" ]; then
    echo "FAIL: ${test_name}"
    echo "  label:    '${label}'"
    echo "  expected: '${expected_control}'"
    echo "  actual:   '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# Control labels should be recognized
run_control_label_test "ready-for-merge-is-control" \
  "ready-for-merge" "true"

run_control_label_test "requires-manual-review-is-control" \
  "requires-manual-review" "true"

run_control_label_test "rejected-is-control" \
  "rejected" "true"

run_control_label_test "ready-for-review-is-control" \
  "ready-for-review" "true"

run_control_label_test "fullsend-no-fix-is-control" \
  "fullsend-no-fix" "true"

run_control_label_test "fullsend-fix-is-control" \
  "fullsend-fix" "true"

# Non-control labels should NOT be recognized
run_control_label_test "area-api-not-control" \
  "area/api" "false"

run_control_label_test "priority-high-not-control" \
  "priority/high" "false"

run_control_label_test "bug-not-control" \
  "bug" "false"

run_control_label_test "empty-not-control" \
  "" "false"
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `bash internal/scaffold/fullsend-repo/scripts/post-review-test.sh`
Expected: All tests passed (these are unit tests for the extracted logic — they should pass immediately since we're defining the function inline in the test file).

- [ ] **Step 3: Add label_actions processing to post-review.sh**

Edit `internal/scaffold/fullsend-repo/scripts/post-review.sh`. Add two blocks:

**Block A: Before `fullsend post-review` (insert after line 131, before line 133).**

This block validates label_actions and appends the reason to the body, rewriting the result JSON file (same pattern as the protected-path downgrade).

```bash
# ---------------------------------------------------------------------------
# Label actions: validate agent-recommended labels and append reason to body.
# Actual label mutations happen after the review is posted (see below).
# ---------------------------------------------------------------------------
REVIEW_CONTROL_LABELS=(
  "ready-for-merge" "requires-manual-review" "rejected"
  "ready-for-review" "fullsend-no-fix" "fullsend-fix"
)

is_control_label() {
  local label="$1"
  for cl in "${REVIEW_CONTROL_LABELS[@]}"; do
    if [[ "${cl}" == "${label}" ]]; then
      return 0
    fi
  done
  return 1
}

VALIDATED_LABEL_ADDS=()
VALIDATED_LABEL_REMOVES=()
LABEL_REASON=""

HAS_LABEL_ACTIONS=$(jq 'has("label_actions")' "${RESULT_FILE}")
if [[ "${HAS_LABEL_ACTIONS}" == "true" ]]; then
  LABEL_REASON=$(jq -r '.label_actions.reason' "${RESULT_FILE}")
  LABEL_COUNT=$(jq '.label_actions.actions | length' "${RESULT_FILE}")

  echo "Validating ${LABEL_COUNT} label action(s)..."

  # Fetch existing repo labels once.
  EXISTING_LABELS=$(gh api "repos/${REPO_FULL_NAME}/labels" --paginate --jq '.[].name' 2>/dev/null || true)

  label_exists() {
    local label="$1"
    echo "${EXISTING_LABELS}" | grep -qFx "${label}"
  }

  for i in $(seq 0 $((LABEL_COUNT - 1))); do
    LA_ACTION=$(jq -r ".label_actions.actions[${i}].action" "${RESULT_FILE}")
    LA_LABEL=$(jq -r ".label_actions.actions[${i}].label" "${RESULT_FILE}")

    if [[ ! "${LA_LABEL}" =~ ^[a-zA-Z0-9._/:\ +\-]+$ ]]; then
      echo "::warning::Refused label '${LA_LABEL}' -- contains invalid characters"
      continue
    fi

    if is_control_label "${LA_LABEL}"; then
      echo "::warning::Refused to ${LA_ACTION} control label '${LA_LABEL}' -- control labels are managed by the review pipeline"
      continue
    fi

    case "${LA_ACTION}" in
      add)
        if ! label_exists "${LA_LABEL}"; then
          echo "::warning::Skipping label '${LA_LABEL}' -- does not exist in repo (will not auto-create)"
          continue
        fi
        VALIDATED_LABEL_ADDS+=("${LA_LABEL}")
        ;;
      remove)
        VALIDATED_LABEL_REMOVES+=("${LA_LABEL}")
        ;;
      *)
        echo "::warning::Unknown label action '${LA_ACTION}' for label '${LA_LABEL}'"
        ;;
    esac
  done

  # Append label reason to body if any labels validated.
  VALIDATED_COUNT=$(( ${#VALIDATED_LABEL_ADDS[@]} + ${#VALIDATED_LABEL_REMOVES[@]} ))
  if [[ "${VALIDATED_COUNT}" -gt 0 ]]; then
    LABEL_NOTICE=$'\n\n---\n'"**Labels:** ${LABEL_REASON}"
    LABEL_MODIFIED_RESULT=$(mktemp)
    jq --arg notice "${LABEL_NOTICE}" \
      '.body = (.body + $notice)' \
      "${RESULT_FILE}" > "${LABEL_MODIFIED_RESULT}"
    RESULT_FILE="${LABEL_MODIFIED_RESULT}"
  fi
fi
```

**Block B: After outcome labels (insert after line 218, before the final echo).**

This block applies the validated labels using the GitHub labels API.

```bash
# ---------------------------------------------------------------------------
# Contextual labels: apply validated label mutations from label_actions.
# ---------------------------------------------------------------------------
for label in "${VALIDATED_LABEL_ADDS[@]}"; do
  echo "Adding contextual label '${label}'..."
  gh api "repos/${REPO_FULL_NAME}/issues/${PR_NUMBER}/labels" \
    -f "labels[]=${label}" --silent || \
    echo "::warning::Failed to add label '${label}'"
done

for label in "${VALIDATED_LABEL_REMOVES[@]}"; do
  echo "Removing contextual label '${label}'..."
  encoded=$(printf '%s' "${label}" | jq -sRr @uri)
  gh api "repos/${REPO_FULL_NAME}/issues/${PR_NUMBER}/labels/${encoded}" \
    -X DELETE --silent 2>/dev/null || true
done
```

- [ ] **Step 4: Run the test file**

Run: `bash internal/scaffold/fullsend-repo/scripts/post-review-test.sh`
Expected: All tests passed

- [ ] **Step 5: Run make lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/scaffold/fullsend-repo/scripts/post-review.sh \
       internal/scaffold/fullsend-repo/scripts/post-review-test.sh
git commit -S -s -m "feat(post-review): process label_actions from review result (#1706)

Validate agent-recommended labels against a control-label guard list,
check label existence, append reason to review body, and apply
mutations via the GitHub labels API after posting.

Mirrors the label_actions processing in post-triage.sh.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```

---

### Task 4: Wire issue-labels skill into review agent harness and definition

**Files:**
- Modify: `internal/scaffold/fullsend-repo/harness/review.yaml`
- Modify: `internal/scaffold/fullsend-repo/agents/review.md`

- [ ] **Step 1: Add skill to harness**

Edit `internal/scaffold/fullsend-repo/harness/review.yaml`. Add `- skills/issue-labels` to the `skills:` list (after line 14):

```yaml
skills:
  - skills/pr-review
  - skills/code-review
  - skills/docs-review
  - skills/issue-labels
```

- [ ] **Step 2: Add skill to agent definition frontmatter**

Edit `internal/scaffold/fullsend-repo/agents/review.md`. Add `issue-labels` to the `skills:` list in the YAML frontmatter (after line 15):

```yaml
skills:
  - code-review
  - pr-review
  - docs-review
  - issue-labels
```

- [ ] **Step 3: Add labeling section to agent definition**

Edit `internal/scaffold/fullsend-repo/agents/review.md`. Insert a new section after "Skill routing" (after line 109) and before "Zero-trust principle":

```markdown
## Contextual labels

After producing the review verdict, invoke the `issue-labels` skill to
recommend contextual labels for the PR based on the diff's area and domain.

- Emit `label_actions` in the result JSON alongside the review verdict.
- Labels target the PR itself -- issue labeling remains the triage agent's
  domain.
- If no labels clearly apply, omit `label_actions` entirely. Silence is
  better than noise.
```

- [ ] **Step 4: Update the pipeline mode output docs in the agent definition**

Edit `internal/scaffold/fullsend-repo/agents/review.md`. Add `label_actions` to the top-level object table (after line 230, the `reason` row):

```markdown
| `label_actions` | object | no | Contextual label recommendations (see `issue-labels` skill) |
```

Also add a jq example showing label_actions usage. After the `failure` jq example block (after line 311), add:

```markdown
For any action with contextual labels, add `label_actions`:

```bash
jq -n \
  --arg action "approve" \
  --argjson pr_number <number> \
  --arg repo "<owner/repo>" \
  --arg head_sha "<sha>" \
  --arg body "<markdown review comment>" \
  --argjson label_actions '{"reason":"PR modifies API surface","actions":[{"action":"add","label":"area/api"}]}' \
  '{action: $action, pr_number: $pr_number, repo: $repo,
    head_sha: $head_sha, body: $body, label_actions: $label_actions}' \
  > "$FULLSEND_OUTPUT_DIR/agent-result.json"
```
```

- [ ] **Step 5: Run make lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/scaffold/fullsend-repo/harness/review.yaml \
       internal/scaffold/fullsend-repo/agents/review.md
git commit -S -s -m "feat(review): wire issue-labels skill into review agent (#1706)

Add issue-labels to the harness skills list and agent definition.
Document when and how to invoke the skill during review, and add
label_actions to the pipeline mode output docs.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```

---

### Task 5: Update user-facing documentation

**Files:**
- Modify: `docs/agents/review.md`
- Modify: `docs/guides/user/customizing-with-skills.md`

- [ ] **Step 1: Update review agent docs with contextual labels note**

Edit `docs/agents/review.md`. After the "Control labels" table (after line 49, before "## Configuration and extension"), add:

```markdown
The `issue-labels` skill may also apply contextual labels (e.g., `area/api`,
`priority/high`) but these are informational -- they do not control agent
behavior.
```

- [ ] **Step 2: Add issue-labels skill section to review agent docs**

Edit `docs/agents/review.md`. Replace the "Configuration and extension" section (lines 51-54) to add the skill subsection:

```markdown
## Configuration and extension

### Skill: `issue-labels`

The review agent includes the `issue-labels` skill to discover your repo's
labels and apply them to PRs during review. This is the same skill used by the
[triage agent](triage.md) -- overloading it affects both agents.

To overload the built-in skill, create your own `issue-labels` skill in
`.agents/skills/issue-labels/SKILL.md` and symlink `.claude/skills` to
`.agents/skills` so it's discoverable by both fullsend and local agent tooling.
You can also overload it at the org level in your `.fullsend` config repo at
`customized/skills/issue-labels/SKILL.md`. At runtime, your version replaces
the upstream default -- no other configuration needed.

See [Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).
```

- [ ] **Step 3: Update the skills table**

Edit `docs/guides/user/customizing-with-skills.md`. Update line 111 (the Review row in the built-in skills table) to include `issue-labels`:

```markdown
| [Review](../../agents/review.md) | `code-review`, `pr-review`, `docs-review`, `issue-labels` | Review evaluation across dimensions |
```

- [ ] **Step 4: Update the triage docs example**

Edit `docs/agents/triage.md`. The example overloaded skill at line 72 still says "Apply contextual labels to triaged issues using team labeling conventions." Update the description to match the generalized skill:

```markdown
description: >-
  Apply contextual labels to issues and pull requests using team labeling conventions.
```

Also update line 77 from "Apply labels to the issue being triaged" to "Apply labels to the issue or pull request being processed."

And update line 82 from "These are managed by the triage pipeline. Never include them in `label_actions`:" to "These are managed by agent pipelines. Never include them in `label_actions`:"

Note: the example's control-label list can stay as-is since it's showing a user-authored skill — users can include whatever control labels they want to guard against.

- [ ] **Step 5: Run make lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add docs/agents/review.md \
       docs/guides/user/customizing-with-skills.md \
       docs/agents/triage.md
git commit -S -s -m "docs: document review agent contextual labels (#1706)

Add issue-labels skill section to review agent docs, update the
built-in skills table, and align triage docs example with the
generalized skill language.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```

---

### Task 6: Final validation

- [ ] **Step 1: Run all tests**

Run: `make lint && bash internal/scaffold/fullsend-repo/scripts/post-review-test.sh && bash internal/scaffold/fullsend-repo/schemas/review-result-label-actions-test.sh`
Expected: All pass

- [ ] **Step 2: Review the full diff**

Run: `git log --oneline main..HEAD` and `git diff main..HEAD --stat`

Verify 5 commits covering:
1. Skill generalization
2. Schema + schema tests
3. Post-script + post-script tests
4. Harness + agent definition
5. Documentation (review docs, skills table, triage docs alignment)

- [ ] **Step 3: Verify no untracked files**

Run: `git status`
Expected: clean working tree
