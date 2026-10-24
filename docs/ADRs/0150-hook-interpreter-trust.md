---
title: "150. Hook interpreter and PATH resolve from agent-unwritable directories"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - hooks
  - security
  - runtimes
---

# 150. Hook interpreter and PATH resolve from agent-unwritable directories

Date: 2026-10-23

## Status

Accepted

Retroactive record of the decision implemented in
[PR #8605](https://github.com/fullsend-ai/fullsend/pull/8605) (merged
2026-10-20, merge commit `bbe0ab242`), extended by
[PR #8589](https://github.com/fullsend-ai/fullsend/pull/8589) (pi) and
[PR #8633](https://github.com/fullsend-ai/fullsend/pull/8633) (CLI
binary pinning). The behavior on `main` is unchanged by this ADR; it
documents the rules and their rationale.

## Context

The runtimes execute sandbox tool hooks through scripts the runner
prepares (the ADR 0090 contract). Those scripts are interpreted — they
need a Python interpreter and a `PATH` to invoke tools. Before #8605,
the interpreter and `PATH` could resolve through directories the agent
user can write: an image can put a virtualenv's `bin` first on `PATH`,
and a bare `python3` in a hook would resolve there.

A hook that runs under an interpreter or tools the agent controls
defeats the hook contract's purpose — the observer becomes the
observed. A fake `python3` that exits 0 makes every hook read as
allowed while the hook scripts and their digests stay byte-identical.

Before #8605, a `PATH` set through the agent-writable `.env` was
already handled: Claude Code baked both values into the digest-checked
`hooks.json`, and Codex restored the pre-`.env` `PATH` for itself. The
gap #8605 closed is narrower: directories the agent can write that are
on the image's own `PATH`.

## Decision

A safe hook setup follows these rules:

1. **Trusted location.** A directory or file is trusted only if the
   agent user neither owns it nor can write it, and the same holds for
   every directory above it, after symlinks are resolved. Where a
   `PATH` entry came from (image, `.env`, home) doesn't matter.
2. **Hooks see only trusted locations.** The hook interpreter, the
   hook script, and every process a hook starts use a `PATH` of
   trusted directories only. That `PATH` is computed before any
   agent-controlled input is read, and the agent cannot change it for
   the run.
3. **One resolver.** Launch and hooks apply the same
   trusted-directory rules; no runtime has its own.
4. **Protected to the point of use.** Resolved values reach the hook
   only through a channel the agent cannot modify (a digest-checked
   config, or a read-only variable set after agent inputs).
5. **No hook, no tool call.** A missing trusted environment, a hook
   that cannot run, or a hook that gives no clear verdict denies the
   call. Stopping the launch is preferred; denying each call is the
   minimum. Cases where a call goes through anyway are listed in
   Consequences.
6. **Every runtime.** Applies to every runtime implementing the ADR
   0090 contract: Claude Code, Codex, and pi today.
7. **No untrusted fallback.** Never fall back to an untrusted
   interpreter, even if it is the only one meeting the version floor;
   stop with an actionable error.
8. **Nothing protectable means fail closed.** If no directory can be
   kept out of the agent's reach, the launch stops rather than
   treating `PATH` as trusted.

### Conformance: how each runtime satisfies the rules

| Runtime | Interpreter | Hook `PATH` | Failure mode |
|---|---|---|---|
| Claude Code | `hookPreflightInterpreter` resolves the absolute `python3` via the launch's `envDataPin` at Bootstrap; `GenerateHooksConfigPinned` renders `PATH='<trusted>' <python> -E -s -B <script>` into `hooks.json`, protected by the digest check (#8261) | Baked into `hooks.json` at Bootstrap, covered by its digest | Bootstrap fails (exit 127) when no trusted `python3` exists |
| Codex | Same `hookPreflightInterpreter` for the interpreter; written into `hooks.json` at Bootstrap | `FULLSEND_TRUSTED_PATH` (the launch `PATH` minus agent-writable directories), recorded read-only before `.env` by `envDataLoad`, exported after it; the adapter sets its children's `PATH` from it | Adapter denies each hooked call when the trusted `PATH` is missing, empty, or has non-absolute entries; Codex itself still starts |
| pi | `FULLSEND_PI_HOOK_PYTHON` (the launch's `envDataPin` result, #8589); exported after `.env`, on the loader drop list | `FULLSEND_PI_HOOK_PATH` (the trusted `PATH`, #8589); same export discipline | Adapter blocks each hook (fail closed) when either variable is missing or not an absolute path; pi still starts |

The shared machinery lives in `internal/runtime/env_data.go` at
`bbe0ab242`: `envDataTrustedPath` computes the trusted `PATH`,
`envDataReadlink` finds a readlink held to the same rule, and
`envDataPin` resolves the interpreter through it.

### Known fail-open cases

The Codex adapter's exit-2-with-lost-stderr case is indistinguishable
from `Failed` to Codex (see `block()` in
`internal/runtime/codex_hook/fullsend-codex-hook.py` at `bbe0ab242`).
Hook scripts that own their fail-open cases (tirith) document them
individually.

## Consequences

- **Breaking change:** with sandbox hooks on, the trusted `python3`
  must be 3.11 or newer (`datetime.UTC`). Images whose only 3.11+
  Python sits in an agent-owned virtualenv over an older system
  `python3` now fail at Bootstrap; the fix is a root-owned 3.11+
  `python3` on `PATH`.
- A future runtime implementing the ADR 0090 hook contract must follow
  rules 1–8; the conformance table above is the reference.
- Only the interpreter, `readlink`, and CLI binaries get the per-file
  check today (resolved path, writable-ancestor walk). Tools a hook
  runs are found through the filtered `PATH` only.
- This ADR composes with, but is distinct from, ADR 0094's plugin
  trust model: plugins are hash-verified (tampering is detected and
  fails closed), while hook execution is prevention (the interpreter
  and `PATH` cannot be subverted in the first place). CLI binary
  pinning for all runtimes landed separately in #8633.
- The time-of-check/time-of-use window noted in ADR 0094 (a process
  left running by an earlier iteration can rewrite the tree between
  preflight and import) applies to hook directories too; trusted
  directories shrink but do not eliminate that window.

## Forge scope

All forges equally. The decision is in the runner's runtime layer
(`internal/runtime/`), which sits above the forge abstraction — hook
resolution is identical whether the run was dispatched from GitHub,
GitLab, or a local invocation.
