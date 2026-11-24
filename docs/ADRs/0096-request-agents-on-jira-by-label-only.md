---
title: "96. Request agents on Jira by label only"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - jira
  - polling
  - dispatch
---

# 96. Request agents on Jira by label only

Date: 2026-11-24

## Status

Accepted

Narrows [ADR 0061](0061-harness-cel-dispatch.md) for Jira: an agent's trigger
rule cannot start it from a comment there. The rest of that ADR stands.

## Context

A person can ask for an agent on a Jira issue in two ways today: by adding a
label such as `ready-to-code` or `ready-for-triage`, or by typing a command
such as `/fs-code` or `/fs-triage` as a comment (see the
[Jira integration guide](../guides/user/jira-integration.md)). A run does
not remove the label that started it, so asking again by label means
removing it and adding it back.

Fullsend finds these requests by polling
([ADR 0063](0063-polling-based-work-discovery.md)), and under load the poller
loses some of them
([ADR 0160](0160-spend-jira-poll-slots-only-on-candidates-with-new-activity.md),
[#9815](https://github.com/fullsend-ai/fullsend/pull/9815)). Labels are much
easier to make reliable than typed commands. Jira's search matches a label
exactly and matches comment text only loosely, and fullsend can remove a
label once it has dealt with the request, where it cannot reasonably remove
someone's comment. This decision does not by itself stop requests being
lost. It leaves labels as the only kind of request that later work on the
poller has to handle.

This ADR aims to leave Jira with one way to ask for an agent, one that
fullsend can find exactly and clear once it has answered.

## Decision

On Jira, a person asks for an agent by adding a label, and fullsend removes
the label once it has answered the request, so that every request on Jira is
one fullsend can find exactly and clear. Triage still starts by itself when a
person opens or edits an issue.

- **Comments do not start agents on Jira, for now.** This covers every typed
  command such as `/fs-code`, and any custom agent's rule that reads comment
  text. A comment remains part of the issue, which an agent reads when it
  runs. Typed commands can return through a later ADR if a reliable way to
  handle them is found.
- **Fullsend removes the label when it answers a label request.** It answers
  either by starting the run or by refusing, for example because the person
  who added the label may not start that agent. How a refusal is reported
  to the person is left to the implementation. Asking again is adding the
  label again.

A label that fullsend does not treat as a request stays on the issue. Today
that includes a label added by a bot account; whether such a label becomes a
request is the subject of
[#9390](https://github.com/fullsend-ai/fullsend/pull/9390). How requests are
found and who may make them do not change.

## Forge scope

Applies to issues that fullsend reads from Jira, whether the code is hosted
on GitHub or GitLab. Issues, pull requests and merge requests on GitHub and
GitLab do not change: labels and typed commands both start agents there, and
a run does not remove the label that started it. Whether those forges should
remove it too is left open. Forgejo is not affected, as fullsend does not read Jira
for it.

## Consequences

- This is a breaking change for Jira users: a repository or custom agent
  that relies on typed commands on Jira needs a label trigger before it
  upgrades.
- `/fs-code --force` is no longer available from a Jira issue. When the code
  agent skips a run because a person's pull request is open, close that pull
  request and ask again. When it skips because the issue has child issues,
  ask for the code agent on the child issues instead. A separate label that
  forces a run can be added later if there is demand for it.
- `/fs-triage` no longer overrides the `skip-triage` label on Jira. To
  triage such an issue, remove that label first.
- When triage promotes a Jira issue it adds `ready-to-code` from a bot
  account, which starts nothing, as today. Without `/fs-code`, a person
  starts the code agent by removing that label and adding it back, unless
  [#9390](https://github.com/fullsend-ai/fullsend/pull/9390) changes this.
- A request the poller missed is not retried, as today. Its label stays on
  the issue, and asking again is removing the label and adding it back.
- The label no longer records that an agent ran. The run's status comments
  do, unless a repository has turned them off, and so does the optional run
  label.
