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

A repo can say *which* runtime, model, effort and per-persona model an agent
uses ([ADR 0091](0091-per-agent-runtime-model-effort.md),
[ADR 0104](0104-per-persona-model-resolution.md)), but not *when*. Every
choice is static per agent, so a one-line docs change and a 2,000-line CI
refactor get the same opus-at-high review. CEL overlays cannot set `model`
or `effort` ([ADR 0088](0088-cel-guarded-overlays.md): both are operational,
not forge-specific), and the normalized event carries no diff size or paths.
Open issues ask for the next step from three directions: a cheaper model or
effort for trivially scoped changes
([#5777](https://github.com/fullsend-ai/fullsend/issues/5777),
[#6891](https://github.com/fullsend-ai/fullsend/issues/6891),
[#2842](https://github.com/fullsend-ai/fullsend/issues/2842),
[agents#497](https://github.com/fullsend-ai/agents/issues/497)), deeper
review when security signals are present
([agents#535](https://github.com/fullsend-ai/agents/issues/535)), and a way
to see the cost effect of a model change before merging it
([#6560](https://github.com/fullsend-ai/fullsend/issues/6560)).

Two facts bound the design. The runtime-parity rule that *the deciding
session is never downgraded* ([#6527](https://github.com/fullsend-ai/fullsend/issues/6527))
still holds. And catalog membership is not availability: four incidents
([#6964](https://github.com/fullsend-ai/fullsend/issues/6964),
[agents#1225](https://github.com/fullsend-ai/agents/issues/1225)) discovered
an unservable id at the first model call, after the sandbox existed, and
retried on opus at several times the cost. A closed draft
([#6361](https://github.com/fullsend-ai/fullsend/pull/6361)) prototyped
per-prompt routing with Claude Code hooks inside the sandbox and measured a
13-issue by 3-model grid on the code agent (opus a median 3.6× haiku; every
model stopped early on the no-change issues); it measured cost and
completion, not quality.
[adaptive-agent-selection.md](../problems/adaptive-agent-selection.md) asks
how selection should learn from outcomes and names the constraints any
learned policy must keep: selection only, shadow mode first, safety as a
constraint rather than an objective, no self-deployment.

## Options

**Per-prompt routing inside the sandbox** (the #6361 hooks). Rejected: the
sandbox sees only the prompt, which is author-controlled text; routing state
is per sandbox and lost; it is Claude-Code-only; and the decision comes after
the review has already read a diff it could have sized for free.

**Routing in an LLM gateway.** Rejected as the place for the decision, kept
as an optional provider. On Vertex the model is addressed in the URL path, so
a gateway can route only if the client moves to the Anthropic Messages
endpoint and the gateway holds the credential the sandbox never sees today; a
silent rewrite also breaks the status-comment model echo, `per_model_usage`
and ADR 0104's closed set. Nothing a gateway sees carries the outcome that
arrives on a later forge event.

**A heuristic in the GitHub Route job** (#6891's proposal). Rejected: GitHub
only, and a second place that decides model and effort next to the ADR 0091
chain.

## Decision

`fullsend run` resolves runtime, model, effort and persona models **per task,
on the host, before the sandbox exists**, from a `routing:` block in
`.fullsend/config.yaml`.

1. **Shape.** `routing.rules[]` each carry `agents:`, a CEL `when:` and a
   `set:` that holds exactly the four fields an `agents:` entry holds
   (`runtime`, `model`, `effort`, `subagents`); nothing else is routable.
   All matching rules merge, later entries win per field (the ADR 0088
   semantics); `subagents` merges per persona key with ADR 0104's rules,
   `~` tombstones included, as one more layer above the `agents:` entry.
   `routing.mode` is `off` (the default when omitted), `shadow` or
   `enforce`. A malformed `routing:` block is a config-time error and the
   static choice runs.
2. **Inputs are system-derived.** Rules see a `task` variable beside
   `runtime`, `config` and a redacted `event`: change stats and paths from
   the forge files API, a deterministic change pattern, actor kind and role,
   labels, attempt count, and the linked issue's number, state and labels.
   PR and issue titles, bodies and comments, commit messages and branch
   names are never routing inputs: they are removed from `event`, and a
   `when:` that references them fails config validation. A downgrade rule
   may key on PR or issue labels only from `routing.trusted_labels`; an
   escalation rule may key on any label.
3. **Precedence.** A rule sits below the `--runtime`/`--model`/`--effort`
   flags and `FULLSEND_*` variables and above the agent's `agents:` entry,
   so an explicit per-run override still wins and a rule only refines the
   static choice.
4. **Guards, validated at config time and before the sandbox.** A rule is a
   *downgrade* when any model or effort it resolves, for the agent or a
   persona, sits below the static choice: effort by `low` < `medium` <
   `high` < `xhigh` < `max`, model by its position in
   `routing.allowed_models`, which is ordered lowest tier first; any other
   rule is an escalation, and every guard below applies to that computed
   result. Every model a rule names is in `routing.allowed_models` ∩ the
   catalog, and persona models stay in ADR 0104's closed set; a runtime
   change must keep the agent's needs (no persona roster on codex, fallback
   chain on Claude only). The agents in `routing.never_downgrade` (default
   `triage`, `prioritize` and `review`; config can add to the set but never
   remove a default) keep their static model or move to a higher one, so a
   downgrade rule for `review` moves effort and `subagents`, never the
   orchestrator's model. The resolved model and every persona model are
   checked against the project's served list once per run; a routed model
   that is not served falls back to the static choice, and a static model
   that is not served fails the run before the sandbox exists, naming the
   model. Nothing is retried inside the sandbox.
5. **Effort before model, model before runtime.** A downgrade lowers effort
   on the same model first, then moves within a vendor tier, then across
   vendors; a runtime move is never a downgrade rule's job.
6. **Every choice is echoed** with the rules that set each field: plan block,
   `metrics.json` (`routing.{mode,rules,applied,static_model,static_effort}`),
   root-span attributes `fullsend.routing.*`, status-comment footer. In
   `shadow` the rules are evaluated and recorded and the static choice runs.
7. **Learning is offline and lands as a pull request.** The root span carries
   the routing ledger (choice, rules, task facts, cost, status, verdict) on
   the OTLP path of [ADR 0050](0050-distributed-tracing-instrumentation.md);
   a replay over that ledger scores a candidate policy, and the only path
   from a learned change to production is a reviewed edit to `routing:`.
   No in-run adaptation, no self-deployment.

## Consequences

- A repo can express "sonnet at low effort for docs-only changes" and "opus
  at xhigh with opus security and challenger personas when `internal/scaffold/`
  changes" as reviewed config, resolving the downgrade and escalation issues
  above with the same mechanism.
- Shadow mode plus the ledger produce the quality evidence #6361 lacked
  before any default changes; cost is judged per work item including
  re-review and fix rounds, never per run.
- The served-model preflight ends the retry-on-unavailable incident class for
  routed and static runs alike.
- Presets may not ship `routing.rules` until the layered merge records which
  layer a key came from (the provenance gap ADR 0104 deferred); a lower layer
  may only tighten `never_downgrade` and `allowed_models`.
- A rule can raise the review orchestrator's model; lowering it needs a later
  ADR that amends #6527, on shadow `per_model_usage` evidence. Persona
  routing on the Claude Code runtime remains the second half of ADR 0104.

### Deferred

- **A routing advisor.** Rules cannot read a diff; a short, read-only run on
  the fast tier can, and its schema-validated verdict (`scope`, `risk`,
  `domains`) can become a `task.assessment` fact beside the system-derived
  ones, with an in-run counterpart that lets a worker consult a stronger
  model without switching session model
  ([#6531](https://github.com/fullsend-ai/fullsend/issues/6531)). That is a
  follow-on decision. The constraint this record fixes for it: an assessment
  read from author-controlled content may fire an escalation rule alone but
  may satisfy a downgrade rule only together with system-derived facts that
  would justify the downgrade on their own, and it never widens the allowed
  set or crosses the never-downgrade floor.
