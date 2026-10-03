---
sidebar_label: Overview
---

# Fullsend CLI

The `fullsend` CLI manages the complete fullsend lifecycle: provisioning GCP infrastructure, configuring GitHub, enrolling repositories, and running agents locally.

## Installation

Download the latest binary from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases). For detailed setup instructions, see [Getting Started](../guides/getting-started/).

## Command groups

| Command group | Description |
|--------------|-------------|
| [`fullsend agent`](agent.md) | Generate a custom agent and manage agent registrations — new, add, list, set, update, remove |
| [`fullsend github`](github.md) | Configure GitHub orgs and repos — setup, enrollment, day-2 operations |
| [`fullsend inference`](inference.md) | Manage inference credentials — GCP Workload Identity Federation for Agent Platform, and OpenAI WIF enrolment for GPT on pi or codex |
| [`fullsend mint`](mint.md) | Deploy and manage the OIDC token mint service |
| [`fullsend repos`](repos.md) | Manage per-repo installations at scale via declarative manifest |

## Additional commands

| Command | Description |
|---------|-------------|
| [`fullsend run`](run.md) | Execute an agent locally in a sandbox. See [running agents locally](../guides/user/running-agents-locally.md). |
| `fullsend poll` | Discover forge events and dispatch agent pipelines. `--forge gitlab` runs the cron poller ([ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md)). `--input-driver jira-poll` polls Jira (see [Jira integration](../guides/user/jira-integration.md)). `--input-driver gitlab-webhook` is the GitLab dispatcher job's webhook fast-path ([ADR 0125](../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)). It reads the native webhook body from the file-type `TRIGGER_PAYLOAD` variable, re-validates it against the GitLab API, and creates each routed stage's pipeline (typed inputs, `FULLSEND_DISPATCH_SECRET` HMAC) with the poller-role credential. It requires `--project`, which has no `CI_PROJECT_PATH` fallback. It deduplicates against the poller's per-mode state on the `fullsend-poll-state-slash` and `fullsend-poll-state-events` branches, so an event the webhook dispatched is not dispatched again by the poller, and the reverse. Shared poller state is occurrence-aware (label additions are keyed on the resource label event ID, dispatched keys are retained for the webhook freshness window, and a failed label dispatch is handed back to the poller as a pending retry); legacy persisted keys keep working. Older pollers do not recognise the new label keys, so before a rollback or mixed-version run, follow the [ADR 0132](../ADRs/0132-occurrence-aware-shared-poller-state.md) guidance to drain in-flight poll jobs or pin every poller to one version. |
| `fullsend lock [agent-name]` | Pin remote dependencies to `lock.yaml` |
| `fullsend scan` | Run security scanners on agent input/output |
| `fullsend eval-measure` | Score wild-run traces into `eval-measurements.jsonl`. See [Eval measurements](../guides/infrastructure/eval-measurements.md). |

## Global flags

All commands that interact with GitHub resolve authentication via `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token` (in that order). Explicit token flags such as `--token` and `--forge-token` override the chain. For GitLab, set `GITLAB_TOKEN` or pass `--gitlab-token` to `repos` subcommands. The CLI runs preflight checks and tells you exactly which OAuth scopes are missing before making any changes.

For the complete command tree with implementation details, see [CLI internals](../guides/dev/cli-internals.md).
