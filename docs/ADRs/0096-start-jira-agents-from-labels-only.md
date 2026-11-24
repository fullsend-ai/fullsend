---
title: "96. Start agents on Jira from labels only"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - jira
  - polling
  - dispatch
---

# 96. Start agents on Jira from labels only

Date: 2026-11-24

## Status

Accepted

## Context

A person can ask for an agent on a Jira issue in two ways today: by adding a
label such as `ready-to-code` or `ready-for-triage`, or by typing a command
such as `/fs-code` or `/fs-triage` as a comment (see the
[Jira integration guide](../guides/user/jira-integration.md)). The label
stays on the issue afterwards, so asking again by label means removing it
and adding it back.

Fullsend finds these requests by polling
([ADR 0063](0063-polling-based-work-discovery.md)), and under load the poller
loses some of them
([ADR 0160](0160-spend-jira-poll-slots-only-on-candidates-with-new-activity.md),
[#9815](https://github.com/fullsend-ai/fullsend/pull/9815)). Labels are much
easier to make reliable than typed commands. Jira's search matches a label
exactly and matches comment text only loosely, and fullsend can remove a
label once it has dealt with the request, where it cannot reasonably remove
someone's comment.

This ADR aims to leave Jira with one way to ask for an agent, one that
fullsend can find exactly and clear once it has answered.

## Decision

We start agents on Jira from labels only, and remove the label once fullsend
has answered the request, so that every request on Jira is one fullsend can
find exactly and clear.

- **Typed commands do not start agents on Jira, for now.** This covers every
  `/fs-` command in a Jira comment, including a custom agent's. Such a
  comment remains an ordinary comment that an agent reads with the rest of
  the issue. Commands can return through a later ADR if a reliable way to
  handle them is found.
- **Fullsend removes the label when it answers a label request.** It answers
  either by starting the run or by refusing, for example because the person
  who added the label may not start that agent. Asking again is adding the
  label again.

A label that fullsend does not treat as a request stays on the issue. Today
that includes a label added by a bot account; whether such a label becomes a
request is the subject of
[#9390](https://github.com/fullsend-ai/fullsend/pull/9390). How requests are
found, who may make them, and triage starting when a person opens or edits
an issue do not change.

## Forge scope

Applies to issues that fullsend reads from Jira, whether the code is hosted
on GitHub or GitLab. Issues, pull requests and merge requests on GitHub and
GitLab do not change: labels and typed commands both start agents there, and
fullsend does not remove the label. Whether those forges should remove it
too is left open. Forgejo is not affected, as fullsend does not read Jira
for it.

## Consequences

- This is a breaking change for Jira users: a repository or custom agent
  that relies on typed commands on Jira needs a label trigger before it
  upgrades.
- `/fs-code --force` is no longer available from a Jira issue. To run the
  code agent after it skipped a run, clear what made it skip.
- When triage promotes a Jira issue it adds `ready-to-code` from a bot
  account, which starts nothing, as today. Without `/fs-code`, a person
  starts the code agent by removing that label and adding it back, unless
  [#9390](https://github.com/fullsend-ai/fullsend/pull/9390) changes this.
- A trigger label still on an issue now means fullsend has not answered it.
  A request the poller missed is not retried, as today; asking again is
  removing the label and adding it back.
- The label no longer records that an agent ran. The run's status comments
  do, and so does the optional run label.
