---
name: public-research
description: >-
  Public data research skill. Provides patterns for gathering context from
  GitHub, web search, and public documentation. Replaces the internal
  org-research skill with public-only data sources.
---

# Public Research Skill

This skill provides techniques for gathering technical context from public
data sources. It replaces the internal `analyze` tool with open, accessible
alternatives.

## Available data sources

### 1. GitHub API

Search issues, PRs, discussions, and code across repositories:

```bash
# Search issues by keyword
gh issue list --repo OWNER/REPO --state all --search "keywords" \
  --json number,title,state,labels,body --limit 30

# Search PRs
gh pr list --repo OWNER/REPO --state all --search "keywords" \
  --json number,title,state,body --limit 20

# Search code across GitHub
gh search code "pattern" --repo OWNER/REPO --json path,repository

# Read file contents
gh api "repos/OWNER/REPO/contents/path/to/file" --jq '.content' | base64 -d

# List repository topics/languages
gh api "repos/OWNER/REPO" --jq '{topics: .topics, language: .language}'
gh api "repos/OWNER/REPO/languages"
```

### 2. Repository analysis

When the target repo is checked out locally:

```bash
# Project structure
find . -maxdepth 3 -type f -name "*.go" -o -name "*.py" -o -name "*.ts" | head -50
tree -L 2 --dirsfirst

# Dependency manifests
cat go.mod 2>/dev/null || cat package.json 2>/dev/null || cat requirements.txt 2>/dev/null

# Deployment configs
find . -name "Dockerfile*" -o -name "*.yaml" -path "*/deploy/*" -o -name "Makefile" | head -20

# Test infrastructure
find . -name "*_test.go" -o -name "*.test.ts" -o -name "test_*.py" | head -20
```

### 3. Web search

For competitive analysis and industry standards. Use targeted searches:

```bash
# Technical documentation
curl -s "https://api.tavily.com/search" \
  -H "Content-Type: application/json" \
  -d '{"query": "specific technical question", "max_results": 5}'
```

If Tavily is unavailable, use GitHub as a proxy for public knowledge:

```bash
gh search repos "topic keywords" --json fullName,description,stargazersCount --limit 10
```

### 4. Public documentation

Read README files and docs from related repositories:

```bash
gh api "repos/OWNER/REPO/readme" --jq '.content' | base64 -d
```

## Research strategy

1. **Start with the target repo** — understand what exists before searching externally
2. **Search for related work** — prior issues, PRs, and discussions
3. **Search related repos** — projects in the same org or ecosystem
4. **Web search last** — only for gaps not covered by repo/GitHub analysis

## What NOT to do

- Do not access internal/proprietary tools or databases
- Do not fabricate sources — if you can't find information, note the gap
- Do not do unfocused research — every search should answer a specific question
- Do not spend excessive tokens on broad web crawling
