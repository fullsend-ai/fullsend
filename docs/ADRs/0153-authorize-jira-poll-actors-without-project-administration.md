---
title: "153. Authorize Jira poll actors without project administration"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
topics:
  - authorization
  - jira
  - least-privilege
---

# 153. Authorize Jira poll actors without project administration

Date: 2026-10-28

## Status

Accepted

## Context

The `jira-poll` input driver
([ADR 0063](0063-polling-based-work-discovery.md)) authorizes each event
author under
[Authorization Contract v1](../normative/authorization/v1/README.md)
([ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)).
It maps the author's Jira project role name to a Fullsend role. At
`189d3637ce77b05c21009ac2cc69c0fa9223d89e`, `Poller.Run`
(`internal/jirapoll/poller.go`) calls `loadRoleStructure` on every cycle
that selects at least one issue, and `LiveClient.GetProjectRoleActors`
(`internal/forge/jira/client.go`) reads `GET /rest/api/3/project/<KEY>/role`
and each role's detail. Jira Cloud serves those endpoints only to accounts
with **Administer Projects**. So the long-lived polling credential must
hold the project's Administrators role just to read who may trigger
agents ([#8401](https://github.com/fullsend-ai/fullsend/issues/8401)).
That is broader than polling needs, which conflicts with the least-privilege
principle in the
[security threat model](../problems/security-threat-model.md).

At the same SHA, `authorizeEvent` (`internal/harnessdispatch/auth.go`)
already authorizes GitHub label-added events without a role threshold,
because applying a GitHub label needs triage access. Jira has an equivalent
check: changing labels needs the **Edit Issues** permission.

## Options

- **Keep project roles as the only source.** No change, but every poller
  keeps Administer Projects.
- **Trust any commenter.** No Jira permission needed, but anyone who can
  comment could run write-gated agents.
- **A Jira permission-check API.** Jira Cloud offers these only to
  administrators. `/user/permission/search` needs Administer Projects, and
  `/permissions/check` for another user needs Administer Jira.
- **Config-owned lists plus Jira's own label permission** (chosen).

## Decision

The `jira` authorization provider is opt-in. A repository with no `jira`
entry under `authorization:` keeps the previous behavior: every Jira event
resolves its actor role through the project role roster, and a label-added
event takes the per-harness role threshold like any other event. A repository
that adds a `jira` entry authorizes Jira actors without the project role
roster unless it opts into it:

1. **With a `jira` entry, label-added events are authorized by Jira's Edit
   Issues permission** by default (`labels: jira_editors`), like GitHub
   label-added events. The dispatch gate admits a Jira `label_changed` event
   with `label.action: added` for every matched harness. The poller still
   drops its own account's events as bot events.
2. **A `jira` provider under `authorization:`** in `.fullsend/config.yaml`
   lists Jira groups and account IDs for the Fullsend `write` and `triage`
   roles. It also sets `labels:` to `jira_editors` (the default once the entry
   is present), `allowlist`, or `project_roles`. It applies only to events
   with `source.system: jira`.
   When the provider sets a `write` or `triage` list (even an empty one),
   the lists are authoritative for slash commands and every other non-label
   event, so emptying them denies everyone rather than re-enabling the
   project role fallback. An actor in `write`
   is `write`, an actor only in `triage` is `triage`, and anyone else is
   `none` (denied). Groups are resolved with `GET /rest/api/3/user/groups`,
   which needs only Browse users and groups, and are cached per actor per
   cycle. Account entries need no Jira call. `labels: allowlist` checks label
   authors against the same lists. `labels: project_roles` uses the roster.
   A `jira` entry with `labels: project_roles` and no `write` or `triage`
   list resolves every event through the roster, exactly as having no entry
   does. That two-line entry is how a repository states that it keeps
   today's behavior.
3. **The roster is loaded only when an event uses it.** With no `jira`
   entry, that is every event. With an entry, it is every event under
   `labels: project_roles` with no lists, a slash command when no lists are
   configured under any other `labels:` value, and a label-added event under
   `labels: project_roles`. A load failure still fails the cycle without
   advancing checkpoints. With an entry that sets no lists and does not
   select `labels: project_roles`, the other events (issue opened, edited,
   closed or reopened, plain comments, label removals) are not resolved,
   carry `actor.role: none`, and pass no role threshold. A setup whose `jira`
   entry uses the lists, or only label triggers under `jira_editors`, never
   calls `GET /rest/api/3/project/<KEY>/role`.

With no `jira` provider, every Jira event, label-added included, resolves
through the project role roster and takes the per-harness role threshold.

## Forge scope

This decision applies to Jira-sourced events from the shared `jira-poll`
driver. It is the same under GitHub Actions and GitLab CI, which both run
that driver. GitHub, GitLab, and Forgejo event authorization, and the
`owners_file` provider, are unchanged.

## Consequences

- A Jira poller needs only Browse Projects, Edit Issues, Add Comments, and
  Edit Own Comments. It also needs Browse users and groups when the lists name
  groups, or when project-role resolution finds a role granted to a group
  (it then calls `GET /rest/api/3/user/groups` for the actor), and
  Administer Projects only when project roles are used. A
  repository that adds no `jira` entry changes nothing and keeps needing
  Administer Projects.
- Once a repository adds a `jira` entry, its Jira label triggers are
  authorized by Edit Issues by default. In many projects that is broader than
  the Developers role, so such repositories that want the old rule must set
  `labels: project_roles`.
- With a `jira` entry that sets no lists and does not select
  `labels: project_roles`, Jira issue opened and edited events no longer pass
  role thresholds. A trigger on them, such as the default triage harness's,
  needs the `jira` lists, or `labels: project_roles`, which resolves them
  through project roles.
- The poller and the dispatch load paths now validate the `authorization`
  block (unknown or duplicate providers, and the `write`, `triage` and
  `labels` keys on a provider other than `jira`) and fail closed, where the
  runtime previously did not validate it. This is not a breaking change:
  `fullsend agent`, `github setup` and `lint` already rejected unknown and
  duplicate providers through `cfg.Validate()`, so such a repository never had
  a successful agent run, and the `write`, `triage` and `labels` keys are new
  in this change, so no existing configuration sets them.
- Repository configuration becomes a second place to keep Jira
  authorization in sync with Jira group membership.
