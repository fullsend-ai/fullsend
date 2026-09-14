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

Accepted — supersedes the locking mechanism in
[ADR 0040](0040-org-pool-for-parallel-e2e-tests.md).

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

- **Acquire.** A run iterates over testing orgs and, within each org, tries to
  create lock repos (`lock-1`, then `lock-2`, …) until one succeeds. Repo
  creation is atomic — the first creator wins, all others get an "already exists"
  error. If all locks in all orgs are held, the run polls with backoff until one
  is released or a timeout expires.

- **Release.** On test completion (pass or fail), the run deletes the lock repo
  it created. Cleanup is registered via `t.Cleanup` so crashes still attempt
  release.

- **Stale lock recovery.** A lock repo whose `created_at` exceeds a staleness
  threshold (15 minutes) is assumed to belong to a crashed run. Any run may
  delete the stale lock and recreate it; the recreation is itself atomic, so two
  runs racing to reclaim the same stale lock do not conflict. The staleness
  threshold is applied per lock — the same stale-lock recovery ADR 0040 defined
  for its single lock, now applied to each lock in the pool.

- **Lock count and forge-specific scaling.** N is tuned per org based on the
  rate-limit budget. On **GitHub**, `N = floor(rate_limit_cap /
  calls_per_run)` — with 12,500/hr and ~3,000 calls/run, N = 4 per org, and
  capacity grows by adding orgs (five orgs → 20 concurrent locks). On
  **GitLab.com Free**, this math does not apply: the per-user cap cannot be
  raised, so a single group with a small N is the ceiling — GitLab supports the
  coordination *primitive* (correctness, no double-booking) but **not pooling
  for scale**. Any GitLab concurrency beyond one user's budget would require a
  paid tier (service accounts) or self-managed admin, both out of scope here.

### Automated org provisioning

A provisioning tool automates the lifecycle of adding a new **GitHub** testing
org:

1. **Org creation** — via the GitHub API (`POST /user/orgs`). Note this endpoint
   is only available for GitHub Enterprise-managed accounts; on standard
   github.com org creation is a manual step, so this path assumes an
   Enterprise-managed context.

2. **App installation** — GitHub Apps cannot be installed via API; this step uses
   Playwright browser automation to navigate the app installation UI for each
   required app.

3. **Inference provisioning** — configures GCP Workload Identity Federation and
   the token mint for the new org, via the repo-scoped `fullsend inference
   provision <org>/<repo> --project <gcp-project>` command.

Commands: `login`, `create-orgs`, `install-apps`, `provision`, and `setup` (runs
the provisioning steps in sequence). Automated login uses stored credentials
with TOTP; interactive login saves browser state for subsequent headless runs.

*GitLab has no equivalent provisioning path on the Free tier* — see Context:
neither user creation nor service accounts are available, so GitLab pool
expansion is not automatable and not offered.

### Forge portability

The lock mechanism depends on two forge operations, both required to reach
GitLab parity:

- **Create repo (atomic, fail-if-exists).** GitHub returns 422, GitLab returns
  409 or 400 "has already been taken". Both must be surfaced as
  `forge.ErrAlreadyExists` and matched by the pool's duplicate-detection helper
  on both forges.

- **Read repo metadata (`created_at`)** — used for stale lock detection.
  Available on both GitHub and GitLab project APIs.

No forge-specific external coordinators (GCS, Redis) are required.

## Consequences

- On GitHub, multiple runs share a testing org concurrently up to the lock cap;
  org capacity is used efficiently without risking rate-limit exhaustion, and
  adding capacity is an operational task (provision a new org, append it to the
  pool list).
- The lock count is a hard cap enforced by the number of lock repo names, not by
  counting — no race between counting and claiming.
- **GitLab.com Free cannot be pooled for concurrency.** The design supports
  GitLab for *correctness* (coordination primitive + ephemeral repos) but its
  scaling axis is GitHub-only. This is a forge constraint, not an
  implementation gap: the per-user rate limit has no API-provisionable lever on
  Free. GitLab scale, if needed, requires a paid tier or self-managed instance.
- A crashed run leaves at most one stale lock that self-heals via the staleness
  check within 15 minutes; during that window the pool has one fewer lock.
- The Playwright dependency for GitHub App installation means fully automated
  pool expansion requires a browser-capable environment. Org creation and
  inference provisioning are API-only and can run headless.
