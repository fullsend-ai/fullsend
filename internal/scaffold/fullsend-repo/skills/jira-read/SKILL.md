---
name: jira-read
description: >-
  Read-only Jira integration skill. Provides patterns for reading issue data,
  comments, hierarchy, and linked issues from Jira Cloud instances.
  Write operations are handled by post-scripts, not the agent.
---

# Jira Read Skill

This skill provides patterns for reading data from Jira Cloud instances.
All Jira API calls require authentication — the pre-script fetches data
and writes it to files the agent can read. This skill documents what
data is available and how to interpret it.

## Data available to the agent

The pre-script fetches issue data and writes it to `$ISSUE_CONTEXT`:

```json
{
  "source": "jira",
  "host": "stage-redhat.atlassian.net",
  "key": "SECURESIGN-1620",
  "level": "feature",
  "summary": "Issue summary text",
  "description": "Full issue description (Atlassian Document Format converted to text)",
  "status": "In Progress",
  "priority": "High",
  "labels": ["label1", "label2"],
  "reporter": "user@example.com",
  "assignee": "user@example.com",
  "created": "2026-01-15T10:00:00.000+0000",
  "updated": "2026-05-18T14:30:00.000+0000",
  "parent": {
    "key": "HATSTRAT-259",
    "summary": "Parent issue summary",
    "description": "Parent description (provides strategic context)"
  },
  "children": [
    {
      "key": "SECURESIGN-1621",
      "summary": "Child issue summary",
      "status": "To Do",
      "type": "Epic"
    }
  ],
  "comments": [
    {
      "author": "user@example.com",
      "created": "2026-05-18T15:00:00.000+0000",
      "body": "Comment text"
    }
  ],
  "linked_issues": [
    {
      "type": "blocks",
      "key": "OTHER-123",
      "summary": "Linked issue summary",
      "status": "Open"
    }
  ],
  "project": {
    "key": "SECURESIGN",
    "name": "Secure Sign",
    "hierarchy": ["Outcome", "Feature", "Epic", "Story", "Task"]
  }
}
```

## Interpreting issue levels

The `level` field is derived from the Jira issue type and project hierarchy:

| Jira type | Level | Decomposes into |
|-----------|-------|----------------|
| Outcome | outcome | features |
| Feature | feature | epics |
| Epic | epic | stories |
| Story | story | tasks |
| Task | task | sub-tasks |
| Bug/Spike | issue | sub-issues |

## Parent context

The `parent` field contains the parent issue's description. This often
provides strategic context that the issue itself lacks. Always read the
parent description when available — it may contain goals, constraints,
or requirements not repeated in the child.

## Comments as conversation history

The `comments` array contains the full comment history. When the refine
agent previously posted a clarification question and the user answered:

1. The question will be in an older comment (posted by the bot)
2. The answer will be in a newer comment (posted by a human)

Check for this pattern to continue iteration without re-asking.

## What the agent CANNOT do

- The agent cannot call the Jira API directly (sandbox network policy blocks it)
- The agent cannot create, update, or comment on Jira issues
- All write operations happen in the post-script using credentials that
  never enter the sandbox
