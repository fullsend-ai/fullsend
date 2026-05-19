---
title: "40. Org pool for parallel e2e tests"
status: Accepted
relates_to:
  - testing-agents
topics:
  - e2e
  - ci
  - parallelism
---

# 40. Org pool for parallel e2e tests

Date: 2026-05-19

## Status

Accepted

## Context

The e2e tests exercise the full admin install/uninstall flow against a live
GitHub org using Playwright browser automation
([ADR 0010](0010-stored-session-for-e2e-browser-auth.md)). Each run creates
GitHub Apps, repos, secrets, variables, and enrollment PRs — then tears them
all down. These operations are destructive and non-reentrant: two concurrent
runs targeting the same org will collide on shared resources (`.fullsend` repo,
org secrets, app slugs) and fail unpredictably.

With a single test org, CI runs are serialized. A push to `main` and an
in-flight PR both trigger e2e, but only one can proceed; the other waits or
fails. As the contributor count grows this becomes a bottleneck.

## Decision

Maintain a pool of identically-configured GitHub orgs (currently `halfsend-01`
through `halfsend-06`). Each e2e run acquires exclusive access to one org
before proceeding, using a lightweight distributed lock implemented as a
purpose-built repo (`e2e-lock`) within each org.

**Acquisition:** The test runner scans the pool in order, attempting to create
the `e2e-lock` repo in each org. Repo creation is atomic on GitHub — if it
succeeds, the caller holds the lock. A `README.md` in the lock repo contains
the run's UUID for ownership verification. If all orgs are locked, the runner
falls back to polling with a configurable timeout (`E2E_LOCK_TIMEOUT`,
default 2 minutes).

**Release:** On test completion (pass or fail), the runner deletes the
`e2e-lock` repo, but only after verifying the UUID matches — preventing a
run from releasing another run's lock.

**Staleness:** If a runner crashes without releasing its lock, the lock repo's
`created_at` timestamp provides an age signal. A fresh lock (under 1 minute
old) resets the wait timer; an old lock is assumed stale and can be
force-acquired by a subsequent run.

Adding new orgs to the pool requires only provisioning the GitHub org (with
the shared test account as owner) and appending its name to the `orgPool`
slice in the test code. No architectural changes are needed.

## Consequences

- Up to N e2e runs execute in parallel, where N is the pool size.
- Each org must be pre-provisioned with the test account as owner and a
  `test-repo` for enrollment testing.
- A crashed run leaves a stale lock that self-heals via the age-based
  staleness check.
- The single `botsend` test account and its stored browser session are shared
  across all orgs; session export and PAT creation remain per-run.
- Pool expansion is an operational task (provision org, update one slice
  literal), not an architectural change.
