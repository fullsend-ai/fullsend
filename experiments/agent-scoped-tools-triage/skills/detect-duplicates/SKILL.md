---
name: detect-duplicates
description: Search for and identify duplicate issues in a repository
allowed-tools: Bash(curl *)
---

Search for issues with similar titles and keywords to the current issue
using the GitHub REST server on the host.

**Read the current issue:**
```bash
curl -s http://host.docker.internal:8081/repos/$OWNER/$REPO_NAME/issues/$ISSUE_NUMBER
```

**List all issues:**
```bash
curl -s http://host.docker.internal:8081/repos/$OWNER/$REPO_NAME/issues
```

**Search for similar issues:**
```bash
curl -s "http://host.docker.internal:8081/search/issues?q=KEYWORDS"
```

Respond ONLY with a JSON object:
```json
{
  "duplicate_of": <issue number or null>,
  "confidence": "high" | "medium" | "low",
  "reason": "<brief explanation>"
}
```
