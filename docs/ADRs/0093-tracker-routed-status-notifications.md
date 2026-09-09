---
title: "93. Event-source routing for run-status notifications"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - tracker
  - notifications
  - status-comments
  - jira
  - portability
---

# 93. Event-source routing for run-status notifications

Date: 2026-08-29

## Status

Accepted

Builds on:

- Forge abstraction: [ADR 0005](0005-forge-abstraction-layer.md)
- Conversation surface / domain split: [ADR 0086](0086-conversation-surface-for-agent-participation.md)

## Context

When an agent run is triggered by an event — a GitHub issue comment, a
GitLab MR update, a Jira issue transition — the resulting status
notifications (start comments, completion comments, emoji reactions,
orphan reconciliation) must be posted back to the tracker that
*originated* the event, not necessarily to the forge that hosts the
code.

Previously, status notifications were hardwired to `forge.Client` with
`owner/repo/number` addressing. This works when the triggering work
item and the code repository live on the same forge, but breaks when
an external issue tracker drives the work.

The concrete failure: when a Jira issue triggers a code-agent run, the
Jira issue number (e.g. 6964193) is passed to `forge.Client` as a GitHub
issue number. No such GitHub issue exists, producing:

```
Failed to post start status: posting start comment:
  create issue comment on #6964193: github api: 404 Not Found
```

The key insight is that the notification destination should be
*dynamically determined by event provenance*: a Jira-triggered run
posts status to Jira, a GitHub-triggered run posts to GitHub, a
GitLab-triggered run posts to GitLab. The code repository and the
notification target are independent axes.

The repository already has a tracker-neutral comment interface
(`tracker.Client`) with adapters for GitHub/GitLab (via `forge.Client`)
and Jira. ADR 0086 established the domain split: `forge.Client` for
git-hosting, `tracker.Client` for issue content, `conversation.Client`
for chat. Status notifications are issue content, not git-hosting
operations, so they naturally route through `tracker.Client`.

## Options

### A. Special-case Jira in the notifier

Add Jira-specific branching to `statuscomment.Notifier` alongside the
existing `forge.Client` calls. Fast to implement but duplicates the
tracker abstraction and grows worse with each new tracker backend.

### B. Dynamic event-source routing through `tracker.Client` (chosen)

Let the event source determine the notification destination at
runtime. The notifier accepts a `tracker.Client`, and callers wire in
the appropriate tracker adapter based on which system originated the
event. This replaces `forge.Client` in the status-notification path
as a natural consequence: comments route to whichever tracker
originated the run.

Reactions become an optional capability (`tracker.Reactor` interface)
since tracker reaction support varies. Jira Cloud supports reactions
on comments but not on issues; GitHub and GitLab support both. Making
`Reactor` optional keeps the interface clean for trackers with partial
or no reaction support.

### C. Post status to both the tracker and the forge

Dual-write so status appears on both the Jira issue and a corresponding
GitHub issue. Requires a matching GitHub issue to exist (or be created),
adds complexity, and doubles API calls for every status update.

## Decision

Adopt **Option B**: dynamically route status notifications based on
event provenance.

The central change is architectural: the notification destination is
no longer hardwired to the code-output forge but is determined by the
event source at runtime. The interface refactoring (`forge.Client` →
`tracker.Client`) is a consequence of this routing decision — it makes
the notifier tracker-agnostic so any adapter can be wired in.

### Event-source wiring

Callers determine the tracker adapter based on the event source:

- **GitHub-triggered runs**: callers construct
  `tracker.NewForgeClient(ghClient)` — status posts to GitHub.
- **GitLab-triggered runs**: callers construct
  `tracker.NewForgeClient(glClient)` — status posts to GitLab.
- **Jira-triggered runs**: callers construct a `tracker.JiraClient` —
  status posts to Jira.

The `ClientFactory` in the GitHub path returns
`tracker.NewForgeClient(gh.New(mintedToken))` so each token refresh
produces a tracker-wrapped client. Both `fullsend run` and
`reconcile-status` read the normalized event from the on-disk dispatch
payload or `GITHUB_EVENT_PATH` and derive the tracker adapter from
`source.system` and the Jira project/number from `entity.key`.

### Interface changes

1. **`tracker.Client`** gains `DeleteComment` for cleaning up transient
   start comments when completion is suppressed.

2. **`tracker.Reactor`** is a new optional interface:

   ```go
   type Reactor interface {
       AddIssueReaction(ctx, project, number, content) (id, error)
       DeleteIssueReaction(ctx, project, number, reactionID) error
       AddCommentReaction(ctx, project, number, commentID, content) (id, error)
       DeleteCommentReaction(ctx, project, number, commentID, reactionID) error
   }
   ```

   `tracker.ForgeClient` implements `Reactor` (GitHub and GitLab support
   emoji reactions on both issues and comments). `tracker.JiraClient`
   does not implement `Reactor` currently: Jira Cloud supports reactions
   on comments but not on issues, and the partial support doesn't
   cleanly fit the `Reactor` interface which includes issue-level
   reactions. Adding Jira comment-reaction support is straightforward
   once needed. Consumers type-assert to `Reactor` and silently skip
   reaction operations when the tracker does not implement it.

### Notifier changes

- `statuscomment.Notifier` accepts `tracker.Client` instead of
  `forge.Client`, making it tracker-agnostic.
- Addressing changes from `(owner, repo string, number int)` to
  `(project string, number int)` to match `tracker.Client`'s
  project-keyed model.
- `ClientFactory` returns `tracker.Client` instead of `forge.Client`.
- `SetTriggerCommentID` accepts `string` (tracker comment IDs are
  strings for JSON round-tripping safety and non-numeric ID support).

### ReconcileOrphaned changes

- Accepts `tracker.Client` and `(project, number)` instead of
  `forge.Client` and `(owner, repo, number)`.
- The `reconcile-status` CLI command reads the normalized event from
  the same sources as `fullsend run` (on-disk dispatch payload or
  `GITHUB_EVENT_PATH`) to determine the tracker destination. For
  GitHub/GitLab events it wraps the forge client in
  `tracker.NewForgeClient()`; for Jira events it constructs a
  `tracker.JiraClient` using the Jira credentials from environment
  variables.

## Consequences

- **Dynamic routing**: status notifications are directed to the tracker
  that originated the event, not the code-output forge. A Jira-triggered
  run posts status to Jira instead of producing a 404 on a non-existent
  GitHub issue.
- Reactions are silently skipped for trackers that do not implement
  `Reactor` (e.g. Jira, which supports comment reactions but not issue
  reactions — partial support that doesn't fit the current interface).
  Adding Jira comment-reaction support is a future option.
- The `statuscomment` package no longer imports `internal/forge`,
  depending only on `internal/tracker` and `internal/config`.
- Adding a new tracker backend (e.g. Linear, Azure DevOps) requires only
  implementing `tracker.Client` (and optionally `tracker.Reactor`);
  status notifications work automatically via event-source routing.
- The orphan reconciler uses the same event-source routing, so
  interrupted runs on Jira issues are also finalized correctly.
- `jira.LiveClient` gains a `DeleteComment` method to satisfy the
  extended `tracker.Client` interface.
