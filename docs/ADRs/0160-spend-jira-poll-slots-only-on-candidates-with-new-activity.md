---
title: "160. Spend Jira poll slots only on candidates with new activity"
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

# 160. Spend Jira poll slots only on candidates with new activity

Date: 2026-11-06

## Status

Accepted

Refines the candidate selection step of
[ADR 0063](0063-polling-based-work-discovery.md). The lock protocol,
change detection, checkpoint rules, and authorization path of ADR 0063 and
[ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md)
are unchanged.

## Context

[ADR 0063](0063-polling-based-work-discovery.md) defines how the `jira-poll`
input driver chooses work each cycle: the configured JQL returns up to M
candidate issues (default 50), the driver drops the ones another poller has
locked, picks N of the rest at random (default 5), and processes only those.
The small N was chosen to reduce contention among concurrent pollers, the
random pick to spread load across them, and the ADR documented that issues
beyond the top M results can be starved. Its configuration sketch promised
per-driver overrides of M and N. As implemented today (verified at fullsend
commit `1ae05959c`, evidence in the pull request description), M and N are
fixed in the driver: no `fullsend poll` flag and no key in
`.fullsend/config.yaml` sets them. The pool is the query result as returned,
so an issue the driver has already handled, with nothing new since its
checkpoint, stays in the pool for as long as the query matches it. Each time
it is picked it spends a slot for nothing, because the driver learns there
is nothing to do only after locking the issue and listing its comments and
changelog.

A deployment preparing a gradual rollout of the code agent
(opendatahub-io/odh-dashboard, tracked as AISDLC-606) shows the cost. It
polls every 30 minutes with a query that matches issues carrying the
`ready-to-code` label or an `/fs-code` comment, updated within the last 24
hours. The agent's own status comments bump each issue's `updated` field, so
a handled issue lingers in the pool for a further day. The driver keeps no
memory between cycles, so each cycle is an independent draw:

| Unlocked candidates | Chance an issue is picked per cycle | Expected wait | One in twenty waits longer than |
|---|---|---|---|
| 5 or fewer | certain | next cycle | next cycle |
| 15 | 1 in 3 | about 90 minutes | about 4 hours |
| 30 | 1 in 6 | about 3 hours | about 8.5 hours |

Below five candidates the cap is invisible, which is why a small team can
go live one trigger at a time; labelling a batch of issues would hit it at
once. Meanwhile the managed poll workflow's GitHub Actions concurrency group
already serializes cycles for one repository
([guide](../guides/user/jira-integration.md#scheduled-workflow)), so in a
single-poller deployment the contention N was meant to reduce does not
exist and the cap only adds latency.

Whatever replaces this must keep what ADR 0063 bought: a bounded per-cycle
Jira API budget; safe overlap of several pollers on one project (several
repositories, or a manual run beside a scheduled one) with locks that have
no compare-and-swap; the stale-lock reclaim; the 24-hour clamp and the
untrusted-property stance on `lastCheck`, which anyone with Edit Issues can
rewrite ([guide security note](../guides/user/jira-integration.md#poll-coordination));
fail-closed authorization per
[ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md) and
ADR 0153, which selection must never bypass; and honest documentation of
starvation beyond the top M. Any selection input an issue editor can
influence is attacker-influenced
([security threat model](../problems/security-threat-model.md)).

This ADR aims to let a single poller serving an active project process every
new trigger within a bounded number of cycles, without weakening any of
those properties.

## Options

**A. Make M and N configurable, as ADR 0063 promised.** Raising N to the
pool size restores next-cycle latency. But a raised N raises the cost of
every cycle, quiet ones included: each selected issue costs the lock write,
verify and release round trips, a checkpoint read, and a full comment and
changelog listing, and N bounds the dispatch blast radius (N times the
per-issue event cap). Operators would have to guess a pool size that the
agent's own comments inflate. Necessary, not sufficient on its own.

**B. Spend slots only on candidates with new activity.** The search result
already carries each candidate's `updated` field. Reading the candidate's
checkpoint beside its lock in the filtering pass, and dropping candidates
whose `updated` is not after the checkpoint, turns the pool into the set of
issues that have something to do. It costs one more property read per
unlocked candidate (at most M per cycle) and nothing else; Jira's search
endpoint documents a parameter that returns issue property values with the
results, which would remove even that. The checkpoint is compared after the
clamps the driver already applies: a future-dated value is unset, a value
older than the backfill window is floored at the window start, and a
missing value is unset. All three make the issue look active, which is how
today's driver treats every candidate, so an editor can only restore the
status quo for one issue, never hide activity on it (hiding would need a
forward-dated checkpoint, which is already ignored). `updated` is maintained
by Jira; an editor can advance it, which is genuine activity, but not turn
it back.

**C. Prefer the most recently active candidates.** Gives the newest
triggers the best latency, but makes selection a deterministic function of
an editor-influenced field: an editor who keeps touching N issues holds all
N slots and starves the rest indefinitely, where random selection only lets
them dilute other issues' odds. It also discards the property random choice
was chosen for: concurrent pollers that sort identically would all try the
same N issues and race on locks that have no compare-and-swap. Oldest-first
resists the holding attack but aligns concurrent pollers in the same way.
Rejected.

**D. A rotation or cursor across cycles to reach issues beyond M.** Needs
state that survives one-shot processes. The Jira-side stores are issue
properties (editor-writable and per issue) and project properties (which
need Administer Projects, the permission ADR 0153 removed); the search API's
page tokens are opaque and short-lived; the target repository is not a
store the poller writes. With B in place the window matters only when more
than M matching issues are updated between two cycles. Deferred; a larger M
or a narrower query covers it.

**E. Keep the driver and work around it.** A shorter cron multiplies the
cost of quiet cycles. A narrower query cannot exclude handled issues,
because JQL cannot see entity properties. Rejected.

## Decision

We adopt an activity filter on the candidate pool, keep random selection
among the filtered candidates, and make N and M configurable within fixed
bounds, so that a single poller clears every pending trigger within a
bounded number of cycles while ADR 0063's coordination protocol and cost
bounds stay as they are.

1. **Pool.** The query result as today, at most M rows, minus locked
   issues, minus candidates with no new activity. A candidate is active
   when the `updated` value in its search result is after its checkpoint,
   read in the same pass as its lock and clamped exactly as the driver
   clamps it before change detection; an unset checkpoint is active. Locked
   candidates are not read, as today.
2. **Selection.** N candidates at random from the active set, as today.
   Random stays because it is the one order concurrent pollers cannot
   share and the one an issue editor cannot steer.
3. **Everything after selection is unchanged.** Lock acquisition with
   jitter and verification, the checkpoint re-read under the lock, change
   detection, bot filtering, authorization, dispatch, and checkpoint commit
   run as they do today. The filter dispatches nothing; it decides only
   which issues the existing pipeline looks at.
4. **Configuration.** `tracker.jira.poll.max_issues_per_cycle` sets N
   (default 5, from 1 to 50) and `tracker.jira.poll.max_candidates` sets M
   (default 50, from N to 500). They live in `.fullsend/config.yaml`, which
   has been the only source of Jira poll settings since the poll flags were
   removed ([#9077](https://github.com/fullsend-ai/fullsend/issues/9077)),
   and where a change goes through the repository's review. There is no
   flag. The bounds are initial values: 50 is one search page, so N never
   exceeds M, and at roughly eight property calls plus the comment and
   changelog listings per selected issue a full cycle stays within a few
   hundred calls; 500 is ten pages, beyond which per-candidate property
   reads dominate the budget. Revisit them when a deployment needs more.
5. **Defaults are unchanged.** A repository that sets nothing keeps M = 50
   and N = 5 and gains the filter.

## Forge scope

Applies to the `jira-poll` input driver on both of its targets: GitHub,
where the managed workflow consumes the dispatch file, and GitLab, where the
driver starts the agent pipelines directly. The GitLab-native cron poller
([ADR 0067](0067-gitlab-cron-polling-event-dispatch.md)) and webhook
fast-path ([ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md)) have
their own discovery and are unaffected. Forgejo has no Jira poll target.

## Consequences

- With A active candidates and no new arrivals, every one is processed
  within A divided by N cycles, rounded up, and the wait is spread evenly
  over those cycles: thirty fresh triggers at the defaults are all handled
  within six cycles, with a mean wait of three and a half, where today the
  mean is six cycles and one in twenty waits seventeen.
- Per-cycle Jira cost gains at most M property reads for idle unlocked
  candidates and loses about seven calls for every idle issue that would
  have been selected; the agent's own comment makes its issue active once
  more, costing one slot in a later cycle rather than a slot in every cycle
  for a day.
- Overlapping pollers see the same active set and still pick at random
  under the unchanged lock protocol; when the active set is N or smaller
  they all pick everything, exactly as today when the pool is small.
- A checkpoint an editor rewinds, deletes or forward-dates makes the issue
  active and nothing else, which is today's treatment of every candidate;
  a checkpoint write that fails after dispatch leaves the issue active and
  it is re-examined next cycle, as today.
- `internal/jirapoll` gains the filter in the lock-filtering pass and
  `internal/cli/poll.go` passes N and M from the two new config keys, which
  `internal/config` validates against the bounds; the implementation must
  confirm that entity property writes do not advance `updated` (otherwise
  the comparison needs the checkpoint's own write time) and that comment
  edits do, so edit detection keeps working.
- The [Jira integration guide](../guides/user/jira-integration.md) "Poll
  coordination" and "Two more scaling limits" sections, and the
  [config reference](../reference/config-reference.md), describe the active
  set, the two keys and their bounds, the revised per-cycle cost, and the
  narrowed top-M starvation case; deployments that keep the defaults change
  no configuration.
- Starvation beyond the top M stays documented and unmitigated by rotation.
