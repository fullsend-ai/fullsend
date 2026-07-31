---
title: "79. Jira project enrollment via fullsend CLI"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - jira
  - enrollment
  - external-issue-trackers
  - credentials
---

# 79. Jira project enrollment via fullsend CLI

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
validated end-to-end Jira-to-agent dispatch. The enrollment steps
(credential provisioning, config updates) were entirely manual; the
CLI automates them.

## Options

### Option 1: Manual credential setup per documentation

Operators follow a guide to create forge secrets and edit
`.fullsend/config.yaml` by hand. Rejected — error-prone for multi-project
setups and inconsistent across forges.

### Option 2: `fullsend jira enroll` CLI command

A CLI command provisions credentials and writes poll driver connection
config. Follows established CLI patterns (cobra subcommands, `--dry-run`).

## Decision

Add a `fullsend jira enroll <target-repo>` CLI command that provisions
Jira API credentials as forge secrets and writes poll driver connection
metadata to `.fullsend/config.yaml`. The command resolves Jira
credentials via environment variables or CLI flags and supports
`--dry-run`.

Enrollment writes Jira connection metadata (project key, host URL)
directly into the poll driver's `poll.input_drivers[].connection`
block in `.fullsend/config.yaml`, consistent with ADR 0063's existing
schema. Credentials are stored as forge-level secrets (not checked
into the repository), compatible with
[ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)'s
credential isolation model. Ensuring credentials stay outside the agent
sandbox is the harness author's responsibility (via `env.runner` /
`env.sandbox` per [ADR 0055](0055-unified-env-var-delivery.md) and
pre/post scripts).

Enrollment is designed to be idempotent — re-running with a new token
updates the forge secret. The enrollment scope is credentials and config only; the dispatch
mechanism is the poll driver's responsibility
([ADR 0063](0063-polling-based-work-discovery.md)), and agent-level
Jira awareness (harness `pre_script` / `post_script`) is the repo
admin's responsibility.

## Consequences

- Jira credential provisioning is automated and consistent across forges.
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
- Jira API token rotation is the repo admin's responsibility —
  re-running `fullsend jira enroll` with a new token is designed to
  update the forge secret. Per-forge idempotency verification is tracked
  as an implementation concern.
- Repo-to-issue association is out of scope — the poll driver
  ([ADR 0063](0063-polling-based-work-discovery.md)) handles which
  issues route to which repositories.
