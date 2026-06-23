---
title: "34. Centralized shim routing via dispatch.yml"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - shim-workflow
  - dispatch
  - routing
  - workflow-call
---

# 34. Centralized shim routing via dispatch.yml

Date: 2026-05-07

## Status

Accepted

## Context

The target-repo shim workflow (`fullsend.yaml`) routes GitHub events to agent
stages by mapping events to `dispatch.yml` calls. Each stage requires its own
job in the shim: an `if:` filter, a `jq` payload builder, and a `gh workflow
run` dispatch step. The shim currently has 9 jobs (8 dispatch + `post-run-link`) and ~470 lines.

Every time a new stage is added, every enrolled repo's shim must be updated.
Adding the retro stage required 2 new jobs (~80 lines) in every target repo.
Command matching uses a 3-line pattern (exact match, space-delimited args,
newline-delimited body) repeated 6 times across the shim.

The routing logic — "which events map to which stages" — is conceptually
part of the centralized fullsend system, not target-repo configuration. Yet
it is duplicated in every enrolled repo and drifts when shims are not
updated after scaffold changes.

The token mint work
([ADR 0029 PR #655](https://github.com/fullsend-ai/fullsend/pull/655))
migrates the shim from `workflow_dispatch` + `gh workflow run` to native
`workflow_call`. This removes the dispatch credential (`FULLSEND_DISPATCH_TOKEN`)
and the imperative dispatch boilerplate, creating the natural opportunity to
also move routing into `dispatch.yml`.

[ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md)
addresses `.fullsend` agent workflow drift via reusable workflows but
explicitly leaves the shim unchanged. This ADR addresses the shim side.

## Options

### Option A: Per-stage jobs in the shim (status quo pattern)

Each stage gets its own job in the shim with an `if:` filter, payload builder,
and dispatch call. With `workflow_call` (token mint), the dispatch mechanism
changes but the structure stays the same: 9 jobs, ~350 lines, routing in the
shim.

New stages require updating every enrolled repo's shim.

### Option B: Merge paired jobs

Combine jobs that dispatch the same stage (`dispatch-fix-bot` +
`dispatch-fix-human` → `dispatch-fix`, `dispatch-retro` +
`dispatch-retro-command` → `dispatch-retro`). Reduces to 6 jobs, ~300 lines.
Quick win, no contract changes. New stages still require shim updates.

### Option C: Centralized routing in dispatch.yml

The shim becomes a single `dispatch` job that forwards the event context to
`dispatch.yml` via `workflow_call` without specifying a stage. `dispatch.yml`
examines `event_type` and the event payload to determine which stage to
trigger, then fans out to the matching agent workflow.

The shim shrinks to ~50 lines (one `dispatch` job + one `stop-fix` job).
`dispatch.yml` gains ~50 lines of routing logic. New stages require zero
changes to enrolled repos.

## Decision

Use Option C. Move event-to-stage routing into `dispatch.yml`. The shim
forwards event context via `workflow_call` without determining the stage.

The shim has three jobs:

1. **`dispatch`** — builds a universal minimal payload from safe GitHub context
   fields and calls `.fullsend/dispatch.yml` via `workflow_call` with
   `event_type`, `event_action`, `source_repo`, `trigger_source`, and
   `event_payload` inputs. Filters out bot comments via `if:` to avoid
   unnecessary invocations.

2. **`stop-fix`** — adds the `fullsend-no-fix` label and posts a comment.
   This job acts directly on the target repo and does not dispatch to
   `.fullsend`. It stays in the shim (~25 lines).

3. **`post-run-link`** — posts a link to the dispatched workflow run as a
   comment on the triggering issue or PR. This job runs after `dispatch`
   completes and stays in the shim.

`dispatch.yml` gains a routing step that maps `event_type` + payload fields to
a stage name:

- `issue_comment` with `/fs-triage`, `/fs-code`, `/fs-review`, `/fs-fix`, `/fs-retro`, `/fs-prioritize`
  commands → corresponding stage
- `issue_comment` on `needs-info` issue from non-bot → `triage`
- `issues` + `labeled` with `ready-to-code` → `code`
- `pull_request_target` opened/synchronize/ready_for_review → `review`
- `pull_request_target` closed → `retro`
- `pull_request_review` with bot `changes_requested` → `fix`

> **Note (2026-06):** Per-repo `reusable-dispatch.yml` now requires PR context for
> review label and slash-command triggers (`ready-for-review`, `/fs-review` gated
> on `issue.pull_request`). Per-org `dispatch.yml` is unchanged pending follow-up.
> Fix dispatch was already PR-only via `pull_request_review` and PR-gated `/fs-fix`.

If no stage matches, `dispatch.yml` exits early with no fan-out. The existing
kill switch, role enablement, and `# fullsend-stage:` marker scanning
([ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md)) run
after stage determination, unchanged.

The `stage` input to `dispatch.yml` becomes optional. When provided
(backwards compatibility with old shims), it is used directly. When omitted,
`dispatch.yml` determines the stage from the event context.

### Security properties preserved

- **`pull_request_target`** runs the base branch version of the shim,
  preventing PRs from modifying it
  ([ADR 0009](0009-pull-request-target-in-shim-workflows.md)).
- **Payload built from individual context fields** — not inline shell — to
  prevent script injection from attacker-controlled fields.
- **Fork-PR blocking** for the fix stage moves to `fix.yml`, where it already
  exists (redundant with the shim's current check).
- **Author association checks** for `/fs-fix`, `/fs-retro`, `/fs-fix-stop` move to
  `dispatch.yml`'s routing step.
- **`workflow_call` inputs stay under 10.** The current 5 inputs minus `stage`
  plus `event_action` and `trigger_source` = 6 inputs.

## Consequences

- Adding a new stage (command or event trigger) requires only a `case` branch
  in `dispatch.yml` and a new agent workflow file. No enrolled repo changes.
- Enrolled repos gain a single concurrency group
  (`fullsend-${{ github.event.pull_request.number || github.event.issue.number }}`).
  This is a behavioral change from the status quo, where stages run
  independently: a new dispatch now cancels any in-progress run for the
  same issue/PR. In practice, only one agent should run per issue/PR at a
  time, and the latest event takes priority.
- Events that don't match any stage still trigger a `workflow_call` to
  `dispatch.yml`, which exits early. Cost: one runner spin-up (~20s). The
  `if:` filter on the dispatch job eliminates bot comments, the
  highest-volume no-op.
- Old shims (per-stage jobs with explicit `stage` input) continue to work —
  `dispatch.yml` supports both explicit and auto-determined stages.
- The `stop-fix` job remains in the shim because it acts on the target repo
  directly (label + comment), not via `.fullsend` dispatch.
- This decision is sequenced after the token mint migration. The token mint
  provides `workflow_call`; this ADR uses it to simplify routing.
- **Per-repo installation
  ([ADR 0033](https://github.com/fullsend-ai/fullsend/pull/707))** needs the same routing
  logic but published upstream as `reusable-fullsend.yml` in
  `fullsend-ai/fullsend`. Per-repo repos have no `.fullsend/dispatch.yml` —
  their thin `fullsend.yml` calls `reusable-fullsend.yml` directly, which
  routes events to stages and fans out to per-stage reusable workflows. The
  routing implementation should be shared: either `dispatch.yml` calls
  `reusable-fullsend.yml` upstream (unifying both models), or both embed
  the same routing script. Per-org shims could also adopt
  `reusable-fullsend.yml` directly, eliminating `dispatch.yml` as a
  routing layer entirely — see ADR 0033 Open Questions.
