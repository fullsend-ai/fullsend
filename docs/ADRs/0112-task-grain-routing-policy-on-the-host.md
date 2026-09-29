---
title: "112. Runtime and model are routed per task by a config policy, on the host"
status: Accepted
relates_to:
  - adaptive-agent-selection
  - agent-architecture
  - operational-observability
topics:
  - config
  - runtime
  - routing
  - cost
---

# 112. Runtime and model are routed per task by a config policy, on the host

Date: 2026-09-11

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

A repo can say *which* runtime, model, effort and persona model an agent uses
([ADR 0091](0091-per-agent-runtime-model-effort.md), [ADR
0104](0104-per-persona-model-resolution.md)) but not *when*.
Issues ask for cheaper runs on trivial changes (#5777, #2842, agents#497),
deeper review on security signals (agents#535) and a cost preview (#6560). The
deciding session's model is never downgraded (#6527), a listed model may be
unservable (#6964, agents#1225), and a learned policy keeps the constraints in
[adaptive-agent-selection.md](../problems/adaptive-agent-selection.md).

## Options

Rejected: **per-prompt routing in the sandbox** (#6361), which reads
author-controlled prompts and loses the prompt cache mid-loop, and a
**Route-job heuristic** (#6891), GitHub-only and a second decision point. An
**LLM gateway** stays only an optional provider that reports the model that
served.

## Decision

`fullsend run` resolves runtime, model, effort and persona models **per task,
on the host, before the sandbox exists**, from a `routing:` block in
`.fullsend/config.yaml`, declared in `repos.yaml` for repositories [ADR
0122](0122-declarative-repo-configuration.md) manages, where any change that
loosens routing is a relaxation under its safety gate. It configures a run that
dispatch has already authorized ([ADR
0098](0098-entity-first-harness-evaluation.md)) and never decides whether a run
happens.

1. **Shape.** `routing.rules[]` carry `agents:`, a CEL `when:`, an optional
   `mode:` and a `set:` of the four fields an `agents:` entry holds. Matching
   rules merge later-wins per field. While any matching escalation applies,
   no field falls below the static choice or below the highest value an
   escalation set, and a field with no order (runtime, a model of the same
   model tier) keeps its static value. `routing.mode` is `off` (default),
   `shadow` (rules are recorded, the static choice runs) or `enforce`, and a
   rule may act less than its block, never more. A malformed block fails the
   run before the sandbox exists. Rules override the agent's `agents:` entry
   and the repo default; flags and `FULLSEND_*` variables still beat any
   rule.
2. **Inputs are system-reported.** Rules see an allowlisted environment:
   change stats and paths, actor, labels, attempt count and the linked
   issue's number, state and labels. Author-written text is never visible;
   paths and stats are author-chosen, a risk the reviewed policy accepts, but
   a downgrade never applies to a change that touches agent-instruction or
   policy files. A downgrade may key on a label or actor only through a
   reviewed trust list that fails closed, and only on evidence the change's
   author cannot supply. An escalation may key on anything visible.
3. **Guards.** A rule is an escalation only if it keeps the static runtime
   and every other field it resolves is unchanged or above the static choice
   (effort `low` to `max`; a changed model only on a higher catalog model
   tier); anything else, including a runtime change or a swap to another
   model of the same model tier, is a downgrade. Models come from
   `routing.allowed_models` (empty by default) and the catalog. Agents in
   `routing.never_downgrade` (default `triage`, `prioritize`, `review`; it
   can only grow) keep their static runtime and keep their static model or
   move to a higher model tier, and their personas never go below their
   static model tier unless a persona file sets a lower `min_tier`;
   escalations may raise both.
   `routing:` and `min_tier:` come from the base branch or pinned harness,
   never the change being routed. When `routing.mode` is not `off`, every
   model is checked against its provider's served list before the sandbox
   exists: a routed miss falls back to static, and a static miss fails the
   run only when no model in its existing fallback chain is served.
4. **Echo.** The plan block, `metrics.json`, root span and status comment
   name the rules that set each field and those a guard suppressed.
5. **Learning is offline.** A routing ledger on the root span ([ADR
   0050](0050-distributed-tracing-instrumentation.md)), outcomes from later
   forge events and a replay score candidate policies; a reviewed edit to
   `routing:` is the only path to production, and a downgrade rule moves to
   `enforce` only after replay shows it never downgraded a PR whose review
   later requested changes.

## Consequences

- Docs-only downgrades and security escalations become reviewed config; a
  downgrade of `review` reaches only its effort and opted-in personas, while
  an escalation may also raise its model.
- For repos that turn routing on, the preflight ends the
  retry-on-unavailable incident class on every provider that exposes a
  served list; repos with routing `off` see no new failure mode.
- Implementing this adds a model tier to catalog entries and provenance fields
  to ADR 0098's entity specification.

### Deferred

- **A routing advisor** that reads the diff (#6531) may fire an escalation
  alone, but a downgrade rule may use its assessment only as one condition and
  must still match with that condition removed; its classifier is an open
  question in
  [adaptive-agent-selection.md](../problems/adaptive-agent-selection.md).
