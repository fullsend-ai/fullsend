---
title: "152. Hook interpreter and PATH resolve from agent-unwritable directories"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
  - tool-call-risk-assessment
topics:
  - hooks
  - security
  - runtimes
---

# 152. Hook interpreter and PATH resolve from agent-unwritable directories

Date: 2026-10-23

## Status

Accepted

Retroactive record of the decision implemented in
[PR #8605](https://github.com/fullsend-ai/fullsend/pull/8605) (merged
2026-10-20, merge commit `bbe0ab242`), with
[PR #8589](https://github.com/fullsend-ai/fullsend/pull/8589) (pi,
merged earlier the same day) and extended by
[PR #8633](https://github.com/fullsend-ai/fullsend/pull/8633) (CLI
binary pinning, issue #8626). The behavior on `main` is unchanged by
this ADR; it documents the rules and their rationale.

## Context

The runtimes execute sandbox tool hooks through scripts the runner
prepares (the [ADR 0090](0090-runtime-neutral-sandbox-hooks-contract.md)
contract). Those scripts are interpreted — they
need a Python interpreter and a `PATH` to invoke tools. Before #8605,
the interpreter and `PATH` could resolve through directories the agent
user can write: an image can put a virtualenv's `bin` first on `PATH`,
and the absolute interpreter was found with a bare `PATH` lookup
(`command -v python3`) against the image's `PATH`, so it could resolve
into a directory the agent can write.

A hook that runs under an interpreter or tools the agent controls
defeats the hook contract's purpose — the observer becomes the
observed. A fake `python3` that exits 0 makes every hook read as
allowed while the hook scripts and their digests stay byte-identical.

Before #8605, a `PATH` set through the agent-writable `.env` was
already handled: Claude Code baked both values into the digest-checked
`hooks.json`, and Codex restored the pre-`.env` `PATH` for itself and
its hook adapter's children (`internal/runtime/codex_run.go:406-409` at
`bbe0ab242^1`, i.e. `191d72397`). The
gap #8605 closed is narrower: directories the agent can write that are
on the image's own `PATH`.

## Options

- **Fail open with a warning** when no trusted interpreter resolves.
  Rejected: a hook running under agent-controlled code defeats the
  ADR 0090 contract; a warning is not a control (rule 5).
- **Per-runtime resolvers.** Let each runtime pin its own interpreter
  and `PATH`. Rejected: three resolvers drift, and the launch loader
  already computes exactly the trusted-directory set needed (rule 3).
- **Accept agent-owned interpreters that meet the version floor.**
  Rejected: the version floor is a compatibility check, not a trust
  check; an agent-owned `python3` is the threat model (rule 7).
- **Hash-verify the interpreter instead of requiring an unwritable
  location.** Rejected as the primary mechanism: verification detects
  a substitution only at the next check and leaves a
  time-of-check/time-of-use window; it does not prevent the write.
  Unwritability prevents the substitution in the first place.

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
   config, or a read-only variable recorded before agent inputs are
   read and exported after them).
5. **No hook, no tool call.** A missing trusted environment, a hook
   that cannot run, or a hook that gives no clear verdict denies the
   call. Stopping the launch is preferred; denying each call is the
   minimum. Paths that still let a call through are tracked in
   [#8764](https://github.com/fullsend-ai/fullsend/issues/8764).
6. **Every runtime.** Applies to every runtime implementing the ADR
   0090 contract: Claude Code, Codex, and pi today. OpenCode does not
   implement the ADR 0090 hook wiring yet (unbound-force#515); until
   it does, it installs no sandbox tool hooks and these rules do not
   apply to it.
7. **No untrusted fallback.** Never fall back to an untrusted
   interpreter, even if it is the only one meeting the version floor;
   stop with an actionable error.
8. **Nothing protectable means fail closed.** If no directory can be
   kept out of the agent's reach, the launch stops rather than
   treating `PATH` as trusted.
9. **Executables, not just directories.** Every executable a hook
   script itself runs — the interpreter, and tools such as `tirith` or
   `git` found through the filtered `PATH` — must itself satisfy
   rule 1. (Launch-time executables — `readlink` and the CLI binaries —
   are pinned by the launch loader under the same rule.)
10. **Scope.** This ADR covers interpreter and `PATH` resolution for
    hook execution. Hook *settings* sources are decided separately
    ([#8637](https://github.com/fullsend-ai/fullsend/pull/8637),
    issue #8635: settings taken from the runner after `.env`);
    hook-config integrity by the `hooks.json` digest check
    ([PR #8261](https://github.com/fullsend-ai/fullsend/pull/8261));
    plugin trust by [ADR 0094](0094-pi-extensions-are-harness-resources.md).

## Forge scope

All forges (GitHub, GitLab, Forgejo) and local invocation. The
decision is in the runner's runtime layer (`internal/runtime/`), which
sits above the forge abstraction — hook resolution is identical
whether the run was dispatched from GitHub, GitLab, Forgejo, or
invoked locally.

## Consequences

- **Breaking change:** with sandbox hooks on, the trusted `python3`
  must be 3.11 or newer (`datetime.UTC`). Images whose only 3.11+
  Python sits in an agent-owned virtualenv over an older system
  `python3` now fail at Bootstrap on Claude Code and Codex (pi has no
  version preflight and blocks each hook instead); the fix is a
  root-owned 3.11+ `python3` on `PATH`.
- A future runtime implementing the ADR 0090 hook contract must follow
  rules 1–10; the per-runtime conformance table lives in
  [docs/contributing/runtime-implementation.md](../contributing/runtime-implementation.md#hook-interpreter-and-path-conformance)
  so it can track the implementation as the code changes.
- Gaps against the rules are tracked in issues:
  [#8763](https://github.com/fullsend-ai/fullsend/issues/8763)
  (per-file trust for executables a hook runs) and
  [#8764](https://github.com/fullsend-ai/fullsend/issues/8764)
  (paths that let a tool call through without a hook verdict).
- This ADR composes with, but is distinct from, ADR 0094's plugin
  trust model: plugins are hash-verified (tampering is detected and
  fails closed), while hook execution is prevention (the interpreter
  and `PATH` cannot be subverted in the first place).
- The time-of-check/time-of-use window noted in ADR 0094 (a process
  left running by an earlier iteration can rewrite the tree between
  preflight and import) applies to hook directories too; trusted
  directories shrink but do not eliminate that window.
