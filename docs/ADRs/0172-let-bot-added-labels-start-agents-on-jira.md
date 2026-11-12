---
title: "172. Let bot-added labels start agents on Jira"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - authorization
  - jira
  - bots
  - dispatch
---

# 172. Let bot-added labels start agents on Jira

Date: 2026-11-12

## Status

Accepted

## Context

[ADR 0107](0107-bot-identity-resolution-for-dispatch-authorization.md) keeps
label-added events as the supported way for one agent to hand work to another.
It names GitHub as the only implementation and lets another adapter enable the
same grant once it has forge-authoritative evidence of who made the label
change. Jira's changelog records the author of each label change, and
[ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md)
already treats an accepted Jira label change as authorization for a human
labeler.

The `jira-poll` driver
([ADR 0063](0063-polling-based-work-discovery.md)) drops every event it
classifies as bot-authored before authorization or harness matching. An actor
counts as a bot when it is the account the poller authenticates as (added for
the dispatch loop in
[#6835](https://github.com/fullsend-ai/fullsend/issues/6835)), when Jira
reports the account type `app`, or when its display name ends in "bot" or
"automation" or starts with "Automation for". So a label added by the
repository's own Fullsend account never starts an agent. Whether a label from
another automation account does depends on its display name. The workaround,
writing labels with a second, ordinarily named account, relies on that name
test and disables the #6835 loop guard, which only works when the poller and
the agents share one Jira account
([#9389](https://github.com/fullsend-ai/fullsend/issues/9389)).

This ADR aims to let a bot-added Jira label start an agent on the same terms
as on GitHub, without relying on display names and while keeping the #6835
guard for comments.

## Options

- **Keep dropping all bot events.** Hand-off stays impossible with the shared
  account and accidental with any other.
- **Admit all bot events.** Bot comments would reopen the #6835 loop, and
  non-label bot dispatch needs the registered bot identity ADR 0107 requires,
  which Jira does not have.
- **Admit bot label-added events only, through the repository's `labels:`
  rule** (chosen).

## Decision

We admit bot-authored Jira label-added events to the dispatch gate to make
Jira the second implementation of ADR 0107's label grant.

1. **Scope.** Only `label_changed` events with `label.action: added` stop
   being dropped. Bot-authored comments, slash commands, label removals, and
   issue opened, edited, closed, and reopened events stay dropped, which keeps
   the #6835 guard.
2. **`labels:` modes.** A bot's label-added event takes the same path as a
   human's under the repository's `jira` authorization entry. Under
   `jira_editors`, Jira's acceptance of the label change, with the changelog
   author as provenance, is the grant, as on GitHub. Under `allowlist`, the
   bot account must be in the `write` or `triage` list; listing it is the
   opt-in. Under `project_roles`, or with no `jira` entry, the bot account
   needs a project role like anyone else. In every mode, matching a harness
   trigger is still required.
3. **Bot classification.** Only provider-controlled metadata classifies a
   label author as a bot: the poller's own account ID or Jira's `app` account
   type. A label author matched only by display name is a human and takes the
   human rules, including
   [ADR 0151](0151-harness-human-role-minimums.md)'s harness minimum, so a
   rename cannot bypass them. On the paths that stay dropped, the display-name
   test may remain, because dropping an event grants nothing.
4. **Loop safety** is the harness author's responsibility, as on GitHub. Only
   a new label addition dispatches, so re-adding a label that is present
   triggers nothing. A loop needs a label to be removed and added again. For
   example, an agent removes its trigger label and later adds it back, or
   agent A adds B's trigger and B adds back A's trigger after A's was removed.
   A harness must not trigger on a label that its own agent, or an agent it
   hands off to, adds back; hand-off chains must be acyclic. The
   per-issue-per-cycle event cap bounds each poll cycle, not the loop. We
   reject telling agents apart by labeler: all of them write through one Jira
   account, which the #6835 guard needs. A per-issue dispatch cap would affect
   every forge and needs its own ADR.
5. **Untrusted content.** Jira is at parity with GitHub. An agent that reads
   issue text and adds a triggering label gives that text's author an indirect
   way to trigger the next agent. The label authorizes only its exact
   transition, and the content the agent acted on stays untrusted, as ADR 0107
   states.

## Forge scope

This decision applies to Jira-sourced events from the shared `jira-poll`
driver, the same under GitHub Actions and GitLab CI. GitHub, GitLab, and
Forgejo event handling are unchanged; ADR 0107 governs their bot events.

## Consequences

- A triage agent's label can start the code agent on Jira with the single
  shared account, so the second-account workaround and its loss of the #6835
  guard are no longer needed.
- Labels Fullsend itself adds, such as the `fs-code-agent` run marker, become
  label-added events that harness triggers can match, so harness authors must
  keep triggers off them.
- A cyclic hand-off runs until a human removes the label or the trigger;
  no platform cap stops it.
- Under `allowlist` and `project_roles`, bot hand-off needs the bot account
  listed or given a project role, so those repositories opt in explicitly.
- The display-name test no longer affects whether a label-added event is
  admitted.
