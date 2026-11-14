---
title: "117. Steer a running cycle with admitted entity updates"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
  - flapping-convergence
topics:
  - concurrency
  - entities
  - authorization
  - data-security
  - entity-context-snapshot
---

# 117. Steer a running cycle with admitted entity updates

Date: 2026-11-02

## Status

Accepted

Partially supersedes two sentences of
[ADR 0141](0141-deterministic-filtered-entity-context-staging.md), for the
steer path only. Every other ADR 0141 rule stays current.

- "An explicitly configured host-side extension may fetch a kind outside the
  snapshot": the host-side steer path may also fetch newer revisions of the
  snapshot's own kinds for the bound entity during a cycle, through the same
  pipeline and fail-closed rules, exposing no raw payload. Sandbox forge reads
  stay denied.
- "requires a versioned schema that lists its path in the manifest": "the
  manifest" there is the snapshot's `index.json`. Steer output is listed
  instead in its own manifest,
  [steer envelope v1](../normative/steer-envelope/v1/README.md), under the same
  filtering, digest, permissions, exact-path, and cleanup rules.

Reason: a steer carries newer revisions of what the snapshot holds, and the
snapshot, whose digest is the cycle's admitted input, cannot change mid-cycle.

This is the first of five steer decisions. The later ones build on it:

- ADR 0118: runtime delivery.
- ADR 0119: GitHub Actions signal.
- ADR 0120: receipts for absorbed steers.
- ADR 0121: on by default.

## Context

[ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
coalesces later events into one pending follow-up, and
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md) runs it as a new
cycle in the same `fullsend run` process. Neither reaches a running sandbox,
so a turn that started on old state posts output that the entity has already
moved past, such as a review of a replaced head
([#1207](https://github.com/fullsend-ai/fullsend/issues/1207)), and a person
who adds direction mid-run waits for the next cycle. On GitHub Actions,
event-only harnesses get no wake listener from ADR 0144, so their follow-up
starts only after the current job ends.

Any answer keeps per-record admission with no data-only lane
([ADR 0133](0133-actor-attributed-data-security-boundary.md)) and wakes that
never consume pending work (ADR 0144).

This ADR aims to let a running sandbox cycle take in admitted entity updates,
including pushes, without weakening those rules.

## Options

| Option | Why not chosen |
|---|---|
| Rely on ADR 0144 cycles alone | The stale turn still finishes and may post; nobody can redirect work in flight |
| Ask the agent to re-check the entity at turn end | Advisory, reads the forge from the sandbox (ADR 0141 denies it), and still pays for the stale turn |
| **Steer the running cycle** (chosen) | — see Decision |

## Decision

We adopt steering, the delivery of freshly admitted, filtered entity records
and a moved change-proposal head into a running sandbox cycle, to let the
agent's next turn work from the entity's current state.

Steering is an optional fast path inside one ADR 0144 cycle; the next cycle
stays the correctness backstop.

### Place in the cycle

- A steer is one delivery into the current sandbox, not a cycle.
- A steer never takes, fills, consumes, or replaces the newest-pending slot or
  the pending platform execution. The prompting notification still schedules
  the follow-up cycle, which runs CEL and the pre-script as usual.
- A steer refused before anything is published, in steps 1 to 4a or by a
  failing release check, leaves the cycle as if no steer was attempted and
  releases its sequence number.
- Once its files or envelope are published, a steer whose acknowledgement
  never arrives has an unknown outcome. It keeps its sequence number and its
  entry in the manifest chain. It is recorded as possibly delivered, is never
  counted as absorbed, and Fullsend never claims the model did not see it.
  The cycle takes no further steers, so a later steer never builds on records
  the agent may not hold. The next cycle is the backstop.
- A lost runtime session fails the cycle.
- The cycle's platform-verified result revision (ADR 0144) starts at the
  revision of the cycle's admitted input. It can move to a steered head only
  under the rules in [Result revision](#result-revision).

### When a steer is attempted

While a cycle's sandbox runs, a notification for its serialization key (an
ADR 0144 event or wake) prompts a steer attempt, unless the running harness's
own agent identity caused it; updates from other registered agents still
prompt an attempt. That filter is routing, not admission.

A cycle runs at most one steer attempt at a time, from step 1 to step 6. The
sequence number is assigned and the cap is checked in step 1, inside that
serialization, so every candidate is prepared against the latest published
manifest. A notification that arrives during an attempt starts a new attempt
after the current one ends. Refused attempts count toward a per-cycle attempt
bound, so activity that is never admitted cannot keep the host busy. Each
attempt runs on the host, in order:

| Step | What happens | If it fails |
|---|---|---|
| 1. Eligibility | Steering is enabled for the harness, the entity is a forge entity, the runtime can take a message into the running session, and the cycle is under its steer cap with time left for a full turn | No steer |
| 2. Still selected | Re-resolve the entity, check [ADR 0149](0149-coalesce-entity-source-discovery-queries.md) source membership for entity-source harnesses, and evaluate harness CEL (see below) | False or unknown: no steer |
| 3. Acquire | Fetch records new, changed, or deleted since the snapshot and earlier steers; for a change proposal whose head moved, fetch the new head into host-private state (see [Head provisioning](#head-provisioning)) | Fetch error, or nothing new or changed: no steer |
| 4. Filter and admit | ADR 0141 pipeline on records, the context-file scan on a moved head, then the admission rule below | Dropped records are counted, never delivered. No steer if: a moved head fails attribution, admission, or the scan; a record the agent already holds was deleted, or its newer revision fails admission or filtering; or no record and no head move is admitted. A deletion is left to the follow-up cycle, so a withdrawn instruction never stays beside its replacement |
| 4a. Pre-script gate | For a harness with a pre-script, run it on the host under the [ADR 0072](0072-pre-script-output-protocol.md) protocol with the steer input below, only if the harness declares that its pre-script supports the steer gate | A pre-script without that declaration: no steer, and it is not run. A skip, an error, a timeout, or no `steer_checked=true` in its output: no steer; the updates wait for the follow-up cycle |
| 5. Release | Under the sandbox write lock: run the release check per record and for a moved head's actor, write the steer files, install a moved head, and append the envelope; then, with the lock released, wait for the acknowledgement | Any record, the head actor, or the head install failing declines the whole steer before the envelope is published, so the delivered set is always the set the pre-script gate saw; the updates wait for the follow-up cycle |
| 6. Record | Add a child event to the cycle's span. Steers count toward the ADR 0106 and ADR 0144 circuit breakers and do not change run status | — |

**Steer input for the pre-script.** In a steer-gate run the host sets:

- `FULLSEND_CONTEXT_DIR` to the cycle's snapshot, as usual;
- `FULLSEND_STEER_DIR` to a host-side, read-only, digest-verified copy of
  every steer directory of this cycle so far plus the candidate steer, in
  steer order.

Later revisions of a record replace earlier ones by record key, so the
pre-script sees the cumulative state the agent will hold. A pre-script that
reads this input signals it by writing `steer_checked=true`, a new key in the
pre-script output protocol added under its versioning rules. A pre-script that
does not write it never lets a steer through. A pre-script written before
steering is never run as a gate at all: the harness must first declare that
its pre-script supports the steer gate. A steer-gate run is a check only. It
gets no forge write credential and must make no forge or entity writes;
Fullsend relays none of its outputs and posts no status for it. After it
exits, the host re-verifies the candidate's manifest and file digests against
host-private state before release.

**Still selected.** CEL runs over the current entity with the cycle's retained
prompting event (`null` for an eventless cycle). The event is reused only so
an event-only harness, whose CEL needs one, stays eligible; it never grants
authorization. The event's entity and state fields are refreshed from the
re-resolved entity, so a closed entity or a removed label is seen as it is
now. Independently of CEL, the re-resolved entity must still be open, and for
an event that a label addition triggered, that label must still be present;
otherwise no steer. In the same step the event is revalidated fresh through
its own admission path, and a failure means no steer:

- an ordinary human actor must meet the harness's effective `human_min_role`
  ([ADR 0151](0151-harness-human-role-minimums.md)), or the Authorization v1
  threshold on legacy non-CEL routes;
- a registered bot must pass the
  [ADR 0107](0107-bot-identity-resolution-for-dispatch-authorization.md)
  `bot_role` gate;
- an exact verified label addition uses Authorization v2's label-transition
  grant, and passes only while the label is still present: for a human it
  satisfies only a `triage` minimum, and a provider-classified bot uses its
  own transition grant without a `bot_role`.

A bot or a label grant is never declined only because `actor.role` is `none`.
This is not a new ADR 0144 check: it runs inside the cycle that consumed the
event and can only decline, so ADR 0144's rule that later checks "do not
reuse" the event is unchanged.

### Admission

A steer release is a "Fullsend-controlled capability invoked after sandbox
initialization", so each release runs the contract's check on each record's
current-revision actor and, when a change proposal's head moved, on the
forge-authenticated actor who moved the head to that revision. That check
"MUST resolve authorization immediately before releasing each result to the
model"
([Authorization v1](../normative/authorization/v1/README.md#model-bound-actor-attributed-data-admission)
and
[v2](../normative/authorization/v2/README.md#model-bound-actor-attributed-data-admission)).

- Human actors must meet the admission threshold of the cycle's snapshot
  records, so one cycle never admits updates at two thresholds. Steer envelope
  v1 pairs only with entity-context-snapshot v1, whose threshold is `triage`
  (observation) or `write` (mutation). Moving to the harness's
  `human_min_role` needs entity-context-snapshot v2 and, with it, steer
  envelope v2.
- Bot actors need a registered ADR 0107 `bot_role`.
- Records that fail, or cannot be attributed or verified, are dropped and
  counted as omissions.
- A head move is attributed only from forge-authenticated push or head-update
  metadata, never from self-asserted Git commit author or committer fields
  (ADR 0133). The sandbox already holds the earlier head, so a moved head
  whose actor cannot be attributed, or fails admission or the release check,
  declines the whole steer, including any accompanying records. The agent
  never receives comments about code that was withheld.
- Event-carried evidence, the route job's verdict, earlier CEL results, and the
  [ADR 0148](0148-reuse-verified-actor-roles-for-entity-cel.md) role cache
  never satisfy the check.

### Content

Records take the ADR 0141 host-side path and filter pipeline; the
sandbox-visible format is
[steer envelope v1](../normative/steer-envelope/v1/README.md). A steer is
content, never capability: it cannot change tools, role, model, credentials,
routing, or network policy.

### Head provisioning

When a change proposal's head moves from `A` (the head last provisioned into
this sandbox, from the snapshot or the latest published head steer) to `B`,
the host provisions it in three stages. The sandbox never calls the forge and
receives no fetch credential.

1. **Fetch (step 3).** With its own forge credential, the host fetches `B`,
   the commits connecting it to `A`, and the change proposal's current base
   into host-private state outside the clone and the run-output tree, and
   checks that the fetched commit ID is `B`. Fetching the current base means
   the agent never diffs `B` against a stale base when the base moved.
2. **Scan (step 4).** The host runs the pre-agent input scanners (Unicode
   normalization and prompt injection) over the repository context files that
   changed in `B`'s tree. A critical finding always declines the whole steer,
   whatever the scan's fail mode for other inputs.
3. **Install (step 5).** Under the sandbox write lock, after the release
   check, the host installs `B`'s objects into the sandbox repository and
   sets `refs/fullsend/head` to `B` and `refs/fullsend/base` to the current
   base. It writes the objects and refs as files rather than running Git
   inside the sandbox, so no agent-writable Git configuration takes part.
   The repository is one the agent can modify, so the host never writes into
   an existing file there. Each object and ref goes to a freshly created file,
   opened relative to a directory handle without following links, and is then
   renamed into place, replacing the directory entry rather than the file it
   names. A hard link or symlink the agent planted is never written through,
   and a symlink or special file on the path declines the steer.

What the agent sees depends on its working tree:

| Working tree | What the host does | What the agent does |
|---|---|---|
| Read-only (`readonly_repo`, for example review) | Also updates the read-only tree, `HEAD`, and the index to `B` under the lock, at a tool boundary the runtime reports, with no agent tool running, so no read sees a mix of `A` and `B`. A runtime that cannot report such a boundary declines read-only head steers. The agent has no local edits there to lose | Reads `B`'s files as usual; the envelope's `Head:` line names `B`, which the tree already shows |
| Writable (for example fix or code) | Never modifies the working tree, the index, or `HEAD`, so no in-flight edit or local commit is overwritten | Rebases or checks out `refs/fullsend/head` itself |

### Result revision

A cycle's *eligible head set* is its initial snapshot head `H_0` plus every
`head.sha` from a steer in that cycle that the runtime acknowledged before
settle. When the cycle completes validation, the host resolves the cycle's
platform-verified result revision (ADR 0144) from that set.

ADR 0144 requires the revision to be platform-verified, but does not fix it to
the cycle's starting head. Every head in the set was fetched by the host from
the forge, so this rule defines the revision for steered cycles without
changing ADR 0144.

| Cycle output | Result revision | What the host has checked |
|---|---|---|
| Result JSON carries `head_sha` (for example review) | `head_sha`, when it is in the eligible head set; otherwise validation fails closed | Only that the agent named a head it was given. This bounds the agent's claim; it does not prove the agent reviewed that head |
| New commits on the PR branch and no `head_sha` (for example fix) | The single eligible head nearest to the extracted `HEAD`: an ancestor of `HEAD` with no other eligible head that descends from it and is also an ancestor of `HEAD`. If no eligible head, or more than one, meets that rule (for example after the agent merged two steered heads), validation fails closed. A result with both new commits and a `head_sha` that disagree also fails closed | That the agent's commits were built on that one head and on no newer eligible one. `H_0` is always eligible, so an agent that ignored every steer resolves to `H_0` |
| Neither | `H_0` | — |

**No approval on an agent-reported steered head.** When a review's
`head_sha` is a steered head rather than `H_0`, `fullsend run` rewrites an
`approve` action to `comment` in the host copy of the result before the
post-script runs. Every approval outcome is therefore withheld, including the
review event, a ready-for-merge label, and auto-merge, whatever post-script
version the harness uses. The review still posts, pinned to that head,
noting that a full cycle must approve it. Changes-requested and comment
verdicts post as usual. The follow-up cycle reviews the head in full and may
approve it.

**Post-script baseline.** When the result revision advances to `B`,
`fullsend run` sets `PRE_AGENT_HEAD` to `B` in the post-script's environment,
so its diff, secret, and pre-commit checks cover only commits the agent added
on top of `B`, and existing post-scripts need no change.

### Dependencies

Steering stays off, regardless of configuration, until these are in place:

- per-record admission at the snapshot's threshold, the ADR 0141 filter
  pipeline, and its host-side extension path;
- the ADR 0144 cycle lifecycle, and ADR 0106 active-run preservation on the
  platform, so a new event does not cancel the running cycle (on GitHub
  Actions, `cancel-in-progress: false`);
- an in-sandbox writer the agent cannot impersonate, for the steer files, the
  head objects, and the agent's input channel.

Handled-state receipts are not a dependency: delivery is correct without
them, and each steered cycle is then followed by a full cycle.

## Forge scope

Steering applies wherever a running cycle receives a verified signal:

- **GitHub:** through ADR 0144 events and wakes. ADR 0144 opens no wake
  listener on GitHub Actions, and event-only harnesses there have no timer
  check either, so this ADR alone does not steer them. Head moves on pull
  requests from forks decline, because the push happens in the fork, where
  the host cannot attribute it.
- **GitLab and Forgejo:** through ADR 0144 events and wakes, once ADR 0144 is
  implemented there; the rules here are forge-neutral. A head move needs the
  forge-authenticated actor who moved the head; until a forge adapter exposes
  it (Forgejo has none yet), head-move steers there decline and the follow-up
  cycle handles the push.
- **Jira and other non-forge entities:** steering is declined, since
  entity-context-snapshot v1 does not stage them.

## Consequences

- Updates during a cycle, including pushes to a change proposal, reach the
  running agent in the same session, so fewer outputs are built from replaced
  state or discarded as `stale-head`.
- A steered sandbox stays up longer, each release adds a fresh permission
  lookup per actor, and each head move adds a host-side fetch, a context
  scan, and an object upload.
- A harness's pre-script gates every steer over the cumulative steer input,
  so steering adds no path past it; a harness with a pre-script steers only
  after that pre-script opts in, and pays one extra pre-script run per steer.
- A review of a steered head can request changes in the same session, but
  only a full cycle can approve that head, and the follow-up cycle always
  runs for a pushed head.
