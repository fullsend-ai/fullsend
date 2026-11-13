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
   - Enforces dry-run safety: `SCRIBE_DRY_RUN` must be set explicitly;
     `false` enables live writes, while any other value previews actions in
     logs without making mutations (the reference dogfood workflow in
     `fullsend-ai/.fullsend` defaults to previewing by passing `true`).
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
- **Sandbox network isolation**: Sandbox outbound network policy permits only
  the configured inference providers (Vertex AI and OpenAI in the shipped
  harness); curl, forge APIs, and Drive APIs are blocked to reduce the risk of
  token or credential exfiltration.
- **Credential isolation and host-side tokens**: Forge tokens (`GH_TOKEN`,
  `GITLAB_TOKEN`) and Jira API tokens remain host-side and are never injected
  into the sandbox container. The GCP service account key
  (`GOOGLE_APPLICATION_CREDENTIALS`) is mounted into the sandbox at
  `/tmp/.gcp-credentials.json` for Vertex AI inference, so it could also access
  Drive if granted; use a dedicated service account with minimal scope
  (invited calendar events only) and restrict sandbox egress to the required
  inference providers to minimize exfiltration risk.
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

### Prerequisites and workflow setup

Scribe is an opt-in agent and mint-only dogfood role; fullsend does not ship a
pre-configured scribe workflow under `.github/workflows/` or within
`internal/scaffold`, and `scribe` is **not** a valid role under the `roles:`
field in `config.yaml` (see
[Config Reference](../reference/config-reference.md#roles)).

To run scribe:
1. If using the fullsend token mint service, register the dogfood role:
   `fullsend mint add-role scribe`.
2. Register the agent harness with `fullsend agent add` (shown below).
3. Supply your own scheduled or dispatch workflow on your CI runner. For an
   example implementation, see the [reference scribe workflow in `fullsend-ai/.fullsend`](https://github.com/fullsend-ai/.fullsend/blob/main/.github/workflows/scribe.yml)
   or the workflow step guide in [agents `docs/scribe.md`](https://github.com/fullsend-ai/agents/blob/main/docs/scribe.md).

## Control labels

The scribe agent does not consume or apply control labels that gate agent
workflow execution. It applies a `meeting-notes` label (or allowlisted Jira
labels) to issues it creates for categorization and filtering.

## Configuration and extension

From the repository root, register the scribe agent (which writes to
`.fullsend/config.yaml` using the default `--fullsend-dir .fullsend`):

```bash
fullsend agent add \
  https://github.com/fullsend-ai/agents/blob/main/harness/scribe.yaml \
  --name scribe
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
workflow in [`fullsend-ai/.fullsend`](https://github.com/fullsend-ai/.fullsend/blob/main/.github/workflows/scribe.yml),
`SCRIBE_SEARCH_QUERY` is mapped from the repository variable
`vars.SCRIBE_GDRIVE_SEARCH_QUERY`, and `SCRIBE_NAME_FILTER` is mapped from
`vars.SCRIBE_GDRIVE_NAME_FILTER`.

Because `harness/scribe.yaml` references these environment variables in
`env.runner` and `env.sandbox`, every variable listed below must exist in the
runner environment (even if set to an empty string `""`); `fullsend run`
validates harness variable references and fails with `<VAR> is not set` if any
referenced variable is omitted. Documented defaults apply when the variable is
empty or set to its default value.

| Name | Required | Default | Description |
|------|----------|---------|-------------|
| `SCRIBE_REPO` | Yes | `-` | Target repository (`owner/name` on GitHub, `group/project` on GitLab) |
| `SCRIBE_SEARCH_QUERY` | Yes | `-` | Google Drive search query for meeting note document names (mapped from `vars.SCRIBE_GDRIVE_SEARCH_QUERY`) |
| `SCRIBE_NAME_FILTER` | No | `none` | Optional document title substring or regex filter (mapped from `vars.SCRIBE_GDRIVE_NAME_FILTER`) |
| `SCRIBE_LOOKBACK_HOURS` | No | `3` | Search lookback window in hours (applied when empty) |
| `SCRIBE_DRY_RUN` | Yes | `-` | `false` enables live writes; any other value previews actions in logs (post-script exits if unset) |
| `SCRIBE_MIN_CONFIDENCE` | No | `0.6` | Minimum confidence threshold for posting topics (applied when empty) |
| `SCRIBE_MODE` | No | `all` | Operating mode: `all`, `comments_only`, or `new_issues_only` (applied when empty) |
| `FULLSEND_FORGE` | Yes | `-` | Forge type: `github` or `gitlab` |
| `GH_TOKEN` | Yes (GitHub) | `-` | GitHub token with issue read/write permissions |
| `CONTENTS_TOKEN` | No (GitHub) | `GH_TOKEN` | Optional token for repository content reads; falls back to `GH_TOKEN` when empty |
| `GITLAB_TOKEN` | Yes (GitLab) | `-` | GitLab access token with API read/write permissions |
| `GOOGLE_APPLICATION_CREDENTIALS` | Yes | `-` | Path to GCP service account key for Drive reads and Vertex AI |
| `SCRIBE_DRIVE_CREDENTIALS` | No | `GOOGLE_APPLICATION_CREDENTIALS` | Path to an alternate Drive-scoped service account key; falls back to `GOOGLE_APPLICATION_CREDENTIALS` when empty |
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
and recently resolved issues for matching, and receives new issues.

Configure Jira connection settings under `tracker.jira` in
`.fullsend/config.yaml`:

```yaml
tracker:
  jira:
    base_url: https://myteam.atlassian.net
    project: PROJ
    # api_url: https://api.atlassian.com  # optional API gateway URL
```

Issue creation delegates to `fullsend issues create --tracker jira`. The
harness scripts in `fullsend-ai/agents` read `JIRA_BASE_URL`, `JIRA_TOKEN` and
the optional `JIRA_USER_EMAIL`, and pass the credentials on to
`fullsend issues create` as `FULLSEND_JIRA_TOKEN` and `FULLSEND_JIRA_EMAIL`.

To set up the Jira credentials:

1. **Repository secrets:** store the token as `FULLSEND_JIRA_TOKEN` and, for a
   bot account, its email as `FULLSEND_JIRA_EMAIL`. There is no `JIRA_TOKEN`
   or `JIRA_USER_EMAIL` secret. The scribe scripts need a token; OAuth client
   credentials are not supported.
2. **Action inputs:** pass the secrets to the fullsend action as inputs, not
   as step `env:`. The action sets `FULLSEND_JIRA_TOKEN` from its input and
   unsets it when the input is empty, so a step `env:` value never arrives.

   ```yaml
   with:
     jira-token: ${{ secrets.FULLSEND_JIRA_TOKEN }}
     jira-email: ${{ secrets.FULLSEND_JIRA_EMAIL }}
   ```

3. **Harness:** set `FULLSEND_TRACKER=jira` and map the Jira variables under
   `env.runner`. Extend the harness with `base:` composition in
   `.fullsend/scribe.yaml`:

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<SHA>/harness/scribe.yaml#sha256=<sha256sum>
env:
  runner:
    FULLSEND_TRACKER: jira
    SCRIBE_JIRA_PROJECT: PROJ
    SCRIBE_JIRA_ISSUE_TYPE: Task
    JIRA_BASE_URL: ${JIRA_BASE_URL}
    JIRA_TOKEN: ${FULLSEND_JIRA_TOKEN}
    # Bot user only. Omit this line for a service-account token, which
    # uses Bearer auth.
    JIRA_USER_EMAIL: ${FULLSEND_JIRA_EMAIL}
```

If `scribe` was previously registered from a remote URL, remove it first from
the repository root before registering the local harness override, as `fullsend
agent add` refuses duplicate agent names. Register the local file relative to
`--fullsend-dir` (default: `.fullsend`):

```bash
fullsend agent remove scribe
fullsend agent add scribe.yaml --name scribe
```

| Name | Required | Default | Description |
|------|----------|---------|-------------|
| `FULLSEND_TRACKER` | Yes (Jira) | `FULLSEND_FORGE` | Must be set to `jira` |
| `SCRIBE_JIRA_PROJECT` | Yes (Jira) | `-` | Jira project key for backlog reads and issue creation (matches `tracker.jira.project`) |
| `SCRIBE_JIRA_ISSUE_TYPE` | No | `Task` | Jira issue type for created issues (applied when empty) |
| `SCRIBE_JIRA_LABELS` | No | `meeting-notes` | Comma-separated allowlist of labels scribe may apply to Jira issues |
| `JIRA_BASE_URL` | Yes (Jira) | `-` | Jira Cloud instance URL (`https://<site>.atlassian.net`) |
| `JIRA_API_URL` | No | `JIRA_BASE_URL` | Target REST API gateway URL, read by external harness scripts |
| `JIRA_TOKEN` | Yes (Jira) | `-` | Jira API token the scripts read. The harness above maps it from the `FULLSEND_JIRA_TOKEN` repository secret |
| `JIRA_USER_EMAIL` | No | `none` | Bot-user email for Basic auth. The harness above maps it from the `FULLSEND_JIRA_EMAIL` repository secret; omit it for a service account's token (Bearer auth) |

In Jira mode:
- Only new issues are created. Comments on existing Jira issues are not
  supported (`all` operates as `new_issues_only`, and `comments_only` is
  rejected).
- Up to 50 pages of unresolved issues are fetched for backlog matching, along
  with recently resolved issues from the past 30 days to avoid re-proposing
  completed work.
- Issue creation delegates to `fullsend issues create --tracker jira`, which
  converts Markdown to Atlassian Document Format (ADF) and invokes Jira REST
  APIs. The CLI reads connection settings from `tracker.jira` in
  `.fullsend/config.yaml`, and the harness scripts translate `JIRA_TOKEN` and
  `JIRA_USER_EMAIL` into the `FULLSEND_JIRA_*` variables expected by the CLI.
- Labels are validated against an allowlist (`SCRIBE_JIRA_LABELS`) and applied
  host-side.
- For complete Jira configuration details and harness examples, see [Jira setup
  in `fullsend-ai/agents` `docs/scribe.md`](https://github.com/fullsend-ai/agents/blob/main/docs/scribe.md).

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
