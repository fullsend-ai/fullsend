---
title: "114. Ephemeral repo lifecycle for testing"
status: Accepted
supersedes:
  - "0040"
relates_to:
  - agent-infrastructure
topics:
  - testing
  - behaviour-tests
---

# 114. Ephemeral repo lifecycle for testing

Date: 2026-09-14

## Status

Accepted

Supersedes [ADR 0040](0040-org-pool-for-parallel-e2e-tests.md) for behaviour
tests. Admin e2e tests continue using ADR 0040's exclusive-lock org pool
unchanged (see Consequences).

## Context

Behaviour tests need real repositories on real forges to exercise dispatch,
harness loading, scaffold mutations, and CI workflows. Two prior approaches
were layered but never documented in a single ADR: a fixed-name repo pool
(`test-repo-01` … `test-repo-NN`) reused across leases with a delete-and-recreate
reset per lease (to avoid multi-gigabyte git-history bloat and the 12+ minute
shallow-clone deepening it caused), nested inside
[ADR 0040](0040-org-pool-for-parallel-e2e-tests.md)'s exclusive per-org
locking. Under growing CI load — every PR push triggers behaviour tests —
this hit two ceilings at once: the fixed pool size (default 12) capped
concurrency, and keeping the org small to limit cost pinned the rate limit
near its floor.

The behaviour test framework ([ADR 0066](0066-behaviour-tests-with-gherkin-and-drivers.md))
motivated moving to ephemeral repos created per scenario, eliminating state
leakage — but the two forges' rate-limit models differ fundamentally.
GitHub's REST limit is org-scoped and scales with repo count: 5,000 base + 50
per repo beyond 20, capped at 12,500/hr
([GitHub rate limits for GitHub Apps](https://docs.github.com/en/apps/creating-github-apps/setting-up-a-github-app/rate-limits-for-github-apps),
retrieved 2026-09-14), so retaining repos raises the ceiling. GitLab.com's
limit is per user, fixed regardless of project count (≈7,200 requests/hr,
200/min on the Projects/Groups/Users endpoints), so retention buys no
headroom there.

At an observed ~3,000 API calls per behaviour test run, a small GitHub org
supports only one or two concurrent runs before hitting its cap; a
well-populated org supports around four — the gap this ADR's retention
policy closes. See [testing-agents.md](../problems/testing-agents.md) for the
broader agent-testing problem space; this ADR addresses test infrastructure
capacity, not LLM/instruction coverage. Full migration scope is tracked in
[#6864](https://github.com/fullsend-ai/fullsend/issues/6864).

## Decision

Behaviour tests use **ephemeral repos** (admin e2e continues using
[ADR 0040](0040-org-pool-for-parallel-e2e-tests.md)'s exclusive-lock org pool
unchanged; see Consequences for a note on a possible future migration):

- **Ephemeral creation.** Each test scenario creates a uniquely-named repo
  `bt-{run-id}-{uuid8}`. The `run-id` is a **time-ordered UUID** (e.g. UUIDv7),
  generated once and shared across all repos in a run; the 8-char UUID makes
  each repo within the run unique. A time-ordered run ID is chosen deliberately:
  it is unique (so two runs never collide, even when started in the same
  instant) yet its leading bits encode a timestamp, so run IDs — and therefore
  the repo names that embed them — still sort chronologically. This single
  identifier does triple duty: it groups a run's repos (shared prefix), orders
  them for pruning (below), and is the same value used as the pool lock's owner
  token ([ADR 0115](0115-lock-based-pool-coordination-for-testing-orgs.md)), so
  a lock maps back to exactly the repos its run created via a `bt-{run-id}-*`
  prefix lookup. No repo is reused across scenarios or runs. This guarantees a
  clean slate and validates the full create-and-install path. The scenario name
  is written to the repo description so the repo backing a given test can be
  identified at a glance (the opaque name alone does not reveal which scenario it
  served).

- **Forge-specific teardown (retain vs. delete).** Creation is shared; teardown
  is deliberately forge-specific because each forge's rate-limit model rewards a
  different strategy. This is a first-class decision, not a tuning detail:

  - **GitHub — retain and batch-prune.** Repos are retained after test
    completion. The accumulated repo count raises the org's rate-limit cap
    toward 12,500/hr, so retention is load-bearing. At the end of a run, when
    the org's `bt-` repo count exceeds a threshold (default 200), the
    framework deletes the **oldest whole runs** — grouping repos by their
    shared `run-id` and considering groups for deletion oldest-first, but
    **skipping any run-id that currently owns an
    [ADR 0115](0115-lock-based-pool-coordination-for-testing-orgs.md) lock
    slot** (checked via the lock repos' owner-token descriptions). Only
    groups whose lock has been released, or which ADR 0115's liveness check
    has judged stale, are eligible for deletion. Eligible groups are deleted
    oldest-first until deleting the next one would drop the total below the
    threshold, so pruning stops with the post-prune count at or above the
    threshold. Because the `run-id` is time-ordered, "oldest first" is a
    simple sort on the prefix. Pruning by whole runs minimizes churn and
    preserves rate-limit headroom; skipping locked runs prevents pruning from
    deleting a still-running suite's repos out from under it, which could
    otherwise let ADR 0115's liveness check mistakenly reclaim that suite's
    lock. Pruning can be disabled to retain repos for debugging.

  - **GitLab.com — delete per scenario.** Rate limits are per user and do not
    scale with project count, so retention buys nothing. GitLab therefore
    **deletes each repo immediately after its scenario** — no retention, no
    end-of-run pruning, and no need to enumerate all repos. This keeps the group
    small and avoids building pruning infrastructure GitLab would never benefit
    from.

  The choice of teardown strategy is driven entirely by the rate-limit model;
  the GitHub retention threshold (~200) is the only genuinely tunable value.

- **Forge-agnostic mechanism, forge-specific policy.** Creation and per-repo
  operations (create, delete, ref/issue/comment) go through the `forge.Client`
  interface on both forges. Only the teardown *policy* branches by forge:
  - GitHub's retain-and-prune path needs to enumerate all repos in the org and
    to observe org-scoped rate-limit state.
  - GitLab's delete-per-scenario path needs **only** repo create and delete. It
    deliberately does **not** require repo enumeration or org-scoped rate-limit
    reporting, since it neither prunes nor benefits from a repo-count-scaled cap.

- **Behaviour test org.** Behaviour tests run against a single org
  (`fullsend-ai-test` by default, overridable per run). The install path is
  forge-selectable so behaviour tests can run against GitLab as well as GitHub.
  Pool coordination across multiple orgs
  is covered in
  [ADR 0115](0115-lock-based-pool-coordination-for-testing-orgs.md).

- **Build optimization.** To avoid repeated cross-compilation of the Linux
  `fullsend` binary on every install, the behaviour framework builds the binary
  once and reuses it across installs (via the `--fullsend-binary` flag) rather
  than cross-compiling per install (the default `--vendor` / `--fullsend-ref`
  path). This recovers the per-repo overhead introduced by ephemeral creation.

- **Shared org-scoped inference credentials.** Ephemeral repos use a single
  org-scoped Workload Identity Federation provider, resolved once per run and
  reused across every repo, rather than provisioning a dedicated per-repo
  provider on each ephemeral repo. This fits the ephemeral model — the
  credential belongs to the org, not to a short-lived repo, so nothing has to be
  provisioned or torn down per repo. It also removes a slow per-repo GCP step
  (provider creation plus IAM propagation) from the hot path, resolving WIF once
  per run instead of once per scenario. And because ephemeral repos never take
  the per-repo provider-ID path, this sidesteps the provider-ID naming
  limitation noted under Consequences entirely.

- **Retained-repo sanitization.** Before a GitHub repo becomes eligible for
  retention past its scenario (i.e., as soon as the scenario finishes and the
  repo enters the retained pool), the framework uninstalls the fullsend app,
  strips any repo-scoped secrets or variables the scenario wrote, and disables
  the repo's GitHub Actions workflows. Retained repos keep real scaffolding and
  CI configuration (that realism is the point — see Consequences), but they
  must not be able to invoke the shared org-scoped WIF provider on their own
  after their scenario ends; sanitization removes the installed app and
  workflow triggers that would let leftover CI configuration do so.

## Consequences

The change bundles two separable decisions; on GitHub each delivers its own win.

**Unique ephemeral repos (vs. a fixed reused pool):**

- **Concurrency is no longer tied to a fixed pool of repo names.** With the
  reused pool, the number of pre-created names (default 12) *was* a hard
  concurrency ceiling — the 13th concurrent scenario blocked waiting for a free
  name. Unique ephemeral repos break that coupling: concurrency becomes a
  tunable scenario-parallelism setting (`GODOG_CONCURRENCY`), no
  longer pinned to how many names were created up front. The rate-limit budget
  is not a hard cap on that number — it sets the point of diminishing returns,
  where added parallelism just contends for the same budget and slows down under
  throttling rather than being refused outright.
- **No git-history bloat, no per-lease reset.** Each repo is single-use and
  never accumulates history, so the 12+ minute shallow-clone deepening problem
  and the delete/await-propagation/fork-orphan reset machinery leave the hot
  path entirely. Deletes move to amortized end-of-run pruning (GitHub) or a
  cheap per-scenario delete (GitLab).
- **Structural isolation.** Unique repo names make cross-scenario state leakage
  impossible without cleanup or reset logic, and the full create-and-install
  path (repo creation, scaffold, app installation, event-delivery/
  workflow-indexing warmup) is exercised on every run — catching regressions a
  reused repo would mask.

**Retention on GitHub (vs. keeping the org small):**

- **~2.5× the rate-limit budget.** ~12 repos pinned the cap near 5,000/hr;
  retaining ~200 repos raises it toward 12,500/hr.
- **More effective concurrent runs.** Raising the cap from ~5,000/hr to
  ~12,500/hr raises how many runs an org can sustain per hour, but the exact
  multiple depends on typical run duration, not just the hourly budget — see
  [ADR 0115](0115-lock-based-pool-coordination-for-testing-orgs.md)'s
  duration-aware `N` formula. For a run lasting close to an hour this is close
  to 4×; shorter runs, run sequentially in more waves per hour, yield a
  smaller safe `N`.
- Retained repos are inert and permitted: GitHub's Terms of Service do not
  prohibit retaining test repos, which contain real scaffolding and CI
  configuration, not empty shells.

Together these lift both ceilings the reused-repo model hit under CI load: the
pool-size concurrency cap and the small-org rate-limit floor.

- **Forge asymmetry is fundamental, not incidental.** Retention as a rate-limit
  lever is GitHub-only; on GitLab.com the per-user limit cannot be raised by
  adding repos or groups. The lifecycle *mechanism* is forge-agnostic, but its
  teardown *policy* is deliberately forge-specific, and the design does not
  pretend one strategy fits both.
- On GitHub the testing org grows large and cluttered by design; batch
  pruning keeps the count at or above the threshold — it stops as soon as
  removing another eligible run group would drop below it — so the rate
  limit is lowest immediately after a prune and highest just before the next
  one triggers. A run-id whose lock is still held (and not yet judged stale)
  is skipped even when it is the oldest group, so the count can temporarily
  exceed the threshold while older runs are still active.
- On GitLab, delete-per-scenario adds no storage over time but pays a delete on
  every scenario — acceptable, since GitLab gains no rate-limit headroom that
  would justify retention.
- The two teardown paths carry different implementation surfaces: GitHub needs
  repo enumeration and org-scoped rate-limit reporting; GitLab needs neither.
  This keeps the GitLab path minimal rather than forcing pruning infrastructure
  onto a forge that cannot use it.
- Each scenario pays a create + install; this is roughly a wash against the old
  model, which paid create + install per lease *plus* the reset overhead this
  change removes. The change earns its keep specifically under growing CI load —
  at low concurrency the reused-repo model was adequate.
- **Admin e2e migration is an open question, not part of this Decision.**
  This ephemeral-repo model is a candidate future replacement for admin e2e's
  use of [ADR 0040](0040-org-pool-for-parallel-e2e-tests.md)'s exclusive-lock
  org pool, but [#6864](https://github.com/fullsend-ai/fullsend/issues/6864)
  — the issue authorizing this ADR — scopes admin e2e as unaffected: its pool
  infrastructure (`AcquireOrg`, `OrgPool`, `ReleaseLock`) stays in place.
  Migrating admin e2e to this model would need its own authorizing issue
  before it proceeds.
- **Known limitation — GitHub WIF provider-ID collisions.** On GitHub, the
  per-repo Workload Identity Federation provider ID is derived from the
  `{owner}/{repo}` pair and truncated to GCP's 32-character limit. A long org
  name combined with these repo names can truncate away the distinguishing
  suffix, so two repos can derive the same provider ID. This affects only
  GitHub's optional per-repo WIF path — which these tests avoid by using a
  shared org-scoped provider (see Decision) — and the convergence tooling
  detects and rejects any such collision rather than misprovisioning, so it is
  unlikely to be hit in practice. It is tracked by
  [#6921](https://github.com/fullsend-ai/fullsend/issues/6921), which replaces
  the derived ID with a hash of the full identifier and resolves it completely.
  The naming scheme in this ADR needs no change when that lands.
