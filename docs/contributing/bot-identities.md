# Bot Identities

Fullsend agents authenticate as GitHub Apps; the table below also includes non-agent bots that appear in trusted-actor lists. Multiple agent roles may share a single app identity. The GitHub App login is derived from the `slug` field in each harness file (`internal/scaffold/fullsend-repo/harness/*.yaml`).

| Agent role | GitHub App login | Notes |
|---|---|---|
| code | `fullsend-ai-coder[bot]` | Opens PRs from issues |
| fix | `fullsend-ai-coder[bot]` | Shares the coder app; pushes to existing PR branches |
| review | `fullsend-ai-review[bot]` | Posts review comments |
| triage | `fullsend-ai-triage[bot]` | Posts triage summaries on issues |
| retro | `fullsend-ai-retro[bot]` | Files retro issues, posts PR comments |
| prioritize | `fullsend-ai-prioritize[bot]` | Prioritizes issues |
| renovate | `renovate-fullsend[bot]` | Dependency updates (not a fullsend agent) |

When referencing bot identities in code (e.g., trusted actor lists, dispatch filters), always verify the login name against this table. Do not assume each agent role has a unique app identity — the fix agent reuses `fullsend-ai-coder[bot]`, not a separate `fullsend-ai-fix[bot]`.
