---
title: "177. Allow repositories to skip Jira actor authorization"
status: Accepted
relates_to:
  - security-threat-model
topics:
  - authorization
  - jira
---

# 177. Allow repositories to skip Jira actor authorization

Date: 2026-11-26

## Status

Accepted

## Context

[ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)
requires authorization at dispatch. Jira's project role roster requires
Administer Projects. [ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md)
offers an alternative through account and group allowlists and a label rule
based on Jira's Edit Issues permission. A repository that trusts all Jira
participants to start agents must still manage allowlists for non-label events
or run the poller with project administration privileges.

This ADR aims to let repositories use Jira permissions and harness triggers
as their dispatch boundary when they trust all participants in the Jira
scope they poll.

## Decision

We allow a repository to explicitly skip Jira actor authorization so it can
accept matching events from any Jira actor. This is a Jira-specific exception
to ADR 0054. The repository sets `check_project_members: false` on its
`provider: jira` entry in `.fullsend/config.yaml`. The default is `true`,
which preserves the existing authorization policy.

With the exception enabled, the poller skips project role and group lookups,
and Jira events bypass the dispatch actor gate. Harness triggers and the
poller's existing event selection still apply. The event retains the actor
identity and carries an unresolved role when the poller has an actor ID.

## Forge scope

The policy applies to Jira polling for GitHub and GitLab targets. Forgejo
does not have a Jira polling target. Forge-native events use their existing
authorization policies.

## Consequences

- A polling account can run with the Jira permissions needed to read and process issues without permission to read the project role roster.
- Anyone who can produce a matching event in the polled Jira scope can start an agent, including agents that can modify the target repository.
- The Jira project permissions and polling query now define which Jira activity can reach harness routing when the repository opts out.
