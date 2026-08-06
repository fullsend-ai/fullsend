---
title: "86. Generalized /fs-stop and fullsend-no-* auto-trigger skips"
status: Accepted
relates_to:
  - agent-architecture
  - code-review
topics:
  - slash-commands
  - dispatch
  - authorization
---

# 86. Generalized /fs-stop and fullsend-no-* auto-trigger skips

Date: 2026-08-06

## Status

Accepted

## Context

[ADR 0034](0034-centralized-shim-routing-via-dispatch.md) placed a `stop-fix`
job in the shim that applied `fullsend-no-fix` for `/fs-fix-stop`. Users
needed the same per-item skip for other auto-triggered agents (triage, code,
review, retro) without disabling on-demand `/fs-*` commands, and without
waiting for the harness CEL trigger port ([#2888](https://github.com/fullsend-ai/fullsend/issues/2888)).
See [#5650](https://github.com/fullsend-ai/fullsend/issues/5650).

## Decision

1. Replace the shim `stop-fix` job with `stop-agent`, which handles
   `/fs-stop <agent>` and `/fs-fix-stop` (alias of `/fs-stop fix`). Logic
   lives in `.github/scripts/stop-agent.sh`, checked out from the default
   branch.

2. Apply `fullsend-no-{triage,code,review,fix,retro}` labels. Bare `/fs-stop`
   applies only labels meaningful for the item type (issue: triage/code; PR:
   review/fix/retro). Prioritize stays slash-only (no `fullsend-no-prioritize`).

3. Enforce those labels on **auto** dispatch paths in bash
   (`reusable-dispatch.yml` / scaffold `dispatch.yml`), including
   `needs-info` re-entry gated by `fullsend-no-triage`. On-demand `/fs-*`
   still bypasses. Long-term home remains CEL triggers; remove the bash
   guards when those land rather than extending them.

4. Authorization: write+ collaborator permission (ADR 0054). The issue/PR
   **author escape hatch** applies only to `/fs-stop fix` / `/fs-fix-stop`
   — authors cannot unilaterally suppress review or other non-fix gates.

5. In-flight workflow cancellation remains out of scope (see also [#5445](https://github.com/fullsend-ai/fullsend/issues/5445)).

## Consequences

- Enrolled repos receive both the shim YAML and `stop-agent.sh` via enroll/
  reconcile; drift detection covers both; unenroll removes both.
- Accepted ADR 0034 Decision text stays historical; this ADR records the
  generalization.
- CEL port work (#2888) should delete bash `fullsend-no-*` guards when
  equivalent trigger expressions exist.
