---
title: "171. Review fork PRs from authorized authors through a scheduled poll"
status: Accepted
relates_to:
  - security-threat-model
  - platform-nativeness
  - code-review
topics:
  - workflows
  - security
  - polling
  - dispatch
  - pull-request-target
---

# 171. Review fork PRs from authorized authors through a scheduled poll

Date: 2026-11-12

## Status

Accepted

Builds on [ADR 0164](0164-run-the-shim-on-pull-request-for-same-repo-prs.md),
which left this decision open, and on the poll architecture of
[ADR 0063](0063-polling-based-work-discovery.md).

## Context

[ADR 0164](0164-run-the-shim-on-pull-request-for-same-repo-prs.md) moves the
shim, each repository's `.github/workflows/fullsend.yaml`, from
`pull_request_target` to `pull_request`. A `pull_request` run from a fork gets
no secrets and a read-only token, so the shim skips every fork PR. Before, a
fork PR was reviewed automatically when its author passed the authorization of
[ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md). Now it
is reviewed only when someone authorized comments `/fs-review`, once per push.
ADR 0164 leaves restoring automatic review as a follow-up decision.

Most human PRs in fullsend's own repositories are affected. In the 30 days to
2026-11-12:

| Repository | Human PRs | From forks | Fork PRs, author triage or higher | Of those, triage only |
|---|---|---|---|---|
| `fullsend-ai/fullsend` | 166 | 105 | 87 | 31 |
| `fullsend-ai/agents` | 48 | 24 | 21 | 5 |

ADR 0164 suggests pushing the branch to the repository instead of a fork.
Authors with only triage access have no push access, so they cannot.

fullsend already finds work on a schedule. A scheduled workflow runs
`fullsend poll` for Jira (ADR 0063), and the GitLab poller dispatches review
for merge requests
([ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md)). fullsend's own
contributor vouch check moved from `pull_request_target` to a ten-minute
schedule for this same GitHub change
([#9245](https://github.com/fullsend-ai/fullsend/issues/9245)); over 200 runs
the median gap between runs was 11 minutes and 90% of gaps were under 18.

gh-aw, GitHub's own agentic workflow tooling, has no automatic run with
secrets for fork PRs in public repositories, and its guidance says not to use
`workflow_run` "as a way to bypass fork safeguards"
([github/gh-aw#76299](https://github.com/github/gh-aw/pull/76299)). It does
run scheduled agents that read pull requests, forks included, through the API.

This ADR aims to give fork PRs from authorized authors an automatic review
again, without a `pull_request_target` exception, without running any
PR-controlled code or workflow file, and without a new hosted service.

## Options

| Option | Delay before review | What it needs | Why not chosen |
|---|---|---|---|
| A. Keep `pull_request_target` for fork PRs, allowed by an Actions event policy | None | An admin-maintained policy per repository or organization | Works against GitHub's default and adds a second trigger mode; ADR 0164 rejected it as the main route |
| B. Do nothing: `/fs-review` on every push | Until someone comments | Nothing | Most human PRs above would need a comment on every push |
| C. `pull_request` plus a `workflow_run` follow-up, or `workflow_run` as a wake-up for option E | None | A second workflow | Ruled out in [#9221](https://github.com/fullsend-ai/fullsend/issues/9221) and ADR 0164: a fork-controlled event would start the privileged run |
| D. Hosted webhook receiver (GitHub App) that authorizes, then starts the workflow | None | A new hosted service, App webhook setup, an App permission to start workflows | Weeks of work and a new trust boundary; not decided here (rule 7) |
| **E. Scheduled poll for fork PRs** | About 5 to 20 minutes | A scheduled workflow in the repository | **Chosen** |

## Decision

We add a scheduled poll that finds fork PRs from authorized authors and
starts review for them, so those PRs get an automatic review without any
fork-triggered workflow run.

1. **Poll input.** `fullsend poll` gains a GitHub pull request input, built on
   the driver architecture of ADR 0063. Each cycle lists the repository's open
   PRs whose head repository differs from the base repository.
2. **Authorization.** A PR qualifies only when both hold:
   - its author passes the authorization that automatic review applied to the
     PR author before ADR 0164
     ([ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)
     and the [authorization contract](../normative/authorization/v1/)); the
     poll reads the author's role on every cycle;
   - the head repository is owned by the PR author. A fork owned by an
     organization or by another user waits for `/fs-review`, because the poll
     cannot tell who pushed there.
3. **One review per head commit.** The poll starts review for a qualifying
   PR's current head commit only when that commit has no review finished or
   running, whether automatic or from `/fs-review`. It decides this from
   records that fullsend's own review App wrote, never from comment text
   anyone can post. A head commit whose review keeps failing is retried a
   bounded number of times, then left to `/fs-review`. Draft PRs are reviewed
   like other PRs, as same-repo drafts are today.
4. **Bounded start.** A cycle starts a bounded number of reviews. When a
   repository turns the poll on, PRs that were last updated before that moment
   are not picked up until their next push.
5. **Where it runs.** A scheduled workflow on the default branch runs the poll
   and starts review through the dispatch workflow. A review started by the
   poll runs the same way as one started by `/fs-review`, and the two
   serialize for the same PR
   ([ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)).
   Dispatch keeps the rules of ADR 0164: every checkout takes the base side,
   and the agent reads the PR diff as data. No job runs a workflow file or
   code from the fork.
6. **Off by default.** A repository turns the poll on in its fullsend
   configuration. `fullsend repos install` writes the scheduled workflow while
   the setting is on and removes it otherwise. The schedule is every five
   minutes, GitHub's shortest interval.
7. **Relation to a webhook receiver.** This ADR does not decide option D. If a
   receiver is added later, it starts the same review path sooner, and this
   poll stays to catch what the receiver misses, as the GitLab poller does in
   ADR 0125.

### Out of scope

- Retro, the agent that runs when a PR closes, for fork PRs.
- Review started by the `ready-for-review` label on a fork PR.
- The same-repo PRs that ADR 0164 leaves to `/fs-review`: Dependabot PRs and
  PRs with merge conflicts.

## Forge scope

- **GitHub:** applies. The poll input and the scheduled workflow are GitHub
  only.
- **GitLab:** not covered. The GitLab poller reviews merge requests on open
  and on pushes to same-project merge requests; it skips pushes to fork merge
  requests because it cannot tell who pushed
  ([ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md)). This ADR does
  not change that.
- **Forgejo:** unaffected; Forgejo has no fullsend dispatch today.

## Consequences

- With the poll on, a fork PR from an authorized author is reviewed about 5
  to 20 minutes after it opens and after each push, later when GitHub delays
  scheduled runs; `/fs-review` remains the way to get a review at once.
- The poll adds no hosted service, no App permission and no Actions event
  policy, and a fork cannot change its workflow file or code because both come
  from the default branch.
- Someone the author gave push access to their own fork can still start a
  review by pushing, as they could before ADR 0164; requiring an author-owned
  fork removes shared and organization-owned forks from that exposure.
- Each repository with the poll on runs a short job every five minutes, and
  GitHub turns scheduled workflows off in a public repository after 60 days
  without activity until someone re-enables them.
- Fork PRs from other authors, and from forks the author does not own, still
  wait for `/fs-review` from someone authorized, as ADR 0164 decided.
