---
title: "69. Jira project enrollment via fullsend CLI"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - jira
  - enrollment
  - external-issue-trackers
  - credentials
---

# 69. Jira project enrollment via fullsend CLI

Date: 2026-07-09

## Status

Accepted

## Context

Fullsend's dispatch model assumes events originate from GitHub — issues,
PRs, and comments flow through the shim workflow
([ADR 0034](0034-centralized-shim-routing-via-dispatch.md)) into agent
pipelines. Enterprise teams commonly use Jira Enterprise/Cloud for backlog management,
so Jira issues need an entry path into the same pipeline.

A working proof-of-concept ([manish-jira](https://github.com/rh-hemartin-fullsendai/manish-jira))
validated the approach: Jira Automation rules fire webhooks to GitHub's
`repository_dispatch` API, a dispatch workflow validates enrollment and
routes to agent workflows, and agents use harness composition
([ADR 0045](0045-forge-portable-harness-schema.md)) to handle
Jira-specific event formats via `base:`, `pre_script`, and `forge:`
overrides. Jira
credentials stay on the GitHub Actions host and never enter the agent
sandbox, following the prefetch model from
[ADR 0017](0017-credential-isolation-for-sandboxed-agents.md).
Today this setup is entirely manual; the enrollment CLI automates it.

Repo-to-issue association (which code repository handles which Jira issue)
is a separate concern handled by the poll driver design and is out of scope
for this ADR.

## Options

### Alternative: Jira Connect or Forge app

A Jira Connect or Forge app could receive webhooks natively without
`repository_dispatch` as a bridge. Rejected because it requires hosting an
external service, an app distribution and consent flow, and a fundamentally
larger product scope. The CLI-only approach delivers value without
operational infrastructure.

### Alternative: Polling via scheduled workflows

A GitHub Actions schedule could poll Jira for new issues using JQL.
Rejected for the enrollment path — polling adds latency and complexity.
However, the poll driver design may use this pattern for repo association,
which is a separate concern.

## Decision

Add a `fullsend jira enroll <owner/repo>` CLI command that configures
the inbound event path (Jira Automation → GitHub `repository_dispatch`)
and the outbound credential path (Jira API token as GitHub secret) for a
single Jira project.

The command creates or updates a `.jira.yml` enrollment config (project
key, host), attempts to create Jira Automation rules via the
[Automation Rule Management API](https://developer.atlassian.com/cloud/automation/rest/api-group-rule-management/),
commits dispatch and agent workflow files, and sets Jira credential
secrets on the repo. Enrollment is idempotent.

The Automation API currently requires site admin for write operations
([AUTO-2120](https://jira.atlassian.com/browse/AUTO-2120)). When the API
returns 403, the CLI prints pre-filled manual instructions for creating
the rules in the Jira UI.

No new agents are introduced. Existing agents gain Jira awareness through
harness composition ([ADR 0045](0045-forge-portable-harness-schema.md)) —
Jira-specific pre/post scripts and `forge:` overrides are the repo
admin's responsibility, not part of enrollment.

The CLI follows the `fullsend github` command pattern: cobra subcommands,
credential resolution cascade (flag → env → prompt), and `--dry-run`
support.

## Consequences

- External issue trackers can connect to fullsend without modifying agents
  or the core dispatch model.
- Jira API tokens follow the prefetch credential isolation model
  ([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)) — stored
  as GitHub secrets, consumed by host-side scripts, never in the sandbox.
- The AUTO-2120 limitation means non-admin users must create automation
  rules manually; if Atlassian resolves it, the manual fallback path
  becomes unused but harmless.
- Jira API token rotation is the repo admin's responsibility — re-running
  `fullsend jira enroll` with a new token updates the secret.
- Repo association is intentionally out of scope — the poll driver design
  addresses which issues route to which code repositories.
