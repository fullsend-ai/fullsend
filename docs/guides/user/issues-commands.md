# Issue Commands

Read and write issue content across GitHub, GitLab, and Jira from
custom agent scripts using `fullsend issues get` and
`fullsend issues post-comment`.

## When to use

The built-in agent pipeline handles comment posting automatically for
GitHub-sourced events. Use these commands when:

- A custom agent needs to post results back to **Jira** or **GitLab**.
- An agent script needs to **read** issue content (title, body, labels,
  comments) from any tracker as structured JSON.
- You need **sticky comments** (find-and-update-by-marker) on a
  non-GitHub tracker.

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
issue ID (not the human-readable key):

```bash
fullsend issues get \
  --tracker jira \
  --project PROJ \
  --number 101 \
  --jira-url https://myteam.atlassian.net \
  --jira-email you@example.com
```

### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--tracker` | Yes (unless config default set) | Tracker backend: `github`, `gitlab`, or `jira` |
| `--project` | Yes | Project identifier: `owner/repo` (GitHub/GitLab) or project key (Jira) |
| `--number` | Yes | Issue number (must be a positive integer) |
| `--token` | No | API token (default: env var per tracker) |
| `--jira-url` | Jira only | Jira instance URL (default: `$JIRA_BASE_URL`) |
| `--jira-email` | Jira only | Jira user email for auth (default: `$JIRA_USER_EMAIL`) |
| `--fullsend-dir` | No | Path to `.fullsend` config directory (sources defaults from its `config.yaml` when flags are omitted) |

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
  --tracker jira \
  --project PROJ \
  --number 101 \
  --marker "<!-- fullsend:triage-agent -->" \
  --jira-url https://myteam.atlassian.net \
  --jira-email you@example.com
```

### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--tracker` | Yes (unless config default set) | Tracker backend: `github`, `gitlab`, or `jira` |
| `--project` | Yes | Project identifier: `owner/repo` (GitHub/GitLab) or project key (Jira) |
| `--number` | Yes | Issue number (must be a positive integer) |
| `--marker` | Yes | Sticky marker for idempotent updates (e.g. `<!-- fullsend:my-agent -->`). GitHub/GitLab: hidden HTML comment in body. Jira: comment entity property. |
| `--result` | No | Path to comment body file, or `-` for stdin (default: `-`) |
| `--token` | No | API token (default: env var per tracker) |
| `--jira-url` | Jira only | Jira instance URL (default: `$JIRA_BASE_URL`) |
| `--jira-email` | Jira only | Jira user email for auth (default: `$JIRA_USER_EMAIL`) |
| `--dry-run` | No | Print what would be posted without posting or editing anything |
| `--keep-history` | No | Append previous content as collapsed history blocks (default: `true`; set `false` to replace in-place) |
| `--only-if-exists` | No | Update an existing comment with this marker but never create one. Use it for an all-clear result, so a findings comment from an earlier run is updated in place (the old findings are kept as collapsed history unless `--keep-history=false`) while a clean first run posts nothing. Only a comment whose author is exactly the identity posting matches; if that identity cannot be resolved, the command fails without posting. |
| `--fullsend-dir` | No | Path to `.fullsend` config directory (sources defaults from its `config.yaml` when flags are omitted) |

### Jira marker storage

For `--tracker jira`, the `--marker` value is stored as an invisible
comment entity property rather than embedded in the visible comment
body. Jira's ADF format has no HTML comment equivalent, so
body-embedded markers would be visible to users. Because the marker
lives in a property, character restrictions do not apply — any
characters valid in the `--marker` flag are fine for Jira.

## Config-based default tracker

Set a default tracker in `.fullsend/config.yaml` to avoid passing
`--tracker` on every invocation:

```yaml
tracker: jira
```

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
| `JIRA_TOKEN` | Jira | Jira API token |
| `JIRA_BASE_URL` | Jira | Jira instance URL |
| `JIRA_USER_EMAIL` | Jira | Email for Jira Cloud Basic auth |

## See also

- [Jira Integration](jira-integration.md) -- polling and dispatch setup
- [Bring Your Own Agent](bring-your-own-agent.md) -- adding custom agents
- [Layered Config Reference](../infrastructure/layered-config-reference.md) -- config field documentation
