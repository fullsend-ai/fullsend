# Prioritize

<img src="icons/prioritize.png" alt="Prioritize agent icon" width="80">

Scores a GitHub issue using the RICE framework (Reach, Impact, Confidence, Effort) and produces structured scores with reasoning for project board ranking.

## How the agent works

The prioritize agent is triggered after triage. It fetches the issue and all its context, then evaluates it across the four RICE dimensions. It can invoke customer-research skills to gather additional signal about reach and impact. The output is a structured JSON result with per-dimension scores and written reasoning, which the post-script uses to update the project board.

The agent runs in a read-only sandbox. It cannot modify issues or push code — it only produces a scored result.

## How it helps

- Issues are ranked consistently using the same framework, reducing bias from whoever happens to see them first.
- Scoring reasoning is transparent and auditable — anyone can read why an issue was ranked the way it was.
- Project boards stay sorted by value, so humans can focus on the highest-impact work first.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-prioritize` | Issue comment | Runs RICE scoring on the issue |

The `/fs-prioritize` command does not accept arguments. It scores the issue
using the current content, comments, and any available `customer-research`
skill data.

## Control labels

The prioritize agent does not apply or consume control labels. It reads the
issue content and produces a structured score — the post-script updates the
project board directly.

## Configuration and extension

Detailed harness-level customization is coming soon. Today, the best way to
influence how the prioritize agent behaves on your repository is by adding
instructions and skills to the repo itself. See
[Customizing Agents with Skills](../guides/user/customizing-with-skills.md).

### Example: Providing a customer-research skill

The prioritize agent looks for a `customer-research` skill and, when available,
uses it to inform Reach and Impact scores. This is not a command — it's an
agent skill that the prioritize agent invokes automatically during scoring.

To provide one, create a skill directory in your target repository at
`.claude/skills/customer-research/` with a `SKILL.md` and any helper scripts
the agent can run to dynamically gather data:

```
your-repo/
  .claude/skills/customer-research/
    SKILL.md
    query-salesforce.sh
    search-drive.sh
```

**`.claude/skills/customer-research/SKILL.md`:**

```markdown
---
name: customer-research
description: >-
  Gather customer context from Salesforce and Google Drive to inform
  RICE prioritization scoring.
---

# Customer Research

Research customer context for the issue being prioritized.

## Step 1: Check Salesforce for affected accounts

Run the Salesforce query script to find accounts with support cases
related to this issue's feature area:

    bash .claude/skills/customer-research/query-salesforce.sh "SEARCH_TERM"

The script returns a JSON summary of affected accounts by tier
(Strategic, Enterprise, Growth). Strategic accounts should
significantly increase the Reach score.

## Step 2: Search Google Drive for strategy and interview documents

Run the Drive search script to find relevant customer interviews,
roadmap documents, and competitive analysis:

    bash .claude/skills/customer-research/search-drive.sh "SEARCH_TERM"

Look for:
- Customer interview transcripts mentioning the problem.
- Strategy or roadmap documents that call out this area.
- Competitive analysis where this feature is a differentiator.

## Step 3: Summarize findings

Return a short summary (under 500 characters) describing:
- How many and which tier of customers are affected.
- Whether this aligns with current strategic priorities.
- Any direct customer quotes or requests that strengthen the case.
```

**`.claude/skills/customer-research/query-salesforce.sh`:**

```bash
#!/usr/bin/env bash
set -euo pipefail
TERM="$1"
curl -sf -H "Authorization: Bearer $SFDC_TOKEN" \
  "$SFDC_URL/services/data/v59.0/query/" \
  --data-urlencode "q=SELECT Account.Name, Account.Tier__c, Count(Id) cnt
    FROM Case WHERE Subject LIKE '%${TERM}%'
    GROUP BY Account.Name, Account.Tier__c
    ORDER BY cnt DESC LIMIT 20"
```

**`.claude/skills/customer-research/search-drive.sh`:**

```bash
#!/usr/bin/env bash
set -euo pipefail
TERM="$1"
curl -sf -H "Authorization: Bearer $GDRIVE_TOKEN" \
  "https://www.googleapis.com/drive/v3/files" \
  --data-urlencode "q=fullText contains '${TERM}' and mimeType='application/vnd.google-apps.document'" \
  -d "fields=files(name,modifiedTime,webViewLink)" \
  -d "orderBy=modifiedTime desc" \
  -d "pageSize=5"
```

This gives the prioritize agent concrete data to distinguish between "one user
wants this" (Reach 0.25) and "three strategic accounts have filed support cases
about it" (Reach 2.0), instead of guessing from the issue text alone. The
scripts run dynamically during each prioritization, so scores reflect current
customer data.

## Source

[`internal/scaffold/fullsend-repo/harness/prioritize.yaml`](../../internal/scaffold/fullsend-repo/harness/prioritize.yaml)
