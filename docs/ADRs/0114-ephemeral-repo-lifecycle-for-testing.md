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

Accepted — supersedes [ADR 0040](0040-org-pool-for-parallel-e2e-tests.md).

## Context

Fullsend's tests (behaviour tests, with admin e2e tests to migrate) need
real repositories on real forges to exercise dispatch, harness loading, scaffold
mutations, and CI workflows. The prior approach took two forms:

1. **Reused repos** — behaviour tests ran against a fixed pool of repo *names*
   (`test-repo-01` … `test-repo-NN`), created once per run and identical across
   runs. Each scenario *leased* a name from the pool and returned it when done,
   so a name was reused by later scenarios within a run, and the pool size
   capped concurrency. The repo *object*, however, was ephemeral per lease: on
   every allocation the repo was deleted and recreated before install. This was
   necessary because repeated runs bloated a repo's git history to gigabyte
   scale, and the pre-review shallow-clone deepening step then took 12+ minutes
   fetching that history; delete-and-recreate restored a clean single-commit
   repo (clearing leftover state was a secondary benefit). The cost was that
   every lease paid a delete/recreate/reinstall cycle.

2. **Pool of orgs with exclusive locking** ([ADR 0040](0040-org-pool-for-parallel-e2e-tests.md))
   — both behaviour tests and admin e2e tests acquired exclusive access to one
   of several pre-provisioned orgs (the `halfsend` pool) via an atomic lock repo
   (`e2e-lock`). A run locked an entire org for its whole duration; a behaviour
   run then used the reused name pool (form 1) *within* that locked org. This
   prevented concurrent-run collisions but serialized runs within each org and
   required pre-provisioned infrastructure per org.

These two forms were layered, not alternatives: a behaviour run locked one org
exclusively and then leased repos from the fixed name pool inside it.

Neither approach was documented in a single ADR; the repo reuse strategy had no
written design rationale at all.

The behaviour test framework ([ADR 0066](0066-behaviour-tests-with-gherkin-and-drivers.md))
motivated a move to ephemeral repos created on demand per scenario. This
eliminates state leakage, but the rate-limit implications differ sharply by
forge:

- **GitHub.** The REST API rate limit is **org-scoped and scales with repo
  count**: 5,000 base + 50 per repo beyond 20, capped at 12,500/hr (the cap is
  reached at ~170 repos). Deleting every repo after each run keeps the org small
  and the rate limit low, so on GitHub it pays to *retain* repos to raise the
  ceiling.

- **GitLab.com.** Rate limits are **per user** (authenticated requests), not
  per project or per group, and do **not** scale with the number of projects.
  Retaining repos buys no rate-limit headroom on GitLab; the per-user budget
  (≈7,200 requests/hr general, 200/min on the Projects/Groups/Users endpoints)
  is fixed regardless of group size.

At an observed ~3,000 API calls per behaviour test run, a single GitHub org with
few repos supports only one or two concurrent runs before hitting the rate limit
cap; a well-populated org supports around four.

Under growing CI load — every PR push triggers behaviour tests — the reused-repo
model on GitHub hit **two ceilings at once**. First, the fixed name pool (default
12) hard-capped how many scenarios could run concurrently. Second, keeping the
org small (only the ~12 pool repos) pinned the rate limit near its 5,000/hr
floor, since the repo-count bonus does not begin until 20 repos. The result was
a low concurrency cap layered on top of a low rate-limit cap — the motivation for
this ADR is to lift both.

## Decision

All tests — behaviour tests and admin e2e tests (migrating from the org pool) —
use **ephemeral repos**:

- **Ephemeral creation.** Each test scenario creates a uniquely-named repo
  `bt-{run-timestamp}-{uuid8}` (the timestamp is shared across all repos in a
  run; the 8-char UUID makes each repo unique). No repo is reused across
  scenarios or runs. This guarantees a clean slate and validates the full
  create-and-install path. The scenario name is written to the repo description
  so the repo backing a given test can be identified at a glance (the opaque
  UUID name alone does not reveal which scenario it served).

- **Forge-specific teardown (retain vs. delete).** Creation is shared; teardown
  is deliberately forge-specific because each forge's rate-limit model rewards a
  different strategy. This is a first-class decision, not a tuning detail:

  - **GitHub — retain and batch-prune.** Repos are retained after test
    completion. The accumulated repo count raises the org's rate-limit cap
    toward 12,500/hr, so retention is load-bearing. At the end of a run, when
    the org's `bt-` repo count exceeds a threshold (default 200), the
    framework deletes the **oldest whole runs** — grouping repos by their shared
    run timestamp and deleting entire timestamp groups, oldest first, until
    removing the next group would drop the total below the threshold. Pruning by
    whole runs minimizes churn and preserves rate-limit headroom. Pruning is
    skipped when `E2E_KEEP_REPOS=true`.

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
  - GitHub's retain-and-prune path needs `AllRepoLister.ListAllOrgRepos` and
    rate-limit reporting (`RateLimitReporter` / `RateLimitQuerier` /
    `IsRateLimitError`).
  - GitLab's delete-per-scenario path needs **only** `CreateRepo` / `DeleteRepo`.
    It deliberately does **not** require
    `ListAllOrgRepos` or org-scoped rate-limit reporting, since it neither
    prunes nor benefits from a repo-count-scaled cap.

- **Behaviour test org.** Behaviour tests run against a single org, named
  `fullsend-ai-test` by default (constant `DefaultBehaviourOrg`, overridable via
  `BEHAVIOUR_ORG`). The install path is forge-selectable so behaviour tests can
  run against GitLab as well as GitHub. Pool coordination across multiple orgs
  is covered in
  [ADR 0115](0115-lock-based-pool-coordination-for-testing-orgs.md).

- **Build optimization.** To avoid repeated cross-compilation of the Linux
  `fullsend` binary on every install, the behaviour framework builds the binary
  once and reuses it across installs (via the `--fullsend-binary` flag) rather
  than cross-compiling per install (the default `--vendor` / `--fullsend-ref`
  path). This recovers the per-repo overhead introduced by ephemeral creation.

- **Admin install test migration.** Admin install tests will migrate from the
  exclusive-lock org pool ([ADR 0040](0040-org-pool-for-parallel-e2e-tests.md))
  to this model. The migration is incremental; both models can coexist during
  the transition.

## Benefits

The change bundles two separable decisions; on GitHub each delivers its own win.

**Unique ephemeral repos (vs. a fixed reused pool):**

- **Concurrency is no longer tied to a fixed pool of repo names.** With the
  reused pool, the number of pre-created names (default 12) *was* a hard
  concurrency ceiling — the 13th concurrent scenario blocked waiting for a free
  name. Unique ephemeral repos break that coupling: concurrency becomes a
  tunable scenario-parallelism setting (`GODOG_CONCURRENCY`, default 20), no
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
- **Roughly 4× effective concurrent runs.** At ~3,000 calls/run that is the
  difference between ~1 and ~4 concurrent runs per org — directly the headroom
  ADR 0115's per-org concurrency is built on.
- Retained repos are inert and permitted: GitHub's Terms of Service do not
  prohibit retaining test repos, which contain real scaffolding and CI
  configuration, not empty shells.

Together these lift both ceilings the reused-repo model hit under CI load: the
pool-size concurrency cap and the small-org rate-limit floor.

## Consequences

- **Forge asymmetry is fundamental, not incidental.** Retention as a rate-limit
  lever is GitHub-only; on GitLab.com the per-user limit cannot be raised by
  adding repos or groups. The lifecycle *mechanism* is forge-agnostic, but its
  teardown *policy* is deliberately forge-specific, and the design does not
  pretend one strategy fits both.
- On GitHub the testing org grows large and cluttered by design; batch pruning
  keeps the count between the threshold and the threshold minus the last-pruned
  run groups, so the rate limit is lowest immediately after pruning.
- On GitLab, delete-per-scenario adds no storage over time but pays a delete on
  every scenario — acceptable, since GitLab gains no rate-limit headroom that
  would justify retention.
- The two teardown paths carry different implementation surfaces: GitHub needs
  `ListAllOrgRepos` and org-scoped rate-limit reporting; GitLab needs neither.
  This keeps the GitLab path minimal rather than forcing pruning infrastructure
  onto a forge that cannot use it.
- Each scenario pays a create + install; this is roughly a wash against the old
  model, which paid create + install per lease *plus* the reset overhead this
  change removes. The change earns its keep specifically under growing CI load —
  at low concurrency the reused-repo model was adequate.
