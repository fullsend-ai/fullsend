---
title: "85. Serve every changed Jira issue with one poller per repository"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
  - security-threat-model
topics:
  - polling
  - jira
  - dispatch
  - scheduling
  - configuration
---

# 85. Serve every changed Jira issue with one poller per repository

Date: 2026-11-19

## Status

Accepted

Partially supersedes
[ADR 0160](0160-spend-jira-poll-slots-only-on-candidates-with-new-activity.md):
the row limit on the candidate pool, the random pick, the use of the per-issue
lock, and both configuration keys are replaced. Its activity filter and its
checkpoint clause remain.

Partially supersedes [ADR 0063](0063-polling-based-work-discovery.md) for the
`jira-poll` driver: the row limit on the candidate pool, the random pick, and
the per-issue lock are replaced, together with what depends on the lock (the
lock held during agent runs, the lock settings, the lock handling of
`fullsend poll cancel`, and the expectation of several concurrent pollers).
Its change detection and its authorization path remain.

## Context

[ADR 0063](0063-polling-based-work-discovery.md) has the `jira-poll` driver
read the first M results of the configured query (50), drop the issues another
poller has locked, and process N of the rest picked at random (5).
[ADR 0160](0160-spend-jira-poll-slots-only-on-candidates-with-new-activity.md)
added a filter that skips issues with nothing new and kept the rest; it is not
implemented yet. Under sustained load this loses requests. An issue outside
the first M results is never examined, an issue inside them can lose the draw
cycle after cycle, and a request that is not processed within the driver's
24-hour backfill window is dropped (see
[Poll coordination](../guides/user/jira-integration.md#poll-coordination)).
ADR 0160 records starvation beyond the first M results as unmitigated.

The lock and the random pick both exist so that several pollers can work the
same issues at once. The managed poll jobs do not do that: on GitHub
([Scheduled workflow](../guides/user/jira-integration.md#scheduled-workflow))
and on GitLab
([GitLab runtime](../guides/user/jira-integration.md#gitlab-runtime))
they run one cycle at a time per repository. The lock and the checkpoint are
also named per target repository, so repositories that share a Jira project
never coordinated through the lock
([Multi-repo polling](../guides/user/jira-integration.md#multi-repo-polling)).
ADR 0160 rejected an oldest-first order only because pollers that sort alike
would race on those locks.

Two limits of the GitHub hand-off have to survive the change. The managed
workflow passes the whole batch to the dispatch step as one job output and
runs each dispatch record as one job of a job matrix. GitHub allows at most
[256 jobs in a matrix](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstrategymatrix)
and
[1 MB in a job's outputs](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idoutputs),
and the driver commits an issue's checkpoint before the dispatch step runs, so
a batch over either limit is lost whole. Nothing enforces either limit today;
five issues per cycle only makes a large batch rare.

[ADR 0098](0098-entity-first-harness-evaluation.md) and
[ADR 0149](0149-coalesce-entity-source-discovery-queries.md) set the durable
direction for discovery, and no Jira adapter for it exists yet, so the
`jira-poll` driver stays in service. This ADR aims to stop that driver from
losing requests to its row limit and its random pick, with one change that
leaves change detection and authorization alone.

## Decision

We run one `jira-poll` poller per repository and serve every issue that
changed within the backfill window, oldest change first by default, to
remove the row limit, the random pick, and the lock.

1. **One poller per repository.** The driver neither reads nor writes the lock
   property, and it ignores lock properties left by earlier versions. What
   ADR 0063 builds on the lock is withdrawn for this driver along with it:
   the lock held during agent runs, the lock settings, and the lock handling
   of `fullsend poll cancel`. The managed GitHub workflow and the managed
   GitLab schedule already run one cycle at a time per repository. A
   scheduler other than the managed ones must itself keep two cycles for one
   repository from overlapping; overlapping cycles dispatch the same requests
   twice. Before it reads an issue's comments and history, the driver
   confirms that it can write to the issue, as the lock write did; an issue
   that fails yields no records, and the driver warns and carries on with the
   other issues. A cycle in which every issue fails this check ends in an
   error, as today, so a credential that cannot write shows as a failed run.
   How the driver confirms it is left to the implementation.
2. **Pool.** The issues that match the configured query and whose `updated`
   value falls within the driver's backfill window, read in the search's
   sort order, with no row limit. The window is the one the driver already
   uses to bound a first poll and to clamp an old checkpoint. Its length is
   fixed at 24 hours today and becomes a setting,
   `tracker.jira.poll.backfill_window_hours` (default 24, a whole number of
   hours, at least 1, with no upper limit). The search is sorted oldest
   change first unless the configured query carries its own sort clause,
   which the driver keeps, as it does today. The default query sorts newest
   first today; it no longer carries a sort clause, so it is served oldest
   change first. When the configured query sorts newest change first
   (`ORDER BY updated DESC`), as the documented examples do today, the driver
   warns once per cycle that oldest change first is recommended; any other
   order draws no warning. A candidate is active as ADR 0160 defines it, with
   the checkpoint returned by the search itself. That value is used only to
   skip idle issues; processing still reads the checkpoint from the issue, as
   today.
3. **Selection.** The driver processes active issues in that order until the
   cycle holds `tracker.jira.poll.max_dispatches_per_cycle` dispatch records
   (default 100, from 1 to 250), until on the GitHub target the next issue's
   records would take the serialized batch past 768 KB, or until five minutes
   have passed since the cycle started. The five minutes include the search
   and the per-issue reads: the driver checks the clock between Jira calls,
   and the rest of the job's time is left for committing checkpoints and
   dispatching. Reading one issue is limited to two minutes. An issue that
   takes longer is treated as a failed read is today: the driver warns,
   naming the issue, takes none of its records, leaves its checkpoint where
   it was, and goes on to the next issue. All of an issue's records go out
   in the same cycle, so an issue that does not fit in the room left waits
   for the next cycle; the driver does not skip ahead to a
   smaller issue. An issue is always taken when the cycle holds no records
   yet, so a cycle can exceed the configured limit by that one issue. One
   issue contributes at most 250 records and, on the GitHub target, 768 KB:
   the driver keeps the oldest records, drops the rest with a warning, and
   advances the checkpoint.
   Today's cap of 100 events per issue, which also drops its overflow and
   advances the checkpoint, is unchanged. At any of these limits the driver
   stops reading, leaves an issue it has not finished for the next cycle,
   dispatches what it has completed, commits those checkpoints, and exits
   normally.
4. **Bounds.** 250 is GitHub's 256-job matrix limit less a small margin; it
   bounds a cycle and a single issue, and it applies on both targets. The
   default is 100 because the limit sets how fast a backlog drains, and a
   request that waits past the window is lost; it is twenty times today's
   five issues per cycle when each issue yields one record.
   768 KB is three quarters of GitHub's 1 MB limit on a job's outputs,
   measured as GitHub measures it; the margin is there because GitHub
   describes its measure as approximate. It applies on the GitHub target only,
   since GitLab starts each pipeline separately and has no batch output. Five
   minutes is the managed GitHub schedule's interval and leaves ten minutes of
   that poll job's default 15-minute timeout. Two minutes per issue is many
   times what an ordinary issue takes, and it leaves room for an issue with
   the longest comment and history listings the driver reads. The default,
   the size limit, and the two time limits are initial values; revisit them
   when a deployment needs more. The window has no upper limit because no
   one length suits every project: what a project can afford depends on how
   many of its issues change, so the operator chooses.
   `max_dispatches_per_cycle` replaces both keys of ADR 0160, neither of
   which shipped.
5. **Unchanged.** Change detection, bot filtering, authorization, the
   window's clamps and ADR 0160's one log line per cycle naming the clamped
   candidates, the checkpoint commit with ADR 0160's `updated` clause,
   and on GitLab the record-by-record dispatch with its retry of an issue
   whose dispatch failed.

## Forge scope

Applies to the `jira-poll` input driver on both of its targets: GitHub, where
the managed workflow turns the dispatch file into a job matrix, and GitLab,
where the driver starts the agent pipelines itself. The 250 limit comes from
GitHub and is applied on GitLab too, so that there is one rule. The size limit
applies on GitHub only. The GitLab-native cron poller
([ADR 0067](0067-gitlab-cron-polling-event-dispatch.md)) and webhook fast path
([ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md)) are unaffected.
Forgejo has no Jira poll target.

## Consequences

- Every issue that changed within the window is eligible each cycle, in
  order, so none is skipped because of its position in the results or by
  chance.
- Requests are still lost when issues change faster than the driver can
  process them within the window, when the poller is stopped for longer than
  the window, or when one issue produces more than 250 dispatch records, or on
  GitHub more than 768 KB of them, in one cycle; removing the window is left
  to the entity-based design.
- On the GitHub target a batch is still lost whole when GitHub discards the
  job output, which it does when the output contains one of the poll job's
  secret values, or when the dispatch step fails after checkpoints are
  committed. A checkpoint commit that fails after the write check passed
  fails the poll step, and the issues whose commits succeeded lose their
  requests. All three exist today, and a larger batch raises what is lost;
  changing the hand-over is left to a separate change.
- An issue the driver cannot write to, or cannot read within two minutes, is
  tried again every cycle until its change leaves the window. Its requests
  are not served. It takes no record room from other issues, and a slow one
  costs up to two minutes of each cycle. Two or three slow issues at the
  front of the line can use up a whole cycle, and the issues behind them are
  then not reached and can lose their requests; this ADR accepts that and
  adds no way to set a slow issue aside.
- A configured query that sets another order is served in that order. Under
  a sustained backlog, an order other than oldest change first can leave
  older requests waiting until they leave the window, so the guide and the
  config reference recommend oldest change first, say what another order
  costs, and drop the newest-first sort clause from their example queries.
- A longer window lets requests survive a longer outage, at two costs. A
  missing or rewound checkpoint replays requests as old as the window. A
  window that holds more issues than a cycle can list makes the driver fall
  behind without failing, so recent requests are not served and the run still
  looks healthy. The guide and the config reference say how to choose the
  length and what a long one costs.
- Under oldest change first, any change to a waiting issue, including a
  comment from anyone allowed to comment, moves it to the back of the line;
  under a sustained backlog that can cost the issue its request.
- A rewound checkpoint can replay old requests from every issue that changed
  within the window, where today it reaches five issues per cycle; the
  per-cycle limit bounds the runs that starts. A rewind does not move an
  issue in the line, so an editor who keeps rewinding issues near the front
  can hold the requests behind them back. The random pick did not allow
  that; this ADR accepts it and extends the
  [Jira guide](../guides/user/jira-integration.md)'s rule that Edit Issues on
  a polled project is part of the trust boundary.
- Each cycle reads the search rows of every issue that changed within the
  window, including those already handled, and no longer makes the lock calls
  or the per-issue pause. The number of Jira calls in a cycle is now bounded
  by the time limit, not by a count.
- The implementation must show that a batch at the record limit and a batch at
  the size limit, built from the largest records the driver can produce, are
  each dispatched whole, that an issue larger than the configured limit is
  dispatched alone and does not hold back the issues behind it, and that a
  cycle stopped by the time limit leaves undispatched requests eligible for
  the next one; the [Jira guide](../guides/user/jira-integration.md), the
  [config reference](../reference/config-reference.md), and the
  [custom poller example](../guides/user/custom-poller-example.md) change
  with it.
- If entity-based discovery
  ([ADR 0098](0098-entity-first-harness-evaluation.md),
  [ADR 0149](0149-coalesce-entity-source-discovery-queries.md)) fully
  replaces the `jira-poll` driver, this change is retired with it. Until then
  it is needed, because the driver loses requests today.
