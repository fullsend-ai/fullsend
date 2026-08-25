---
title: "90. Runtime-neutral sandbox tool hooks contract"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
topics:
  - runtime
  - sandbox
  - hooks
  - security
---

# 90. Runtime-neutral sandbox tool hooks contract

Date: 2026-08-18

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

`fullsend run` drives agents through a pluggable `runtime.Runtime`
([runtimes.md](../runtimes.md)), but the sandbox tool hooks — Tirith command
scanning, SSRF and canary checks, secret redaction, unicode normalization,
context suppression, tool allowlist — were only reachable through a
Claude-specific `ClaudeHooksBootstrap` type assertion, and their wiring lived
solely in the generated Claude Code `settings.json`. A second runtime
(OpenCode, #1260) would therefore ship with **no** sandbox tool hooks, and
every further runtime (Cursor CLI #6319, pi, …) would face the same choice:
re-implement the hooks or drop them. Related runner code also branched on
`rt.Name() == "claude"` and hardcoded Claude artifact paths.

The credential-isolation architecture ([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md),
[ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md)) makes the OpenShell sandbox, its
L7 egress policy, and credential placeholders the primary security boundary;
the hooks are defense-in-depth. The recommendation on #1260 was that a
runtime may lack them only if that absence is explicit. Longer-term hook layers (ACP
proxy chains #587, tool proxies #5243) remain open.

## Decision

The sandbox tool hooks are a **runtime-neutral contract**, not a Claude Code
feature:

- The hook scripts (`internal/security/hooks/*.py`) are the portable
  artifact. `security.HookFiles` returns them and `security.HookPlan` returns
  their wiring — ordered `HookGroup{Phase, Tools, Scripts}` entries in
  PreToolUse/PostToolUse phases with Claude tool names as the canonical
  vocabulary. Claude's `GenerateClaudeSettings` is rendered from `HookPlan`
  so the two cannot diverge. The stdin/stdout/exit-code wire protocol is
  documented in [runtimes.md](../contributing/runtime-implementation.md#sandbox-hook-contract).
- The bootstrap extension is `runtime.SandboxHooksBootstrap` (carrying
  `security.SandboxHookConfig`). Every runtime's `Bootstrap` SHOULD honour it
  by installing the scripts (`installHookScripts`, any directory) and wiring
  `HookPlan` through its own interception mechanism (Claude Code:
  `settings.json`; OpenCode: `tool.execute.before/after` plugin; pi:
  `tool_call`/`tool_result` extension). A runtime that cannot MUST record the
  absence in the security feature matrix in `docs/runtimes.md`.

## Consequences

- New runtimes get Tirith/SSRF/canary/redaction parity by writing a thin
  adapter over the existing scripts instead of re-implementing scanners.
- The Claude runtime is unchanged in behaviour: same scripts, same
  `settings.json`, same `claude-debug.log` and CLAUDE.md bridge.
- Tool-name translation (Claude `Bash` vs lowercase `bash`, #608) becomes an
  adapter responsibility; the plan keeps Claude names as the vocabulary.
- The contract is versioned (v1 = the scripts' current stdin/stdout fields).
  Verifying it against Claude Code surfaced two pre-existing gaps the
  runtimes.md matrix now records instead of a blanket ✓: the runner's
  hook wiring was not loaded from where it was written (#6358 — since fixed
  via `--settings`), and the
  PostToolUse payload differs (`tool_response`,
  `hookSpecificOutput.updatedToolOutput`, parallel execution — #6357).
- Alongside this decision the runner's remaining Claude-specific branches
  were replaced by optional capability interfaces (`DebugLogNamer`,
  `ContextBridger`) — an implementation detail recorded in
  [architecture.md](../architecture.md), not a separate architectural
  decision.
- The security matrix and config-key matrix in `docs/runtimes.md` gain a
  column per runtime and become part of a runtime PR's definition of done.
- ACP proxies (#587) or tool proxies (#5243) can later supersede per-runtime
  adapters without changing the scripts or the plan.

> **Done ([#6357](https://github.com/fullsend-ai/fullsend/issues/6357)):**
> PostToolUse contract v2 — scripts read `tool_response` (fallback
> `tool_result`), replace via `hookSpecificOutput.updatedToolOutput`, and
> enforce unicode → canary → suppress → redact in `posttool_chain.py`. See
> [runtimes.md](../contributing/runtime-implementation.md#sandbox-hook-contract).

> **Done ([#608](https://github.com/fullsend-ai/fullsend/issues/608)):**
> The canonical Claude tool-name vocabulary is recorded once in
> `security.CanonicalClaudeTools` (with `security.LegacyClaudeTools` for
> names agents and adapters still use), mirrored into
> `tool_allowlist_pretool.py` and kept identical by a Go test; `HookPlan`
> tools and the pi adapter's maps are tested against it. The allowlist hook
> stays exact-match and fail-closed — no case-insensitive allowing — but a
> blocked name that is a case variant of an allowlisted entry is reported as
> a normalization gap (`tool_name_unnormalized` when the adapter did not
> translate, `allowlist_entry_unnormalized` when the allowlist is the
> non-canonical side, `tool_name_case_collision` when neither spelling is
> a Claude tool; all `ALLOWLIST_HOOK_ERROR`, severity `high`) rather than
> as a forbidden tool (`tool_blocked`, `critical`). MCP names are matched
> verbatim. The pi adapter's maps are held to canonical-or-legacy names
> (`ls` → `LS`), a deliberate relaxation of "canonical only". See
> [runtimes.md](../contributing/runtime-implementation.md#sandbox-hook-contract).
