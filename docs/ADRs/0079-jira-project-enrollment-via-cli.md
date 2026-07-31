---
title: "79. Jira project setup via fullsend CLI"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - jira
  - enrollment
  - external-issue-trackers
  - credentials
---

# 79. Jira project setup via fullsend CLI

Date: 2026-07-09

## Status

Accepted

## Context

Fullsend's dispatch model (see [agent-infrastructure](../problems/agent-infrastructure.md))
uses pluggable input drivers to normalize events from multiple sources
([ADR 0061](0061-harness-cel-dispatch.md)). For Jira, the poll driver
([ADR 0063](0063-polling-based-work-discovery.md)) discovers work items
via JQL and dispatches agents through the shared pipeline. ADR 0063
defers credential placement as an open question — Jira API tokens need
to reach the poll driver and agent pre-scripts, but no ADR specifies
how those credentials are provisioned or where enrollment metadata lives.

A proof-of-concept ([manish-jira](https://github.com/rh-hemartin-fullsendai/manish-jira))
validated end-to-end Jira-to-agent dispatch using classic (unscoped)
API tokens against `<tenant>.atlassian.net`. The PoC is GitHub-specific
and uses push-based `repository_dispatch`; this ADR intentionally
narrows scope to credential provisioning and poll-driver config,
deferring dispatch mechanics to
[ADR 0063](0063-polling-based-work-discovery.md). The enrollment steps
were entirely manual in the PoC; the CLI automates them.

Atlassian is deprecating unscoped API tokens. Scoped tokens require the
`api.atlassian.com` gateway with a Cloud ID in the URL
(`https://api.atlassian.com/ex/jira/{cloudId}/rest/api/3/...`) and
Basic auth (`email:token`). The Cloud ID is a stable site identifier
resolvable from any tenant hostname via
`https://<host>/_edge/tenant_info`; the CLI resolves it once at
enrollment time.

## Options

### Option 1: Manual credential setup per documentation

Operators follow a guide to create forge secrets and edit
`.fullsend/config.yaml` by hand. Rejected — error-prone for multi-project
setups and inconsistent across forges.

### Option 2: `fullsend jira setup` CLI command

A CLI command provisions credentials and writes poll driver connection
config. The verb `setup` is chosen over `enroll` to avoid overloading:
`fullsend github enroll` is a lightweight config toggle that does not
set secrets, while `jira setup` provisions forge secrets alongside
config. Follows established CLI patterns (cobra subcommands,
`--dry-run`).

## Decision

Add a `fullsend jira setup <target-repo>` CLI command that operates on
a remote `owner/repo` target via the forge API (no local clone
required). The command accepts Jira credentials via environment
variables (`JIRA_HOST`, `JIRA_EMAIL`, `JIRA_API_TOKEN`) and supports
`--dry-run`. Credentials are never accepted via CLI flags to avoid
shell-history and process-list exposure.

The CLI resolves the Jira Cloud ID from the host URL at enrollment
time (`https://<host>/_edge/tenant_info`) and writes two non-secret
values into the poll driver's `poll.input_drivers[].connection` block
in `.fullsend/config.yaml`:

```yaml
poll:
  input_drivers:
    - type: jira-poll
      connection:
        cloud_id: "<resolved-at-enrollment>"
        project_key: EXAMPLE
```

Two credentials are stored as forge-level secrets (not checked into
the repository): `JIRA_EMAIL` (Atlassian account email) and
`JIRA_API_TOKEN` (scoped API token). Together these support Basic auth
against the `api.atlassian.com` gateway. This is compatible with
[ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)'s
credential isolation model. Ensuring credentials stay outside the
agent sandbox is the harness author's responsibility (via `env.runner`
/ `env.sandbox` per [ADR 0055](0055-unified-env-var-delivery.md) and
pre/post scripts).

The command is designed to be idempotent — re-running with a new token
updates the forge secret. The scope is credentials and config only;
the dispatch mechanism is the poll driver's
responsibility ([ADR 0063](0063-polling-based-work-discovery.md)),
and agent-level Jira awareness (harness `pre_script` /
`post_script`) is the repo admin's responsibility. This is distinct
from the CLI's `EnrollmentLayer` ([ADR 0006](0006-ordered-layer-model.md)),
which manages forge-level installation scaffolding.

## Consequences

- Jira credential provisioning is automated and consistent across
  forges. Two forge secrets (`JIRA_EMAIL`, `JIRA_API_TOKEN`) and two
  config values (`cloud_id`, `project_key`) fully describe a Jira
  connection.
- Credentials are stored as forge secrets, compatible with
  [ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)'s
  isolation model. Sandbox isolation is enforced downstream by harness
  configuration (`env.runner` / `env.sandbox` per
  [ADR 0055](0055-unified-env-var-delivery.md), pre/post scripts), not
  by enrollment.
- The Jira-token portion of ADR 0063's open question on credential
  placement is resolved for the enrollment path; the CLI writes
  connection metadata directly into `poll.input_drivers[].connection`.
  No separate `integrations.jira` config key is introduced — if a
  push-based dispatch path (e.g. Jira Automation webhooks) is adopted
  later, a future ADR can introduce integration-level config at that
  point. Forge-native credentials (`GITHUB_TOKEN`, App creds) remain
  unaddressed by this ADR.
- Credentials are accepted only via environment variables, not CLI
  flags, to avoid shell-history and process-list exposure.
- The Cloud ID is resolved once at enrollment time from the Jira host
  URL. Runtime API calls use the `api.atlassian.com` gateway with the
  stored Cloud ID, avoiding per-poll lookups.
- Jira API token rotation is the repo admin's responsibility —
  re-running `fullsend jira setup` with a new token is designed to
  update the forge secret. Per-forge idempotency verification is
  tracked as an implementation concern.
- Repo-to-issue association is out of scope — the poll driver
  ([ADR 0063](0063-polling-based-work-discovery.md)) handles which
  issues route to which repositories.
