---
title: "84. Lease uniquely named repos for DEV behaviour tests"
status: Accepted
relates_to:
  - testing-agents
  - agent-infrastructure
topics:
  - behaviour-tests
  - e2e
  - mint
  - wif
---

# 84. Lease uniquely named repos for DEV behaviour tests

Date: 2026-11-13

## Status

Accepted

Builds on [ADR 0082](0082-workflow-host-allow-list.md) (workflow-host
allow-list) and [ADR 0066](0066-behaviour-tests-with-gherkin-and-drivers.md)
(behaviour tests). Leaves the org pool of
[ADR 0040](0040-org-pool-for-parallel-e2e-tests.md) in place.

## Context

DEV behaviour tests lease fixed names, `test-repo-01` … `test-repo-12`, in
22 pool orgs (`halfsend-01` … `halfsend-22`). A lease creates its repo and
release deletes it ([#7398](https://github.com/fullsend-ai/fullsend/pull/7398),
[#8578](https://github.com/fullsend-ai/fullsend/issues/8578)). Because a name is
a slot, runs also need a claim loop, a slot GC
([#8621](https://github.com/fullsend-ai/fullsend/issues/8621)) and a shared
`e2e-lock` bridge for older runners
([#8620](https://github.com/fullsend-ai/fullsend/issues/8620)). In windows
where an org ran out of quota, `lock`, `claim` and `gc` made 56% of counted
calls, against 15% in healthy windows. 21% of DEV jobs got a primary-quota
403 ([#9154](https://github.com/fullsend-ai/fullsend/issues/9154)). Pool orgs
hold so few repos that they stay at GitHub's base App rate limit of 5,000
requests an hour. Each run creates and deletes about 86 repos, and reusing a
name right after deleting it exposes GitHub's eventual consistency
([#7839](https://github.com/fullsend-ai/fullsend/issues/7839),
[#7860](https://github.com/fullsend-ai/fullsend/issues/7860)).

Two things tie runs to fixed names. First, DEV installs are vendored
([ADR 0047](0047-vendored-installs-with-vendor-flag.md)), so
`job_workflow_ref` names the leased repo. The job's preview mints trust only
exact `WORKFLOW_HOST_REPOS` entries (ADR 0082), and that list is packed at
deploy time into Cloudflare variables capped at 5 KB. A repo named after
deploy is refused. Non-vendored installs avoid this, and STAGE uses them
pinned to `main`, but they cannot install a fork PR's code
([#6864](https://github.com/fullsend-ai/fullsend/issues/6864)). Second, per-repo
inference WIF keys the provider, its condition and its Vertex AI grant on the
repo name. A new name per lease would mean IAM writes per scenario and
provider churn ([#7868](https://github.com/fullsend-ai/fullsend/issues/7868),
[#6179](https://github.com/fullsend-ai/fullsend/issues/6179)).

#7868 compared the options. The maintainers' meeting of 2026-10-01 agreed on
unique repo names with org-level WIF while keeping the org pool. Moving to a
single org (#6864) is a separate question that this ADR does not decide.
This ADR aims to let a DEV lease use a repo name nobody has used before,
without redeploying a mint or writing IAM per lease, while the repo-scoped
credential path that real installs use stays tested.

## Options

How the mint trusts a workflow host it has not seen before is the only
choice with real alternatives:

- **Store the exact host list somewhere larger** and write each new name
  before its first mint call. This adds a write per lease and a storage
  dependency to the production mint code path. Workers KV can also return
  stale reads for up to 60 seconds after a write.
- **Redeploy the mint per lease.** This puts a deploy, and Cloudflare API
  quota, on every scenario's path.
- **Non-vendored installs.** These cannot test fork PR code, so they are
  rejected for DEV. They remain open under #6864.
- **Trust the whole org (`<org>/*`).** This also trusts `e2e-lock`, the GC
  repo and the probe repos, and leaves no name for the "unapproved host"
  denial scenario.
- **Build pattern support only into test builds of the mint.** A production
  build then cannot trust a pattern at all. This is rejected because
  behaviour tests would no longer run the same mint binary as production.
  The runtime setting, with the enforced gate under Decision, keeps one
  binary.
- **Trust a name prefix, in test mints only** (chosen).

## Decision

We give every DEV behaviour-test lease a new, never-reused repo name, and
let test mints trust it by prefix. This removes name reuse and slot contention
from DEV runs.

**Names.** A lease creates `<org>/bt-<id>`, where `<id>` is unique to the
lease. `<id>` must be short enough that derived repos (`-fork`, `-mint-host`,
`-url-harness-host`) stay within GitHub's 100-character limit. The repo
description keeps today's ownership marker. A unique name cannot collide, so
a lease makes no claim pass.

**Mint workflow-host trust.** The mint gets a separate, test-only setting
that holds `owner/prefix*` patterns. Each pattern needs a literal prefix of at
least three characters after the `/`, so a bare `*` and `owner/*` are
refused. A `job_workflow_ref` is accepted if it matches the upstream, an
exact `WORKFLOW_HOST_REPOS` entry, or one of these patterns. ADR 0082's
basename gate and every other check still apply.

Only behaviour-test preview mints set this, with `<org>/bt-*` for each DEV
pool org. The deploy flag that writes it is marked test-only, and
`fullsend mint workflow-host` keeps exact entries. The mint enforces the
gate itself: it refuses a non-empty pattern setting unless it is deployed
as a behaviour-test preview, and an absent or empty setting adds no pattern
hosts. The follow-up mint work must implement this check and test it, along
with owner and repository boundary matching, malformed patterns, an empty
setting, rejection outside a preview deploy, and continued enforcement of
caller enrollment and workflow basenames. The probe for an
unapproved host moves to a name outside the prefix, so that denial stays
tested.

This trusts more names but no more people. Anyone who can create `bt-x` in a
pool org can already create a free `test-repo-NN`, which today's mints
trust. Preview mints also last only one job.

**Inference WIF.** Each DEV pool org gets one org-scoped WIF provider and one
Vertex AI grant to its principals, created once when the org is provisioned.
The provider's attribute condition admits only `<org>/bt-*` repositories. The
driver passes it to `fullsend github setup` with `--inference-wif-provider`
and never provisions WIF per lease.

**Repo-scoped path.** `test-repo-01` in each DEV pool org stays a fixed-name
slot. It keeps its existing per-repo provider and an exact
`WORKFLOW_HOST_REPOS` entry. Scenarios that call Vertex (today the
`runtime-pi` capability) lease these 22 slots through the existing claim.
That keeps the per-repo provider path and the exact-entry host check under
test, with no new providers.

**Cleanup.** Release disables GitHub Actions on the repo and leaves it in
place. A sweep runs at most once per org per job, under the existing GC
collector lease. It deletes marked `bt-` repos and their derived repos that
are older than the slot-reclaim age (job limit plus margin, 60 minutes
today). It deletes the oldest first, and only while the org keeps at least
180 repos. Before counting a repo toward that floor, it disables Actions on
any repo a crashed job left enabled.

The floor of 180 has this basis: an App installation gets 5,000 requests an
hour plus 50 per repository over 20, capped at 12,500
([GitHub docs](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/rate-limits-for-github-apps)).
The cap is reached at 170 repos, and 180 leaves a
margin of ten. It is an initial value, to revisit if GitHub changes the
formula.

**Lock, claims and GC.** Unique-name leases skip the slot claim and slot GC,
because no name is contended and no slot is left occupied. The sweep replaces
GC for `bt-` repos. Org choice still skips orgs whose quota headers are near
the reserve. Claim and GC remain only for the 22 repo-scoped slots. This ADR
does not change the shared `e2e-lock` bridge. Leases keep joining it until
its own removal condition holds (no runner that takes the exclusive lock can
still start), and then it is removed with no replacement.

STAGE keeps its slots: it is non-vendored, and its durable mint lists exact
callers. Moving STAGE would be a separate decision.

## Forge scope

GitHub only. Both constraints are GitHub mechanisms: vendored
`job_workflow_ref`, and App rate limits that grow with repository count, plus
GitHub OIDC claims in the WIF condition. GitLab behaviour drivers exist, but
the suite is not wired to live GitLab and does not use the DEV pool orgs.
Forgejo has no behaviour driver. Either forge needs its own decision when it
gets a live pool.

## Consequences

- DEV leases never reuse a name, so the delete-then-recreate races and their
  workarounds leave the hot path. A new repo ID still lags on dispatch-side
  permissions ([#6701](https://github.com/fullsend-ai/fullsend/issues/6701)),
  which dispatch must keep handling.
- Pool-org quota rises from 5,000 to 12,500 requests an hour once an org
  holds 170 repos. Most leases also stop polling for slots. Those two causes
  are behind the 403s in #9154.
- In the steady state, creates and deletes are not reduced, but deletes move
  off the scenario path into one batched sweep.
- The mint gains a host-pattern setting. It refuses the setting outside a
  behaviour-test preview deploy, and `mint status` must report it. The
  check is code in the mint, but the preview marker is still deploy
  configuration, so an operator with deploy access could set it on a
  production mint. A production deploy policy that rejects both the preview
  marker and any host-pattern setting, with a negative deployment test, would
  close that gap. It is not part of this decision and is left to a tracked
  follow-up. The exact-entry commands never write the setting.
- The org-scoped provider admits any `bt-` repo with Actions enabled, so
  disabling Actions at release and in the sweep is required, not optional.
  The test inference gateway's admission rule (`pool_regex`) must add the DEV
  `bt-` prefix. It keeps the existing `test-repo-NN` rule for STAGE and the
  retained DEV slots, and the pinned org-ID checks stay unchanged.
