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
--paginate`, with `status`, `filename`, `previous_filename` and
`contents_url`) and skips the review — with a `::notice::` in the job log and
an entry in `GITHUB_STEP_SUMMARY` — when every path in that list is on the
prose allowlist and every listed page is free of executable markup.

**Prose is an allowlist, not "`docs/` minus contracts".** `case` globs match
`/`, so a lone `docs/*.md` arm reaches every nested markdown file under
`docs/`, and the tree keeps growing pages whose prose is itself a contract or
is load-bearing for contributors — `docs/ADRs/`, `docs/normative/`,
`docs/contributing/`, `docs/reference/`, `docs/cli/`, `docs/architecture.md`,
`docs/.vitepress/` — which a denylist has to chase one review round at a time.
A directory is skippable only once it is listed:
`docs/guides/`, `docs/problems/`, `docs/agents/` and `docs/glossary.md`.
Everything else falls to a catch-all that stops the skip, which is also what
keeps markdown outside `docs/` reviewed: `skills/*/SKILL.md`, `AGENTS.md`, and
`CLAUDE.md` are executable agent instruction, not prose. **Lockfiles are not
skippable** either — npm resolves from `package-lock.json`, so a lockfile-only
diff can repoint a transitive dependency's `resolved` URL and `integrity` hash
without touching `package.json`, and skipping review there would remove the
only automated reader from a supply-chain-relevant change.

**A prose path is not inert.** VitePress compiles every markdown page under
`docs/` into a Vue component — `docs/.vitepress/config.ts`'s `srcExclude` only
drops icons and `testing/` — so a page can carry a root-level `<script setup>`
that runs at build time (`docs/v/index.md` already ships one, importing a
third-party package), a `<style>` block, a `head:` frontmatter key that
injects tags, `{{ }}` expressions evaluated during SSG, or bound attributes
and directives (`:prop`, `@event`, `v-*`, `on*`) on raw HTML. Each
allowlisted page is therefore read at the PR head (`contents_url`, one call
per page, only for PRs that are already prose-only by path) and keeps its
review if any of those appear outside fenced or inline code, where VitePress
renders text verbatim (`v-pre`). The scan is a fail-open heuristic: a
legitimate page that uses interpolation in prose merely stays reviewed.

Three listing hazards are handled explicitly. A truncated listing never skips
— GitHub caps `/pulls/{n}/files` at 3000 entries and stops paginating without
erroring, so a 3500-file PR whose first 3000 files are prose would otherwise
look docs-only. Renames are classified on both paths, since moving a Go file
to `docs/guides/notes.md` would otherwise present as prose. And a page that
cannot be read at the head never skips, for the same reason an unreadable
listing does not.

The listing costs one paginated `gh api` call plus one content read per
changed page, incurred only when a review would otherwise have run.

This skip is per-repo only — see Consequences for why it is not mirrored
into the per-org scaffold.

### 4. A skipped push still clears the merge labels

`docs/architecture.md` holds that each review run start clears
`ready-for-merge` together with `ready-for-review`, so merge approval is
never stale after new commits. That clearing lives in the review run itself,
so every skip above would have left a `ready-for-merge` applied to an earlier
head standing on commits nobody reviewed — a draft push is caught by GitHub's
own draft merge block, but a push to a `fullsend-no-review` PR or a
prose-only push is not.

A `clear-stale-merge-labels` job in `reusable-dispatch.yml` therefore runs on
every `pull_request_target` `synchronize` whose composite stage output is not
`review` — which covers the three skips and, as a side effect, a push by an
actor below triage that never routed — and removes both labels via the
issues API, treating a 404 as "not present" and any other failure as a job
failure so the stale label is visible. It is a job of its own rather than a
step in `route` so the route job, which parses untrusted event data, keeps
its read-only token; the per-repo shim already grants the dispatch job
`issues: write` and `pull-requests: write`. The scaffold mirrors it as a last
step of its single job, whose token is widened the same way.

## Consequences

- Automatic review stops running on drafts, on `fullsend-no-review`-labeled
  PRs, and on PRs whose every listed path — new and previous filenames alike —
  is on the prose allowlist and whose every page is free of executable markup,
  so a diff that goes unreviewed can now contain guide, agent-page, glossary or
  problem-doc prose, and cannot contain code, lockfiles, a VitePress page that
  runs anything at build time, markdown under any other `docs/` directory, or
  any markdown outside `docs/`.
- The prose skip is a judgement call rather than a free win: docs currency is
  one of the review agent's own dimensions, `docs/contributing/design-decisions.md`
  ranks external prompt injection as the top threat, and prose is where
  instruction-like text that later agents read would land — a prose-only PR now
  reaches human review with no automated reader having looked at it first.
- That residual risk is bounded by what the check can see rather than by
  trust: one non-prose path or one executable construct anywhere in the diff
  re-arms the review, an unreadable page or an unreadable or truncated file
  listing never skips, and `/fs-review` forces a review on a draft, a labeled
  PR, or a prose-only PR at any time.
- `fullsend-no-review` must be created and applied by hand until an
  `/fs-review-stop` command exists, unlike `/fs-fix-stop`.
- A push that is skipped still invalidates the previous verdict: the labels
  are cleared, but nothing re-applies them until a round runs — `/fs-review`,
  marking the PR ready, or removing `fullsend-no-review` and pushing again.
- The prose skip is per-repo only because the per-org mode is deprecated
  ([ADR 0044](0044-deprecate-per-org-installation-mode.md)) and
  `docs/contributing/workflow-contracts.md` scopes cross-mode sync to payload
  construction, stage routing, and secret threading — the draft and label
  checks, being stage routing, are mirrored into the scaffold as usual.
