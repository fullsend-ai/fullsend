---
title: "96. Skip provably unnecessary review dispatch"
status: Accepted
relates_to:
  - agent-architecture
  - code-review
topics:
  - routing
  - dispatch
  - review-agent
  - cost
  - labels
---

# 96. Skip provably unnecessary review dispatch

Date: 2026-08-25

## Status

Accepted

Builds on [ADR 0034](0034-centralized-shim-routing-via-dispatch.md)
(centralized dispatch routing, and the `fullsend-no-fix` label pattern this
ADR mirrors) and [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)
(authorization on all agent dispatch paths — unaffected by this change).

## Context

The review agent's automatic triggers — `pull_request_target`
opened/synchronize/ready_for_review, the `labeled` ready-for-review path, and
the `issues` event's ready-for-review handoff — dispatch to the `review`
stage for every matching event that passes the ADR 0054 authorization gate.
There is no way to skip a review that provably shouldn't run:

1. **Draft PRs.** Opening a draft or pushing to it dispatches a review
   immediately, even though the PR isn't meant to be looked at yet. The
   review then runs again when the PR is marked ready — the draft-time run
   is pure waste.
2. **PRs where review isn't wanted right now.** The fix agent has
   `fullsend-no-fix` (ADR 0034) as a per-PR kill switch. No equivalent
   exists for review — a maintainer who wants to silence automatic review
   runs on a PR (e.g. a long-lived draft-adjacent branch, or a PR under
   active discussion) has no lever.
3. **Documentation-prose-only changes.** A PR that touches only prose under
   `docs/` gives the review agent nothing substantive to assess, but still
   consumes a full inference run.

Each of these is inference spend with no corresponding value — the routing
hygiene equivalent of not compiling code that can't have changed.

## Decision

Three additive skips in `reusable-dispatch.yml`'s "Determine stage" step.

### 1. Draft skip

`opened` / `synchronize` do not route to `review` while the PR is a draft
(`PR_IS_DRAFT`, newly threaded from `github.event.pull_request.draft`).
`ready_for_review` is never gated on draft status — a PR is not a draft by
the time that event fires, and it is precisely the "now it's ready" signal
the skip exists to wait for. An explicit `/fs-review` comment still works on
drafts: a human asking for a review takes priority over the default.

### 2. `fullsend-no-review` label

Mirrors `fullsend-no-fix`. When present on the PR, automatic review dispatch
is skipped on all three automatic paths — `pull_request_target`
opened/synchronize/ready_for_review, the `labeled` ready-for-review path, and
the `issues`-event ready-for-review handoff. As with `fullsend-no-fix`,
`/fs-review` bypasses it: the label only suppresses automatic triggers, never
the explicit command.

Unlike `fullsend-no-fix`, there is not yet an `/fs-review-stop` comment
command to apply the label with a collaborator-permission check. For now the
label must be created and applied by hand (via the GitHub UI or API), which
is itself an implicit permission gate — applying any label already requires
write access. Adding a `stop-review` job that mirrors `stop-fix`'s
permission-checked flow in `fullsend.yaml` (and its scaffold templates) is
straightforward but a large-enough addition (a new job, plus the matching
scaffold/template updates) to warrant its own change; it is left as follow-up
work rather than bundled into this routing-hygiene pass.

### 3. Documentation-prose-only skip

When a review would otherwise be dispatched, a new `docs-lockfile-check` step
fetches the PR's changed-file list (`gh api .../pulls/{number}/files
--paginate`) and skips the review — with a `::notice::` in the job log and an
entry in `GITHUB_STEP_SUMMARY` — when every changed file matches
`docs/*.md`, excluding `docs/ADRs/**`.

The pattern is deliberately narrow, because `case` globs match `/`:

- `docs/*` would swallow the executable TypeScript under `docs/.vitepress/`
  and `docs/normative/**`, which publishes a normative event schema other
  repos build payloads against.
- A bare `*.md` would swallow `skills/*/SKILL.md`, `AGENTS.md`, and
  `CLAUDE.md` — markdown that is executable agent instruction, not prose.
- ADRs are excluded because a new ADR is among the things most worth
  reviewing.
- **Lockfiles are not skippable.** npm resolves from `package-lock.json`, so
  a lockfile-only diff can repoint a transitive dependency's `resolved` URL
  and `integrity` hash without touching `package.json`. Skipping review there
  would remove the only automated reader from a supply-chain-relevant
  change. Renovate PRs therefore still get reviewed.

A truncated file listing (GitHub caps `/pulls/{n}/files` at 3000 entries and
stops paginating without erroring) never skips: a 3500-file PR whose first
3000 files are prose would otherwise look docs-only.

This skip is per-repo only — see Consequences for why it is not mirrored
into the per-org scaffold.

## Consequences

- Fewer wasted review runs: drafts, `fullsend-no-review`-labeled PRs, and
  documentation-prose-only PRs no longer trigger automatic review dispatch,
  reducing inference cost with no loss of coverage — nothing that would have
  been usefully reviewed is skipped.
- `/fs-review` remains a complete escape hatch for all three skips. A
  maintainer can always force a review on a draft, a no-review-labeled PR, or
  a prose-only PR by commenting.
- `fullsend-no-review` currently requires manual label creation and
  application — there is no `/fs-review-stop` command yet, unlike
  `/fs-fix-stop`. Tracked as follow-up work.
- The documentation-prose skip is per-repo-only by design. The per-org
  installation mode is deprecated ([ADR 0044](0044-deprecate-per-org-installation-mode.md)),
  and `docs/contributing/workflow-contracts.md` scopes the cross-mode sync
  requirement to jq payload construction, stage routing, and input/secret
  threading — this step is none of those. Adding a new feature to a chain
  scheduled for removal was judged not worth the duplicated maintenance. The draft skip and the `fullsend-no-review`
  label check, being pure "Determine stage" routing logic (the part
  `docs/contributing/workflow-contracts.md` requires to stay identical
  across installation modes), are mirrored into the scaffold as usual.
- Adds one paginated `gh api` call per review dispatch to fetch the
  changed-file list — incurred only when a review would otherwise run, so it
  never adds cost on top of a dispatch that was going to happen anyway.
