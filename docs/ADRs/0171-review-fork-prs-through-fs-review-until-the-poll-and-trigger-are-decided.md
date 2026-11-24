---
title: "171. Review fork PRs through /fs-review until the poll and its trigger are decided"
status: Accepted
relates_to:
  - security-threat-model
  - platform-nativeness
  - code-review
topics:
  - workflows
  - security
  - polling
  - dispatch
  - pull-request-target
---

# 171. Review fork PRs through /fs-review until the poll and its trigger are decided

Date: 2026-11-19

## Status

Accepted

Follows up the open question that
[ADR 0164](0164-run-the-shim-on-pull-request-for-same-repo-prs.md) left on
automatic review for fork PRs.

## Context

[ADR 0164](0164-run-the-shim-on-pull-request-for-same-repo-prs.md) moves the
shim from `pull_request_target` to `pull_request` before GitHub blocks
`pull_request_target` by default on 2026-12-02. A fork's `pull_request` run
gets a read-only token, no secrets and no OIDC token, so it cannot review the
PR or start a run that does. Fork PRs are reviewed only when someone
authorized comments `/fs-review`. That comment fires `issue_comment`, which
runs the default-branch workflow, and dispatch authorizes the commenter
([ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)), so no
fork-controlled run is involved. In the 30 days to 2026-11-12, 105 of 166
human PRs in `fullsend-ai/fullsend` came from forks, and 87 of those authors
had triage access or higher.

Getting automatic review back takes two parts, and neither is ready:

- **A poll.** No event a fork can cause may safely start a privileged run, so
  default-branch code has to list open fork PRs, authorize each author and skip
  commits already reviewed. That work belongs to entity-source discovery
  ([ADR 0098](0098-entity-first-harness-evaluation.md),
  [ADR 0149](0149-coalesce-entity-source-discovery-queries.md)), which is
  not built yet ([#8418](https://github.com/fullsend-ai/fullsend/issues/8418)).
  A fork-PR input on `fullsend poll` today would be an interim driver with its
  own caller workflow, to be replaced later.
- **A wake-up with a bounded wait.** GitHub's Actions schedule cannot provide
  one. Across 400 runs each of three public repositories with a five-minute
  schedule, the median gap was 6.3 minutes and the longest 39 minutes, and
  about 4 in 10 slots did not run. A new test repository got no scheduled run
  for more than three hours. A bounded wake-up needs something outside the
  user repository: a webhook receiver, an operator's timer, or a fullsend
  platform timer. Fullsend runs none of these today, and putting them in the
  mint, the service that holds the App keys, was rejected in review.

This ADR aims to fix what fork PRs get after 2026-12-02 without building an
interim poll or a new hosted trigger that later decisions would replace.

## Options

| Option | What fork PRs get | Why not chosen |
|---|---|---|
| **A. `/fs-review` only, automatic review deferred** | A review when someone authorized comments | **Chosen** |
| B. A best-effort poll in the user repository on the Actions schedule | Automatic review with no bound on the wait | An interim poll driver and caller workflow ahead of entity-source discovery, for a wait GitHub does not bound |
| C. B, started by a webhook route and a timer in the mint | Automatic review within minutes | Adds an internet-facing route and timer to the service that holds every App key |
| D. B, started by a separate webhook service or a fullsend platform timer | Automatic review within minutes | No such service exists or is decided; too much to build and run before 2026-12-02 |
| E. Keep `pull_request_target`, or use `workflow_run`, for forks | Automatic review | Ruled out by ADR 0164 |

## Decision

We keep `/fs-review` as the only way to review a fork PR, and defer automatic
review of fork PRs until both parts can be decided on their own terms.

1. **Review path.** A fork PR is reviewed when someone authorized for
   `/fs-review` comments it, as ADR 0164 rule 3 defines: a person, not a bot,
   with triage access or higher, or an OWNERS approver or reviewer when that
   provider is on. A fork author with that access may comment on their own PR.
2. **Nothing new now.** Fullsend adds no fork PR poll, no poll caller
   workflow, no webhook route and no timer for fork PRs.
3. **When to revisit.** A new ADR restores automatic review once
   entity-source discovery can declare a fork PR source, and once a place
   outside the user repository to run a bounded wake-up is decided. Each can
   be its own ADR.

## Forge scope

- **GitHub:** applies.
- **GitLab:** unchanged. The GitLab poller keeps skipping pushes to fork merge
  requests because it cannot tell who pushed (see the
  [review agent](../agents/review.md)).
- **Forgejo:** unaffected; Forgejo has no fullsend dispatch today.

## Consequences

- From 2026-12-02, fork PRs get no automatic review or retro, including PRs from
  maintainers working in forks.
- Each push someone wants reviewed needs a `/fs-review` comment, and a missed
  comment lets a fork PR merge without a fullsend review.
- Fork authors with read access only depend on a maintainer to comment, as
  they did before ADR 0164.
- Fullsend's trust boundary and the mint stay unchanged.
- The later ADRs can build on entity-source discovery directly, with no
  interim driver to retire.
