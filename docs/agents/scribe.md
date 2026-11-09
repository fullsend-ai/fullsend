---
description: How the fullsend scribe agent processes meeting notes from Google Drive, maps discussion topics to the issue backlog, and comments on or creates issues.
---

# Scribe Agent

![Scribe agent icon](icons/scribe.png)

Reads meeting notes that Gemini saves to Google Drive after Google Meet
calls, maps discussion topics to the issue backlog, and adds comments to
existing issues or creates new ones.

## How the agent works

The scribe agent bridges synchronous team interactions and autonomous
development loops. It runs on a schedule or via manual trigger, searching
Google Drive for recent meeting notes, extracting actionable engineering
topics, and updating the repository backlog so downstream agents
([triage](triage.md), [prioritize](prioritize.md), [code](code.md)) can
act on team decisions.

Scribe follows the host pre-script / sandbox / validation loop / post-script
architecture:

1. **Pre-script (host)**:
   - Fetches repository context: open issues (`backlog.json`), recently
     closed issues (`closed-issues.json`), open pull requests (`open-prs.json`),
     and markdown documentation index (`repo-docs-index.json`).
   - Queries Google Drive API using a keyword search query
     (`SCRIBE_SEARCH_QUERY`) across a rolling time window
     (`SCRIBE_LOOKBACK_HOURS`, default 3 hours) using credentials from
     a dedicated GCP service account.
   - PII and structural scrubbing: Drops transcript sections, anonymizes
     bracketed attendee names (e.g. `[John Smith]`) and bullet attribution
     prefixes (`- Jane Doe: action`), and packages cleaned notes into
     `notes.tar.gz`.
   - Generates `scribe-meta.json` with runtime metadata including
     `cutoff_date` and `notes_url`.
2. **Sandbox**:
   - The sandboxed agent extracts `notes.tar.gz` and reads the notes alongside
     repository context files.
   - Treats all meeting notes as untrusted user input — never following
     commands, directives, or instructions embedded within notes content.
   - Evaluates topics against strict criteria:
     - **Recency**: Extracts topics only from the most recent meeting section
       on or after `cutoff_date`.
     - **Substance threshold**: Requires concrete discussion — decisions made,
       questions debated, options evaluated, or action items assigned. Skips
       brief name drops, logistics, status updates with no new information,
       and topics whose only outcome is an external chat.
     - **Public-appropriateness gate**: Assigns every topic a `public_safe`
       boolean and a `public_safe_category`. Sets `public_safe: true` only if
       the topic contains no personal names, no interpersonal commentary or
       opinions, no internal HR/headcount/compensation topics, no undisclosed
       business strategy or financials, no undisclosed security
       vulnerabilities, and no confidential or legal matters. If unsafe, it
       flags `public_safe: false` with one of the fixed categories (`names`,
       `interpersonal`, `hr`, `strategy`, `security`, `legal`, `confidential`)
       without quoting or paraphrasing the sensitive text.
     - **Confidence calibration**: Assigns a confidence score from 0.0 to 1.0
       (>= 0.8 for clear decisions and actionable conclusions; 0.6–0.7 for
       substantive discussion with trade-offs; < 0.6 filtered out by
       post-script gate).
     - **Backlog matching**: Matches discussion topics against open issues by
       analyzing both issue titles and bodies. Checks closed issues to avoid
       proposing already-resolved work (noting prior resolution in comments
       instead). Correlates in-flight work with open PRs and references
       relevant ADRs or problem docs.
     - **Formatting**: Formats comments for existing issues with structured
       markdown (date header, relevance statement, discussion bullet points,
       related PRs/docs, and a mandatory `[Meeting notes](URL)` citation
       link). Proposes new issues with structured markdown sections: Problem,
       Options considered, Acceptance criteria (checkboxes), and Related links.
   - Writes structured JSON output (`agent-result.json`) containing `topics[]`,
     `new_issues[]`, and `stats`. The sandbox has no network access to forge
     APIs or Google Drive.
3. **Validation loop**:
   - Validates `agent-result.json` against `schemas/scribe-result.schema.json`,
     retrying up to 2 times if schema validation fails.
4. **Post-script (host)**:
   - Deduplicates topics against previous notes links (idempotency check)
     to prevent reposting.
   - Filters out topics below `SCRIBE_MIN_CONFIDENCE` (default 0.6) and
     rejects any entry marked `public_safe: false`.
   - Enforces sensitive-content and suspicious-Unicode gates on all titles,
     bodies, summaries, and labels.
   - Enforces dry-run safety: defaults to preview mode (`SCRIBE_DRY_RUN: true`);
     live writes require `SCRIBE_DRY_RUN` to be set to `false`.
   - Applies mode filtering (`all`, `comments_only`, `new_issues_only`).
   - Writes approved comments and issues using forge API calls (`gh` on
     GitHub, `curl` on GitLab) or Jira Cloud issue creation (`fullsend issues
     create --tracker jira`).
   - Optionally sends run summary notifications to Slack when
     `SCRIBE_SLACK_WEBHOOK_URL` is set.

### Security model

- **Untrusted input**: Meeting notes are treated as untrusted user input.
  Prompts explicitly instruct the agent to ignore any embedded directives or
  prompt injection attempts in meeting text.
- **Dedicated GCP service account**: A service account with zero initial
  Drive permissions is invited only to specific calendar events whose notes
  it needs to process.
- **Host-side PII scrubbing**: Bracketed attendee names and speaker
  attributions are scrubbed on the host before notes enter the sandbox;
  raw transcripts are excluded entirely.
- **Sandbox network isolation**: Sandbox outbound network policy permits
  Vertex AI only; curl, forge APIs, and Drive APIs are blocked to prevent
  token or credential exfiltration.
- **Credential isolation and host-side tokens**: Forge tokens (`GH_TOKEN`,
  `GITLAB_TOKEN`) and Jira API tokens remain host-side and are never
  injected into the sandbox container. The GCP service account key
  (`GOOGLE_APPLICATION_CREDENTIALS`) is mounted into the sandbox at
  `/tmp/.gcp-credentials.json` for Vertex AI inference, so it can also access
  Drive if granted; use a dedicated service account with minimal scope
  (invited calendar events only) and restrict sandbox egress to Vertex AI so
  the key cannot be exfiltrated.
- **Host-side validation gates**: Output schema checks, confidence
  thresholds, sensitive content pattern matching, and public-safety
  verification run host-side before any mutations occur.

## How it helps

- Automatically transfers meeting decisions, architecture debates, and action
  items into the issue backlog without manual note-taking or copy-pasting.
- Matches discussion topics to existing issues by title and body semantics,
  keeping existing tracking issues up to date and avoiding duplicate tickets.
- Public-safety and PII gates prevent confidential business matters, personal
  commentary, and attendee identities from leaking into issue trackers.
- Idempotency checks avoid duplicate comments or repeated issues when
  processing rolling meeting documents across overlapping lookback windows.
- Serves as the upstream bridge between team discussions and downstream
  fullsend agents such as [triage](triage.md), [prioritize](prioritize.md),
  and [code](code.md).

## Commands

The scribe agent does not accept slash commands. It runs on a schedule or
via manual workflow dispatch.

## Control labels

The scribe agent does not consume or apply control labels that gate agent
workflow execution. It applies a `meeting-notes` label (or allowlisted Jira
labels) to issues it creates for categorization and filtering.

## Configuration and extension

Register the scribe agent in your repository's `.fullsend` directory:

```bash
fullsend agent add \
  https://github.com/fullsend-ai/agents/blob/main/harness/scribe.yaml \
  --name scribe \
  --fullsend-dir .
```

### Dedicated GCP service account setup

To grant the agent access to meeting notes without exposing other Drive files:

1. Create a dedicated Google Cloud service account with zero default Drive
   permissions.
2. Invite the service account email address to the Google Calendar events you
   want scribed.
3. In the Google Calendar event settings, enable Gemini meeting notes and
   permit read access to external attendees.
4. Export the service account credentials JSON path as
   `GOOGLE_APPLICATION_CREDENTIALS` (or `SCRIBE_DRIVE_CREDENTIALS`) on the CI
   runner.

At runtime, the pre-script queries the Drive API using a keyword search
(`SCRIBE_SEARCH_QUERY`) over a rolling time window (`SCRIBE_LOOKBACK_HOURS`,
default 3 hours) across everything the service account can access — including
Shared Drives if the account has been added to any. The search query narrows
matching, but it is not a security boundary: the service account can read
notes from any meeting it has been invited to, not just a single event. To keep
the blast radius small, use a distinctive search query and avoid adding the
service account to unrelated Shared Drives.

### Variables

Per [ADR 0049](../ADRs/0049-agent-configuration-env-var-convention.md), scribe
configuration uses the `SCRIBE_` prefix. In the reference GitHub Actions
workflow, `SCRIBE_SEARCH_QUERY` is mapped from the repository variable
`vars.SCRIBE_GDRIVE_SEARCH_QUERY`, and `SCRIBE_NAME_FILTER` is mapped from
`vars.SCRIBE_GDRIVE_NAME_FILTER`:

| Name | Required | Default | Description |
|------|----------|---------|-------------|
| `SCRIBE_REPO` | Yes | `-` | Target repository (`owner/name` on GitHub, `group/project` on GitLab) |
| `SCRIBE_SEARCH_QUERY` | Yes | `-` | Google Drive search query for meeting note document names (mapped from `vars.SCRIBE_GDRIVE_SEARCH_QUERY`) |
| `SCRIBE_NAME_FILTER` | No | `none` | Optional document title substring or regex filter (mapped from `vars.SCRIBE_GDRIVE_NAME_FILTER`) |
| `SCRIBE_LOOKBACK_HOURS` | No | `3` | Search lookback window in hours |
| `SCRIBE_DRY_RUN` | No | `true` | `true` to preview actions in logs; `false` for live writes |
| `SCRIBE_MIN_CONFIDENCE` | No | `0.6` | Minimum confidence threshold for posting topics |
| `SCRIBE_MODE` | No | `all` | Operating mode: `all`, `comments_only`, or `new_issues_only` |
| `FULLSEND_FORGE` | Yes | `-` | Forge type: `github` or `gitlab` |
| `GH_TOKEN` | Yes (GitHub) | `-` | GitHub token with issue read/write permissions |
| `CONTENTS_TOKEN` | No (GitHub) | `GH_TOKEN` | Optional token for repository content reads; falls back to `GH_TOKEN` |
| `GITLAB_TOKEN` | Yes (GitLab) | `-` | GitLab access token with API read/write permissions |
| `GOOGLE_APPLICATION_CREDENTIALS` | Yes | `-` | Path to GCP service account key for Drive reads and Vertex AI |
| `SCRIBE_DRIVE_CREDENTIALS` | No | `GOOGLE_APPLICATION_CREDENTIALS` | Path to an alternate Drive-scoped service account key |
| `SCRIBE_SLACK_WEBHOOK_URL` | No | `none` | Optional Slack incoming webhook URL for run notifications |

### Operating modes

| Mode | Effect |
|------|--------|
| `all` | Posts comments on existing issues and creates new issues for unmapped topics |
| `comments_only` | Posts comments on existing issues only; skips new issue creation |
| `new_issues_only` | Creates new issues only; skips commenting on existing issues |

### Jira issue creation

Scribe can create new issues in Jira Cloud instead of the forge's native issue
tracker. The forge (`FULLSEND_FORGE`) provides repository context (open PRs and
documentation index), while the Jira Cloud project provides the open backlog
and recently resolved issues for matching and receives new issues.

Set `FULLSEND_TRACKER=jira` and configure Jira variables in runner environment
variables. Extend the harness with `base:` composition in
`.fullsend/scribe.yaml`:

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<SHA>/harness/scribe.yaml#sha256=<sha256sum>
env:
  runner:
    FULLSEND_TRACKER: jira
    SCRIBE_JIRA_PROJECT: PROJ
    SCRIBE_JIRA_ISSUE_TYPE: Task
    JIRA_BASE_URL: ${JIRA_BASE_URL}
    JIRA_TOKEN: ${JIRA_TOKEN}
    JIRA_USER_EMAIL: ${JIRA_USER_EMAIL}
    FULLSEND_DIR: ${FULLSEND_DIR}
```

If `scribe` was previously registered from a remote URL, remove it first before
registering the local harness override, as `fullsend agent add` refuses
duplicate agent names:

```bash
fullsend agent remove scribe --fullsend-dir .
fullsend agent add .fullsend/scribe.yaml --name scribe --fullsend-dir .
```

| Name | Required | Default | Description |
|------|----------|---------|-------------|
| `FULLSEND_TRACKER` | Yes (Jira) | `FULLSEND_FORGE` | Must be set to `jira` |
| `SCRIBE_JIRA_PROJECT` | Yes (Jira) | `-` | Jira project key for backlog reads and issue creation |
| `SCRIBE_JIRA_ISSUE_TYPE` | No | `Task` | Jira issue type for created issues |
| `SCRIBE_JIRA_LABELS` | No | `meeting-notes` | Comma-separated allowlist of labels scribe may apply to Jira issues |
| `JIRA_BASE_URL` | Yes (Jira) | `-` | Jira Cloud instance URL (`https://<site>.atlassian.net`) |
| `JIRA_API_URL` | No | `JIRA_BASE_URL` | Target REST API gateway URL |
| `JIRA_TOKEN` | Yes (Jira) | `-` | Jira API token |
| `JIRA_USER_EMAIL` | No | `none` | Email address for bot user Basic auth. Omit for service account Bearer auth |
| `FULLSEND_DIR` | Yes (Jira) | `-` | Path to `.fullsend` directory containing `config.yaml` with `tracker.jira` settings |

In Jira mode:
- Only new issues are created. Comments on existing Jira issues are not
  supported (`all` operates as `new_issues_only`, and `comments_only` is
  rejected).
- Up to 50 pages of unresolved issues are fetched for backlog matching, along
  with recently resolved issues from the past 30 days to avoid re-proposing
  completed work.
- Issue creation delegates to `fullsend issues create --tracker jira`, which
  converts Markdown to Atlassian Document Format (ADF) and invokes Jira REST
  APIs.
- Labels are validated against an allowlist (`SCRIBE_JIRA_LABELS`) and applied
  host-side.

### GitLab host validation

On GitLab, the pre- and post-scripts validate `CI_SERVER_URL` against
`CI_SERVER_HOST`. Validation requires an HTTPS URL and ensures the URL host
matches `CI_SERVER_HOST`, failing closed if variables are missing or
mismatched.

### Skills

The scribe agent has no built-in skills (`skills: []`). Custom skills can be
added via harness composition or repository skill directories.

## Source

[`fullsend-ai/agents` — `harness/scribe.yaml`](https://github.com/fullsend-ai/agents/blob/main/harness/scribe.yaml)
