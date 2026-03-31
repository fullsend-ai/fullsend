---
name: assess-completeness
description: Check if an issue has all required information for action
allowed-tools: Bash(curl *), WebFetch
---

Read the issue via the GitHub REST server:
```bash
curl -s http://host.docker.internal:8081/repos/$OWNER/$REPO_NAME/issues/$ISSUE_NUMBER
```

Evaluate whether the issue contains sufficient information.
- For bugs: steps to reproduce, expected vs actual behavior, environment info.
- For features: use case description, acceptance criteria.
- For questions: enough context to provide a useful answer.
- If the issue references external links (logs, pastebins, docs), fetch them to verify the information is actually there.

Respond ONLY with a JSON object:
```json
{
  "issue_type": "bug" | "enhancement" | "question" | "other",
  "complete": true | false,
  "missing": ["<missing item 1>", "<missing item 2>"],
  "external_context": "<summary of relevant information gathered from external links, or null if none>"
}
```
