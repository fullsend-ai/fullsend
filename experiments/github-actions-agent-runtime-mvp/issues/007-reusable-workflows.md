# 007: Reusable Workflows for Multi-Repo Deployment

## Problem

The four agent workflows are defined inline in each component repo. As fullsend expands to multiple repos, maintaining copies becomes unsustainable — bug fixes, security patches, and improvements must be applied to every copy.

## Proposed Architecture

```
fullsend-ai/fullsend (central repo)
├── .github/workflows/
│   ├── reusable-triage-agent.yml      (workflow_call)
│   ├── reusable-review-agent.yml      (workflow_call)
│   ├── reusable-fix-agent.yml         (workflow_call)
│   └── reusable-implementation-agent.yml (workflow_call)

component-repo
├── .github/workflows/
│   └── review-agent.yml  → uses: fullsend-ai/fullsend/...@v1
```

### Parameters to Extract

| Parameter | Example |
|-----------|---------|
| LLM model | `gemini-3.1-pro-preview` |
| Scanner template | `fullsend-issue-scan` |
| Iteration cap | `45` |
| Reviewer bot login | `fullsend-reviewer[bot]` |
| Review criteria | Custom prompt additions |

### Constraints

- Reusable workflows in private repos can only be called from the same repo — `fullsend-ai/fullsend` must be public
- Secrets require `secrets: inherit` within same org, or explicit passing cross-org
- Pin to commit SHA or tag (`@v1`) for stability
