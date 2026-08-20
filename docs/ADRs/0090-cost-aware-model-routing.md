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


A second, model-controlled dataset exists from 2026-07-26: the stock
**code agent** run on 13 real `fullsend-ai/agents` issues, once per model
(haiku / sonnet / opus), 39 trials, via a standalone workflow on
`guyoron1/agents` (Vertex AI, mint bypassed, `--no-post-script`). All 39
trials completed and produced `metrics.json` + `code-result.json` +
transcripts (artifacts `routing-trial-<issue>-<model>-<run_id>`).

| Issue (fullsend-ai/agents) | haiku turns / $ | sonnet turns / $ | opus turns / $ |
|---|---|---|---|
| #311 flag unregistered public exports | 41 / $0.32 | 70 / $1.45 | 60 / $2.10 |
| #314 verify API assumptions vs docs | 70 / $0.57 | 61 / $1.05 | 52 / $1.41 |
| #315 cross-method semantic tracing | 61 / $0.46 | 81 / $1.21 | 52 / $1.65 |
| #316 authorization flow analysis | 53 / $0.38 | 80 / $1.21 | 53 / $1.63 |
| #334 sibling-PR file overlap | 49 / $0.43 | 60 / $1.40 | 53 / $1.69 |
| #337 pre-flight PR state check | 67 / $0.57 | 74 / $1.48 | 49 / $1.34 |
| #340 coding agent should use a fork | 25 / $0.20 | 24 / $0.37 | 13 / $0.33 |
| #342 preserve human changes on force-push | 86 / $0.75 | 76 / $1.77 | 71 / $4.30 |
| #344 yq/jq expression validation | 61 / $0.45 | 74 / $1.56 | 58 / $2.22 |
| #347 path construction from untrusted input | 66 / $0.46 | 77 / $1.71 | 53 / $1.66 |
| #360 scaffold changes not trivial | 69 / $0.54 | 63 / $1.13 | 52 / $2.07 |
| #362 subset/superset issue coordination | 60 / $0.59 | 95 / $1.87 | 50 / $2.09 |
| #365 cross-repo label failure warning | 23 / $0.25 | 32 / $0.46 | 16 / $0.39 |

What the grid shows for routing policy:

- **Cost spread**: haiku $0.20–0.75/issue, sonnet $0.37–1.87, opus
  $0.33–4.30; median opus:haiku ratio ≈ 3.6×, worst case (#342) 5.7×.
- **No-op detection is model-insensitive**: on #340 and #365 every model
  independently concluded no code change was needed and stopped early
  (13–32 turns). "Assess cheap, escalate on evidence of work" would have
  produced identical outcomes at haiku cost (cf. #2842).
- **Turn counts do not track model tier**: haiku used fewer turns than
  sonnet on 8/13 issues; cost differences are dominated by per-token
  price, not wandering.
- **Limitation**: the grid measures cost and completion, not output
  quality — every trial drafted a PR body, but no quality judgment pass
  has been run over the 13×3 outputs. Transcripts are retained for that
  follow-up, which is the gate before turning any of this into a routing
  default.

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
