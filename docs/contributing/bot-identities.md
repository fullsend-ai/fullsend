# Bot Identities

Fullsend agents authenticate as GitHub Apps; the table below also includes non-agent bots that appear in trusted-actor lists. Multiple agent roles may share a single app identity. The GitHub App login is derived from the `slug` field in each harness file (in the [`fullsend-ai/agents`](https://github.com/fullsend-ai/agents) repo).

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

**Shared vendor identity:** The default deployment model uses a shared, vendor-owned App (per ADR 0029/0059/0068). For adopting orgs other than `fullsend-ai`, the review bot's login is `fullsend-ai-review[bot]`, not `${ORG_NAME}-review[bot]`. Any code that gates on the review bot's identity must match **both** the org-specific form (`${ORG_NAME}-review[bot]`) and the shared vendor form (`fullsend-ai-review[bot]`). See #5550 for the bug this caused.

**REST vs. GraphQL login format:** the `[bot]` suffix above is the REST/App-slug form. GitHub's GraphQL API omits it — a bot author's `login` field comes back as `fullsend-ai-coder`, not `fullsend-ai-coder[bot]`, with `__typename: "Bot"`. Comparing a GraphQL-sourced login against a literal `"...[bot]"` string never matches (see #5575) — match on `__typename == "Bot"` plus the un-suffixed login instead.

**`gh pr view --json` format:** the `gh pr view --json author` CLI command uses a different schema than raw GraphQL — it exposes `.author.is_bot` (boolean) and `.author.login` (with an `app/` prefix, e.g. `app/fullsend-ai-coder`), but does **not** expose `__typename`. When using `gh pr view --json`, check `.author.is_bot == true` plus `.author.login` against the `app/`-prefixed name (see #5536).

## Per-commit DCO classification

DCO (Developer Certificate of Origin) eligibility must be classified **per commit**, using the commit author/committer email — never by the fact that a bot-triggered run is operating on the branch. Mixed-author branches (human commits + bot commits) are common on fix-agent PRs.

**Rules:**

1. **Bot-authored commits** (committer email matches `<digits>+<slug>[bot]@users.noreply.github.com`) are exempt from DCO. They must **not** carry a `Signed-off-by` trailer. The Probot DCO app auto-skips them.
2. **Human-authored commits** require valid `Signed-off-by` trailers. These trailers must be **preserved** through any rebase, amend, or history rewrite performed by post-scripts or validation logic.
3. **Never use branch-wide `git filter-branch --msg-filter`** to strip `Signed-off-by` trailers. This destroys valid human attestations on mixed-author branches. See #6688 for the incident this caused.

**Identifying bot commits in Go code:**

```go
import "github.com/fullsend-ai/fullsend/internal/forge"

if forge.IsBotCommitEmail(committerEmail) {
    // Bot commit — exempt from DCO, must not have Signed-off-by
}
```

**Identifying bot commits in shell (post-scripts):**

```bash
# Use GIT_BOT_EMAIL (set by the "Resolve bot identity" workflow step)
# for exact match, or the regex pattern for general detection.
committer_email=$(git log -1 --format='%ce' "$sha")

# Exact match against the resolved bot identity:
if [[ "$committer_email" == "${GIT_BOT_EMAIL}" ]]; then
    # Bot commit
fi

# Pattern match for any GitHub App bot:
if [[ "$committer_email" =~ ^[0-9]+\+.*\[bot\]@users\.noreply\.github\.com$ ]]; then
    # Bot commit
fi
```

**Post-script guidance:** When a post-script needs to validate or modify DCO trailers, it must iterate over commits individually and classify each by its committer email. Only bot-authored commits should be inspected or modified. Human-authored commits must pass through unmodified.
