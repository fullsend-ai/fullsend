---
name: topissues
description: >
  Build a merged RICE priority table: top unassigned backlog issues plus
  issues assigned to the current user. Use for /topissues or when the user
  asks for highest-priority issues, RICE scores, or backlog overview.
allowed-tools: Bash(python3 skills/topissues/scripts/topissues.py:*)
---

# Top Issues (RICE)

Deterministically list the highest-priority open work using **RICE Score** on the org GitHub Project board (same fields written by `post-prioritize.sh`).

## Prerequisites

- `python3`
- `gh` CLI authenticated with read access to the target repository and org project
- Org variable `FULLSEND_PROJECT_NUMBER` (or pass `--project N`)

## Script

From the repository root:

```bash
python3 skills/topissues/scripts/topissues.py [OPTIONS]
```

## Flags

| Flag | Description |
|------|-------------|
| `--top N` | Top N unassigned backlog issues without open linked PRs or open blockers (default: 10) |
| `--repo owner/name` | Repository override (default: current repo via `gh repo view`) |
| `--project N` | GitHub Project number (default: `FULLSEND_PROJECT_NUMBER` env or org variable) |
| `--user LOGIN` | GitHub user for "mine" pool (default: `gh api user`) |
| `--format markdown\|json` | Output format (default: markdown) |
| `--quiet` | Suppress stderr on API failures |

## Slash command

Portable `/topissues` is defined in [commands/topissues.md](../../commands/topissues.md). Run the script and paste stdout **verbatim** — do not invent or adjust scores.

## RICE scores

Scores are read **only** from the project custom field **RICE Score** on the org project board. Issues must be on that project and scored by the prioritize agent; issue comments are not used.

The top backlog pool excludes issues with **open blockers** (GitHub `blockedBy` links — e.g. #470 blocked by open #788). Assigned issues in the mine pool are not filtered by blocker status.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Missing `gh` or not in a resolvable repository |
| 2 | Invalid arguments |
| 3 | GraphQL/API failure |
