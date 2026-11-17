---
title: "164. Run the shim on pull_request for same-repo PRs"
status: Accepted
relates_to:
  - security-threat-model
  - platform-nativeness
topics:
  - workflows
  - security
  - pull-request-target
---

# 164. Run the shim on pull_request for same-repo PRs

Date: 2026-11-09

## Status

Accepted

Supersedes [ADR 0009](0009-pull-request-target-in-shim-workflows.md).

## Context

[ADR 0009](0009-pull-request-target-in-shim-workflows.md) put the PR events of
the shim workflow on `pull_request_target`. The shim is the small
`.github/workflows/fullsend.yaml` in each repository that forwards repository
events to fullsend's dispatch workflow. ADR 0009 rejected `pull_request`
because that event runs the PR branch's copy of the workflow, so a PR could
rewrite the shim to leak the dispatch token.

Two things have changed since then:

- **GitHub now blocks `pull_request_target` by default on public
  repositories.** The 2026-09-17 changelog post on workflow execution
  protections set enforcement for 2026-11-02; GitHub later moved it to
  **2026-12-02**, with brownouts (two-hour trial enforcement windows) on
  2026-11-09 and 2026-11-16. A run without an Actions event policy that allows
  the event ends in `startup_failure`, which broke fullsend repositories during
  the 2026-11-09 brownout
  ([#9221](https://github.com/fullsend-ai/fullsend/issues/9221)).
- **The dispatch token no longer exists.** The shim now calls
  `reusable-dispatch.yml` with `workflow_call` and passes repository secrets
  ([ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md),
  [ADR 0062](0062-dispatch-version-skew.md)). GitHub App tokens come from
  fullsend's central token mint
  ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)), which accepts
  only OIDC tokens whose `job_workflow_ref` names an allowed workflow host
  repository, so a rewritten shim in a consumer repository cannot point the
  mint at its own workflow. Writers to a workflow host repository are trusted
  already, because the mint does not check the ref of that workflow.

What `pull_request` exposes, by PR author:

| PR author | Workflow file that runs | Secrets | What a rewritten shim could reach |
|---|---|---|---|
| Same-repo branch (pushed by a writer, or by an agent whose token cannot edit workflow files) | PR branch | Yes | Nothing new: a writer can already push a branch whose workflow reads the same repository secrets, and an agent cannot change the shim (see Scope and accepted exposure). The shim's `pull_request_review` trigger (since [#264](https://github.com/fullsend-ai/fullsend/pull/264)) already runs the PR branch's copy today |
| Fork | PR branch | No | Nothing: by default GitHub gives fork runs no secrets and a read-only token (private repositories can change both; see Decision rule 2) |

gh-aw, GitHub's own agentic workflow tooling, limits `pull_request` and
`pull_request_target` triggers to same-repo PRs by default, using the same
guard (`head.repo.id == github.repository_id`). It admits fork PRs only
through an explicit `forks:` allowlist or author association
([github/gh-aw#72579](https://github.com/github/gh-aw/pull/72579)).

This ADR aims to keep automatic PR review and retro (the retrospective agent
that runs when a PR closes) working on public repositories after 2026-12-02,
without asking each repository or organization to add an event-policy
exception.

## Options

| Option | Fork PRs | Works by 2026-12-02 | Why not chosen |
|---|---|---|---|
| A. Keep `pull_request_target`, allow it with an event policy | Automatic | Yes | Every adopting repository or organization must create and maintain an allow policy for one workflow file. GitHub's default points the other way |
| **B. `pull_request` with a same-repo guard; forks via `/fs-review`** | `/fs-review` comment | Yes, as a normal release upgrade | **Chosen** |
| C. `pull_request` plus a `workflow_run` follow-up job | Automatic | Yes | Runs the same privileged agent on fork input, just behind another event. Ruled out in #9221 |
| D. Platform-service webhook receiver (GitHub App) that authorizes, then starts `workflow_dispatch` | Automatic, with authorization | No (weeks of work) | Brings back automatic fork review, but needs a hosted receiver. Not decided here |

## Decision

We move the shim's PR trigger from `pull_request_target` to `pull_request`
and run it only for same-repo PRs, so automatic review and retro need no
event-policy exception.

1. **Trigger.** The shim listens to `pull_request` with the types it uses
   today (`opened`, `synchronize`, `ready_for_review`, `closed`, `labeled`,
   `unlabeled`). It no longer lists `pull_request_target`.
2. **Same-repo guard, enforced twice.** The shim's dispatch job runs a
   `pull_request` event only when the PR head repository is the base
   repository (`github.event.pull_request.head.repo.id ==
   github.repository_id`). A fork PR runs its own copy of the shim and can
   delete that guard, so the dispatch workflow also refuses `pull_request`
   events whose head repository differs from the base repository and starts
   no stage for them. In vendored installs
   ([ADR 0047](0047-vendored-installs-with-vendor-flag.md)) the dispatch
   workflow is a file in the PR too, so there the only fork control is that
   GitHub sends fork PR runs no secrets and no write or OIDC token. In every
   install mode, private repositories must keep both "send secrets to
   workflows from fork pull requests" and "send write tokens to workflows from
   fork pull requests" turned off.
3. **Fork PRs.** To review a fork PR, someone authorized for `/fs-review`
   comments it: a person (not a bot) with triage access or higher, or, with
   the `owners_file` provider on, an OWNERS approver or reviewer. That runs on
   `issue_comment` from the base branch, with the authorization of
   [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)
   unchanged. Fork PRs get no automatic retro.
4. **Base checkout.** Every checkout in the dispatch workflow takes the base
   side for `pull_request`, as it does today for `pull_request_target`: the
   PR's base commit where a checkout pins one, and the default branch where a
   checkout relied on the event default (on `pull_request` that default would
   be the PR merge commit). The agent reads the PR diff as data, and the
   dispatch workflow runs no PR code. For same-repo PRs, the shim itself comes
   from the PR branch and receives repository secrets. Base checkout does not
   protect against that exposure; see Scope and accepted exposure.
5. **Old callers.** The dispatch workflow and CLI keep accepting
   `pull_request_target` and print a warning that asks the caller to upgrade
   the shim.
6. **Rollout.** The new shim ships as a required change in the next fullsend
   release, together with the dispatch and CLI changes, so no repository can
   run the new shim against a dispatch that would check out PR code.
   Repositories receive it through their normal upgrade (`fullsend repos
   install` re-renders the shim and its pin in one commit).

## Scope and accepted exposure

- **Accepted exposure.** For same-repo PRs, the PR branch's shim receives the
  repository secrets it forwards. Writer trust bounds this. Agent roles do not
  get the GitHub `workflows` permission (an existing decision), so a PR pushed
  by an agent cannot edit the shim.
- **Forwarded secrets.** The shim forwards only the secrets a run needs and
  keeps that set to the minimum.
- **Out of scope.** Two follow-up decisions are not made here:
  - restoring automatic review for authorized fork authors, for example with
    option D;
  - keeping secrets out of PR-modifiable code entirely, for example by
    fetching them at run time with mint-issued tokens.

## Forge scope

- **GitHub:** applies; this is a GitHub Actions trigger choice.
- **GitLab:** unaffected. GitLab dispatch uses the protected-branch webhook
  trigger and poller of
  [ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md).
- **Forgejo:** unaffected; Forgejo has no fullsend shim today.

## Consequences

- Repositories that take the release keep automatic review and retro for
  same-repo PRs after 2026-12-02, with the same secrets, mint tokens and review
  App as today, so inference setup and where reviews appear do not change;
  public repositories that do not upgrade lose both, while `/fs-*` commands,
  triage and the fix loop keep working.
- Fork PRs lose automatic review and retro, whoever the author is. This
  includes members and maintainers working from forks, who get automatic
  review today because they pass authorization. Someone authorized for
  `/fs-review` (rule 3) must comment it to get a review. Authors who want
  automatic review can push their branch to the repository instead of a fork.
  Restoring automatic review for authorized fork authors (for example with
  the webhook receiver of option D) is a separate follow-up decision.
- Retro on `closed` now runs only for same-repo PRs, whose authors already
  have write access, so an external author closing their own fork PR no longer
  triggers retro.
- Two kinds of same-repo PR get no automatic review: GitHub runs no
  `pull_request` workflow while a PR has merge conflicts, and
  Dependabot-triggered runs get only Dependabot secrets, so dispatch skips
  them with a notice. Someone authorized for `/fs-review` comments it
  instead.
- Base checkout and the fork refusal become the dispatch workflow's security
  invariants, replacing ADR 0009's "the shim never checks out PR code".
  Same-repo PR authors are trusted with the secrets the shim receives, as
  writers already are.
