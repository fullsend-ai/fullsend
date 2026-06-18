---
title: "52. Jira Integration Model"
status: Accepted
relates_to: []
topics:
  - jira
  - integration
  - issue tracking
  - credentials
---

# ADR 0052: Jira Integration Model

## Status

Accepted

## Context

fullsend agents currently interact exclusively with GitHub Issues via the GitHub API. The refinement pipeline needs to work with Jira issues as well, since many teams use Jira as their primary issue tracker while hosting code on GitHub.

Prior work on Jira integration (PR #2162 by Manish Kumar) validated the pattern of static API tokens with credential isolation, where Jira credentials are available to pre/post scripts on the trusted GitHub Actions runner but never enter the agent sandbox. That PR was closed without merging (review feedback required an ADR first), but the technical approach was validated on staging.

This ADR covers the **Phase 1 Jira integration** — the minimum viable model to get the refinement pipeline working against Jira production. It intentionally defers several improvements to later phases.

## Decision

We will support Jira as an issue source for the refinement pipeline using the following model.

### Phase 1 decisions (this PR)

#### Authentication: Static API tokens

- Jira access uses a service account email + API token stored as GitHub Actions secrets (`JIRA_HOST`, `JIRA_EMAIL`, `JIRA_API_TOKEN`)
- We chose static tokens because they work today, require zero infrastructure, and match what was validated in fullsend-ai/features
- **Credential isolation**: Jira credentials are available ONLY to pre/post scripts running on the trusted GitHub Actions runner. They are explicitly excluded from the agent sandbox environment. The agent cannot make Jira API calls directly.
- Pre-scripts fetch data and write it to `issue-context.json`; post-scripts read agent output and post results back to Jira

#### Issue source routing

Scripts use an `ISSUE_SOURCE` environment variable (`"github"` or `"jira"`) to determine:

- Where to fetch issue data from (pre-scripts)
- Where to post results back to (post-scripts)
- What format to use (Markdown for GitHub, ADF for Jira via `markdown-to-adf.py`)

#### Trigger mechanisms: Manual dispatch + comment poller

For Phase 1, we ship two trigger mechanisms:

1. **Manual dispatch**: Direct `gh workflow run jira-dispatch.yml -f jira_key=PROJ-123 -f command=/fs-refine`. This is the simplest path — zero Jira-side configuration needed.
2. **Jira comment poller** (cron, every 5 min): Polls Jira projects for issues labeled "fullsend" that have `/fs-refine` comments. Zero Jira admin access needed. Trade-off: GitHub Actions scheduled workflows run on a best-effort basis — the 5-minute cron is not guaranteed and can be delayed by minutes or skipped entirely during periods of high GitHub Actions load. This is acceptable for Phase 1 but is a key reason to prioritize Jira Automation Rule webhooks in Phase 2.

Both mechanisms route through `jira-dispatch.yml`, which validates the issue key and dispatches the appropriate pipeline workflow.

#### Security: Private Jira on public repos

The risk is specific: **private** Jira data leaking into **public** GitHub Actions artifacts and logs. Public Jira projects running on public repos are fine — the data is already publicly accessible.

The pipeline uses `JIRA_PROJECT_VISIBILITY` to control this. Teams set it as a **GitHub Actions repository variable** on their `.fullsend` config repo (Settings → Secrets and variables → Actions → Variables tab → `JIRA_PROJECT_VISIBILITY`). Values:

- `"public"` — Jira project is publicly accessible, skip the check
- `"private"` or unset (default) — Jira project is private, require a private config repo

When the value is `"private"` and the config repo is public, the pipeline **hard-fails** with a security error explaining the options.

The variable flows through the full chain: scaffold shim (`vars.JIRA_PROJECT_VISIBILITY`) → reusable workflow (`jira_project_visibility` input) → agent environment → pre-script. The check is also enforced independently at `jira-dispatch.yml` and `jira-comment-poller.yml` before any agent is invoked.

#### Data flow

1. Pre-script fetches issue data from Jira REST API v3, normalizes to `issue-context.json`
2. Agent reads `issue-context.json` in sandbox (read-only)
3. Agent produces structured JSON output (explore/refine/critique result)
4. Post-script reads agent output, posts formatted comment back to Jira using ADF

### Deferred to Phase 2+

| Decision | Why it's good | Why it's deferred |
|----------|--------------|-------------------|
| **Jira Automation Rule webhooks** | Instant trigger (no polling delay). A Jira Automation Rule fires a `repository_dispatch` event to GitHub when a comment containing `/fs-refine` is added. | Requires Jira admin access to create the rule per project. Phase 1 avoids any Jira-side setup requirements so teams can self-serve. The `jira-dispatch.yml` already handles `repository_dispatch` events — teams just need to create the Jira rule when ready. |
| **OAuth 2.0 with WIF for short-lived tokens** | Eliminates token rotation, follows ercohen's token mint proposal, aligns with fullsend's existing OIDC mint pattern. | Requires new infrastructure (token mint service for Jira). Static tokens work for initial adoption. Migrate when the token mint supports Jira. |
| **`fullsend jira enroll` CLI command** | Streamlines onboarding — one command sets up secrets, creates Jira Automation Rules, configures the poller. | Manish started this in PR #2162 but it wasn't merged. Needs design work to handle both per-org and per-repo modes. |
| **Jira custom field mapping for child issues** | Map additional Jira fields (story points, sprints, components, fix versions) when creating child issues. | Phase 1 creates children with summary, description, type, and parent link. Custom field mapping requires per-project configuration that varies across teams. |
| **Jira project discovery and enrollment** | Auto-detect which Jira projects to poll, manage project-to-repo mappings in config.yaml. | Phase 1 uses the "fullsend" label convention — poll all issues with that label. Explicit project enrollment adds config complexity that isn't needed for initial adoption. |
| **Private Jira on public repos** | Some teams have private Jira but public GitHub repos. A single-workflow pipeline mode or encrypted artifacts could enable this safely. | Architecturally complex. Phase 1 blocks this combination with a safety check. Teams in this situation should use a private config repo. |

## Consequences

### Positive

- Jira credentials never touch the agent sandbox (security)
- Same agent prompts work for both GitHub and Jira issues
- Two trigger mechanisms cover the main use cases without requiring Jira admin access
- Public repo safety check prevents accidental data leakage
- The `jira-dispatch.yml` webhook handler is already built — enabling Jira Automation Rules in Phase 2 is zero code change

### Negative

- Static API tokens require manual rotation
- Cron poller delay is unpredictable — GitHub Actions scheduled workflows are best-effort and can be delayed or skipped under load (webhook trigger in Phase 2 eliminates this entirely)
- ADF (Atlassian Document Format) conversion adds complexity
- Child issues are created in both GitHub and Jira, with Jira hierarchy support (parent linking with retry-without-parent fallback) and automatic issue type resolution against the project's available types

### Risks

- Token rotation is a manual process — if a token expires, the pipeline silently fails until someone notices (mitigated: post-scripts log clear errors on auth failures)
- The poller creates GitHub Actions runs even when no commands are found (mitigated: the workflow exits quickly when there's nothing to process)
- GitHub's best-effort cron scheduling means the poller may not fire reliably, leading to user frustration if they expect timely responses (mitigated: manual dispatch is always available as a fallback, and Phase 2 Jira Automation webhooks provide instant, reliable triggering)
