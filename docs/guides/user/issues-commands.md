# Issue Commands

Read and write issue content across GitHub, GitLab, and Jira from
custom agent scripts using `fullsend issues get`,
`fullsend issues create`, `fullsend issues post-comment`, and
`fullsend issues link`.

## When to use

The built-in agent pipeline handles comment posting automatically for
GitHub-sourced events and for Jira-sourced triage and code runs. Use these
commands when:

- A custom agent needs to post results back to **Jira** or **GitLab**.
- An agent script needs to **read** issue content (title, body, labels,
  comments) from any tracker as structured JSON.
- You need **sticky comments** (find-and-update-by-marker) on a
  non-GitHub tracker.
- A workflow depends on **typed issue links** in Jira (for example, a
  bug that blocks a task).
- A planning or decomposition script needs to **create** issues,
  including Jira sub-tasks under a parent issue.

## Jira connection settings

The Jira commands take every non-secret setting from the
[`tracker.jira`](../../reference/config-reference.md#trackerjira-connection-and-polling-settings)
mapping in the config.yaml under `--fullsend-dir`. There are no Jira URL or
email flags and no `JIRA_*` environment variables:

```yaml
tracker:
  jira:
    base_url: https://myteam.atlassian.net
    project: PROJ
    # api_url: https://api.atlassian.com  # default
    allowed_targets:
      projects: [OPS]
```

- `base_url` and `project` are required. A config that only selects Jira
  (`jira: {}`) is rejected for Jira API calls.
- Requests go through the API gateway at `api_url`, which defaults to
  `https://api.atlassian.com`. Issue links in the output use `base_url`.
- `project` is the repository's primary project and the default
  `--project`.
- `issues create` may target only `project` or a project listed in
  `allowed_targets.projects`. Any other project is refused before Jira is
  called.

Credentials come only from the environment:

- `FULLSEND_JIRA_TOKEN` (required unless client credentials are set) is a
  scoped Jira API token.
- `FULLSEND_JIRA_EMAIL` (optional) is the bot user's account email. When it
  is set, the token is sent with Basic auth. When it is unset, the token is a
  service-account token sent as a Bearer token.
- `FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET` are a service
  account's OAuth 2.0 credential, used instead of the token and email. The
  client exchanges them for a short-lived Bearer access token. Setting them
  together with `FULLSEND_JIRA_TOKEN`, or only one of them, is an error.

`--token` is rejected with `--tracker jira`, so the token never appears on
the command line.

## `fullsend issues get`

Reads an issue's title, body, labels, and comments from the specified
tracker and prints them as JSON.

```bash
fullsend issues get \
  --tracker github \
  --project owner/repo \
  --number 42
```

For Jira, `--project` is the project key and `--number` is the numeric
issue ID (not the human-readable key). `--project` defaults to
`tracker.jira.project`:

```bash
fullsend issues get \
  --fullsend-dir .fullsend \
  --tracker jira \
  --number 101
```

The output JSON has `number`, `title`, `body`, `url`, `labels`, and
`comments`. For Jira, it also always includes `issue_type` (for example
`"Bug"`, `"Story"`, or `"Task"`).

To read Jira custom fields, pass their field IDs with `--fields`. The
raw value of each requested field appears under `custom_fields`. A
requested field that is unset or missing from the issue is `null`:

```bash
fullsend issues get \
  --fullsend-dir .fullsend \
  --tracker jira \
  --project PROJ \
  --number 101 \
  --fields customfield_10875,customfield_12345
```

```json
{
  "number": 101,
  "title": "Widget is broken",
  "body": "...",
  "url": "https://myteam.atlassian.net/browse/PROJ-101",
  "labels": [],
  "issue_type": "Bug",
  "custom_fields": {
    "customfield_10875": "https://github.com/acme/widgets/pull/7",
    "customfield_12345": null
  },
  "comments": []
}
```

Values keep the shape Jira returns, so select-list or user fields are
objects, not strings. GitHub and GitLab output has no `issue_type` or
`custom_fields`. For those trackers, `--fields` prints a warning and is
otherwise ignored.

### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--tracker` | Yes (unless config default set) | Tracker backend: `github`, `gitlab`, or `jira` |
| `--project` | GitHub/GitLab | Project identifier: `owner/repo` (GitHub/GitLab) or project key (Jira, default: `tracker.jira.project`) |
| `--number` | Yes | Issue number (must be a positive integer) |
| `--token` | No | GitHub/GitLab API token (default: env var per tracker). Rejected for Jira |
| `--fields` | Jira only | Comma-separated Jira custom field IDs (`customfield_<digits>`) to include under `custom_fields`. Repeatable. |
| `--fullsend-dir` | Jira | Path to `.fullsend` config directory. Supplies the default `--tracker` and the [Jira connection settings](#jira-connection-settings) |

## `fullsend issues create`

### Prerequisites

You need permission to create issues in the target project and a tracker
API token. For GitHub or GitLab, pass the project as `owner/repo` and supply
the token with `--token` or the relevant
[environment variable](#environment-variables). For Jira, configure the
[Jira connection settings](#jira-connection-settings), set
`FULLSEND_JIRA_TOKEN` (or the OAuth client credentials), and pass
`--fullsend-dir`. `--project` defaults to
`tracker.jira.project`. Another project must be listed in
`tracker.jira.allowed_targets.projects`. Jira also requires an issue type;
use the issue type's name or ID with `--type`.

Creates a new issue and prints its number, URL, and (for Jira) key as
JSON:

```json
{
  "number": 43,
  "key": "PROJ-43",
  "title": "Sub-task title",
  "url": "https://myteam.atlassian.net/browse/PROJ-43"
}
```

`key` is only present for Jira. `--body` is the issue description as
Markdown, or `-` to read it from stdin (capped at 2 MiB). Jira issue
descriptions are limited to 32 KiB of Markdown, whether supplied directly
or through stdin. Fullsend converts that Markdown to Atlassian Document
Format (ADF), Jira's structured format for rich text. Whitespace-only
`--body` input (including whitespace read from stdin) is treated as empty
and omitted from the Jira request rather than triggering an ADF conversion
error.

```bash
fullsend issues create \
  --tracker github \
  --project owner/repo \
  --title "Widget is broken" \
  --body "Steps to reproduce..."
```

For Jira, `--type` is required. `--parent` creates the issue under a
parent issue, which is how Jira sub-tasks are created:

```bash
cat plan.md | fullsend issues create \
  --fullsend-dir .fullsend \
  --tracker jira \
  --type 10003 \
  --parent PROJ-42 \
  --title "Sub-task title" \
  --body -
```

`--type` and `--parent` are only implemented for Jira so far. GitHub
and GitLab have their own equivalents (GitHub issue types and
sub-issues, GitLab `issue_type`), but they are not wired up yet.
Passing either flag with `--tracker github` or `--tracker gitlab` is
an error rather than being silently ignored.

### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--tracker` | Yes (unless config default set) | Tracker backend: `github`, `gitlab`, or `jira` |
| `--project` | GitHub/GitLab | Project identifier: `owner/repo` (GitHub/GitLab) or project key (Jira, default: `tracker.jira.project`; any other key must be in `tracker.jira.allowed_targets.projects`) |
| `--title` | Yes | Issue title |
| `--body` | No | Issue description as Markdown, or `-` to read from stdin (default: empty) |
| `--type` | Jira only (required) | Jira issue type: an all-digit value is an issue type ID (e.g. `10003`), anything else an issue type name (e.g. `Sub-task`) |
| `--parent` | Jira only | Parent issue key (e.g. `PROJ-42`) for sub-task creation |
| `--token` | No | GitHub/GitLab API token (default: env var per tracker). Rejected for Jira |
| `--fullsend-dir` | Jira | Path to `.fullsend` config directory. Supplies the default `--tracker` and the [Jira connection settings](#jira-connection-settings) |

## `fullsend issues post-comment`

Posts a comment with a sticky marker on an issue. On re-runs, finds
the existing comment by its marker and its own author (see
[Trust model](#trust-model)) and edits in-place. By default,
old content is collapsed into `<details>` blocks to preserve history;
set `keep_history: false` in config.yaml (or pass `--keep-history=false`)
to replace the body with no history.
This prevents comment flooding on re-runs. For GitHub and GitLab, the marker is embedded as an invisible
HTML comment in the body. For Jira, the marker is stored as a comment
entity property (Jira has no HTML comments, so a body-embedded marker
would be visible to users).

```bash
echo "Triage complete. See PR #99." | fullsend issues post-comment \
  --fullsend-dir .fullsend \
  --tracker jira \
  --number 101 \
  --marker "<!-- fullsend:triage-agent -->"
```

### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--tracker` | Yes (unless config default set) | Tracker backend: `github`, `gitlab`, or `jira` |
| `--project` | GitHub/GitLab | Project identifier: `owner/repo` (GitHub/GitLab) or project key (Jira, default: `tracker.jira.project`) |
| `--number` | Yes | Issue number (must be a positive integer) |
| `--marker` | Yes | Sticky marker for idempotent updates (e.g. `<!-- fullsend:my-agent -->`). GitHub/GitLab: hidden HTML comment in body. Jira: comment entity property. |
| `--result` | No | Path to comment body file, or `-` for stdin (default: `-`) |
| `--token` | No | GitHub/GitLab API token (default: env var per tracker). Rejected for Jira |
| `--dry-run` | No | Print what would be posted without posting or editing anything |
| `--keep-history` | No | Append previous content as collapsed history blocks (default: `true`; set `false` to replace in-place) |
| `--only-if-exists` | No | Update an existing comment with this marker but never create one. Use it for an all-clear result, so a findings comment from an earlier run is updated in place (the old findings are kept as collapsed history unless `--keep-history=false`) while a clean first run posts nothing. Only a comment whose author is exactly the identity posting matches; if that identity cannot be resolved, the command fails without posting. |
| `--fullsend-dir` | Jira | Path to `.fullsend` config directory. Supplies defaults from its `config.yaml` when flags are omitted, including the [Jira connection settings](#jira-connection-settings) |

### Jira marker storage

For `--tracker jira`, the `--marker` value is stored as an invisible
comment entity property rather than embedded in the visible comment
body. Jira's ADF format has no HTML comment equivalent, so
body-embedded markers would be visible to users. Because the marker
lives in a property, character restrictions do not apply — any
characters valid in the `--marker` flag are fine for Jira.

## `fullsend issues link`

Creates a typed link from the `--from` issue to the `--to` issue. Only
Jira supports typed issue links; GitHub and GitLab have no first-class
equivalent, so `--tracker github` and `--tracker gitlab` fail with a
"not supported" error.

```bash
fullsend issues link \
  --fullsend-dir .fullsend \
  --tracker jira \
  --from PROJ-123 \
  --to PROJ-456 \
  --type Blocks
```

`--from` and `--to` are Jira issue keys (`PROJECT-NUMBER`); the two
issues may be in different projects. `--type` is the Jira link type
name, such as `Blocks`, `Relates`, or `Cloners`. Link type names are
instance-specific, so the CLI passes `--type` through without
validating it; an unknown name fails with Jira's error. The link is
created in the type's outward direction: the example above records that
`PROJ-123` blocks `PROJ-456`.

### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--tracker` | Yes (unless config default set) | Tracker backend: `github`, `gitlab`, or `jira` (only `jira` supports links) |
| `--from` | Yes | Source issue key (e.g. `PROJ-123`) |
| `--to` | Yes | Target issue key (e.g. `PROJ-456`) |
| `--type` | Yes | Link type name (e.g. `Blocks`), passed through verbatim |
| `--token` | No | Rejected for Jira, which reads `FULLSEND_JIRA_TOKEN` or the OAuth client credentials |
| `--fullsend-dir` | Jira | Path to `.fullsend` config directory. Supplies the default `--tracker` and the [Jira connection settings](#jira-connection-settings) |

## Config-based default tracker

Set a default tracker in `.fullsend/config.yaml` to avoid passing
`--tracker` on every invocation:

```yaml
tracker:
  jira:
    base_url: https://myteam.atlassian.net
    project: PROJ
```

The provider block that is present under `tracker` selects the default.
For Jira the same block also supplies the
[connection settings](#jira-connection-settings).
Existing configs that use the scalar form (`tracker: jira`) must be migrated
to this form; the scalar is rejected.

Then pass `--fullsend-dir .fullsend` (or let the agent pipeline supply
it). An explicit `--tracker` flag overrides the config default. See
[Layered Config Reference](../infrastructure/layered-config-reference.md)
for how the `tracker` field resolves through the config overlay chain.

## Trust model

An existing comment is edited only when it carries the marker **and**
its author is exactly the identity the command posts as: the login on
GitHub and GitLab, where markers are hidden HTML comments in the body,
and the account ID (from `GET /myself`) on Jira, where markers are
comment entity properties. Display names and login shapes are never
trusted. A comment anyone else wrote with the same marker, as body text
or as a comment property on their own comment, is ignored, never edited.
Anyone who can edit the command's own comments (a repository maintainer,
or a Jira user with Edit All Comments) is still trusted: their edits are
kept as history when the comment is next updated.

If that identity cannot be resolved after a short retry, the command
fails with an error naming the cause and posts or edits nothing, with or
without `--only-if-exists`. Posting a new comment instead would leave the
earlier one behind with content no later run updates. To recover:

1. Rerun the command. A transient API failure clears on its own.
2. If it fails again, check that the token can read its own identity:
   `GET /user` (or the GraphQL `viewer`) on GitHub, `GET /user` on
   GitLab, `GET /myself` on Jira.

## Environment variables

| Variable | Tracker | Description |
|----------|---------|-------------|
| `GH_TOKEN` or `GITHUB_TOKEN` | GitHub | GitHub API token |
| `GITLAB_TOKEN` | GitLab | GitLab API token |
| `FULLSEND_JIRA_TOKEN` | Jira | Scoped Jira API token (required unless the OAuth client credentials are set) |
| `FULLSEND_JIRA_EMAIL` | Jira | Bot user account email. When it is set, the token is sent with Basic auth. When it is unset, the token is a service-account token sent as a Bearer token |
| `FULLSEND_JIRA_CLIENT_ID`, `FULLSEND_JIRA_CLIENT_SECRET` | Jira | A service account's OAuth 2.0 client credentials, used instead of `FULLSEND_JIRA_TOKEN` |

Jira site and project settings are never read from the environment; see
[Jira connection settings](#jira-connection-settings).

## See also

- [Jira Integration](jira-integration.md) -- polling and dispatch setup
- [Bring Your Own Agent](bring-your-own-agent.md) -- adding custom agents
- [Layered Config Reference](../infrastructure/layered-config-reference.md) -- config field documentation
