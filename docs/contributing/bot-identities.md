# Bot Identities

Fullsend agents authenticate as GitHub Apps; the table below also includes non-agent bots that appear in trusted-actor lists. Multiple agent roles may share a single app identity. The GitHub App login is determined by the `role` field in each harness file (in the [`fullsend-ai/agents`](https://github.com/fullsend-ai/agents) repo); `slug` is an install-time hint the mint never reads.

| Agent | GitHub App login | Notes |
|---|---|---|
| code | `fullsend-ai-coder[bot]` | `role: coder`. Opens PRs from issues |
| fix | `fullsend-ai-coder[bot]` | `role: coder`. Shares the coder app; pushes to existing PR branches |
| review | `fullsend-ai-review[bot]` | Posts review comments |
| triage | `fullsend-ai-triage[bot]` | Posts triage summaries on issues |
| retro | `fullsend-ai-retro[bot]` | Files retro issues, posts PR comments |
| prioritize | `fullsend-ai-prioritize[bot]` | Prioritizes issues |
| renovate | `renovate-fullsend[bot]` | Dependency updates (not a fullsend agent) |
| sync | `fullsend-ai-sync[bot]` | Converges scaffold files and per-repo variables across `.fullsend`'s `repos.yaml` targets; commits directly to `main` |

When referencing bot identities in code (e.g., trusted actor lists, dispatch filters), always verify the login name against this table. Do not assume each agent role has a unique app identity — the fix agent reuses `fullsend-ai-coder[bot]`, not a separate `fullsend-ai-fix[bot]`.

## Scaffold-sync write path

The `fullsend-ai-sync[bot]` App has a broader write path than the agent Apps listed above. Three properties are load-bearing and must be considered when building trusted-actor lists, dispatch filters, or security assessments:

### Ruleset bypass

The sync App holds `bypass_mode: always` on the `fullsend-ai/fullsend` `main` ruleset. Its commits are direct pushes to `main` — no PR, no review, no status checks. This is intentional: scaffold convergence is a cross-repo reconciliation that runs unattended after every push to `main`.

### Workflow-write scope

The sync App has workflow-write permission. Combined with the ruleset bypass, `.github/workflows/` on `main` is writable by the sync path with no human in the loop. This is distinct from the coder token path: the `coder` token has no `workflows` permission (`internal/mintcore/github.go`), so code agents cannot push workflow files (see #6512). The sync App can. Both facts are correct — read in isolation, the #6512 statement about the coder token is easy to take as covering every bot, but it applies only to the coder token, not the sync path.

### App-token push recursion

GitHub's "events triggered by `GITHUB_TOKEN` will not create a new workflow run" suppression is scoped to `GITHUB_TOKEN` and does not apply to GitHub App installation tokens. A sync commit to `main` therefore re-triggers `notify-scaffold-sync`, which dispatches again. Observed 2026-08-24: merge `cfdbf2ff` at 14:29 → dispatch → sync commit 14:51 → dispatch → sync commit 14:52 → dispatch → stop. Each scaffold-touching merge costs ≥2 dispatch rounds. This is by design — the second round converges any files that depend on the first sync — but it interacts with the convergence non-idempotence tracked in #6553. See also [CI Workflows § Scaffold-sync dispatch recursion](ci-workflows.md#scaffold-sync-dispatch-recursion).

## General identity notes

### Shared vendor identity

The default deployment model uses a shared, vendor-owned App (per ADR 0029/0059/0068). For adopting orgs other than `fullsend-ai`, the review bot's login is `fullsend-ai-review[bot]`, not `${ORG_NAME}-review[bot]`. Any code that gates on the review bot's identity must match **both** the org-specific form (`${ORG_NAME}-review[bot]`) and the shared vendor form (`fullsend-ai-review[bot]`). See #5550 for the bug this caused.

### REST vs. GraphQL login format

The `[bot]` suffix above is the REST/App-slug form. GitHub's GraphQL API omits it — a bot author's `login` field comes back as `fullsend-ai-coder`, not `fullsend-ai-coder[bot]`, with `__typename: "Bot"`. Comparing a GraphQL-sourced login against a literal `"...[bot]"` string never matches (see #5575) — match on `__typename == "Bot"` plus the un-suffixed login instead.

### `gh pr view --json` format

The `gh pr view --json author` CLI command uses a different schema than raw GraphQL — it exposes `.author.is_bot` (boolean) and `.author.login` (with an `app/` prefix, e.g. `app/fullsend-ai-coder`), but does **not** expose `__typename`. When using `gh pr view --json`, check `.author.is_bot == true` plus `.author.login` against the `app/`-prefixed name (see #5536).
