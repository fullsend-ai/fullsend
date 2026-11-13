# Jira Integration

> **Pre-alpha.** This feature is under active development and not ready for general use. If you're reading this, you're probably helping build it — thank you. Expect rough edges, breaking changes, and missing pieces.

Connect fullsend to a Jira project so that Jira issue activity — comments, label changes — triggers the same agents that run on GitHub and GitLab.

## Setup overview

1. **Create a dedicated Jira account** for fullsend and choose an API token
   or an OAuth 2.0 client credential. Store it as a GitHub Actions secret or
   GitLab CI/CD variable. `repos install` manages these credentials on either
   forge. See [Credential setup](#credential-setup), [Use a dedicated account
   for the poller](#use-a-dedicated-account-for-the-poller), and [Use OAuth
   2.0 client credentials](#use-oauth-20-client-credentials-for-a-service-account).
2. **Grant it the minimum Jira permissions** in the target project, usually
   through the **Developers** project role. The poller needs Browse
   Projects, Edit Issues, Add Comments, and Edit Own Comments to operate on
   issues and write the entity properties it uses for lock and checkpoint
   coordination. Then choose how Jira users are authorized:
   - **Recommended:** list who may trigger agents under a `jira` provider in
     `.fullsend/config.yaml`. The poller account then also needs the
     Browse users and groups global permission, but only if the lists name
     groups.
   - **Project roles:** with no `jira` entry, every Jira event is authorized by
     the actor's Jira project role; with an entry that sets no lists, slash
     commands are. Reading project roles needs the **Administrators** project
     role (Administer Projects). With `labels: project_roles`, label-added
     events are too, and with no lists every event is. See
     [Keeping project roles](#keeping-project-roles-as-today).

   With no `jira` entry, label-added events also go through project roles, so
   the poller needs Administer Projects. A `jira` entry (default
   `labels: jira_editors`) authorizes them by Edit Issues instead. See
   [Actor role resolution](#actor-role-resolution).
3. **Install fullsend** in the GitHub repository that will run the agents (`fullsend github setup`). See [Configuring GitHub](../getting-started/configuring-github.md) and [Prerequisites](#prerequisites).
4. **Configure the Jira site and project** under `tracker.jira` in
   `.fullsend/config.yaml`: set `base_url`, `project`, and
   `poll.enabled: true`, plus `poll.query` if you want a narrower candidate
   set. The poller reads these settings only from the config. See
   [Custom JQL](#custom-jql).
5. **Run `fullsend repos install`** with either `--jira-token` (and optional
   `--jira-email`) or `--jira-client-id` and `--jira-client-secret`. On GitHub,
   install writes the scheduled workflow; on GitLab, it creates the pipeline
   schedule. See [Scheduled workflow](#scheduled-workflow) and
   [GitLab runtime](#gitlab-runtime).
6. **Check which agents handle Jira events.** The poller dispatches an agent
   only when its harness `trigger:` CEL expression matches the Jira event, and
   produces zero dispatches for a harness with no trigger, its own or
   inherited. The built-in triage and code harnesses ship triggers that match
   Jira events, so they need no extra configuration:

   | Built-in agent | Jira events its trigger matches | From |
   |---|---|---|
   | triage | A `/fs-triage` comment from a human, the `ready-for-triage` label being added, or a human opening or editing an issue | The agents release that follows v0.49.0 |
   | code | A `/fs-code` comment from a human, or the `ready-to-code` label being added | The agents release that follows v0.52.0 ([agents#1842](https://github.com/fullsend-ai/agents/pull/1842)) |

   The actor still needs the role the agent requires. See
   [Actor role resolution](#actor-role-resolution). The default prioritize
   trigger also matches a `/fs-prioritize` comment on a Jira issue, but
   prioritize has no Jira support yet
   ([#2266](https://github.com/fullsend-ai/fullsend/issues/2266)). Review,
   fix and retro do not run from Jira events: see [How it works](#how-it-works).

   If your built-in agents are pinned to an earlier agents release, or for a
   custom agent, add a `trigger:` to a harness YAML file registered via
   `config.yaml`'s `agents[].source` (see [Configuring agent
   behavior](customizing-agents.md#configuration-with-base-composition)). To
   use the built-in triggers instead, move to the release listed above. Re-pin
   a harness registered with `source:` or composed with `base:` with
   `fullsend agent update <name>`. The
   poller reads only the harness `trigger:`. A `trigger` on an `agents:` entry
   in `config.yaml` does not affect it: that field decides routing for the
   built-in review, retro, triage and prioritize agents on GitHub only
   ([Choosing when review runs](customizing-agents.md#choosing-when-review-runs),
   [Choosing when retro runs](customizing-agents.md#choosing-when-retro-runs),
   [Choosing when triage and prioritize
   run](customizing-agents.md#choosing-when-triage-and-prioritize-run)).
   See [CEL Triggers Reference](cel-triggers-reference.md).

   > [!WARNING]
   > A harness you registered with `source:` that points at a default
   > harness, or composes it with `base:`, and sets no trigger of its own
   > inherits the default trigger when you re-pin its ref to a release that
   > ships one. For code (from the agents release that follows v0.52.0), a
   > `/fs-code` comment or an added `ready-to-code` label on a Jira issue then
   > dispatches it. For triage (from the agents release that follows
   > v0.49.0), every Jira issue that a person with triage access or above
   > opens or edits dispatches triage, not only
   > `/fs-triage`, and on GitHub an entry registered under another name, such
   > as `my-triage`, is dispatched on every issue a person opens or edits.
   > Set your own trigger to keep a narrower one.
   > On Jira, an issue opener or editor gets triage access or above from the
   > project roles when there is no `jira` entry (or `labels: project_roles`
   > with no lists), or from the `jira` authorization lists when there is
   > one. Any other `jira` entry that sets no `write` or `triage` list leaves
   > opened and edited events unresolved, so they do not dispatch. See [Actor role resolution](#actor-role-resolution).

## How it works

A scheduled GitHub Actions workflow runs `fullsend poll --input-driver jira-poll` on a cron. For a GitLab project, a scheduled GitLab CI job runs it with `--target-forge gitlab` instead (see [GitLab runtime](#gitlab-runtime)). Each cycle:

1. Queries Jira for recently updated issues in your project.
2. Detects new comments and label changes since the last poll.
3. Converts each change to a [NormalizedEvent](../../normative/normalized-event/v1/) — the forge-neutral event struct that all input drivers (GitHub, GitLab, Jira) produce.
4. Routes through the standard agent routing rules.
5. Writes dispatch records that trigger agent workflows, or, for a GitLab target, starts the agent pipelines directly.

For architectural details on the polling protocol, see [Architecture](../../architecture.md#agent-dispatch-and-coordination-layer).

The same conventions work across forges:

| Jira action | Agent triggered |
|---|---|
| Comment starting with `/fs-triage` | triage |
| Comment starting with `/fs-code` | code |
| Label `ready-to-code` added | code |

After the code agent has opened a pull request or merge request, see
[Changing direction after a pull request
exists](#changing-direction-after-a-pull-request-exists) before you comment
`/fs-code` again.

A slash command is only recognized as the **first token of the comment's first
line**. The rest of that line is recorded in the normalized event as
`transition.comment.instruction`. Later lines are not part of it. No built-in
agent that runs from Jira reads that field: only the fix agent receives an
instruction today, parsed from the `/fs-fix` comment on the pull or merge
request, and fix does not run from Jira events. The built-in triage and code
agents instead read the Jira issue and its comments through the read-only Jira
provider (`jira-ro`). So the whole comment, including any lines after the
command, reaches them as part of the issue context, together with every other
comment on the issue. A custom agent's `trigger` can test
`event.transition.comment.instruction`.
A command buried mid-sentence ("please /fs-triage this") or on a later line does not trigger. Slash commands follow the `/fs-{agent}` pattern for stages that can legitimately run against a bare Jira issue. `review`, `fix`, and `retro` are **not** among them: per the [jira-poll adapter spec](../../normative/normalized-event/v1/jira-poll-adapter.md#state), those stages are change-proposal-scoped (they act on an existing PR) and harness CEL triggers for them MUST require `entity.kind == 'change_proposal'` — which a Jira issue comment alone never has. A `/fs-review` comment on a Jira issue is not expected to dispatch anything.

Comments and changelog entries from the **authenticated poller account** are classified as `actor.kind: bot` and do not dispatch agents. The poller learns that identity from Jira's `/myself` endpoint during the auth preflight, so a dedicated regular Atlassian account (plain display name, `accountType: atlassian`) is recognized without renaming it or adding display-name heuristics. This is the invariant that a mutation performed by fullsend — applying `needs-info`, posting a triage comment, writing a run-status update — cannot trigger the same agent again. Human comments, including `/fs-triage`, still dispatch. Filtered self-authored events still advance the per-issue checkpoint, so they are not reconsidered on the next poll.

The poller also classifies an actor as a bot, and drops its events, when
either of these holds:

- The actor's Jira account type is `app`.
- The actor's display name, compared case-insensitively, ends in `bot` or
  `automation` as a separate word, or starts with `Automation for `. A
  separate word follows a space, hyphen, underscore or `[`, and may be
  followed by a closing `]`. `Release Bot`, `ci-bot`, `[automation]` and
  Atlassian's `Automation for Jira` match. `Dependabot` and `Marbot` do not.
  A person whose display name matches is treated as a bot too, so their
  comments and label changes do not dispatch.

### Use a dedicated account for the poller

Give the poller a Jira account of its own, rather than a person's API token.
Today that means a dedicated non-human account: a regular Atlassian user
account (often called a bot account) created for fullsend, with its own email
and API token. It is not an Atlassian-managed service account. Those
authenticate through `api.atlassian.com` with scoped API tokens. Fullsend's
shared Jira client supports scoped tokens through the API gateway. It uses
`FULLSEND_JIRA_TOKEN`, plus `FULLSEND_JIRA_EMAIL` for a bot user, or no email
for a service account (see the
[Config Reference](../../reference/config-reference.md#trackerjira-connection-and-polling-settings)).
The poller, `fullsend issues` commands, run-status notifications, and
`fullsend reconcile-status` use that shared client. They accept either
`FULLSEND_JIRA_TOKEN` and optional `FULLSEND_JIRA_EMAIL`, or
`FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET`. The built-in
triage and code agents use `FULLSEND_JIRA_TOKEN` and optional
`FULLSEND_JIRA_EMAIL` too, both to write agent results and for the sandbox's
read-only Jira access. For how the repository secrets reach Jira-consuming
agent runs, see [Jira repository secrets in agent
runs](#jira-repository-secrets-in-agent-runs).
The Jira-aware harness integration is tracked in
[agents#1900](https://github.com/fullsend-ai/agents/issues/1900).

### Use OAuth 2.0 client credentials for a service account

An Atlassian-managed service account can have an OAuth 2.0 credential (a
client ID and client secret) instead of an API token. Set them as the
`FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET` repository secrets
in place of `FULLSEND_JIRA_TOKEN`. Fullsend exchanges them with the
`client_credentials` grant at `https://auth.atlassian.com/oauth/token` for a
Bearer access token, which is valid for 60 minutes. The token is cached and
refreshed before it expires, or once when Jira answers 401. Requests still go
through the `tracker.jira.api_url` gateway
(`/ex/jira/{cloudId}/rest/api/3/...`). The poller, `fullsend issues`,
run-status notifications, and `fullsend reconcile-status` all accept them.

The two credential types are mutually exclusive:

- Setting `FULLSEND_JIRA_TOKEN` together with either client credential is an
  error. The error names the variables, never their values.
- The client ID and client secret must both be set.
- `FULLSEND_JIRA_EMAIL` applies only to a token and is rejected with client
  credentials.

`fullsend repos install --jira-client-id --jira-client-secret` writes them as
GitHub repository secrets or GitLab CI/CD variables and forwards them to the
Jira poller and Jira-originated status updates (see
[Jira polling](../../cli/repos.md#jira-polling)). GitLab requires the client
secret to meet its masking rules.

The built-in triage and code agents cannot use client credentials yet. Their
sandbox `jira-ro` provider and result-write scripts need `FULLSEND_JIRA_TOKEN`,
which cannot be set together with the client credentials. Support for agent
runs is tracked in [#9220](https://github.com/fullsend-ai/fullsend/issues/9220).

Use the same Jira account for the poller, status notifications, and agent
result writes. The poller treats only its own account as a bot, so writes from
another account dispatch agents again. If the token belongs to a person:

- That person's own comments and label changes never dispatch agents,
  including their `/fs-triage` and `/fs-code` commands.
- Jira history and comments cannot tell what the person did from what
  fullsend did, because both appear under the person's name.
- The poller runs with every Jira permission the person holds, not only the
  ones it needs, and stops working if the person leaves or loses access.

### Labels present when an issue is created

A label-added trigger, such as the code agent's `ready-to-code`, does not fire
for an issue that was created with the label already applied. The poller
builds `label_changed` events from the issue's changelog, and Jira records no
changelog entry for labels set at creation. On an issue's first poll, the
poller instead emits a single `opened` event with the reporter as the actor
and the issue's current labels in `state.labels`. It does this only when the
issue was created within the 24-hour backfill window (see
[Poll coordination](#poll-coordination)). The default triage trigger matches
that `opened` event. The default code trigger does not.

To start the code agent on such an issue, do one of the following:

1. Comment `/fs-code` on the issue.
2. Remove the `ready-to-code` label, then add it again.

The person who comments or re-adds the label needs the role the code agent
requires, as for any other trigger. See
[Actor role resolution](#actor-role-resolution).

### Changing direction after a pull request exists

The code agent opened a pull request for your Jira issue, and then the plan
changed: new acceptance criteria, a narrower scope, or a different approach.
Pick the row that matches what you want. On a GitLab target, read "merge
request" for "pull request".

| You want to | Do this | Result |
|---|---|---|
| Adjust the pull request you have | Comment `/fs-fix <instruction>` on the pull request | The fix agent pushes new commits to the same pull request |
| Throw the work away and build from the edited Jira issue | Wait for the code run to end, close the pull request, then comment `/fs-code` on the Jira issue | The code agent opens one new pull request |
| Run the code agent after it skipped the run | Comment `/fs-code --force` on the Jira issue (GitHub only; [GitLab route](#if-the-code-agent-skips-the-run)) | The code agent runs despite an open human pull request or child issues |

Do not comment `/fs-code` again while the pull request is still open. See
[If you rerun without closing the pull
request](#if-you-rerun-without-closing-the-pull-request) for what happens.

#### Adjust the pull request you have

1. Open the pull request. Its URL is in the comment the code agent posted on
   the Jira issue.
2. Comment on the pull request, not on the Jira issue:

   ```text
   /fs-fix Also reject empty names, as the updated acceptance criteria say.
   ```

3. Wait for the fix agent's new commits on the same pull request.

The fix agent reads the pull request and your comment. It does not read the
Jira issue, so it does not see an edit you made there. Restate the change in
the comment. The Jira issue shows no update for this step, because fix does
not write to Jira (see
[Where built-in agent results appear](#where-built-in-agent-results-appear)).

#### Check whether a code run has ended

On a GitHub target, a new code run for a Jira issue cancels an earlier code
run for the same issue, whether that run is queued or already working. The
cancelled run's work is lost. Runs of other agents, such as triage, do not
cancel it. On a GitLab target, the new run waits for the earlier one.

So before you comment `/fs-code` or re-add the label, read the **newest**
run-status comment on the Jira issue. An older "Started" comment can stay on
the issue after its run has ended.

| Newest run-status comment starts with | Meaning |
|---|---|
| `🤖 Code · Started` | The run is in progress. A new `/fs-code` cancels it |
| `🤖 Finished Code` | The run has ended, whatever the result |
| `🤖 Code · ⚠ Cancelled` or `🤖 Code · ❌ Terminated` | The run was stopped. It has ended |

The poller runs every five minutes, so a new run can take a few minutes to
post its first comment. A repository can also turn these comments off with
[`status_notifications`](../../reference/config-reference.md#status_notifications).
If you see no run-status comment, check the workflow run in GitHub Actions or
the pipeline in GitLab.

#### Start over with a new pull request

1. [Check that the earlier code run has
   ended](#check-whether-a-code-run-has-ended). The newest run-status comment
   on the Jira issue must not start with `🤖 Code · Started`.
2. Close the pull request.
3. Comment on the Jira issue:

   ```text
   /fs-code
   ```

   Removing the `ready-to-code` label and adding it again starts a run too.
4. Wait for the code agent's comment on the Jira issue. It now carries the URL
   of the new pull request.

The new run builds from the default branch and reads the Jira issue as it is
now, including your edits. It does not carry over commits from the closed
pull request, whether the code agent, the fix agent, or you pushed them. If
the run picks the same branch name as before, it replaces the old branch.

#### If you rerun without closing the pull request

The code agent names its branch `agent/<KEY>-<short-description>`, for
example `agent/PROJ-123-reject-empty-names`. It writes the description again
on each run, so you cannot tell in advance whether a rerun reuses the branch.
With the earlier pull request still open, a rerun ends in one of two ways:

| Branch name the rerun picks | What happens |
|---|---|
| A different one | The run opens a second pull request and the first stays open. The code agent's comment on the Jira issue then shows only the newer URL |
| The same one | The run fails when it pushes, and the pull request keeps its earlier commits ([agents#409](https://github.com/fullsend-ai/agents/issues/409)) |

Neither is what you want. Use `/fs-fix` on the pull request, or close it
first.

#### If the code agent skips the run

Before it starts, the code agent skips the run in two cases. Each one posts a
comment on the Jira issue:

| Comment on the Jira issue starts with | Why |
|---|---|
| `An open pull request already addresses <KEY> — skipping automated implementation.` | An open pull request written by a person names the issue key in its title or description. Pull requests written by bots, including the code agent's own, do not count |
| `<KEY> has child issues — skipping automated implementation.` | The issue has child issues, open or closed. Subtasks do not count, so an issue split into subtasks still runs |

To run the code agent anyway, comment on the Jira issue:

```text
/fs-code --force
```

Put `--force` directly after `/fs-code` on the first line of the comment. It
is ignored anywhere else. A run started by the `ready-to-code` label has no
comment, so it cannot be forced.

The forced run then behaves like any code run: a `🤖 Code · Started` comment
appears on the Jira issue, and the code agent comments with the pull request
URL when it is done.

On a GitLab target, `--force` does nothing yet
([#9187](https://github.com/fullsend-ai/fullsend/issues/9187)). Until that is
fixed, remove the cause of the skip:

- **Open merge request by a person:** close it, or edit its title and
  description so they no longer name the issue key. Then comment `/fs-code`.
- **Child issues:** run the code agent on the child issues.

On GitLab, a merge request counts as bot-written when its author's username
starts with `fullsend`, or ends in `[bot]` or `_bot`.

## Event semantics — input only

The `event_type` field in dispatch records (e.g. `"comment_added"`, `"label_changed"`) describes the Jira-side activity that **triggered** the dispatch — it is an **input** event, not a description of what the agent will do. When you see `event_type: "comment_added"` in `dispatches.json`, it means "a comment was added to a Jira issue, and that comment matched a routing rule." It says nothing about the agent's output.

For a Jira-sourced run, `fullsend run` exports the tracker-independent `FULLSEND_WORK_ITEM_SOURCE` (`jira`), `FULLSEND_WORK_ITEM_KEY` (for example `PROJ-123`), and `FULLSEND_WORK_ITEM_URL` variables from the normalized event, replacing any inherited values, and clears `ISSUE_NUMBER` because a Jira key has no GitHub or GitLab issue number. The run fails closed, rather than guessing an identity, when the dispatch payload declares Jira but its key or URL is missing or malformed. Harness scripts that handle Jira work items should read the `FULLSEND_WORK_ITEM_*` variables instead of `ISSUE_NUMBER`.

### Tracking runs with an optional label

To find issues an agent has worked on using JQL, opt in to a run-start label:

```yaml
tracker:
  jira:
    base_url: https://example.atlassian.net
    project: PROJ
    run_label: fs-code-agent
```

`tracker.jira.run_label` has no default. Omit it or set it to `""` to disable
label writes; Jira-sourced runs log that labeling was skipped. A configured
name must not contain whitespace or control characters. Configuration validation
rejects invalid names. If an invalid name reaches runtime, labeling logs a
warning and is skipped; Jira runs and polling continue. The setting applies to
all Jira-sourced agents, so choose a role-appropriate name for your repository.

The runner adds the label alongside the start-status notification, before
sandbox setup, even when status comments are disabled. It remains on failed
or cancelled runs. Existing labels are preserved, and an issue already carrying
it receives no label update. A failed label read or write produces a warning
and does not fail the run. **Edit Issues**, already in the bot's minimum
permission set, covers the write.

Use `labels = "fs-code-agent"` in JQL to find these issues. Choose a label
that does not collide with your poll query's trigger or exclusion labels.
Writes use the same configured Jira account as status comments; changes by the
poller's own account are classified as bot actions and do not dispatch agents.
The label marks that a run started, not that the agent created or merged a PR.

### Jira connection variables in agent runs

For a Jira-sourced run, `fullsend run` also sets two Jira URLs before harness env expansion, so `${JIRA_BASE_URL}` and `${JIRA_API_URL}` in `env.runner`, `env.sandbox`, and `host_files` entries with `expand: true` resolve to them:

| Variable | Value |
|---|---|
| `JIRA_BASE_URL` | `tracker.jira.base_url`. It replaces any inherited value, such as the `JIRA_BASE_URL` repository variable or the `jira_base_url` dispatch input, so config is the only source. |
| `JIRA_API_URL` | The per-site API gateway base `{api_url}/ex/jira/{cloudId}`, with no trailing slash, where `api_url` is `tracker.jira.api_url` (default `https://api.atlassian.com`). It is the same base fullsend's own Jira client uses, and the cloud ID is discovered the same way, from `{base_url}/_edge/tenant_info`. |

The built-in agents send Jira REST calls to `${JIRA_API_URL:-${JIRA_BASE_URL}}/rest/api/3/...`. Because `fullsend run` always sets `JIRA_API_URL` for a Jira-sourced run that starts, those calls go through the gateway, which accepts a service account's scoped token and an OAuth 2.0 access token; a bot user's classic token has been observed to work there too, though Atlassian does not promise it.

A Jira-sourced run fails closed. It stops before the sandbox starts, rather than letting agents fall back to the site URL, where a service account's credential is rejected:

| Case | Result |
|---|---|
| Discovery succeeds | Both variables are set; the run continues |
| Discovery fails or times out (10-second limit) | The run stops; the error names the site URL |
| `fullsend run --offline` | The run stops: the gateway address needs a network lookup |
| `tracker.jira` connection settings missing or invalid (for example `base_url` or `api_url` not `https`, or with credentials, a query, or a fragment) | The run stops; the URL is never printed |

Reference these variables only in a harness overlay that applies to Jira runs (`when: 'has(event.source) && event.source.system == "jira"'`). On a GitHub- or GitLab-sourced run, fullsend leaves them as they are, and an unset reference fails the run. Both values are URLs, not credentials, and a runner secret cannot override `JIRA_API_URL` or `JIRA_BASE_URL`.

For sandboxed use, the network profile must allow the gateway host. The built-in agents' `jira-ro` provider profile allows `api.atlassian.com`. The legacy `atlassian-cloud` profile below allows only `*.atlassian.net`, so it serves bot-user calls to the site URL only.

### Where built-in agent results appear

Starting with the agents release that follows v0.52.0
([agents#1843](https://github.com/fullsend-ai/agents/pull/1843)), the built-in
triage and code agents write their results to the Jira issue that started the
run. This works for both GitHub and GitLab code-host targets. Run-status
notifications (start, completion, and orphan reconciliation) are routed through
`tracker.Client` to the originating tracker, so the Jira issue also receives
run-status updates.

| Stage | Written to the Jira issue | Stays on the code host |
|---|---|---|
| triage | The triage comment, label and component updates, and close actions as Jira transitions | No code artifacts |
| code | A short code-stage update with the URL of the created or updated PR (GitHub target) or MR (GitLab target) | Branch, commits, PR or MR, review, and merge |

Review, fix, and retro act on pull or merge request events on the code host and
do not write to Jira.

The post-scripts write to the issue at `FULLSEND_WORK_ITEM_URL`. Triage stops
before any write if `FULLSEND_WORK_ITEM_KEY` does not match that URL, or if an
explicit `FULLSEND_TRACKER` or `ISSUE_URL` points somewhere else. Code stops if
`FULLSEND_TRACKER` and `FULLSEND_WORK_ITEM_SOURCE` are both set and disagree.

- **Transitions:** triage closes issues with the Jira transition names set by
  `JIRA_DUPLICATE_TRANSITION`, `JIRA_NOT_PLANNED_TRANSITION`, and
  `JIRA_SPLIT_TRANSITION` in the triage harness's Jira overlay (by default
  `Duplicate`, `Won't Do`, and `Obsolete`). Override them in a harness if your
  Jira workflow uses different names. Tracker-neutral transition discovery is
  tracked in [#6704](https://github.com/fullsend-ai/fullsend/issues/6704).
- **Updates Jira cannot apply:** triage lists them under **Not applied in
  Jira** in its comment instead of dropping them. This covers a label add for
  a label not used anywhere on the Jira site (triage does not create new
  labels), and component changes skipped because the issue's current
  components could not be read.
- **Retries:** comments are not duplicated. The code-stage update and the
  sticky triage comments use fixed markers. Other triage comments use a marker
  scoped to the CI run (`GITHUB_RUN_ID` or `CI_PIPELINE_ID`). A re-run of the
  same workflow or pipeline edits its earlier comment, while a new triage run
  posts a new one.
- **Write failures:** a failed Jira write fails the step, so it is never
  reported as a success. For triage this covers label adds and removals,
  component updates, comments, and transitions. For code the step fails even
  though the PR or MR already exists.
- **Bot loops:** the writes authenticate with `FULLSEND_JIRA_TOKEN` and
  optional `FULLSEND_JIRA_EMAIL`, the same account the poller classifies as
  `actor.kind: bot`, so they do not dispatch agents again. See
  [How it works](#how-it-works).
- **Credentials:** the triage and code harnesses map `FULLSEND_JIRA_TOKEN` and
  `FULLSEND_JIRA_EMAIL` in `env.runner` for the runner-side post-scripts only.
  The `jira-ro` provider reads `FULLSEND_JIRA_TOKEN` on the host, and the
  sandbox gets the provider's placeholder, not the real token.

When the code agent makes no PR or MR, or the run fails, nothing beyond the
run-status update is written to Jira.

Custom agents can use `fullsend issues post-comment --tracker jira` to post
results back to Jira, and `fullsend issues create --tracker jira` to create
Jira issues and sub-tasks. These commands use the `tracker.Client` Jira
implementation
([#5989](https://github.com/fullsend-ai/fullsend/issues/5989)). See
[Issue commands](issues-commands.md) for usage.

## Prerequisites

- A GitHub repo with fullsend installed (`fullsend github setup` completed).
- A Jira Cloud instance. **Jira Data Center is not currently supported** — the client is hard-wired to Cloud-only APIs (REST v3, cursor-based search pagination, `groupId`-based group lookup), so requests against a Data Center instance will fail. Tracked as future work.
- A Jira API token ([Create API token](https://id.atlassian.com/manage-profile/security/api-tokens))
  for a [dedicated non-human account](#use-a-dedicated-account-for-the-poller)
  with the [minimum Jira permissions](#minimum-jira-permissions). That is
  usually the **Developers** project role. The **Administrators** role is
  needed only when project roles are used: with no `jira` entry, for slash
  commands when the `jira` entry sets no lists, or for `labels: project_roles`.

## Credential setup

1. Open your GitHub repo's **Settings > Secrets and variables > Actions**.
2. Add the following secrets and variables:

| Secret / variable | Value |
|---|---|
| `FULLSEND_JIRA_TOKEN` | Scoped Jira API token for the poller and agent run-status updates; use instead of the client credentials below |
| `FULLSEND_JIRA_EMAIL` | Optional. Bot user's email for a scoped user token. Omit it for a service-account token |
| `FULLSEND_JIRA_CLIENT_ID`, `FULLSEND_JIRA_CLIENT_SECRET` | Alternative to `FULLSEND_JIRA_TOKEN`: a service account's OAuth 2.0 credential (see [Use OAuth 2.0 client credentials](#use-oauth-20-client-credentials-for-a-service-account)). Not yet supported by the built-in triage and code agents |
| `JIRA_BASE_URL` | Jira site URL used by built-in agent result-write scripts. For Jira-sourced runs, `fullsend run` replaces it with `tracker.jira.base_url` and also sets `JIRA_API_URL` (see [Jira connection variables in agent runs](#jira-connection-variables-in-agent-runs)) |

> **Upgrading:** Fullsend and the built-in agents no longer read the
> `JIRA_TOKEN` and `JIRA_USER_EMAIL` repository secrets. Store the token once,
> as `FULLSEND_JIRA_TOKEN`, and a bot user's email as `FULLSEND_JIRA_EMAIL`.
> After you upgrade, you can delete the two old secrets. A harness whose
> `base:` pins a fullsend-ai/agents commit must move to a version whose
> `jira-ro` provider reads `FULLSEND_JIRA_TOKEN`. If your repository
> uses `FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET`, do not add
> `FULLSEND_JIRA_TOKEN`: the two cannot be set together, and the built-in
> Jira agents need a token until
> [#9220](https://github.com/fullsend-ai/fullsend/issues/9220).

The poller and Jira-originated agent workflows use either the token and
optional email or the OAuth client ID and secret. They take the Jira site and
API gateway from `tracker.jira` in `.fullsend/config.yaml`. `fullsend repos
install --jira-token --jira-email` or `--jira-client-id --jira-client-secret`
writes the selected credentials for repositories whose
`tracker.jira.poll.enabled` is `true`.

`fullsend run` uses the `FULLSEND_JIRA_*` credentials for Jira status updates.
It does not pass them to host-side pre/post/validation scripts by default. A
harness whose scripts call Jira must declare their credentials under
`env.runner` (for example `FULLSEND_JIRA_TOKEN: "${FULLSEND_JIRA_TOKEN}"`). The built-in triage
and code harnesses do this in their Jira overlays, so their post-scripts can
write results to the Jira issue. See
[Harness reference § `env.runner`](../../reference/harness-reference.md#field-details).

### Jira repository secrets in agent runs

`fullsend repos install` writes one Jira credential set as repository secrets
or CI/CD variables (see [Jira polling](../../cli/repos.md#jira-polling)).
They reach the poller and Jira-originated agent runs through their own path,
not through `FULLSEND_GITHUB_RUNNER_SECRETS`, because that bundle refuses
every `FULLSEND_*` name. The runtime mapping is:

| Hop | API token | OAuth 2.0 client credentials |
|---|---|---|
| Fullsend composite action input | `jira-token`, `jira-email` | `jira-client-id`, `jira-client-secret` |
| `fullsend run` process environment | `FULLSEND_JIRA_TOKEN`, optional `FULLSEND_JIRA_EMAIL` | `FULLSEND_JIRA_CLIENT_ID`, `FULLSEND_JIRA_CLIENT_SECRET` |
| Host-side scripts (pre, post, validation) | Only when the harness declares the variables in `env.runner` | Only when the harness declares both variables in `env.runner` |
| Sandbox-bound sites | Not inherited; references resolve empty (see breaking change below). | Not inherited; references resolve empty (see breaking change below). |

OAuth 2.0 client credentials follow the same path. The action inputs
`jira-client-id` and `jira-client-secret` reach `fullsend run` as
`FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET`, only when set.
Host-side scripts receive them only when the harness declares them in
`env.runner`. Sandbox-bound and provider expansion sites refuse them.

- **Absent secrets are absent, not empty.** The action's Run fullsend step
  unsets empty inputs. A token without an email selects scoped-token Bearer
  auth; a token with an email selects bot-user Basic auth. The client ID and
  secret select OAuth 2.0 client-credentials auth.
- **Declaring them in a harness.** Host-side scripts do not inherit either
  variable from the `fullsend run` environment. A harness whose scripts call
  Jira declares both:

  ```yaml
  env:
    runner:
      FULLSEND_JIRA_TOKEN: "${FULLSEND_JIRA_TOKEN}"
      FULLSEND_JIRA_EMAIL: "${FULLSEND_JIRA_EMAIL}"
  ```

  The email reference does not fail validation when the secret is absent.
  The entry is dropped instead, so the script sees no `FULLSEND_JIRA_EMAIL`.
  A missing `FULLSEND_JIRA_TOKEN` still fails the run when the harness
  references it. A harness may also declare `FULLSEND_JIRA_CLIENT_ID` and
  `FULLSEND_JIRA_CLIENT_SECRET`. These are optional in the same way, and with
  client credentials set, `FULLSEND_JIRA_TOKEN` is optional too. One harness
  can therefore declare both credential types.
- **Breaking change: sandbox-bound expansion.** On every forge,
  `${FULLSEND_JIRA_TOKEN}` and `${FULLSEND_JIRA_EMAIL}` references in
  `env.sandbox`, expanded `host_files` content, `FULLSEND_OUTPUT_FILE`, and
  provider config or credential values now resolve empty without a validation
  error. These sites previously expanded the values from the `fullsend run`
  process environment. Remove those references; where a trusted host-side
  script needs Jira access, declare the credential under `env.runner` as
  described above. Host-side scripts already require an explicit `env.runner`
  mapping on both GitHub and GitLab, so this change does not introduce a
  GitLab-specific host-script migration.
- **Residual exposure in the `fullsend run` process.** The action sets both
  values in the initial environment of the process that runs `fullsend run`,
  because the Jira client reads them from the process environment. Other
  same-user processes on the runner can read them from
  `/proc/<pid>/environ` for the whole run, even when no harness declares them.
  Unlike [runner secrets](../../cli/run.md#runner-secrets), they are not staged
  through a file. Deploy these secrets only on runners you trust.
- **No Jira settings as workflow inputs.** The site URL, API gateway URL,
  project, and query stay in the effective
  [`tracker.jira` config](../../reference/config-reference.md#trackerjira-connection-and-polling-settings).

The built-in triage and code harnesses declare `FULLSEND_JIRA_TOKEN` and
`FULLSEND_JIRA_EMAIL` under `env.runner` in their Jira overlays. They map the
values to the `JIRA_TOKEN` and `JIRA_USER_EMAIL` names their post-scripts
read. Their `jira-ro` provider reads `FULLSEND_JIRA_TOKEN` for the sandbox
(see [Sandbox credentials and network access](#sandbox-credentials-and-network-access)).

### Sandbox credentials and network access

To give a sandbox Jira access, use the built-in agents' `jira-ro` provider
(`providers/jira-ro.yaml` and `profiles/fullsend-jira-ro.yaml` in
[fullsend-ai/agents](https://github.com/fullsend-ai/agents)). It reads
`FULLSEND_JIRA_TOKEN` on the host and gives the sandbox only a placeholder.
Its profile allows the `api.atlassian.com` gateway, so it works with both a
bot user's token and a service account's token. The built-in triage and code
agents use it.

The fullsend binary also ships a legacy OpenShell credential provider (`atlassian-cloud`) and network profile (`fullsend-atlassian-cloud`) that grant sandboxed agents access to Jira Cloud. List the bare name `atlassian-cloud` under a harness's `providers:` and `fullsend run` uses the built-in definition and imports its profile; nothing is written to the `.fullsend` directory:

- **Provider** (`atlassian-cloud`) — declares the `atlassian-cloud` credential provider using the `fullsend-atlassian-cloud` profile type. Jira Cloud Basic auth requires a base64-encoded `email:api-token` string; because OpenShell does not yet support composite credential injection, provide a single pre-encoded token (generate with `printf 'you@example.com:your-api-token' | base64`).
- **Profile** (`fullsend-atlassian-cloud`) — allows outbound HTTPS to `*.atlassian.net:443` with read-write access and endpoint enforcement. Permitted binaries are `curl` and `node`.

The wildcard host (`*.atlassian.net`) permits egress to any Atlassian Cloud tenant. Per-tenant scoping is not yet supported by OpenShell; this is an accepted risk for now.

## Repo configuration

The Jira poller produces the same
[NormalizedEvents](../../normative/normalized-event/v1/) that GitHub and GitLab
do, so routing works the same way. From the agents release that follows
v0.52.0, the built-in triage and code harnesses ship `trigger` expressions
that match Jira events. A custom agent, or a built-in agent pinned to an
earlier release, needs its own matching `trigger`. See
[Setup overview](#setup-overview) step 6 for which events each built-in
trigger matches. For Jira-triggered runs,
`fullsend run` exports `FULLSEND_WORK_ITEM_SOURCE`, `FULLSEND_WORK_ITEM_KEY`,
and `FULLSEND_WORK_ITEM_URL`, clears `ISSUE_NUMBER`, and fails closed on a
malformed Jira identity. It also sets `JIRA_BASE_URL` and `JIRA_API_URL` from
`tracker.jira` (see
[Jira connection variables in agent runs](#jira-connection-variables-in-agent-runs)). The built-in triage and code agents write their results
to the Jira issue, while branches, commits, PRs or MRs, review, and merge stay
on the code host. See
[Where built-in agent results appear](#where-built-in-agent-results-appear) for
details.

If your repo already has a `.fullsend/config.yaml` from `fullsend github setup`
and uses the built-in triage and code agents from the agents release that
follows v0.52.0 or later, you need no harness changes to receive dispatches.
Otherwise, add a harness override with a Jira `trigger` expression for each
agent that should handle Jira events (see step 6 and
[Configuring agent behavior](customizing-agents.md#configuration-with-base-composition)).

### Public repos and private data

On a public repository, the run artifact of a Jira-started run publishes what
the agent read from the Jira issue. Any signed-in user can download it. To
stop that, add this to `.fullsend/config.yaml`:

```yaml
artifacts:
  by_source:
    jira: metadata      # keeps metrics and traces, removes the conversation
```

Use `none` if issue keys and branch names must stay private too. To check
that it worked, and to see what each level keeps, follow
[Run artifacts and private data](run-artifacts-and-private-data.md).

### Letting the assignee start Code

The developer who works a Jira issue is usually its **assignee**, while a
product manager, QE engineer or partner is often the **reporter**. By default
any human with the required role can start Code with `/fs-code`. To accept
`/fs-code` only from the reporter or the assignee, override the code
harness's `trigger` and test `event.actor.is_entity_author` (the reporter)
and `event.actor.is_entity_assignee` (the current assignee).

**`harness/code.yaml`:**

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

trigger: >
  event.entity.kind == "work_item"
    && !has(event.entity.linked_change_proposal)
    && event.source.system in ["github", "gitlab", "jira"]
    && ((event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-code"
        && event.actor.kind == "human"
        && (event.actor.is_entity_author || event.actor.is_entity_assignee))
      || (event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-to-code"
        && event.transition.label.action == "added"))
```

Register it with `name: code` and `source: harness/code.yaml` under `agents:`
in `.fullsend/config.yaml`. The override changes Jira runs only: on GitHub, a
harness `trigger` on the built-in `code` agent does not change how GitHub
starts it. A child `trigger` replaces the base trigger, so
the expression above copies the default code trigger and adds the
author-or-assignee clause to its `/fs-code` arm. The `ready-to-code` label arm
is unchanged, because triage applies that label when it promotes an issue.

The clause only narrows who can start Code with `/fs-code`. Anyone the label
rules allow can still start Code by adding `ready-to-code`. To limit that path
too, add the same author-or-assignee clause to the label arm.

The actor still needs the role that
[Actor role resolution](#actor-role-resolution) assigns, and anyone with
permission to assign issues can assign themselves. The poller reads the
assignee when it polls, so a reassignment between the poll and the run does
not change a run that was already dispatched.

To check it works, post `/fs-code` on an issue assigned to someone else, as a
user who is neither its reporter nor its assignee: after the next poll, no
Code run starts. Then post `/fs-code` as the assignee: the next poll starts a
Code run.

## Scheduled workflow

`fullsend repos install` manages the scheduled Jira poll workflow for you.

1. Set `tracker.jira.base_url`, `tracker.jira.project`, and
   `tracker.jira.poll.enabled: true` in the repository's configuration (its
   `config:` block in `repos.yaml`, or a `config_base` preset; see
   [Custom JQL](#custom-jql)).
2. Run `fullsend repos install` with one credential set:

   - API token: `--jira-token "$FULLSEND_JIRA_TOKEN"`, with optional
     `--jira-email "$FULLSEND_JIRA_EMAIL"` for a bot user's token.
   - OAuth: `--jira-client-id "$FULLSEND_JIRA_CLIENT_ID"` and
     `--jira-client-secret "$FULLSEND_JIRA_CLIENT_SECRET"`.

   One credential set is required only on the first Jira-enabled install.
   Later installs keep the existing secrets unless you supply new values. See
   [Jira polling](../../cli/repos.md#jira-polling).

Install writes `.github/workflows/fullsend-poll-jira.yml` in the same scaffold
commit or pull request as the rest of the installation. The workflow:

- runs every five minutes and on manual dispatch (`workflow_dispatch`);
- calls Fullsend's shared `reusable-poll-jira.yml`, which runs
  `fullsend poll --input-driver jira-poll` against the repository's
  `.fullsend/config.yaml` and returns the dispatch matrix;
- hands that matrix to `reusable-dispatch.yml`, which runs the agents;
- passes the selected Jira credential set to the poll job. For Jira-originated
  runs, it passes the same set to Fullsend's status writer; the values do not
  enter the runner-secret bundle or sandbox. The built-in agents' harnesses
  map the same set in `env.runner` for their post-scripts' own Jira writes.

The workflow has no Jira URL, project, or query inputs. The poller reads them
from the configuration. Both reusable workflows are pinned to the scaffold's
`fullsend_ref`, so that ref must be a Fullsend release that ships
`reusable-poll-jira.yml`. When install manages the caller, it checks that the
selected ref includes the reusable workflow before writing credentials or
workflow files. If the ref is too old, install fails for that repo; upgrade
`fullsend_ref` and run install again.

Install keeps the workflow current. When `poll.enabled` is `false` or
omitted, it removes the workflow. `repos uninstall` removes it too. Install
changes the file only when it carries the `# fullsend-component: jira-poll`
marker line that Fullsend writes. If you copied the poll workflow from an
earlier version of this guide, it has no marker: install reports it and
leaves it untouched. Delete that file and re-run `repos install` to switch to
the managed workflow.

**How this works:** The poll job queries Jira and builds a dispatch matrix in
the format expected by `reusable-dispatch.yml`. When the `matrix` input is
provided, `reusable-dispatch.yml` skips its routing and dispatch steps and
goes directly to running harness agents with the pre-computed matrix. Because
both reusable workflows are pinned to the same ref, the poller, dispatch, and
the agents always come from the same fullsend version. Custom pollers can
reuse fullsend's harness infrastructure the same way. See
[Custom Poller Example](custom-poller-example.md) for more details on this
pattern.

**The poll job's concurrency group (`cancel-in-progress: false`)** makes overlapping poll cycles queue rather than cancel each other. It is the primary defense against concurrent runs: only one poll cycle for this workflow runs at a time in the common case, including manually triggered runs. The poller's own Jira entity-property locking is a secondary guard for cases outside that group: it re-checks for a live lock immediately before writing, narrowing the race to the jitter window between that check and the write, but Jira entity properties have no compare-and-swap, so a lock created in that narrow window can still be clobbered by a genuinely concurrent poller. Treat the GHA concurrency group as the real safety mechanism, not the lock.

### Custom JQL

By default the poller searches for all non-done issues in the project, ordered by most recently updated.

The poller reads the Jira site, project, and query only from
[`tracker.jira`](../../reference/config-reference.md#trackerjira-connection-and-polling-settings)
in `.fullsend/config.yaml` or in the repository's `config:` block in
`repos.yaml`:

```yaml
tracker:
  jira:
    base_url: https://myteam.atlassian.net
    project: PROJ
    poll:
      enabled: true
      query: 'labels = "fullsend" AND statusCategory != Done ORDER BY updated DESC'
```

`base_url` and `project` are required. `api_url` defaults to
`https://api.atlassian.com`. The poller always scopes the query as
`project = "<project>" AND (<query>)`, keeping a trailing `ORDER BY` at the
end, so the example above runs as
`project = "PROJ" AND (labels = "fullsend" AND statusCategory != Done) ORDER BY updated DESC`.
The parentheses in `poll.query` must be balanced. When `poll.query` is
omitted, the poller uses the default filter above. Unless `poll.enabled` is
`true`, the poller skips the cycle without contacting Jira.

The `--jira-url`, `--jira-project`, and `--jql` flags were removed and are
rejected, and `JIRA_BASE_URL` is not read. Because the query is always
scoped to `project`, the poller can always resolve Jira project roles for
slash commands and label events.

## GitLab runtime

Jira work items can also dispatch agents in a GitLab project. When
`tracker.jira.poll.enabled` is true, `repos install` provisions the
`fullsend jira poll` schedule, and the managed poll job runs the poller with
`--target-forge gitlab`. The poller does
not write a matrix for a downstream step. It starts each matched agent
directly, through the same protected pipeline route the GitLab poller uses
for issues and merge requests.

### Requirements

- The project is installed with `fullsend repos install` from a release that
  includes Jira dispatch. Older scaffolds limit the `resource_key` pipeline
  input to GitLab issue and merge request keys. The poller reads the committed
  `.gitlab/ci/fullsend-pipeline.yml` before each dispatch and fails with
  `does not accept Jira work items; rerun repos install` when the installed
  scaffold predates Jira dispatch.
- The poll job runs in a scheduled pipeline on the protected default branch,
  so the protected `FULLSEND_GITLAB_POLLER_TOKEN` and
  `FULLSEND_DISPATCH_SECRET` variables that `repos install` provisions are
  available to it. The poller refuses to dispatch without
  `FULLSEND_DISPATCH_SECRET`, because the agent pipeline rejects unsigned
  dispatches.
- The effective config sets `tracker.jira.base_url`, `tracker.jira.project`,
  and `tracker.jira.poll.enabled: true` (see [Custom JQL](#custom-jql)).
- The poll job receives `FULLSEND_JIRA_TOKEN` and, for a bot user,
  `FULLSEND_JIRA_EMAIL`. A service account can use
  `FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET` instead (see
  [Use OAuth 2.0 client credentials](#use-oauth-20-client-credentials-for-a-service-account)).
  Only the Jira poll mode keeps these variables. The events poller and the
  dispatcher unset them.
- `FULLSEND_JIRA_TOKEN` and, for a bot user, `FULLSEND_JIRA_EMAIL` (or the
  client ID and the masked client secret) are set as protected CI/CD
  variables for poller and run-status calls. The triage and code
  result-write scripts use the same token and email.
- `JIRA_BASE_URL` is set as a protected CI/CD variable for the triage and
  code result-write scripts.

For projects whose effective `tracker.jira.poll.enabled` is `true`,
`repos install` writes the `FULLSEND_JIRA_TOKEN` and `FULLSEND_JIRA_EMAIL`
variables from `--jira-token` and `--jira-email`, or
`FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET` from
`--jira-client-id` and `--jira-client-secret`. It also creates the
`fullsend jira poll` pipeline schedule. The scaffolded poll job runs the
command below for that schedule, so you do not need to write your own job. The
schedule is created only after the scaffold that contains the Jira poll job is
on the default branch, so after a first install or upgrade, merge the
scaffold MR and run `repos install` again. Setting `poll.enabled: false`, or
omitting it, removes the schedule. See
[Jira polling](../../cli/repos.md#jira-polling).

`fullsend` does not yet create the `JIRA_BASE_URL` variable above for you. That
setup is tracked in
[#8906](https://github.com/fullsend-ai/fullsend/issues/8906).

### Running the poller

Use the identity the scaffold's `pin-ci-job-identity.sh` pins from the job
record, as `run-poll-job.sh` does for the GitLab poller, rather than
`CI_PROJECT_PATH` and `CI_SERVER_URL`. Pipeline variables can override those
two.

```bash
export CI_COMMIT_REF_NAME="${FULLSEND_PINNED_REF}"
fullsend poll \
  --input-driver jira-poll \
  --target-forge gitlab \
  --project "${FULLSEND_PINNED_PROJECT_PATH}" \
  --gitlab-url "${FULLSEND_PINNED_GITLAB_URL}" \
  --fullsend-dir .fullsend
```

| Flag | GitLab target behavior |
|---|---|
| `--target-forge gitlab` | Dispatch to a GitLab project. The default, `github`, keeps the GitHub Actions flow above |
| `--project` | Full project path, including every nested group (for example `group/subgroup/project`). Defaults to `CI_PROJECT_PATH` |
| `--gitlab-url` | Instance URL. Must be `https`. A self-managed instance served under a path prefix keeps the prefix |
| `--output` | Optional. When set, the dispatch records are also written to the file |
| `--target-repo` | Rejected, because it names a GitHub repository |

The command uses the Poller role credential and the dispatch route the
scaffold selected in `FULLSEND_GITLAB_DISPATCH_ROUTE`: the Poller API route,
or the `CI_JOB_TOKEN` child-pipeline route.

### What the agent pipeline receives

Each dispatch is an agent pipeline on the pinned default branch, with these
typed pipeline inputs signed by the `FULLSEND_DISPATCH_SECRET` HMAC:

| Input | Value |
|---|---|
| `stage` | The matched agent |
| `event_type` | `jira_` plus the event's `source.raw_type`, for example `jira_comment` |
| `resource_key` | `jira-` plus the Jira key, for example `jira-PROJ-123` |
| `originating_url` | The Jira browse URL |
| `repo_full_name` | The full GitLab project path |
| `is_fork` | `false` |
| event payload | `{"type": ..., "_normalized_event": ...}`. The comment body and instruction are shortened, ending in `[…truncated]` when shortened and dropped entirely (no marker) at the shortening ladder's last step, when needed to fit the dispatch chunk budget — except a `/fs-*` command body, which is carried untruncated and fails the dispatch when it does not fit, exactly as on the GitLab transport. On this transport the carried event's `state.labels` is bounded to a 1024-byte JSON prefix (entries past the bound are dropped, except fullsend's control labels `fullsend-fix` and `fullsend-no-fix`, which are kept), like on the GitLab transport, so an overlay keyed on a user label past the bound does not match |

`actor_id` and `status_iid` are empty: a Jira actor has no GitLab user ID, and
a Jira issue has no GitLab IID.

The agent job runs the same checks as for any other dispatch: identity pin,
pipeline creator, HMAC, protected default branch, kill switch, and role
credential selection. For a `jira_*` event type it then:

- Skips the GitLab Developer-access check on the actor, because the poller
  already authorized the actor against its Jira project role. See
  [Jira membership is not GitHub membership](#jira-membership-is-not-github-membership).
- Passes the normalized event to `fullsend run --event-file`, so the run gets
  the `FULLSEND_WORK_ITEM_*` variables and posts run status to the Jira issue.
  A payload without a Jira normalized event fails the job.
- Refuses the `review` and `fix` stages, which need a merge request.

### Failures

The poller fails closed. An invalid `--project` or `--gitlab-url` stops the
command before any Jira call. A dispatch also fails, and creates no pipeline,
when:

- the event targets a different project
- the Jira URL is malformed. A malformed Jira work item key never gets this
  far: the poller skips such an event before dispatch, before matching (see
  the `WARNING: skipping event …` row in the troubleshooting table); the
  GitLab dispatch driver's own key check remains as defence in depth
- the project has no fullsend pipeline wrapper, or its scaffold predates Jira
  dispatch
- GitLab rejects the pipeline, for example because the Poller credential
  cannot create pipelines in the project

When any dispatch for a Jira issue fails, that issue's `lastCheck` is not
advanced and the cycle exits non-zero. The next cycle retries the issue, which
can dispatch again any of its records that already succeeded. The exception is
a transport-budget refusal (`dispatch transport budget exceeded`, logged as a
WARNING): the refusal is deterministic — the same record re-marshals to the
same oversized payload — so the refused record never holds the checkpoint
back; the checkpoint advances past it once the issue's other records have
dispatched. That event's activity is lost, not repeated: an
oversized `/fs-*` command, for example, never runs and leaves only the
WARNING log line. The largest `/fs-*` command that still dispatches is about
3,000 ASCII characters — fewer when the text is escape-heavy, since JSON
encoding expands `<`, `>`, and `&` sixfold — because the complete event
carries the command text twice (as `comment.body` and as the re-derived
`comment.instruction`); commands between that boundary and the roughly
6,550 ASCII characters the comment-only fallback used to carry whole on the
GitLab transport are now refused instead of dispatched.

GitLab targets keep their lock and checkpoint properties in their own
namespace, `fullsend.poll.gitlab.{instance}.{group path}.{project}.*`, where
`{instance}` is the host and any path prefix of `--gitlab-url`. A GitLab
project never shares Jira coordination state with a GitHub repository of the
same name, or with a project of the same path on another GitLab instance.

## Local testing

You can run `fullsend poll` locally to verify your Jira connection and inspect dispatch output before deploying the scheduled workflow.

### Required environment variables

Set the same poller credentials you would configure as GitHub Actions
secrets. Omit `FULLSEND_JIRA_EMAIL` for a service-account token:

```bash
export FULLSEND_JIRA_TOKEN="your-scoped-jira-api-token"
export FULLSEND_JIRA_EMAIL="bot@example.com"
```

For a service account's OAuth 2.0 credential, set
`FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET` instead of both.

The Jira site and project come from `tracker.jira` in
`.fullsend/config.yaml` (see [Custom JQL](#custom-jql)), with
`poll.enabled: true`.

### Running the poller

From the repo root, use `go run` (per [AGENTS.md](../../../AGENTS.md) convention — do not use a `fullsend` binary from `$PATH` or another checkout):

```bash
go run ./cmd/fullsend poll \
  --input-driver jira-poll \
  --target-repo owner/repo \
  --output dispatches.json \
  --fullsend-dir .fullsend
```

Replace `owner/repo` with the GitHub repository slug where agent workflows run.

### Inspecting output

The poller writes dispatch records to the `--output` path. Inspect them with `jq`:

```bash
# Pretty-print all dispatch records
jq '.' dispatches.json

# Count dispatches
jq 'length' dispatches.json

# Show agent and event type for each dispatch
jq '.[] | {agent, event_type, status_number}' dispatches.json
```

Key fields in each dispatch record:

| Field | Description |
|---|---|
| `agent` | Agent to dispatch (e.g., `triage`, `code`) |
| `event_type` | What triggered the dispatch (e.g., `comment_added`, `label_changed`) |
| `event_payload` | JSON-encoded [NormalizedEvent](../../normative/normalized-event/v1/) — inspect with `jq -r '.event_payload \| fromjson' dispatches.json` |
| `source_repo` | GitHub repo slug where the agent runs |
| `status_repo` | GitHub repo slug for status tracking |
| `status_number` | Entity identifier as a string (Jira numeric issue ID) |

### Dry-run tip

To inspect what the poller *would* dispatch without triggering any agent workflows, run the `fullsend poll` command above and stop there — do not run the "Dispatch agent workflows" step from the [scheduled workflow](#scheduled-workflow). The poll step writes `dispatches.json` and advances Jira checkpoints, but no workflows are dispatched until the separate dispatch script reads that file and calls `gh workflow run`.

If you want to avoid advancing Jira checkpoints during testing, poll against a dedicated test project or use a narrowly scoped `tracker.jira.poll.query` that selects only test issues.

## Dispatch record format

> **Implementation detail.** Once Jira support is stabilized, users will not need to know this dispatch format — it will be an internal detail between the poll job and the `fullsend run` workflows. This section is documented now because the integration is pre-alpha and operators may need to debug or inspect dispatch records directly.

The poll step writes an array of dispatch records to `dispatches.json`. Each record is an execution ref compatible with the `workflow_call` shim (`reusable-dispatch.yml`). Fields:

| Field | Type | Description |
|---|---|---|
| `agent` | string | Agent to run (e.g., `"triage"`, `"code"`). Determined by CEL trigger evaluation. |
| `role` | string | Harness role for this agent. |
| `event_type` | string | Jira event type that triggered the dispatch (e.g., `"comment_added"`, `"label_changed"`, `"opened"`, `"reopened"`, `"edited"`, `"closed"`). |
| `event_payload` | string | JSON-encoded [NormalizedEvent](../../normative/normalized-event/v1/). Example below. |
| `source_repo` | string | GitHub repo slug where the agent workflow runs. |
| `trigger_source` | string | (Optional) Trigger source identifier. |
| `status_repo` | string | GitHub repo slug for status tracking. |
| `status_number` | string | Entity identifier as a string (Jira's numeric issue ID). |

### `event_payload` example

The `event_payload` field is a JSON-encoded [NormalizedEvent](../../normative/normalized-event/v1/) — the same forge-neutral struct that GitHub and GitLab input drivers produce. An example for a `/fs-triage` comment on `PROJ-101`:

```json
{
  "repo": "acme/platform",
  "entity": {
    "kind": "work_item",
    "id": 10042,
    "key": "PROJ-101",
    "url": "https://myteam.atlassian.net/browse/PROJ-101"
  },
  "transition": {
    "kind": "comment_added",
    "comment": {
      "body": "/fs-triage please triage this bug",
      "command": "/fs-triage",
      "instruction": "please triage this bug"
    }
  },
  "actor": {
    "id": "5f7c012e0b2a4c001f3d9876",
    "kind": "human",
    "role": "write",
    "is_entity_author": false,
    "is_entity_assignee": false
  },
  "state": {
    "labels": ["bug", "fullsend"]
  },
  "source": {
    "system": "jira",
    "raw_type": "comment",
    "raw_action": "created"
  }
}
```

The `entity.key` field is required when `source.system` is `"jira"` — it carries the human-readable Jira key (e.g., `PROJ-101`). See the [NormalizedEvent spec](../../normative/normalized-event/v1/) for the full schema.

## Actor role resolution

The `jira` provider under `authorization:` in `.fullsend/config.yaml` is
opt-in
([ADR 0153](../../ADRs/0153-authorize-jira-poll-actors-without-project-administration.md)).
With no `jira` entry, the poller resolves every event, label-added events
included, through the Jira project roles (see
[Project role mapping](#project-role-mapping)), which needs Administer
Projects. To authorize actors without that permission, add a `jira` entry:

```yaml
authorization:
  - provider: jira
    write:            # Fullsend role write
      groups: [example-team-developers]
      accounts: ["5f7c012e0b2a4c001f3d9876"]   # Jira account IDs
    triage:           # Fullsend role triage
      groups: [example-team]
      accounts: []
    labels: jira_editors  # jira_editors (default) | allowlist | project_roles
```

- **Label added.** When a `jira` entry exists, by default
  (`labels: jira_editors`), any human who adds a label can trigger the agents
  whose trigger matches it. With no `jira` entry, a label-added event takes
  the project-role rule described below instead. Jira only lets users with the Edit Issues
  permission change labels, so this works like GitHub, where applying a
  label needs triage access. Edit Issues is often granted more widely than
  the Developers role. Set `labels: allowlist` to check label authors
  against the lists below, or `labels: project_roles` to keep the
  project-role rule described below.
- **Slash commands and other activity, with lists.** When the `jira` entry
  sets a `write` or `triage` list, the lists decide, and Jira project roles
  are not read. A list left empty (for example after removing its last
  entry) still counts as set, so everyone is denied; remove the `write` and
  `triage` keys entirely to stop using the lists (see the next bullet for
  what then decides). An actor listed under `write` (by account ID,
  or through a listed group) can run every agent. An actor listed only under
  `triage` can run `/fs-triage` and custom agents whose harness
  sets `role: triage` or `role: review`. Everyone else is denied. This also
  applies to issues being opened or edited.
- **Slash commands without lists.** Slash commands are authorized by the
  actor's Jira project role, as in the table below. When a `jira` entry exists
  but sets no lists and does not set `labels: project_roles`, issues being
  opened, edited, closed, or reopened, plain comments, and label removals are
  not authorized for any agent. With no `jira` entry, or with
  `labels: project_roles` and no lists, every event takes the project-role
  rule.

### Keeping project roles as today

To keep authorizing every Jira event through project roles, as with no
`jira` entry, write the two-line entry:

```yaml
authorization:
  - provider: jira
    labels: project_roles
```

It needs no lists. If you add a `write` or `triage` list to it, the lists
decide every event except label-added ones.

Group entries match a Jira group's name or group ID and are checked with
Jira's user-groups API, once per actor per poll cycle. Account entries are
compared with the actor's account ID directly. To find an account ID, open
the user's Jira profile: the ID is the last part of its URL.

### Minimum Jira permissions

| Permission | When the poller account needs it |
|---|---|
| Browse Projects, Edit Issues, Add Comments, Edit Own Comments | Always |
| Browse users and groups (global permission) | When the `jira` lists name groups, or when project-role resolution finds a project role granted to a group (the poller then calls `GET /user/groups` for the actor) |
| Administer Projects (the **Administrators** project role) | Only when project roles are used: no `jira` entry, `labels: project_roles`, or slash commands when the `jira` entry sets no lists |

The poller reads the project role list only in a cycle that has an event
needing it. A setup whose `jira` entry uses the lists without
`labels: project_roles`, or only label triggers under `jira_editors`, never
needs Administer Projects. With no `jira` entry,
or with `labels: project_roles` and no lists, every event needs the role list.

### Project role mapping

> **Known limitation.** Roles are resolved by Jira project **role name**, not by actual granted permissions. This is intentional for the MVP — see below.

When project roles are used, the poller maps each event actor to a dispatch authorization role (`read`, `write`, `admin`) by looking up their Jira project role membership and matching on the role's **name**:

| Jira project role name (case-insensitive) | Dispatch role |
|---|---|
| `Administrators` | `admin` |
| `Developers` | `write` |
| anything else (including custom role names) | `read` |

This does **not** check the project's permission scheme, so it can be wrong in both directions:

- An org with a custom role literally named `Developers` that has *not* been granted edit permissions will be over-privileged for write-gated dispatch (e.g. `/fs-code`).
- An org using differently-named roles (e.g. `Contributors`, `Engineering`) for people who *do* have edit access will be silently downgraded to `read`, and their slash commands will be ignored.

If your project uses Jira's default role names ("Administrators"/"Developers") with their default permissions, this works as expected. If you use custom role names, expect actors to resolve to `read` regardless of their real permissions until real permission-scheme resolution is implemented (tracked as future work, not planned for the MVP).

### Jira membership is not GitHub membership

> **Known limitation.** No cross-system identity check is performed between Jira and GitHub. The Jira project is the entire authorization boundary for Jira-sourced events.

The role resolved above feeds directly into the dispatch authorization gate (see [Architecture](../../architecture.md#agent-dispatch-and-coordination-layer)) — there is no separate check against the target GitHub repo's actual collaborators. This means anyone holding a Jira project role that maps to `write` (Jira's default "Developers" role, by default) can trigger write-gated slash commands like `/fs-code` against your repo, **even if that person has no GitHub access to it at all**.

The same holds for a [GitLab target](#gitlab-runtime): the agent job skips its GitLab Developer-access check for Jira dispatches, so project membership in GitLab is not checked either.

If your Jira project's membership is broader than your GitHub repo's collaborator list — which is common, since the two are usually administered separately — treat that gap as real: anyone in that gap can use their Jira membership alone to induce agent-proposed changes to your repository. Before enabling this driver, check who holds `write`-mapped roles in the target Jira project and make sure you're comfortable with each of them being able to do that.

The same applies to the `jira` lists and to label triggers. Under a `jira`
entry's default `labels: jira_editors`, anyone with Edit Issues on the project can
add a label such as `ready-to-code` and dispatch the agent whose trigger
matches it. Use `labels: allowlist` if that group is broader than you want.

## Poll coordination

The poller uses Jira entity properties for distributed lock coordination and checkpoint tracking, following a write-then-verify protocol. Each cycle randomly selects up to N (default 5) issues from the candidate set for processing, spreading load across cycles. Two properties are stored on each processed issue:

- **Lock** (`fullsend.poll.{owner}.{repo}.lock`) — prevents concurrent pollers from *detecting changes on* the same issue simultaneously. The lock covers the change-detection window only: it is released when an issue finishes processing, before the dispatch records are written and consumed by the downstream dispatch step, and it is not renewed while an issue is being processed — a cycle that stalls longer than the stale threshold (15 minutes) can have its lock reclaimed by a concurrent poller, which may produce duplicate dispatches. Lock ownership through dispatch scheduling is part of the same tracked follow-up as dispatch confirmation.
- **Last check** (`fullsend.poll.{owner}.{repo}.lastCheck`) — tracks the timestamp of the most recent processed change. Only changes newer than this timestamp trigger agent dispatch.

These properties are namespaced per target repo, so multiple repos can poll the same Jira project without interference. GitLab targets use a separate `fullsend.poll.gitlab.*` namespace that also includes the GitLab instance (see [GitLab runtime](#gitlab-runtime)). The properties are visible in Jira's issue properties API but do not appear in the issue UI.

> **Security note — coordination state is tamperable by any project editor.** Writing an issue entity property requires only Jira's **Edit Issues** permission, which is typically far broader than the project roles this driver maps to `write`. That means anyone who can edit a polled issue can write its `lastCheck` and lock properties. The driver treats both as untrusted: a `lastCheck` dated in the future is ignored (it would otherwise suppress all detection on the issue), and one rewound before the backfill window is clamped to the window's start, so a rewind can replay at most one backfill window of history rather than the whole issue — but within that window it can still re-surface old comments. Per-issue dispatch is capped to bound the blast radius. Even so, **treat Edit-Issues on a polled project as part of the trust boundary**: prefer scoping the poll to a project whose editors you trust, and remember that the authorization gate keys on the *comment's* Jira role, so who can edit issues (and comments) matters as much as who holds `write`.

Three edge cases of checkpoint tracking are worth knowing:

- **First enable on an existing backlog.** Every issue starts with an unset `lastCheck`, so on the first poll of an issue the poller has no checkpoint to filter by. Activity is instead bounded by a fixed 24-hour backfill window (not currently configurable): an `opened` event is emitted only for issues created within the window, and only comments and changelog entries from within the window produce events. Backlog issues with no activity (including comment edits) inside the window are silently checkpointed without dispatching. If you want the initial rollout scoped even tighter, narrow the candidate set with a custom `--jql`.
- **Resuming after a gap longer than 24 hours permanently drops events in the
  gap.** The same 24-hour backfill window that bounds a first poll also bounds
  resumption. When the poller reads a stored `lastCheck` older than
  `now − 24h`, it clamps the checkpoint forward to `now − 24h` — the clamping
  is a security control (it prevents a rewound `lastCheck` from replaying an
  issue's entire history), but it cannot distinguish a malicious rewind from a
  legitimate long stop. Any Jira activity between the true checkpoint and the
  clamped value is silently skipped. The cycle succeeds, the only evidence is
  a `WARNING: lastCheck for <KEY> ("<timestamp>") predates the backfill window; clamping to <floor>`
  line in the run log — treat this as an alert, not noise. Because detection
  is checkpoint-based, ordinary cron jitter and occasional skipped ticks are
  harmless (schedule accuracy affects latency, not correctness); the guarantee
  simply ends at 24 hours. A realistic trigger: **GitHub disables scheduled
  workflows after 60 days of repository inactivity**, which stops the poller
  with no error anywhere. After a long gap, the starvation limit (M = 50, see
  below) compounds the problem — many issues may have accumulated new activity
  simultaneously, and there is no rotation across cycles to reach issues
  beyond the top M. If your deployment cannot tolerate the 24-hour bound, the
  scheduler is pluggable: an external cron or Kubernetes CronJob that runs
  independently of GitHub Actions avoids the auto-disable risk.
- **Comment edits count as new activity, and are attributed to the editor.** The poller filters comments on the later of their created and updated timestamps, so editing a comment (for example, adding a slash command to an old comment) is detected on the next cycle. Jira bumps a comment's updated timestamp on modifications other than body edits too (such as visibility changes), and any such bump counts as activity. The flip side: modifying a comment whose slash command was already dispatched makes it look new again and can re-dispatch it. An edit-detected comment is attributed to the account that last modified it (Jira's `updateAuthor`), not the original author — so a user who edits someone else's comment to inject a command is authorized on *their own* role, not the original author's.

Because each cron run is a fresh process, per-cycle Jira API cost is worth sizing for: a cycle loads the project's role structure (a role-list call plus one detail call per role) only when some selected issue has an event that needs project-role resolution (no `jira` entry, `labels: project_roles`, or a slash command when the `jira` entry sets no lists), then, in such a cycle, makes one additional call for each distinct actor below the highest role priority the first time they're seen that cycle, to check their group memberships (cached per actor for the rest of the cycle, but not across cron runs). Cost scales with the number of distinct commenting actors per cycle rather than the size of the groups backing a role.

Two more scaling limits:

- **Issues beyond the top M candidates can be starved.** Each cycle's JQL search returns at most M (default 50) candidates, ordered by `updated DESC`. If a project has more than M issues with in-scope activity at once, whichever issues aren't in that top-M window this cycle are simply never selected — there's no rotation across cycles to eventually reach them. This is expected to self-resolve for most projects (an issue with new activity moves toward the front of `updated DESC` on its own), but a project that consistently has more than M simultaneously-active issues will have some starved indefinitely. Narrow the default `--jql` (e.g. scope to a label or a subset of issue types) if this applies to you.
- **Comments are re-listed in full, oldest-first, every cycle.** The Jira REST API has no way to filter comments server-side by date, so `ListComments` always paginates from the start of an issue's comment history; the poller then filters client-side by timestamp. For issues with very long comment histories, this means re-fetching (and re-decoding) old comments every cycle just to discard them. Listings are capped at 10,000 entries per issue; an issue beyond that cap has its newest activity invisible to the poller (a WARNING is logged when the cap is hit). Each API response is bounded to 10MB, but the accumulated per-issue comment history is bounded only by the page cap — a per-issue byte budget is a tracked follow-up; against real Jira Cloud (which caps individual comments) the realistic worst case is tens of MB per issue.
- **Sustained Jira errors can stall a cycle.** Each Jira call retries up to 5 times with exponential backoff, honoring `Retry-After` (capped at 5 minutes). Under a sustained 429 or outage a single cycle can therefore run long; the workflow's concurrency group queues subsequent cron runs rather than stacking them, and a failed cycle simply retries from its checkpoints on the next run.

## Multi-repo polling

Multiple GitHub repos can poll the same Jira project. Each repo runs its own poll workflow and gets an isolated view of coordination state — no cross-repo configuration is needed. Before adding pollers, use Jira Components or labels to segment your project so each repo's `tracker.jira.poll.query` filter only matches issues relevant to it — this avoids redundant API calls and keeps each poller's candidate window focused.

### Should each repo have its own poll workflow?

Yes. Each repo should contain its own `.github/workflows/fullsend-poll-jira.yml`.
The `--target-repo` flag passed to `fullsend poll` (defaulting to
`${{ github.repository }}`; an `owner/repo` slug, and one that contains `..`
is refused) controls two things:

1. **Entity property namespace.** Lock and checkpoint properties are keyed as `fullsend.poll.{owner}.{repo}.*`, so repos `acme/frontend` and `acme/backend` polling the same Jira project write to separate property keys on each issue and never interfere with each other.
2. **GHA concurrency group.** The workflow's `concurrency.group` is scoped to the workflow file, which lives in the repo — so each repo's poll cycles queue independently.

This means each repo discovers and dispatches changes on its own schedule, with its own credentials and its own `tracker.jira.poll.query` filter. There is no shared state between repos.

### How entity property locks namespace across repos

The poller stores two properties per issue, both namespaced by the target repo's `{owner}/{repo}` slug:

| Property | Key pattern |
|---|---|
| Lock | `fullsend.poll.{owner}.{repo}.lock` |
| Checkpoint | `fullsend.poll.{owner}.{repo}.lastCheck` |

When `acme/frontend` and `acme/backend` both poll issue `PROJ-42`:

- `acme/frontend` reads and writes `fullsend.poll.acme.frontend.lock` and `fullsend.poll.acme.frontend.lastCheck`.
- `acme/backend` reads and writes `fullsend.poll.acme.backend.lock` and `fullsend.poll.acme.backend.lastCheck`.

The two repos lock independently, checkpoint independently, and can process the same Jira comment — each dispatching to its own agent workflows. This is intentional: the same `/fs-triage` comment on a Jira issue may be relevant to multiple repos if their harness triggers match.

### 50-candidate cap with multiple repos

Each repo's poller runs its own JQL query and gets its own top-M (default 50) candidate window. The cap is per-poller, not global — three repos polling the same project each see up to 50 candidates per cycle, not 50 shared across all three.

However, more pollers against the same Jira project means more API calls per cycle. Each poller independently reads entity properties for lock filtering, fetches changelogs, and paginates comments. For large multi-repo deployments, consider:

- **Narrowing `tracker.jira.poll.query` per repo** to reduce candidate overlap. Use Jira Components or labels to segment the project (see the recommendation above), then scope each repo's query — e.g., `component = Frontend` or `labels = frontend` — so it skips issues that belong to other repos.
- **Staggering cron schedules** so pollers from different repos don't hit the Jira API simultaneously. For example, offset each repo's schedule by one or two minutes.
- **Monitoring Jira rate limits.** Jira Cloud uses points-based quotas, and multiple pollers multiply the cost. Watch for `429` responses in workflow logs — the poller retries with backoff, but sustained rate limiting slows all repos' cycles.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| 401 on Jira API calls | Invalid Jira credential | Regenerate the API token and update `FULLSEND_JIRA_TOKEN`, or create a new OAuth credential and update `FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET`. The Jira API calls use `tracker.jira.api_url`; set `FULLSEND_JIRA_EMAIL` only for a bot user's token |
| `jira: OAuth token request to "https://auth.atlassian.com/oauth/token" with FULLSEND_JIRA_CLIENT_ID and FULLSEND_JIRA_CLIENT_SECRET: unexpected status 401 (invalid_client)` | The service account's OAuth 2.0 client ID or secret is wrong, expired, or revoked | Create a new OAuth 2.0 credential for the service account and update `FULLSEND_JIRA_CLIENT_ID` and `FULLSEND_JIRA_CLIENT_SECRET` with `repos install --jira-client-id --jira-client-secret` |
| `jira: set either FULLSEND_JIRA_TOKEN or FULLSEND_JIRA_CLIENT_ID and FULLSEND_JIRA_CLIENT_SECRET, not both` | Both credential types are set | Keep one. `repos install` removes the other set when you supply new credentials |
| 200 on `/myself` but 403 on issue search | Org restricts personal API tokens for project data | Ask your Atlassian org admin to allow API token access for project data |
| No dispatches produced | No changes since last poll | Check the `lastCheck` entity property on the issue — the poller only dispatches for changes newer than this timestamp |
| Agent comments or label changes re-dispatch in a loop | A different Jira account posted the agent output than the one the poller authenticates as | The poller classifies its own `/myself` account as `actor.kind: bot` and filters those events. The built-in agents post with the poller's own `FULLSEND_JIRA_TOKEN` and `FULLSEND_JIRA_EMAIL`. Confirm that a custom agent which posts comments or labels uses the same credential, not another account's token. Human follow-up, including `/fs-triage`, still dispatches |
| Your own comments or label changes never dispatch | The poller's `FULLSEND_JIRA_TOKEN` belongs to your own Jira account | The poller classifies its own `/myself` account as `actor.kind: bot`, so that account's comments (including `/fs-triage`) and label changes are filtered with no error. Give the poller a dedicated account; see [Use a dedicated account for the poller](#use-a-dedicated-account-for-the-poller) |
| `load project roles for PROJ: ... 401 You cannot edit the configuration of this project` | The poller read Jira project roles without Administer Projects. That happens with no `jira` entry in `.fullsend/config.yaml`, with `labels: project_roles`, or on a slash command when the entry sets no lists | Add a `jira` entry with `write`/`triage` lists so project roles are not read, or grant the poller account the **Administrators** project role. See [Minimum Jira permissions](#minimum-jira-permissions) |
| `resolve jira list groups: ... get user groups` errors | The `jira` lists name groups, or project-role resolution found a role granted to a group, but the poller account lacks the Browse users and groups global permission | Grant Browse users and groups, or list account IDs instead of groups |
| Slash command ignored (with `jira` lists) | Actor is not in the `write` list (or the `triage` list for triage-level agents) | Add the actor's account ID or one of their groups to the right list — see [Actor role resolution](#actor-role-resolution) |
| Slash command ignored (without `jira` lists) | Actor lacks `write` role in Jira project | The actor must be a member of a Jira project role named exactly "Developers" or "Administrators" — see [Project role mapping](#project-role-mapping) if you use custom role names |
| Issue opened or edited on Jira never dispatches triage | A `jira` entry is present but sets no `write` or `triage` list and not `labels: project_roles`, so opened and edited events are unresolved (with no `jira` entry, or with `labels: project_roles` and no lists, they resolve through project roles) | Add `write`/`triage` lists to the entry, or set `labels: project_roles`. See [Actor role resolution](#actor-role-resolution) |
| Label added but nothing dispatched | There is no `jira` entry, or it sets `labels: allowlist` or `labels: project_roles`, and the label author is not authorized by it | Add the author to the lists, or add a `jira` entry with the default `labels: jira_editors` |
| `--jira-url, --jira-project no longer supported` | The workflow still passes the removed poller flags | Remove them and set `tracker.jira.base_url`, `tracker.jira.project`, and `tracker.jira.poll.query` in `.fullsend/config.yaml` — see [Custom JQL](#custom-jql) |
| `the jira-poll driver requires tracker.jira in config.yaml` | The effective config has no `tracker.jira` site and project | Set `tracker.jira.base_url` and `tracker.jira.project` — see [Custom JQL](#custom-jql) |
| `Jira polling is disabled` and no dispatches | `tracker.jira.poll.enabled` is not `true` | Set `tracker.jira.poll.enabled: true` |
| Duplicate dispatches | `lastCheck` was cleared or missing | The poller treats a missing `lastCheck` as "never polled" and processes all recent changes. This is self-correcting — the next cycle advances `lastCheck` past the duplicates |
| Old comment or label change on a newly polled issue never dispatches | Activity predates the first-poll backfill window | On an issue's first poll, activity older than the 24-hour backfill window is permanently skipped (the checkpoint advances past it). To pick up older activity, scope the initial `tracker.jira.poll.query` to recently updated issues and widen it gradually, or have someone re-trigger by commenting again |
| Activity during a long outage or disabled workflow is missing after resume | Poller gap exceeded the 24-hour backfill window | The same 24-hour window that bounds a first poll also bounds resumption — any `lastCheck` older than 24 hours is clamped forward. Check the run log for `predates the backfill window` — if present, events in the gap are permanently lost. Have affected users re-trigger by commenting again. See [Poll coordination](#poll-coordination) |
| GitLab Jira dispatch fails with `does not accept Jira work items` | The project's scaffold predates Jira dispatch | Rerun `fullsend repos install` for the project and merge the scaffold update. See [GitLab runtime](#gitlab-runtime) |
| `WARNING: skipping event … for issue …: normalized event: …` in the poll log | The poller could not map the Jira event to a valid normalized event (for example a non-numeric issue id, or an author with neither an `accountId` nor a `name`) | The event is never dispatched: the checkpoint advances past it (for a GitLab target, once the issue's other dispatches succeed; until then each retry logs the warning again, see [Failures](#failures)) — unless *every* event of the cycle was dropped (failed validation or failed matching), in which case the poll fails and no checkpoint advances (see the next two rows). For an author with neither `accountId` nor `name`, a comment from another account maps cleanly and re-triggers. A non-numeric issue id belongs to the issue, so no new activity on it maps; report it to fullsend with the log line, which names the raw id Jira sent (an id the Jira UI does not show). An issue served without a `labels` field is treated as unlabeled, not skipped |
| The poll fails with `all N events this cycle failed normalized-event validation` | Every event of the cycle was dropped for failing normalized-event validation — either a systemic cause (a Jira response-shape change, or an adapter gap) or the cycle's one event being bad: the guard counts events, not issues, so a single bad event in a one-event cycle is "every event" | No checkpoint was advanced, so the events are retried next cycle — and, while the dropped events are the cycle's only activity, every cycle: the red runs repeat until the events age past the first-poll backfill floor (24h by default) or another selected issue produces a valid event. Read the per-event `WARNING: skipping event …` lines above the error for the cause; if they name one cause for every issue, the Jira response shape may have changed — report it to fullsend with the log lines |
| The poll fails with `all N events this cycle were dropped before dispatch (… failed matching)` | Every event of the cycle was dropped before dispatch and at least one failed trigger matching — either the matcher errored on each event, or a mixed cycle where the remaining events failed validation; the error's counts say how many of each | No checkpoint was advanced, so the events are retried next cycle — with the same bound as failed validation: the red runs repeat until the events age past the first-poll backfill floor (24h by default) or another selected issue produces a valid event. Read the per-event `WARNING: skipping event …` and `WARNING: matching event …` lines above the error for the cause — when the error lists both counts, inspect both kinds of warning. A matcher that errors on every event is a harness trigger or execution-ref problem, not a Jira problem |
| The poll fails with `all N events this cycle failed normalized-event validation` and the per-event warnings name `entity.key "P-1" is not a Jira work item key` (every cycle) | The Jira project's key is a single character, which exists only on legacy Server/Data Center deployments: the config gate accepts it (`jiraProjectKeyRe`), but a Jira work item key requires a two-or-more-character project part, so every event of the project fails validation | Such a project cannot be polled by this driver. No checkpoint advances, so the failure repeats every cycle; report it to fullsend with the log lines |
| Triage or code results do not appear on the Jira issue | The built-in agents are pinned to a release that predates Jira result writes | Use built-in agents from the agents release that follows v0.52.0, or later. Re-pin a harness registered with `source:` or composed with `base:` with `fullsend agent update <name>`. See [Where built-in agent results appear](#where-built-in-agent-results-appear) |
| Code run skipped; the Jira issue has a comment starting `An open pull request already addresses <KEY> — skipping automated implementation.` | An open pull request written by a person names the issue key in its title or description. Bot pull requests do not count | GitHub: comment `/fs-code --force` on the Jira issue. GitLab: close the merge request, then comment `/fs-code`. See [If the code agent skips the run](#if-the-code-agent-skips-the-run) |
| Code run skipped; the Jira issue has a comment starting `<KEY> has child issues — skipping automated implementation.` | The issue has child issues, open or closed. Subtasks do not count | Run the code agent on the child issues. On GitHub you can also comment `/fs-code --force` to implement the parent issue. See [If the code agent skips the run](#if-the-code-agent-skips-the-run) |
| Code step fails after the PR or MR was created | The Jira write of the code-stage update failed | Check the step log for the Jira error, and confirm the Jira credentials reach the agent job, that `tracker.jira.base_url` is set (a missing or invalid setting stops a Jira-sourced run before the sandbox, with a `jira-sourced run:` error in the run log), and that the account can comment on the issue. A re-run edits the existing update rather than posting a second one |

## See also

- [CEL Triggers Reference](cel-triggers-reference.md) — NormalizedEvent fields and routing rules
- [Bring Your Own Agent](bring-your-own-agent.md) — adding custom agents
- [Configuring agent behavior](customizing-agents.md) — harness configuration
- [Issue commands](issues-commands.md) — `fullsend issues get`, `create`, and `post-comment`, including Jira
