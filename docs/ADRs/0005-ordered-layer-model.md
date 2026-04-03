---
title: "5. Ordered layer model for install, uninstall, and analyze"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - installation
  - layers
  - idempotency
---

# 5. Ordered layer model for install, uninstall, and analyze

Date: 2026-04-02

## Status

Accepted

## Context

Installing fullsend into an org involves multiple concerns with ordering dependencies: the config repo must exist before workflows can be written to it; secrets must be stored before enrollment can reference them. Uninstalling must reverse this order. An analyze command must inspect each concern independently to report what exists, what is missing, and what install would do.

## Decision

Each installation concern is a `Layer` implementing `Install`, `Uninstall`, and `Analyze`. Layers are composed into an ordered `Stack`. Install runs layers forward; uninstall runs them in reverse; analyze runs them forward and collects reports.

The current stack order is: config-repo → workflows → secrets → dispatch-token → enrollment.

Each layer is idempotent — re-running install skips already-completed work. Uninstall collects all errors rather than stopping on the first, so partial teardown still makes progress. Each layer declares the OAuth scopes it needs via `RequiredScopes`, enabling a preflight check that fails early when the token lacks required permissions.

## Consequences

- Adding a new installation concern means implementing the `Layer` interface and inserting it at the right position in the stack.
- The analyze command can report partial installations and explain exactly what install would create or fix.
- Idempotency means install is also the repair command — no separate "fix" operation needed.
- The ordering contract is implicit (stack construction order). A future layer that violates ordering assumptions will fail at runtime, not compile time.
- Reverse-order uninstall with error collection ensures best-effort cleanup even when some layers fail.
