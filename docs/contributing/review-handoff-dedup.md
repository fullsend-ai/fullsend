# Review handoff dedup

Code-agent PR creation currently fires two review-eligible GitHub events for
the same head SHA: `pull_request_target.opened` and
`pull_request_target.labeled` (`ready-for-review`). The per-PR review
concurrency group has `cancel-in-progress: true`, so one run cancels the
other after setup has started. This is the regression in
[#7384](https://github.com/fullsend-ai/fullsend/issues/7384).

This document is the rollout contract for the fix. The coder app cannot push
`.github/workflows/` changes, so GitHub Route-job edits ship as a maintainer
patch. The agents-repo post-script must supply provenance the webhook does
not otherwise carry.

## Required behavior

- Opening an eligible PR initiates review.
- Opening a PR plus its automatic code-agent `ready-for-review` label
  produces **one** effective automatic review of that initial revision, in
  either event order, including concurrent delivery.
- New head commits (`synchronize`, including fix-agent pushes) initiate a
  new review and may supersede an old review.
- Explicit `/fs-review` and a deliberate `ready-for-review` application
  (no provenance label on the event snapshot) can request another review of
  the same revision.
- Duplicate handling is **not** inferred from a bot username, a `[bot]`
  suffix, or the mere presence of a previous review of the same SHA.
- Authorization stays independent of duplicate handling. The `[bot]`
  exemption on `opened|synchronize|ready_for_review` remains: GitHub Apps
  often have no collaborator `role_name`, which is why that exemption
  exists. Removing it recreates the historical bot-authorization failure.
- Duplicate work is prevented at routing time, before review-agent setup.
  Do not hide cancellation comments, allow both reviews to run, or disable
  cancellation of obsolete reviews.

`ready-for-review` remains a supported handoff/state label. The code
post-script continues to apply it.

## Provenance

The producer adds `fullsend-auto-review-handoff` **before**
`ready-for-review`. A `labeled` / `label_changed` event for
`ready-for-review` whose snapshot includes that provenance label is the
automatic creation handoff and must not dispatch review. The `opened` path
supplies the single initial review.

The label is not a routing trigger of its own. Do not add it to GitLab
`routableLabels` or to agents `MANDATORY_LABELS`.

### Agents-repo change (`fullsend-ai/agents`)

The existing `labeled` webhook does not carry an explicit "this is the
automatic creation handoff" flag. The post-script must add one.

In `scripts/post-code.src.sh`, after `forge_create_pr`:

1. Create the provenance label with `forge_create_label` (not
   `forge_ensure_label` — that helper no-ops for non-mandatory names).
   Suggested description: `Internal: automatic code-agent review handoff;
   not a dispatch trigger.`
2. `forge_add_label "fullsend-auto-review-handoff"` **first**.
3. `forge_add_label "ready-for-review"` **second**, so the GitHub
   `labeled` snapshot for `ready-for-review` includes provenance.
4. **GitHub only:** remove `fullsend-auto-review-handoff` immediately
   afterwards. GitHub webhook payloads are snapshots at event time, so the
   already-queued `labeled` event still carries provenance, and a later
   explicit re-application is not blocked by a leftover marker.
5. **GitLab:** leave provenance in place. The cron poller may observe the
   label set after both adds; stripping it in the same script would drop
   provenance from the poll snapshot. `scripts/pre-review.src.sh` must
   remove `fullsend-auto-review-handoff` (and should continue to treat
   `ready-for-review` as a state marker) when a review run starts.

Do not skip applying `ready-for-review`. Do not suppress bot-applied labels
in the dispatcher by username.

Cover in `scripts/post-code-test.sh`:

- Provenance is applied before `ready-for-review`.
- GitHub removes provenance after both adds; GitLab does not.
- Auto-merge and assignee still receive the correct PR/MR number.

Cover in `scripts/pre-review-test.sh`:

- Review start removes leftover provenance on both forges.

## Fullsend changes in this repository

| Piece | Status |
|-------|--------|
| `internal/dispatch` HarnessRouter skip on provenance | Implemented in this PR (GitLab poller and any other `EventRouter` consumer) |
| `.github/scripts/review-handoff.sh` decision helper + tests | Implemented in this PR |
| GitHub Route job in `reusable-dispatch.yml` | **Maintainer patch** (coder app cannot push workflow files) |
| Scaffold `dispatch.yml` (keep in sync per [workflow contracts](workflow-contracts.md)) | Same maintainer patch |
| Agents-repo post-script / pre-review | Separate PR on `fullsend-ai/agents` |

### Maintainer patch

File: [`patches/7384-review-handoff-dedup.patch`](patches/7384-review-handoff-dedup.patch)

The Route job sparse-checkouts only `.fullsend/config.yaml`, so it cannot
source `.github/scripts/review-handoff.sh`. The patch inlines the same
check with the existing `has_label` helper.

From the repository root, with a token that has the `workflows`
permission:

```bash
git apply --check docs/contributing/patches/7384-review-handoff-dedup.patch
git apply docs/contributing/patches/7384-review-handoff-dedup.patch
go test ./internal/scaffold/ ./internal/dispatch/
bash .github/scripts/review-handoff-test.sh
pre-commit run --files \
  .github/workflows/reusable-dispatch.yml \
  internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml
```

`review-handoff-test.sh` also runs `git apply --check` and applies the
patch to temp copies until the workflow files already contain the skip, so
re-running it after the maintainer commit stays green.

The patch does **not** change concurrency groups or `cancel-in-progress`.
It does **not** add blanket bot-label suppression.

## Coordinated rollout

Scope: supported **per-repo** GitHub installations. Per-org dispatch is
deprecated ([ADR 0044](../ADRs/0044-deprecate-per-org-installation-mode.md));
the scaffold copy is patched only to keep routing logic in sync.

Safe independent deploys:

| Agents pin | Workflow patch | Result |
|------------|----------------|--------|
| old (no provenance) | old | Today's duplicate (unchanged) |
| new (provenance) | old | Provenance label is inert; duplicate continues |
| old (no provenance) | new | Labeled path still dispatches (no provenance); duplicate continues |
| new | new | One automatic review of the initial revision |

Recommended order:

1. Merge this fullsend PR (router, tests, docs, patch file).
2. Apply the maintainer workflow patch and merge it.
3. Land the agents-repo post-script / pre-review change and pin
   installations that should pick it up.

Older pinned installations keep working. They do not pick up either half
until they move the pin.

GitLab: the poller already entity-dedups the same stage in one cycle, and
does not currently emit MR `ready-for-review` label events. The Go router
skip still applies if MR label discovery is added later. GitHub
`synchronize` after a fix-agent push is unchanged.

## Verification

Local, in this repo:

```bash
go test ./internal/dispatch/ -count=1
bash .github/scripts/review-handoff-test.sh
```

Those tests cover both creation/label orders, concurrent delivery of the
automatic pair, subsequent `synchronize` / fix-agent pushes, and explicit
same-revision `/fs-review` and labeled requests. They do not replace a
live GitHub delivery of `opened` and `labeled` one second apart; that
requires the maintainer patch plus the agents pin.

Until the workflow patch and agents-repo change are applied, this issue is
**not** fixed in production.
