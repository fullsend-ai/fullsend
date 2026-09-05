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
--paginate`, both `filename` and `previous_filename`) and skips the review —
with a `::notice::` in the job log and an entry in `GITHUB_STEP_SUMMARY` —
when every path in that list matches `docs/*.md` and none matches a protected
prefix.

The protected prefixes are matched *before* `docs/*.md`, because `case` globs
match `/`: a lone `docs/*.md` arm reaches every nested markdown file under
`docs/`, contracts included. They are the directories whose prose is itself a
contract — `docs/ADRs/` (a new ADR is among the things most worth reviewing),
`docs/normative/` (the event and pre-script contracts other repos build
payloads against, where the prose spec beside a JSON schema carries as much of
the contract as the schema does), `docs/contributing/` (the rules agents and
humans are held to, including `workflow-contracts.md`, which governs this very
workflow), `docs/reference/` (the field-level interface reference), and
`docs/.vitepress/` (site source — executable, and occasionally markdown).

Everything else falls to a catch-all that stops the skip, which is what keeps
markdown outside `docs/` reviewed: `skills/*/SKILL.md`, `AGENTS.md`, and
`CLAUDE.md` are executable agent instruction, not prose. **Lockfiles are not
skippable** either — npm resolves from `package-lock.json`, so a lockfile-only
diff can repoint a transitive dependency's `resolved` URL and `integrity` hash
without touching `package.json`, and skipping review there would remove the
only automated reader from a supply-chain-relevant change.

Two listing hazards are handled explicitly. A truncated listing never skips —
GitHub caps `/pulls/{n}/files` at 3000 entries and stops paginating without
erroring, so a 3500-file PR whose first 3000 files are prose would otherwise
look docs-only — and renames are classified on both paths, since moving a Go
file to `docs/notes.md` would otherwise present as prose.

The listing costs one paginated `gh api` call, incurred only when a review
would otherwise have run.

This skip is per-repo only — see Consequences for why it is not mirrored
into the per-org scaffold.

## Consequences

- Automatic review stops running on drafts, on `fullsend-no-review`-labeled
  PRs, and on PRs whose every listed path — new and previous filenames alike —
  is markdown under `docs/` outside the protected prefixes, so a diff that goes
  unreviewed can now contain guide, agent-page, glossary or problem-doc prose,
  and cannot contain code, lockfiles, ADRs, `docs/normative/`,
  `docs/contributing/`, `docs/reference/`, `docs/.vitepress/`, or any markdown
  outside `docs/`.
- The prose skip is a judgement call rather than a free win: docs currency is
  one of the review agent's own dimensions, `docs/contributing/design-decisions.md`
  ranks external prompt injection as the top threat, and prose is where
  instruction-like text that later agents read would land — a prose-only PR now
  reaches human review with no automated reader having looked at it first.
- That residual risk is bounded by what the check can see rather than by
  trust: one non-prose path anywhere in the diff re-arms the review, an
  unreadable or truncated file listing never skips, and `/fs-review` forces a
  review on a draft, a labeled PR, or a prose-only PR at any time.
- `fullsend-no-review` must be created and applied by hand until an
  `/fs-review-stop` command exists, unlike `/fs-fix-stop`.
- The prose skip is per-repo only because the per-org mode is deprecated
  ([ADR 0044](0044-deprecate-per-org-installation-mode.md)) and
  `docs/contributing/workflow-contracts.md` scopes cross-mode sync to payload
  construction, stage routing, and secret threading — the draft and label
  checks, being stage routing, are mirrored into the scaffold as usual.
