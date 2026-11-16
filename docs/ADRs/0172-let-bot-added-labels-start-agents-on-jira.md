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
repository's own Fullsend account never starts an agent. The workaround,
writing labels with a second, ordinarily named account, relies on that name
test and disables the #6835 loop guard, which only works when the poller and
the agents share one Jira account
([#9389](https://github.com/fullsend-ai/fullsend/issues/9389)).

This ADR aims to let the Fullsend account's own label start an agent, and a
label from another account only when the repository names that account,
without relying on display names and while keeping the #6835 guard for
comments.

The reasoning in #9389, that an organization controls which accounts can edit
issues in its Jira project, holds for accounts but not for automation rules.
Atlassian reports every change a rule makes as made by the rule's actor
([What is a flow actor?](https://support.atlassian.com/cloud-automation/docs/what-is-a-rule-actor/)),
by default Automation for Jira. Anyone who can create, comment on, or
transition an issue can fire a rule, including people without Edit Issues, and
the changelog names the actor, not the person who fired the rule. Jira's
acceptance of such a label change therefore says nothing about who chose to
make it. No one has recorded which `accountType` Jira reports for that actor,
so the design must not depend on it.

## Options

- **Keep dropping all bot events.** Hand-off stays impossible with the shared
  account and accidental with any other.
- **Admit all bot events.** Bot comments would reopen the #6835 loop, and
  non-label bot dispatch needs the registered bot identity ADR 0107 requires,
  which Jira does not have.
- **Admit label-added events from every account classified as a bot.** Any
  automation rule that adds a label would then start agents for whoever can
  fire the rule.
- **Admit label-added events from the poller's own account, and from accounts
  the repository lists, through the repository's `labels:` rule** (chosen).

## Decision

We admit Jira label-added events from the poller's own account, and from
accounts the repository's `jira` entry lists, to the dispatch gate to make
Jira the second implementation of ADR 0107's label grant.

1. **Scope.** Only `label_changed` events with `label.action: added` stop
   being dropped. Bot-authored comments, slash commands, label removals, and
   issue opened, edited, closed, and reopened events stay dropped, which keeps
   the #6835 guard.
2. **`labels:` modes.** An admitted label-added event takes the same path as
   a human's under the repository's `jira` authorization entry. Under
   `jira_editors`, Jira's acceptance of the label change, with the changelog
   author as provenance, is the grant. Under `allowlist`, the account must be
   in the `write` or `triage` list, so the poller's own account must be
   listed; listing it is the opt-in. Under `project_roles`, or with no `jira`
   entry, the account needs a project role like anyone else. In every mode,
   matching a harness trigger is still required.
3. **Which accounts count.** A label-added event is admitted only when its
   author is the poller's own account ID (provider-controlled metadata from
   Jira's `/myself`) or an account ID listed in the `write` or `triage` list
   of the repository's `jira` entry. Any other author that the poller
   classifies as a bot, whether by Jira's `app` account type or by display
   name, such as Jira Automation's actor or an installed app, stays dropped
   until the entry lists its account ID. The display-name test therefore stays
   on the dropped path only, where it grants nothing, and a rename cannot
   admit an event or bypass
   [ADR 0151](0151-harness-human-role-minimums.md)'s harness minimum. An
   author that no test classifies as a bot is a human and takes the human
   rules.
4. **Loop safety** is the harness author's responsibility, as on GitHub. Only
   a new label addition dispatches, so re-adding a label that is present
   triggers nothing. A loop needs a label to be removed and added again. For
   example, an agent removes its trigger label and later adds it back, or
   agent A adds B's trigger and B adds back A's trigger after A's was removed.
   A harness must not trigger on a label that its own agent, or an agent it
   hands off to, adds back; hand-off chains must be acyclic. The
   per-issue-per-cycle event cap bounds each poll cycle, not the loop. We
   reject telling agents apart by labeler: all of them write through one Jira
   account, which the #6835 guard needs.
5. **Jira-only backstop.** Because harness authors alone cannot be trusted to
   get a trigger right at upgrade, the poller also limits how many dispatches
   one issue can receive from admitted label additions in a rolling window.
   The count is kept per issue beside the checkpoint (`lastCheck`) the poller
   already holds, so it applies to Jira only and changes nothing on other
   forges. Past the limit, further admitted label additions on that issue are
   dropped with a warning until the window passes or a human-authored event
   arrives. The limit, window, and setting are decided in the follow-up
   implementation.
6. **Untrusted content.** Jira is at parity with GitHub. An agent that reads
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
- A cyclic hand-off runs until the per-issue backstop (Decision 5) drops
  further additions or a human removes the harness trigger or routing and
  cancels any active or queued runs. Removing the label is at most
  best-effort: a running agent can add it back, and the poller rebuilds label
  additions from changelog history without checking that the label is still
  present, so an addition already recorded can still dispatch after removal.
- Only `allowlist` repositories opt in explicitly, by listing the poller's
  account. Under `jira_editors`, the default for an entry, and under
  `project_roles` or with no entry, the poller's account already holds the
  grant or a role, so the change turns on at upgrade with no configuration
  change. The default code trigger in the Jira guide matches a `ready-to-code`
  label from any actor kind, and triage applies that label, so once the
  follow-up ships, triage starts the code agent by itself on every such
  deployment. This reverses the guide's current statement that changelog
  entries from the poller account do not dispatch agents. The follow-up
  implementation must be marked breaking (`!`), ship with a release note, and
  document the opt-out: `event.actor.kind == "human"` on the label arm of a
  harness override trigger.
- Jira Automation and installed apps do not gain a grant. Their label
  additions stay dropped, as today, unless the entry lists the actor's account
  ID, and a repository that lists one accepts that anyone who can fire its
  rules can start agents. The security threat model notes this.
- The display-name test no longer affects whether a label-added event is
  admitted.
