---
sidebar_label: fullsend poll
---

# fullsend poll

Discover forge events and dispatch agent pipelines. The command has three
drivers: the GitLab cron poller, the Jira poller, and the GitLab webhook fast
path.

## Usage

```bash
fullsend poll [flags]
```

## GitLab cron poller

`--forge gitlab` runs the cron poller
([ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md)).
Events-mode polling also recovers issue opens whose webhook delivery was
missed.

## Jira driver

`--input-driver jira-poll` polls Jira (see
[Jira integration](../guides/user/jira-integration.md)).

### Configuration

It reads the Jira site, API gateway, project, and query only from the
effective `tracker.jira` config (`base_url` and `project` required; the removed
`--jira-url`, `--jira-project`, and `--jql` flags are rejected). It skips the
cycle unless `tracker.jira.poll.enabled` is `true`.

### Authentication

It authenticates with `FULLSEND_JIRA_TOKEN` (plus `FULLSEND_JIRA_EMAIL` for a
bot user) or with OAuth 2.0 client credentials via `FULLSEND_JIRA_CLIENT_ID`
and `FULLSEND_JIRA_CLIENT_SECRET`.

### Dispatch target

By default it writes dispatch records (`--output`) for a GitHub repository
(`--target-repo`, an `owner/repo` slug; one that contains `..` is refused).

With `--target-forge gitlab` it starts the protected agent pipelines of the
GitLab project named by `--project` (full path, nested groups allowed; default
`CI_PROJECT_PATH`) on the https `--gitlab-url` instance. It uses the same
signed, typed-input dispatch route and Poller credential as the GitLab poller,
and advances a Jira issue's checkpoint only once its pipelines are created
([GitLab runtime](../guides/user/jira-integration.md#gitlab-runtime)).

## GitLab webhook fast path

`--input-driver gitlab-webhook` is the GitLab dispatcher job's webhook
fast-path
([ADR 0125](../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)). It reads
the native webhook body from the file-type `TRIGGER_PAYLOAD` variable,
re-validates it against the GitLab API, and creates each routed stage's
pipeline (typed inputs, `FULLSEND_DISPATCH_SECRET` HMAC) with the poller-role
credential (the `api` route, the default). It requires `--project`, which has
no `CI_PROJECT_PATH` fallback.

### Dispatch routes

Scheduled cron polling and the webhook fast path run as the trigger-only
service account. Once its dispatch activation is recorded, the managed webhook
carries the account's pipeline trigger token
([#8509](https://github.com/fullsend-ai/fullsend/issues/8509)), the scaffold
sets `FULLSEND_GITLAB_DISPATCH_ROUTE=child-pipeline`, and the command instead
starts each pipeline with the job's own `CI_JOB_TOKEN` through the pipeline
trigger API (`source=pipeline`). An unknown route, or `child-pipeline` without
`CI_JOB_TOKEN`, fails closed.

`fullsend poll --forge gitlab --print-dispatch-routes` prints the routes the
CLI supports (`api` and `child-pipeline`) and exits. The scaffold runs it
before selecting `child-pipeline`, and a CLI that predates the route rejects
the flag, so the job stops before any dispatch.

### Shared poller state

It deduplicates against the poller's per-mode state on the
`fullsend-poll-state-slash` and `fullsend-poll-state-events` branches, so an
event the webhook dispatched is not dispatched again by the poller, and the
reverse.

Shared poller state is occurrence-aware (label additions are keyed on the
resource label event ID, dispatched keys are retained for the webhook freshness
window, and a failed label dispatch is handed back to the poller as a pending
retry); legacy persisted keys keep working. Older pollers do not recognise the
new label keys, so before a rollback or mixed-version run, follow the
[ADR 0132](../ADRs/0132-occurrence-aware-shared-poller-state.md) guidance to
drain in-flight poll jobs or pin every poller to one version.

## Fix-stage dispatch on GitLab

Both GitLab drivers dispatch the fix stage only for a changes-requested marker
note authored by the registered Analyst, whose numeric user ID the scaffold
exports as the job-local `FULLSEND_GITLAB_ANALYST_USER_ID`.
