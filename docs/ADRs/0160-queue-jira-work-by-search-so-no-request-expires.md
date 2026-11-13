---
title: "160. Queue Jira work by search so that no request expires"
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
  - labels
---

# 160. Queue Jira work by search so that no request expires

Date: 2026-11-13

## Status

Accepted

The `jira-poll` driver finds work by searching for issues that carry a
request and no label saying it was handled, oldest first. This replaces the
candidate window, the random pick, and the per-issue checkpoint with its
24-hour floor.

Partially supersedes [ADR 0063](0063-polling-based-work-discovery.md) for
the `jira-poll` driver: its candidate selection (up to M results, N picked
at random) and its `lastCheck` change detection. Its poll command, driver
architecture, lock protocol and dispatch path remain current. The lock and
the dispatch stay scoped to one repository; the queue's labels are shared
by every repository that polls a project.

Extends
[ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md)
and [ADR 0157](0157-require-jira-authorization-entry-for-jira-poll.md): a
label that was not added by an edit in the project is authorized as a
command is, and removing an outcome label is checked against the person
who removed it (Decision item 6). The rest of the authorization path of
[ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md) is
unchanged.

Narrows what the `jira-poll` driver accepts from a trigger rule
([ADR 0061](0061-harness-cel-dispatch.md)): a rule the driver cannot read
stops the poll with an error, and a reaction to a Jira issue being closed
or reopened is not served (Decision item 1).

This file replaces an earlier ADR 0160, "Spend Jira poll slots only on
candidates with new activity"
([#9166](https://github.com/fullsend-ai/fullsend/pull/9166)), accepted on
2026-11-10 and never implemented. It tuned the selection that this decision
removes. Its text remains in the history of that pull request.

## Context

[ADR 0063](0063-polling-based-work-discovery.md) defines how the `jira-poll`
driver chooses work. The configured query returns up to M candidates
(default 50), most recently updated first by default. The driver picks N of
them at random (default 5), and for each one reads the comments and
changelog newer than a checkpoint it keeps in an issue property, rebuilds
them as events, and dispatches. A checkpoint older than 24 hours is
floored at 24 hours, and an issue seen for the first time yields events
from the last 24 hours only (verified at fullsend commit `1b3f16b3f`,
evidence in the pull request description).

That design assumes work arrives more slowly than agents take it. When it
arrives faster, for example a steady stream of vulnerability tickets, three
things happen:

- No request has a bounded wait, because each cycle is an independent
  random draw.
- A request outside the top M results is never drawn at all.
- A request not drawn within 24 hours is lost permanently while the poll
  run still succeeds, because the checkpoint floor moves past it.

A fourth gap is independent of load: a label that is already on an issue
when it is created produces no label event, so it does not start the
built-in code agent
([guide](../guides/user/jira-integration.md#labels-present-when-an-issue-is-created)).

The earlier ADR 0160 filtered the pool to candidates with new activity and
made N and M configurable. That shortens the wait while arrivals stay below
capacity. It keeps the window, the random draw and the 24-hour floor, so
under sustained load requests still wait without bound and still expire.

A replacement must reach every request eventually however long the backlog
grows, must expire nothing, must not cost more per cycle as history
accumulates, and must switch on without operator action for a repository
that uses the shipped trigger rules. It must also keep dispatch
fail-closed: every dispatch backed by a real event from a real actor,
authorized as today.

Jira constrains the shape. Its search can test labels and comment text and
can sort by creation time. It cannot search the issue properties the
driver writes or compare two fields, and its history operators cover
status, assignee and a few other fields, not labels, summary or
description. A record that an issue was handled is therefore visible to a
search only as a label.

[ADR 0098](0098-entity-first-harness-evaluation.md) decided that events are
a fast source of candidates and entity state is the authority on whether
work remains. Its execution path waits on contracts that do not exist yet
([#8418](https://github.com/fullsend-ai/fullsend/issues/8418)). This
decision takes the discovery half, state finds the candidates, and keeps
today's event-backed dispatch, so it depends on none of those contracts.

This ADR aims to make the `jira-poll` driver reach every waiting request
eventually, lose none of them to age, and switch on without operator
action.

## Options

**A. Tune the existing selection.** The earlier ADR 0160. Rejected for the
reason above: no bound and continued expiry under sustained load.

**B. Widen or page the window.** Scan further back, or make the 24-hour
cutoff configurable. Any cutoff still expires requests that are waiting,
removing it needs a stored start line, and the cost of a cycle grows with
the backlog. Rejected.

**C. Queue by search, with labels as the record of what was handled.**
The waiting set is a search result, so it needs no cursor and no start
line, and a handled issue leaves it. It keeps one 24-hour limit, on how
soon the driver must notice an event that a search cannot see (item 4); a
request that is waiting has none. Oldest first is an order every poller
shares, which the random pick avoided: the managed GitHub workflow runs
one cycle of a repository at a time, and a cycle that cannot take an
issue's lock skips it and continues, so the cost of an overlap is at most
a repeated hand-off. Chosen.

**D. Wait for entity-first execution.** The right long-term home
([ADR 0098](0098-entity-first-harness-evaluation.md)), and not available in
time. C moves toward it and can hand its per-agent terms to harness entity
filters when those exist.

**E. Serve requests only, and drop automatic triggers on Jira.** One
mechanism and no staging, but triage would stop running when a person opens
or edits an issue unless each team added labelling inside Jira. Rejected as
a regression.

**F. Configure each agent's labels and commands, or build them into
fullsend.** Per-agent fields do not belong in `config.yaml`
([ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md)), and names built
into fullsend would duplicate the agents' trigger rules and go stale when a
repository changes one. Rejected in favour of reading the trigger rule.

## Decision

We adopt a queue found by search for the `jira-poll` driver. A request on
an issue with no outcome label is found at any age and reached in order of
issue age. Automatic triggers and repeated requests are noticed within 24
hours and then wait without expiring.

1. **What starts an agent is read from its trigger rule.** For each enabled
   agent the driver takes the trigger rule it already evaluates
   ([ADR 0061](0061-harness-cel-dispatch.md)), keeps the parts that can be
   true for a Jira issue, and reads from them the labels whose addition
   starts the agent, the commands that start it, and whether it starts when
   a person opens or edits an issue. Nothing is configured, so a repository
   that changes a trigger changes the queue with it. A rule the driver
   cannot read this way stops the cycle with an error naming the agent and
   the part. That covers a label or command matched by pattern or by
   exclusion, a label event that does not require the label to be added, a
   comment event with no fixed command, and a reaction to an issue being
   closed or reopened, which the queue does not serve yet. `fullsend lint`
   reports the same error from the same code. Doubt is an error, so no part
   of a rule is skipped silently.
2. **Three labels per agent record its state on an issue.** They are
   `ai-<agent>-agent`, the hand-off label, `ai-<agent>-queued` and
   `ai-<agent>-declined`, built from the agent's name: for the code agent,
   `ai-code-agent`, `ai-code-queued` and `ai-code-declined`. The names
   describe a role, not a product, and are not configurable in this
   version.
3. **Waiting is a search condition.** An issue is waiting for an agent when
   it matches the repository's poll query and either carries the agent's
   queued label, or carries a request (one of the agent's labels, or a
   comment containing one of its commands) and neither the agent's
   hand-off label nor its declined label. Age is not part of the
   condition.
4. **What a search cannot see is staged as a label.** A search cannot tell
   that a person opened or edited an issue, or that a new request arrived
   on an issue that already carries an outcome label. Before searching, the
   driver lists the issues in the poll query that changed in the last 24
   hours, reads their summary, description and label history in bulk and,
   where an issue carries an agent's hand-off or declined label, its
   comments.
   It adds the agent's queued label when, since the driver last recorded an
   outcome for that agent on the issue, a person opened or edited the issue
   (for an agent that starts on open or edit), posted or edited a comment
   with one of the agent's commands, or added one of its labels again. For
   an automatic trigger the driver's own removal of the queued label, read
   from the label history, counts as a recorded outcome, so an open or
   edit that was examined and not dispatched is not staged again. The
   event must itself fall inside the 24 hours, and an event that lands
   while a cycle is working on the issue must be staged by a later cycle: a
   repeated hand-off is acceptable there and a lost event is not. Staging
   hands nothing to an agent, so load cannot delay it. The 24 hours is
   today's backfill window, kept so that an upgrade or a first install
   stages what the current driver would have processed. One cycle reads at
   most the 500 most recently changed issues in this pass and logs a
   warning at the cap, so it misses an event only when more than 500
   issues change between two cycles. The cap is an initial value to revisit
   with the first deployment that reaches it.
5. **Two lines, each oldest first.** Each cycle searches for two sets of
   waiting issues, both inside the repository's query with its sort clause
   removed and both ordered by creation time ascending. The first line is
   issues with no outcome label for the agent they wait for: first requests
   and newly opened issues. The second line is issues that carry an outcome
   label and are queued again: repeated requests and edits. Jira's search
   cannot select by permission, so the driver then asks Jira in bulk which
   of those issues its account may edit and ignores the rest: an issue it
   cannot label can be neither handed off nor declined. It asks per issue,
   because a project can grant edit rights issue by issue and a
   project-level answer then says yes where the issue says no. The driver
   hands N issues to agents per cycle. One slot in every five, rounded
   down and at least one when N is two or more, is reserved for the second
   line; with N of one, a cycle serves the second line first one time in
   five, chosen at random. A slot one line does not use goes to the other.
   The ratio is an initial value to revisit with the first deployments. An
   issue that is examined and not
   handed off delays the ones behind it within the cycle and does not use a
   slot. A cycle stops examining after five minutes, hands off what it has
   and logs a warning; the next cycle searches again and continues with
   what is left. Issues the account cannot edit are dropped before
   examination and use none of that time. An issue that is still waiting
   after it was examined, because its lock was held or a read failed in a
   way a retry may fix, is skipped for the rest of the cycle and logged. An
   issue returns to the
   first line only when someone removes an outcome label, which item 6
   authorizes, so an edit or a repeated request on an old issue cannot move
   ahead of a first request on a newer one.
6. **Pickup rebuilds the real events.** Under the existing lock the driver
   reads the issue's labels, comments and history directly, skips an issue
   whose labels no longer make it wait (Jira's search can lag behind a
   label the driver has just written), and builds, for each agent the
   issue is waiting for, every request the driver has not yet recorded an
   outcome for, each with the actor from Jira's record: who added the
   label; who wrote or last edited the command comment; who opened or
   edited the issue. The actor is always the author of the content the
   event carries. A label that was already on the issue when it was created
   is a request by the issue's creator, the Jira field that nobody can
   edit, and is authorized through the lists or project roles as a command
   is, never through the Edit Issues rule for labels
   ([ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md)),
   because creating an issue does not require Edit Issues. The same holds
   for a queued label present at creation and for a label added before the
   issue moved into the project. A queued label added by anyone but the
   driver's own account is that person's label request. Removing a
   hand-off or declined label makes the issue's earlier requests wait
   again; the driver serves them only when the person who removed the
   label would be allowed to make the same request, and each still runs as
   its own author. A label change made by the driver's own account is
   never a request and never makes earlier requests wait again. Every
   event passes bot filtering, authorization and the trigger rule as today.
   The dispatch gate applies these rules, not the driver
   ([ADR 0063](0063-polling-based-work-discovery.md) keeps authorization
   out of input drivers): the driver marks an event whose label was not
   added by an edit in the project, the gate applies the Edit Issues rule
   only to an unmarked label event, and the person who removed a label is
   put to the same gate. The driver dispatches the newest event that
   passes, at most one per agent per pickup. The search finds candidates
   and authorizes nothing.
7. **The outcome is written as a label.** When a dispatch is written the
   driver adds the agent's hand-off label and removes its queued label, at the
   point where the checkpoint is committed today, so a failed write repeats
   a hand-off and fails the cycle visibly and never loses a request. Every
   other outcome is written as soon as its issue has been examined, so a
   cycle that is cut short still shortens the queue. An
   issue the driver cannot label is never handed to an agent: the bulk
   check enforces that for a missing permission, and the implementation
   must show it holds where a workflow blocks edits. When none of an
   agent's requests on the issue can be dispatched (the text is not a
   command, the actor is a bot or lacks permission, the rule is false, or
   the issue cannot be read, or its event validated or evaluated, for a
   reason a retry will not fix) the driver adds the declined label, and
   comments with the reason only when it refused a request from a person.
   A cycle in which every event fails validation or evaluation fails as it
   does today and declines nothing, because that points to the
   configuration and not to the requests. An automatic trigger that is not
   dispatched only loses its queued label. An outcome label records what
   happened to the requests seen so far and does not close the issue to
   later ones: a new command or a label added again is staged as item 4
   describes.
8. **The agent's own comment is the receipt.** A request that is followed
   on the issue by a result or run-status comment from that agent has been
   acted on: the driver records the hand-off label and does not dispatch. This
   is what lets the queue switch on by default, with no setting and, for a
   repository whose trigger rules the driver can read, no operator action.
   On the first cycles every old request looks unhandled,
   because the labels are new; the ones an agent answered are recognized
   and skipped, and any other runs, however old, because a label or command
   that was never acted on is still a request. The old checkpoint is
   neither read nor written. The rule stays in force afterwards, so a
   hand-off whose label write failed is not repeated once the agent has
   answered.
9. **One per-cycle setting.** `tracker.jira.poll.max_issues_per_cycle`
   sets N, the issues handed to agents (default 5, from 1 to 50). It lives
   in `.fullsend/config.yaml`, the only source of Jira poll settings since
   [#9077](https://github.com/fullsend-ai/fullsend/issues/9077). The
   five-minute limit on examining is fixed and is not a setting. It fits
   the managed poll job, which runs every five minutes and is stopped after
   15 by default, and it bounds the load one cycle puts on Jira. An
   examined issue costs about seven calls, five of them the lock protocol
   with its pause of 0.5 to 1.5 seconds, plus a label write and at most one
   comment when it is declined. The staging pass adds its bulk reads and
   one comment listing for each recently changed issue that carries an
   outcome label. Both values are initial ones to revisit with
   measurements from the first deployments.
10. **A cycle can be previewed.** `fullsend poll` gains a preview option
    that reports what a cycle would stage, hand off and decline, and why,
    and writes nothing to Jira and dispatches nothing. It replaces the
    guide's dry-run tip, which today still advances checkpoints.

## Forge scope

Applies to the `jira-poll` input driver on both of its targets: GitHub,
where the managed workflow consumes the dispatch file, and GitLab, where
the driver starts the agent pipelines directly. The GitLab-native
cron poller ([ADR 0067](0067-gitlab-cron-polling-event-dispatch.md)) and
webhook fast-path
([ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md)) have their own
discovery and are unaffected. Forgejo has no Jira poll target.

## Consequences

- Under sustained load the backlog grows and every first request is still
  reached, in creation order, because nothing enters the first line ahead
  of it: it waits for the issues ahead of it divided by its share of N per
  cycle. A repeated request or an edit waits in the second line, which has
  a reserved share and no bound while such events on older issues arrive
  faster than that share.
- The poller writes labels on Jira issues by default, where today it
  writes none and the runner's `tracker.jira.run_label` is opt-in. The
  queue is visible in Jira as a result: each label can be searched and
  shown on a board. To ask again a person repeats the command or adds the
  label again, as today, or removes the outcome label. The hand-off label
  records that a dispatch was written, not that a run started: a request
  whose dispatch fails downstream is recovered by asking again, as today.
- A new agent needs no change to the driver. The `cve` agent proposed in
  [#9309](https://github.com/fullsend-ai/fullsend/pull/9309), for example,
  would be queued on the label and the command its trigger rule names, and
  its hand-offs recorded as `ai-cve-agent`. A request whose actor is an
  automation account is still declined, as such events are ignored today;
  [#9389](https://github.com/fullsend-ai/fullsend/issues/9389) proposes
  letting a label added by a bot start an agent.
- The comment search matches text, so an issue that only mentions a command
  is examined once and gets the declined label. A project that discusses
  fullsend itself sees the most of this.
- Labels are shared by every repository that polls a project. An issue
  usually describes a change to one codebase, so repositories that share a
  project select their own issues, for example by component or by a field
  naming the repository, as the
  [guide](../guides/user/jira-integration.md#multi-repo-polling) already
  recommends. An issue that two repositories both match is handled by
  whichever reaches it first, or by both when their cycles overlap, since
  the lock is per repository. Today each handles it independently, which
  the guide describes as intentional, so that section is rewritten.
  Some deployment may want every repository to act on one issue, for
  example a project of product-wide mandates; that is out of scope until
  one asks for it. The likely form is an optional suffix on the label
  names, set once per repository, so that each repository reads and writes
  only its own labels, as the lock and checkpoint properties are named per
  repository today. A suffix the repository chooses, not one derived from
  its name, keeps the labels valid when the repository is renamed.
- Anyone with Edit Issues can write these labels, as they can write the
  checkpoint today. Adding a hand-off or declined label hides the requests made
  so far, visibly and reversibly, and not a later one. Removing one makes
  those requests wait again; they run only if the remover could make them,
  and as their own authors. The trust requirement on Edit Issues is
  unchanged. An agent's result writer must refuse to add or remove any
  agent's three labels, as the triage writer refuses the labels that steer
  triage today, so that an agent acting on issue text cannot hide a
  request. That change is in the agents repository and the queue does not
  wait for it: until it lands, such an agent can hide or delay requests on
  the one issue it runs on, and can start nothing.
- The queue ignores every issue the poller's account cannot edit, and logs
  how many each cycle. A poller that can read a project and not edit it
  finds requests and acts on none of them, which the guide must state
  beside the required permissions.
- A poll query with a rolling time limit, such as "updated in the last
  day", brings expiry back for that repository, because the queue applies
  the query as written. Nothing rejects such a clause, since a repository
  may want one; the default query has none, and the guide must say what it
  does and tell teams to remove one left over from the old driver. A fixed
  start date on `created` expires nothing newer than it, and is the way to
  keep older requests out of the queue.
- `tracker.jira.run_label` keeps its meaning: one label the runner adds on
  every agent's run. A value equal to one of an enabled agent's three
  labels is refused with an error, because the runner would then mark
  requests that agent never handled.
- A request on an issue with no outcome label never expires. Automatic
  triggers and repeated requests are staged within 24 hours, so a poller
  outage longer than that loses the ones from the gap, where today any
  trigger is lost when it is not drawn within 24 hours. Repeating the
  request recovers any of them.
- An upgrade or a first install runs every label and command in the poll
  query that no agent has answered and that passes authorization, oldest
  first and N per cycle. A start date in the query limits that, and the
  preview shows it beforehand. Two smaller effects follow an upgrade once.
  A request that an agent acted on before agents wrote to the Jira issue
  ([agents#1843](https://github.com/fullsend-ai/agents/pull/1843)) has no
  comment to recognize it by and runs once more. An old command from a
  person who was not allowed to give it, which the old driver ignored
  silently, is refused with a comment however old it is.
- An upgrade stops the poller, with an error, for a repository whose
  trigger rule the driver cannot read or which reacts to closed or reopened
  Jira issues. No shipped agent is affected. One cycle dispatches at most N
  issues times the number of agents.
- Going back to the previous release works. The old driver ignores the
  labels and resumes from checkpoints that have not moved since the
  upgrade, so it applies its 24-hour floor: it skips older activity and may
  repeat hand-offs from the last day. The labels stay on the issues and do
  nothing. A repository that set `max_issues_per_cycle` removes it first,
  because the previous release rejects keys it does not know under
  `tracker.jira.poll`.
- The rule reader is one package used by `fullsend lint` and
  `fullsend poll`. `internal/jirapoll` loses the random pick, the
  checkpoint and the backfill window; the Jira client gains label removal,
  the bulk history read and the bulk permission check. The
  [Jira integration guide](../guides/user/jira-integration.md), the
  [config reference](../reference/config-reference.md), the normative
  [`jira-poll` adapter](../normative/normalized-event/v1/jira-poll-adapter.md)
  and the CLI and trigger references are updated to match.
