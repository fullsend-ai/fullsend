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
