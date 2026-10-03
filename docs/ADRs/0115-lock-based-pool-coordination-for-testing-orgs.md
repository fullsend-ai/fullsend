---
title: "115. Lock-based pool coordination for testing orgs"
status: Accepted
supersedes:
  - "0040"
relates_to:
  - agent-infrastructure
topics:
  - testing
  - concurrency
  - ci
---

# 115. Lock-based pool coordination for testing orgs

Date: 2026-09-14

## Status

Accepted

Supersedes the locking mechanism in
[ADR 0040](0040-org-pool-for-parallel-e2e-tests.md) for behaviour tests.

## Context

[ADR 0114](0114-ephemeral-repo-lifecycle-for-testing.md) establishes ephemeral
repos as the testing model. Ephemeral repos have unique names, so concurrent
runs in the same org do not collide on shared state — but they share the org's
rate-limit budget, and how that budget scales differs fundamentally by forge:

- **GitHub.** The rate limit is **org-scoped** and repo-count-scaled (up to
  12,500/hr). At ~3,000 API calls per run, a well-populated org supports roughly
  four concurrent runs. Capacity scales by **adding orgs**.

- **GitLab.com Free.** The rate limit is **per user**, not per group, and cannot
  be raised by adding groups, repos, or tokens for the same user. There is also
  **no API path to add users**: regular user creation (`POST /users`) requires
  instance admin, which SaaS customers do not have, and **service accounts are a
  Premium/Ultimate feature unavailable on Free**. Consequently GitLab.com Free
  concurrency is **hard-capped at a single bot user's budget** and cannot be
  pooled for scale.

[#6864](https://github.com/fullsend-ai/fullsend/issues/6864), which authorizes
ADR 0114, specifies that behaviour tests should have **no pool, channel,
semaphore, pre-allocation, or outstanding tracking**, with the existing pool
infrastructure kept only for admin e2e. That directive targets the *within-run*
scenario-allocation machinery ADR 0114 removes: the channel-based pool of fixed
repo names that gated how many scenarios could run concurrently inside one test
invocation. It does not speak to *cross-run* coordination — multiple separate
CI invocations (e.g., concurrent PR pushes) sharing one org's rate-limit budget
— which was not a recognized problem when #6864 was scoped and only became
apparent under the growing CI load described in ADR 0114's Context. This ADR's
lock repos are that cross-run concurrency cap, not the within-run semaphore
#6864 eliminates — but they do apply to behaviour tests' default single-org
deployment (see Pool membership), which is a real extension beyond #6864's
original scope. That extension is accepted here because the alternative is not
"no coordination": without it, the default single-org deployment falls back to
[ADR 0040](0040-org-pool-for-parallel-e2e-tests.md)'s coarser exclusive-lock-per-org
model, which is what this ADR exists to replace, or no coordination at all,
which lets concurrent runs exhaust the shared rate limit unpredictably.

ADR 0040's exclusive locking model (one run per org) does not fit GitHub: it
wastes org capacity by blocking concurrent runs that could safely share the rate
limit. The coordination mechanism must:

- Enforce a per-org concurrency cap (not exclusive access) where the forge
  permits concurrency.
- Use an atomic primitive — no read-then-write races.
- Work across both GitHub and GitLab.

## Decision

### Lock-based concurrency control

Each testing org has N **lock repos** (`lock-1` … `lock-N`) — a direct expansion
of ADR 0040's single exclusive lock into a per-org pool. Each is a lightweight
repository used purely as a distributed lock; together the N of them act as a
counting semaphore, so an org holds up to N concurrent runs instead of 0040's
one.

- **Acquire.** A run treats the pool as the full set of `(org, lock-slot)`
  pairs across all configured orgs and attempts to create lock repos in a
  **randomized order**, taking the first that succeeds. Repo creation is atomic
  — the first creator wins, all others get an "already exists" error, so the
  claim itself is resolved by the create, with no read-then-write to reserve a
  slot. The run's **owner token — its unique run ID — is written as the lock
  repo's description in that same create call**, so it is set atomically with
  the claim and returned in the create response: the creator knows it holds the
  slot with no extra call. This run ID is the *same* identifier that prefixes
  every ephemeral repo the run creates
  ([ADR 0114](0114-ephemeral-repo-lifecycle-for-testing.md) names them
  `bt-{run-id}-{uuid8}`), so a held lock maps back to exactly its run's repos via
  a `bt-{run-id}-*` lookup. It is a **time-ordered UUID** (e.g. UUIDv7): unique,
  so no two runs claim the same identity, yet time-sortable, which 0114 relies on
  for oldest-first pruning. This token is what identifies the holder: it is the
  identity checked when the slot is released and when a slot is reclaimed as
  stale, since the repo's existence alone cannot say *which* run owns it (after a
  stale reclaim the repo still exists but belongs to a different run). Carrying
  the token in the description — rather than in a committed file such as a README
  — keeps the protocol both cheaper and race-free: the description is set in the
  create itself and read back together with `created_at` in a single
  repo-metadata request, whereas a file must be written in a second call and is
  populated asynchronously, forcing a verify-read to work around the forge
  serving stale default content. Randomizing across the whole
  set, rather than
  filling one org's slots before moving to the next, is a deliberate
  **load-balancing** decision: because each org is a separate rate-limit budget
  (that budget is the reason for having multiple orgs), concurrent runs must
  spread across orgs rather than draining one while others sit idle. Selecting a
  random free slot achieves this without observing load — an org is chosen with
  probability proportional to its free capacity, so emptier orgs attract
  proportionally more runs and per-org budgets are drawn down evenly. The same
  randomization avoids a thundering herd on any single slot (e.g. every run
  racing for `lock-1`), which would otherwise waste rate-limit budget on failed
  create attempts. If all slots in all orgs are held, the run polls with backoff
  until one is released or a timeout expires; if the timeout expires the run
  fails, except when every org is rate-limited, which is reported distinctly so
  callers may skip rather than fail.

- **Heartbeat.** While a run holds a slot, it periodically re-writes its lock
  repo's description with the **same, unchanged owner token**, purely to bump
  the repo's `updated_at`. The interval is a fraction of the progress window
  (below), so a live run's heartbeat always lands well before the window
  elapses. Because the write repeats the existing token verbatim, it introduces
  no read-then-write race: it is a blind, idempotent write, not a
  compare-and-swap.

- **Release.** On test completion (pass or fail), the run deletes **only the
  slot it owns** — it reads the lock's owner token (its description) and deletes
  only if the token matches its own run ID, so a run never deletes a lock it does
  not hold. This guards the reclaim-then-revive race: if a run's
  slot is reclaimed as stale (below) while the run is merely slow rather than
  dead, the revived run must not delete the slot a second run now holds. Cleanup
  is registered via `t.Cleanup` so crashes still attempt release.

- **Stale lock recovery.** Staleness is judged by **liveness, not a fixed age**.
  ADR 0040's single 15-minute timeout was a blunt compromise — it had to be
  longer than the longest expected run yet short enough to recover crashed slots,
  two goals a single clock cannot both meet: a long-but-healthy run gets its lock
  stolen, while a run that crashes early ties up its slot for the full timeout.
  Because a live run heartbeats its own lock, a would-be reclaimer can observe
  whether the run is still making progress instead of guessing from a fixed
  timeout. Recovery proceeds in layers:

  1. **Age gate (cheap filter).** The `created_at`, `updated_at`, and owner
     token are already read as one metadata request. A young lock is left
     alone — the common case costs nothing more. The gate must exceed the
     acquire-to-first-heartbeat latency so a run that has just claimed a slot
     but not yet sent its first heartbeat is never mistaken for dead.

  2. **Heartbeat check (primary signal).** `created_at` says only when the
     lock was claimed, not whether the run is still alive, and repo-creation
     activity is not a usable proxy for liveness either: a live run can go
     many minutes between `bt-{run-id}-*` repo creations on GitHub, and on
     GitLab.com, where ADR 0114 deletes each repo immediately after its
     scenario, a live run routinely has **zero** matching repos at any given
     instant — the same absence that a dead run would show. Keying liveness
     off ephemeral-repo activity therefore reads "alive" as "dead" in both
     cases. Instead, a lock past the age gate is judged by its own
     `updated_at`, refreshed by the heartbeat above: recent `updated_at` means
     the run is alive — **do not reclaim**, however long it has been running.
     No heartbeat beyond a *progress window* (sized to the longest legitimate
     gap between heartbeats, far smaller than a whole run) means the run is
     dead **or hung**, and the slot is eligible for reclaim. This catches both
     crashes and wedged-but-alive runs, works identically on both forges since
     it never inspects `bt-` repos, and needs no repo enumeration at all. On
     **GitLab.com Free**, where pooling does not raise concurrency (N ≈ 1),
     this is nonetheless the **primary** stale-lock-recovery path, not an
     unused one: a single lock still needs to be recovered from a crashed run,
     and per-scenario repo deletion means `bt-`-repo activity was never a
     usable substitute signal there to begin with.

  3. **Fallbacks.** A lock whose owner token is **missing or malformed** is
     reclaimed directly — the heartbeat check cannot attribute activity without
     a valid run ID. And an **absolute-age ceiling**, set to exceed the maximum
     CI job duration (past which a run is guaranteed terminated; e.g. ~2h,
     tracking the job's configured `timeout-minutes`), reclaims regardless of
     heartbeat activity. The ceiling is only a backstop for runaway or
     timeout-less (local) runs that keep heartbeating indefinitely — not the
     primary path — so it can be generous without delaying recovery of
     genuinely dead slots, which layer 2 already handles quickly.

  A reclaim is a **compare-and-delete**, not an unconditional delete-then-create:
  immediately before deleting, the reclaimer re-reads the lock's description
  and proceeds only if it still matches the stale token just judged dead. If it
  matches, the reclaimer deletes and recreates the repo recording its own owner
  token; recreation is atomic, so if the reclaimer's create loses a race to
  another reclaimer that recreated first, the create fails with
  "already exists" and the losing reclaimer must treat that the same as a
  failed initial acquisition — back off and retry elsewhere, not assume it
  holds the slot. If the re-read instead shows a different token — because the
  original run heartbeated in the meantime, or a different reclaimer already
  won — the reclaimer aborts without deleting and treats the slot as held by
  whichever run's token it just read. This closes the race an unconditional
  delete-then-create had: two reclaimers racing the same stale lock could
  previously both pass the liveness check and both delete unconditionally, so
  the second reclaimer's delete would remove the first reclaimer's
  freshly-created lock and both would believe they held the slot. With the
  re-read precondition, a reclaimer only ever deletes a lock it just confirmed
  still carries the stale token, and the atomic recreate settles any residual
  simultaneous-delete race the same way the initial acquire does. The
  ownership-verified release described above separately guards the case where
  a reclaimed run's original owner is merely slow, not dead, and revives after
  losing its slot.

- **Lock count and forge-specific scaling.** N is tuned per org based on the
  rate-limit budget **and** how long a run occupies its slot:
  `N = floor(rate_limit_cap × typical_run_duration_hours / calls_per_run)`.
  The simpler `rate_limit_cap / calls_per_run` form implicitly assumes each run
  occupies its slot for a full hour; when runs are shorter, sequential waves of
  runs under the same N complete more often within an hour and can exceed the
  budget even though no more than N ever run *concurrently*, so
  `typical_run_duration_hours` — the median wall-clock time from lock
  acquisition to release, measured empirically per org and re-derived when it
  shifts materially — has to be part of the formula rather than assumed to be
  one hour. On **GitHub**, with a 12,500/hr cap and ~3,000 calls/run, a typical
  run duration of one hour gives `N = floor(12,500 × 1 / 3,000) = 4` per org;
  a typical run duration of 30 minutes instead gives
  `N = floor(12,500 × 0.5 / 3,000) = 2`, since twice as many sequential waves
  fit in the same hour at half the duration. Capacity still grows by adding
  orgs (five orgs at N = 4 → 20 concurrent locks, under the one-hour
  assumption). On **GitLab.com Free**, this math does not apply: the per-user
  cap cannot be raised, so a single group with a small N is the ceiling —
  GitLab supports the coordination *primitive* (correctness, no double-booking)
  but **not pooling for scale**. Any GitLab concurrency beyond one user's
  budget would require a paid tier (service accounts) or self-managed admin,
  both out of scope here. N is **external configuration**, not a
  source-code constant: a default with optional per-org overrides, so a
  warming-up org or a GitLab group (N = 1) can differ from the GitHub norm,
  and so N can be recalibrated if measured run duration shifts without a code
  change. N may therefore be **non-uniform across the pool**, but this adds no
  acquisition complexity: because acquisition is a randomized
  first-success-wins over the flat set of `(org, lock-slot)` pairs (above),
  each org simply contributes as many pairs as its N, and the mechanism never
  reads or compares N as a number. Membership and per-org N travel together as
  one configuration value, expanded once in memory into the candidate set —
  there is no per-org API call to discover N.

### Pool membership

The pool is a **fixed, statically-configured list** of testing orgs, not a set
discovered at runtime. "Static" here means *explicitly declared, not queried
from the forge* — it does **not** mean compiled into the binary. Pool
membership (and the per-org N above) lives in **external configuration**, not a
source-code constant, so it can change without a code change or rebuild. A run
knows the pool because it is configured, so the orgs a run considers are
deterministic and auditable — there is no membership query and no dependency on
org-enumeration APIs, which differ across forges and would be trivial on
GitLab.com Free (a single group) in any case.

The values are **forge-specific**, because the two forges have different shapes:
a set of GitHub orgs at N ≈ 4 each versus a single GitLab group at N = 1. A run
already knows its forge — it drives a GitHub or GitLab `forge.Client` — so it
resolves that forge's pool and N from configuration rather than probing for
them. The concrete home for these values is the run's environment: CI/CD
variables for automated runs (org names are not secret — only tokens are — so
plain variables suffice) and the equivalent local environment for developer
runs. The ADR does not pin the exact variable names or whether the values are
forge-namespaced or supplied by a forge-scoped pipeline; those are
implementation choices. The decision is that pool membership and N are
forge-specific *external configuration*, resolved by the forge the run targets.

The minimal deployment is a single org — one GitHub org, or the lone
GitLab.com Free group — which the lock pool alone drives up to N concurrent
runs. On GitHub, listing additional orgs in the configuration raises concurrency
further (each org contributes up to N locks).

Expanding the pool is a **manual operational task**: provision the org, install
the required apps, provision its inference credentials, and add its name to the
configuration value. This is deliberately not automated, and because the list is
external configuration it takes no code change or rebuild. The pool changes
rarely, and browser-driven org and app provisioning would add more moving parts
— credential storage, headless automation, an Enterprise-only org-creation API —
than a rare manual step justifies.

### Forge portability

The lock mechanism depends on three forge operations, all required to reach
GitLab parity:

- **Create repo with a description (atomic, fail-if-exists).** GitHub returns
  422, GitLab returns 409 or 400 "has already been taken". Both must be surfaced
  as `forge.ErrAlreadyExists` and matched by the pool's duplicate-detection
  helper on both forges. The create must also accept a description in the same
  call (it carries the owner token) — supported by both forges.

- **Read repo metadata (`description`, `created_at`, `updated_at`)** — the
  owner token, age, and heartbeat recency, used for release ownership checks
  and stale lock detection, read together in a single request. All three
  fields are returned by GitHub and GitLab project APIs.

- **Update repo description.** Used by a live run to heartbeat its own lock —
  rewriting the same owner token, which bumps `updated_at` with no other side
  effect. (The reclaimer's compare-and-delete re-read immediately before
  deleting reuses the read-metadata operation above, not this one.) Both
  forges' project/repo update endpoints support updating the description
  independently of other fields.

No forge-specific external coordinators (GCS, Redis) are required.

## Consequences

- On GitHub, multiple runs share a testing org concurrently up to the lock cap;
  org capacity is used efficiently without risking rate-limit exhaustion, and
  adding capacity is a manual operational task (provision a new org, append it
  to the configured pool value) — a configuration change, not a code change.
- The lock count is a hard cap enforced by the number of lock repo names, not by
  counting — no race between counting and claiming.
- **GitLab.com Free cannot be pooled for concurrency.** The design supports
  GitLab for *correctness* (coordination primitive + ephemeral repos) but its
  scaling axis is GitHub-only. This is a forge constraint, not an
  implementation gap: the per-user rate limit has no API-provisionable lever on
  Free. GitLab scale, if needed, requires a paid tier or self-managed instance.
- A crashed run leaves at most one stale lock, reclaimed once its heartbeat
  stops (a short progress window, not a full run length); during that window
  the pool has one fewer lock. A pathological run that stays active is bounded
  only by the absolute-age ceiling.
- Because the pool is a configured list rather than a discovered one, growing it
  requires a configuration change — but an *external* one (a CI/CD or
  environment variable), not a code change or rebuild. This is an accepted
  trade: the pool changes rarely, and a deterministic, auditable membership is
  worth more than hands-off expansion.
- **This ADR extends lock-based coordination to behaviour tests' default
  single-org deployment**, which goes beyond the "no pool, no semaphore"
  scope [#6864](https://github.com/fullsend-ai/fullsend/issues/6864) set for
  behaviour tests (see Context). Admin e2e's own migration to this model, if
  it happens, is separately out of scope (see [ADR 0114](0114-ephemeral-repo-lifecycle-for-testing.md)'s
  Consequences).
