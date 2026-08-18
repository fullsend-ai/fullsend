---
title: "90. Cost-aware model routing"
status: Accepted
relates_to:
  - agent-architecture
  - operational-observability
topics:
  - cost
  - routing
  - sandbox
  - hooks
---

# 90. Cost-aware model routing

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

Every stock fullsend agent (triage, code, review, fix, retro, prioritize,
scribe) is pinned to `model: opus` in its harness. There is no per-task or
per-prompt routing: a one-line triage question, a file read, and a
multi-file refactor are all billed at Opus rates. Model choice is a static
harness field, so the only lever an org has today is editing YAML by hand.

A benchmark run on 2026-07-12 against the stock agents measured the effect
of two cheap interventions already used by contributors locally — token
filtering (RTK) and minimal-diff prompting (Ponytail) — without touching
model selection:

| Agent | Cost reduction |
|-------|----------------|
| triage | 52.6% |
| code | 24.2% |
| all agents (total) | 15.7% |

Paired difference was significant (p = 0.00006). Model routing on top of
this is the next-largest lever and is orthogonal to it.

[Costwise](https://github.com/guyoron1/costwise) already implements
hook-based routing for Claude Code sessions: a `UserPromptSubmit` hook
scores each prompt (intent, error severity, code presence, multi-file
scope, retry context, ...) into SIMPLE / MEDIUM / COMPLEX and rewrites
`model` in `settings.json` to `haiku` / `sonnet` / `opus`; a `PreToolUse`
hook on `Agent|Task` does the same for sub-agent spawns. It has retry
detection (false downgrade → bump tier), budget limits, and fails open.
fullsend already generates the sandbox `settings.json` for its security
hooks (`internal/security/hooks.go`), so the integration surface exists.

## Options

1. **Change harness defaults only** (`triage → haiku`, `review → sonnet`).
   Minutes of work, ~30–40% on those runs, but static: a hard triage still
   runs on Haiku and a trivial review still runs on Sonnet.
2. **Phase 1 — Costwise hooks in the sandbox image.** Install the Python
   package and its hook scripts in `images/sandbox/Containerfile`; have
   `GenerateClaudeSettings` emit the two hook entries. Per-prompt routing
   inside every run, no Go logic. Limitation: routing decisions and the
   tracking DB live in the ephemeral sandbox.
3. **Phase 2 — native Go port** (`internal/routing/`): classifier, signals,
   pricing, budget, retry detection, tracking store on the host. Enables
   agent-level routing before the sandbox starts (pick the initial model
   from the task) and removes the Python dependency. 1–3 weeks.

## Decision

Do 1 and 2 now, 3 after 2 has produced production data.

- Phase 1 ships behind an opt-in switch (`FULLSEND_COSTWISE=1`) so the
  default sandbox behaviour is unchanged. Hook scripts are baked into the
  image at `/opt/costwise/hooks`; the sandbox `settings.json` gets a
  `UserPromptSubmit` entry and a `PreToolUse` (`Agent|Task`) entry next to
  the existing security hooks. Costwise writes short model names, which
  Claude Code resolves for Vertex AI the same way the harness `model:`
  field is resolved today.
- Harness defaults for the stock agents move to `triage: haiku`,
  `review: sonnet` (in `fullsend-ai/agents`).
- Phase 2 is planned, not started: `internal/routing/` port, host-side
  tracking store, agent-level routing in `internal/cli/run.go` before the
  Claude command is built. A later ADR records that design.

## Consequences

Easier:
- Per-prompt cost reduction on every agent run with zero harness changes;
  a run that misroutes recovers via retry detection or, on hook crash,
  falls open to the harness model.
- Routing quality can be measured in production before any Go is written.

Harder / trade-offs:
- Adds a Python package (plus pydantic, click) to the sandbox image; the
  image already carries Python for pre-commit and the security hooks.
- Routing state is per-sandbox in Phase 1; cross-run learning and
  analytics wait for Phase 2 (or a post-script that exports the SQLite DB).
- A model switched mid-run is invisible to the harness `model:` field, so
  run-summary cost accounting must read the actual model per turn, not the
  harness value.
- The env-var switch is a stopgap; the durable home is a
  `security.sandbox_hooks`-style harness field.
