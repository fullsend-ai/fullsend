# Review Agent: Contextual Labels via issue-labels Skill

**Issue:** #1706
**Date:** 2026-06-11

## Problem

The triage agent uses the `issue-labels` skill to discover repo label
conventions and apply contextual labels (e.g., `area/api`, `priority/high`) to
issues. The review agent has no equivalent — PRs it reviews receive no
contextual labels, even when the diff clearly maps to a known area or priority.

## Approach

Generalize the existing `issue-labels` skill to work for both issues and PRs,
then wire it into the review agent's harness, schema, agent definition, and
post-script. No new skill is created; the same skill serves both agents.

## Changes

### 1. `internal/scaffold/fullsend-repo/skills/issue-labels/SKILL.md`

Generalize to be agent-agnostic:

- Change description from "triaged issues" to "issues and pull requests."
- Remove the "Control labels (do NOT recommend these)" section entirely. The
  post-scripts for both agents already validate and refuse control labels
  server-side — duplicating the list in the skill is a maintenance burden and
  already out of sync (`question` is missing from the skill but present in the
  triage post-script).
- Reword triage-specific language: "issue being triaged" becomes "issue or pull
  request."
- In Step 2 (issue types check), add: "Skip this step when labeling a pull
  request — GitHub issue types do not apply to PRs."
- Step 3 (research conventions) stays unchanged — querying recent issues is
  sufficient since label taxonomies are repo-wide.

### 2. `internal/scaffold/fullsend-repo/harness/review.yaml`

Add `issue-labels` to the `skills:` list:

```yaml
skills:
  - skills/pr-review
  - skills/code-review
  - skills/docs-review
  - skills/issue-labels
```

### 3. `internal/scaffold/fullsend-repo/agents/review.md`

Add `issue-labels` to the frontmatter `skills:` list. Add a short section after
"Skill routing" explaining when to invoke it:

- Invoke the `issue-labels` skill after producing the review verdict.
- Based on the diff's area/domain, recommend labels to add or remove.
- Emit `label_actions` in the result JSON alongside the review verdict.
- Labels target the PR itself — issue labeling remains the triage agent's
  domain.
- If no labels clearly apply, omit `label_actions` entirely.

### 4. `internal/scaffold/fullsend-repo/schemas/review-result.schema.json`

Add an optional `label_actions` property. Reuse the same `$defs/label_actions`
shape from `triage-result.schema.json`:

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

The field is optional — not listed in any `required` array or conditional
`then` clause. When omitted, the post-script skips label processing.

### 5. `internal/scaffold/fullsend-repo/scripts/post-review.sh`

Add a `label_actions` processing block after the outcome-labels section
(after line 218). This mirrors the triage post-script's implementation:

**Control-label guard:**

```bash
CONTROL_LABELS=(
  "ready-for-merge" "requires-manual-review" "rejected"
  "ready-for-review" "fullsend-no-fix" "fullsend-fix"
)
```

With an `is_control_label()` function matching the triage pattern.

**Label existence check:**

```bash
label_exists() {
  local label="$1"
  local encoded
  encoded=$(printf '%s' "${label}" | jq -sRr @uri)
  gh api "repos/${REPO_FULL_NAME}/labels/${encoded}" \
    --silent 2>/dev/null
}
```

**Processing loop:**

1. Extract `label_actions` from the result JSON. If absent or null, skip.
2. Read `label_actions.reason` (single sentence).
3. Iterate `label_actions.actions[]`:
   - Validate label name regex: `^[a-zA-Z0-9._/: +-]+$`
   - Reject control labels with `::warning::`
   - Check label exists in repo; skip with `::warning::` if not
   - Apply `add` via `POST /repos/{}/issues/{}/labels`
   - Apply `remove` via `DELETE /repos/{}/issues/{}/labels/{}`
4. If at least one label was applied, append to the review body:
   `**Labels:** {reason}`

Labels are applied using the GitHub labels API (not `gh pr edit`) to match the
triage post-script's pattern. While the review dispatch does not currently
listen on `pull_request.labeled`, using the API keeps the approach consistent
and future-proof.

### 6. `docs/agents/review.md`

After the "Control labels" table, add a note:

> The `issue-labels` skill may also apply contextual labels (e.g., `area/api`,
> `priority/high`) but these are informational — they do not control agent
> behavior.

Add a "Skill: `issue-labels`" subsection under "Configuration and extension"
matching the triage docs pattern — explaining:

- The review agent includes the `issue-labels` skill to discover repo labels
  and apply them to PRs during review.
- The skill is shared with the triage agent; overloading it affects both.
- How to overload (same mechanism: `.agents/skills/issue-labels/SKILL.md` or
  org-level `.fullsend` config repo).

### 7. `docs/guides/user/customizing-with-skills.md`

Update the built-in skills table to add `issue-labels` to the review agent row:

```
| [Review](../../agents/review.md) | `code-review`, `pr-review`, `docs-review`, `issue-labels` | Review evaluation across dimensions |
```

## What does NOT change

- **Triage post-script** — no changes needed. It already validates control
  labels server-side.
- **Triage agent definition** — unchanged.
- **Label conventions query** — stays issue-only per design decision (label
  taxonomies are repo-wide).
- **Dispatch workflow** — no event routing changes needed. Review dispatch does
  not listen on `pull_request.labeled`.

## Testing

- Unit: validate the updated schema accepts results with and without
  `label_actions`.
- Integration: verify post-script processes `label_actions` correctly — applies
  valid labels, refuses control labels, skips non-existent labels.
- Mirror `post-review-test.sh` updates to cover the new label processing block.
