---
title: "100. Sandbox tool hooks on Codex — a translating adapter with per-invocation verification"
status: Accepted
relates_to:
  - security-threat-model
  - tool-call-risk-assessment
topics:
  - runtime
  - sandbox
  - hooks
---

# 100. Sandbox tool hooks on Codex — a translating adapter with per-invocation verification

Date: 2026-09-02

## Status

Accepted

## Context

[ADR 0090](0090-runtime-neutral-sandbox-hooks-contract.md) made the sandbox tool hooks — Tirith,
SSRF, canary, the sanitizer chain — runtime-neutral, so each runtime wires the same
`security.HookPlan` through its own mechanism rather than dropping the controls. Codex has a hook
system of its own, and this ADR records how the two are joined. The hooks are defense in depth
inside the sandbox boundary, not the boundary itself
([security-threat-model.md](../problems/security-threat-model.md)) — the pattern-matching layer
whose reach and limits [tool-call-risk-assessment.md](../problems/tool-call-risk-assessment.md)
examines. The credential half of the codex runtime is [ADR 0099](0099-codex-agent-runtime.md).

Codex's protocol differs from the scripts' in ways that **fail open** if bridged naively, which is
what makes an adapter mandatory rather than a convenience. Verified against `rust-v0.152.1`:
a hook exiting anything but 0 or 2 is recorded as `Failed` and does **not** block, so the scripts'
`exit 1` convention would make every check advisory; only a synchronous handler can apply control
effects; and `PostToolUse` accepts only `additionalContext`, so the sanitizers' rewrite cannot be
applied to a built-in tool's output.

## Decision

`$CODEX_HOME/hooks.json` is rendered from `security.HookPlan`, every handler invoking one embedded
**adapter** that runs the shared scripts and translates the protocol in both directions: a block
becomes exit 2 with the reason on stderr, a rewrite the runtime cannot apply becomes an
`additionalContext` warning, and the wiring never carries an `async` key. Hooks load with
`--dangerously-bypass-hook-trust`, justified by fullsend's own SHA-256 guard over the adapter,
which is a stronger check than the trust hash it replaces.

Because the hook scripts and their directory stay agent-writable between iterations — the residue
Claude Code and pi also have — codex additionally **re-verifies every script against runner-held digests
before each invocation** — digests the runner records outside the sandbox at Bootstrap and injects into the launch command at Run, here carried in the codex process's
environment, which the agent cannot reach once it is running —, pins the interpreter (absolute path, `-I`,
`-B`) and `PATH` across the agent-writable `.env`, and re-asserts the hook configuration the runner
derived from the harness after `.env` so the agent's copy cannot win.

## Consequences

- The sanitizer chain **detects and blocks but cannot redact** for the model, and codex has **no
  hook-driven session halt**, so a canary hit blocks the tool result — which on codex withholds it
  entirely, unlike Claude Code — but the run continues.
- Codex is **stricter on hook integrity than Claude Code and pi**: the scripts are checked by name
  before every invocation, the hooks directory must hold exactly the installed files, and the
  interpreter, `PATH` and the scripts' own configuration are all pinned past `.env`. On the other
  two runtimes each of those is agent-writable between iterations with no check.
- The harness-supplied `FULLSEND_CANARY_TOKEN` and `FULLSEND_TOOL_ALLOWLIST` are covered too, even
  though `SandboxHookConfig` does not carry them: Bootstrap reads them back from the workspace
  `.env` while that file is still exactly what the runner wrote, before any iteration has run.
- Codex artifacts keep raw tool output where Claude Code's stream carries the post-hook result, so
  `output.jsonl` and the extracted rollout are filtered through the shared secret-pattern redactor:
  a credential is masked there, a canary is not withheld.
- `apply_patch` reaches the scripts as `Edit`, so an agent allowlisted only for `Write` is blocked
  by the opt-in allowlist hook; and a command that leaves a live PTY session produces no
  `PostToolUse` payload, so its output is not scanned.
- Every codex behaviour the translation rests on has to be re-checked on a `CODEX_VERSION` bump —
  hook exit-code semantics, the sync-only rule, the `PostToolUse` output fields and the payload
  shape — and any hook script that grows a new by-name tool dependency reopens the ambient-resolution
  class that `PATH` pinning closed. The list is in
  [runtime-implementation.md](../contributing/runtime-implementation.md#codex-runtime-internals-6920).
