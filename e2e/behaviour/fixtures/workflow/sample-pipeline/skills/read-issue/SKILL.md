---
name: read-issue
description: Read the issue named in the workflow args and list its unchecked checklist items.
---

# Read an issue

The workflow args name the issue as `issue <number>`. Run the plugin's helper
with that number; it reads the issue from the repository the run works on
(`REPO_FULL_NAME`) over the GitHub REST API:

```bash
"${CLAUDE_PLUGIN_ROOT}/scripts/checklist.sh" <number>
```

It prints `{"title": ..., "items": [...]}`: the issue title and every
unchecked Markdown checklist item (`- [ ] ...`) in the order they appear.
Return exactly that object.
