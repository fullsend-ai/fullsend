---
name: verify-reproducibility
description: Verify whether a reported bug is reproducible by inspecting the codebase
allowed-tools: Bash(curl *), Bash(grep *), Bash(find *), Bash(cat *)
---

Read the issue via the GitHub REST server:
```bash
curl -s http://host.docker.internal:8081/repos/$OWNER/$REPO_NAME/issues/$ISSUE_NUMBER
```

If the issue is a bug report with reproduction steps:
1. Analyze the reproduction steps for feasibility
2. Use local tools to inspect the codebase for related code
3. Determine if the bug is plausible based on the code

If the issue is not a bug, set applicable to false.

Respond ONLY with a JSON object:
```json
{
  "applicable": true | false,
  "reproducible": "confirmed" | "not_confirmed" | "unclear" | null,
  "notes": "<findings from the verification attempt>"
}
```
