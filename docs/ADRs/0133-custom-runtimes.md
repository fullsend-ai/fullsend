---
title: "133. Custom script entrypoints for deterministic orchestration"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - runtime
  - harness
---

# 133. Custom script entrypoints for deterministic orchestration

Date: 2026-10-01

## Status

Accepted

## Context

Fullsend's usual launch path gives a built-in agent runtime control of the
task's tool loop. Some workloads instead need a program to own ordering,
branching, batching, and worker concurrency, with model calls as bounded
steps in that program. Both are valid workflow shapes; this decision adds the
script-led shape without changing the agent-led default.

Two existing workloads illustrate the need:

- **strat-creator** uses deterministic Bash/Python batches over Jira work.
  Its GitLab jobs process many tickets and run for multiple hours.
- **architecture-context** uses a Python meta-harness for fixed phases and
  per-component work. It takes about 30 minutes with 20 workers and longer
  when worker concurrency is reduced; phases may call Claude or Codex.

In these workflows, scripts own sequencing and state while agents perform
specific tasks. Moving that sequencing into a prompt would shift
responsibility for interpreting it to the agent runtime.
[ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md) records
earlier cases where agents skipped or reordered scripted steps. Multi-hour
execution is an existing workload requirement.

## Options

**Keep orchestration in CI.** This preserves the existing scripts but leaves
their execution outside Fullsend's managed sandbox path.

**Use runtime-native workflow features.** [Claude Workflows](https://code.claude.com/docs/en/workflows)
and [pi-extensible-workflows](https://github.com/vekexasia/pi-extensible-workflows)
offer deterministic orchestration within their runtimes but do not run
existing workflows unchanged; porting and evaluating them is a material cost.

**Add a Fullsend script entrypoint (chosen).** This keeps script-controlled
sequencing in the existing Fullsend run and sandbox while preserving the
agent-led path for tasks where an agent should own the tool loop.

## Decision

Add an opt-in `entrypoint:` mapping to harness YAML. It selects a
user-supplied command as the sandbox's top-level process. `command` is an
argv array; the runner launches it directly. The script, rather than an agent
prompt, owns intra-run sequencing, branching, batching, and worker
concurrency. The entrypoint and its required companion files must be delivered
as harness resources; the implementation specification defines the packaging
details. An optional `stream_format` belongs under
`entrypoint:` and describes a single supported stdout stream.

Without `entrypoint:`, the existing agent-led launch remains unchanged.
With it, Fullsend still owns dispatch, sandbox creation and policy,
credentials it provisions, the run timeout, and result collection. The
entrypoint runs within that one sandbox for a single run, bounded by the
configured run timeout and the hosting job's limits (GitHub-hosted jobs stop
at six hours; GitLab jobs at the configured job timeout). This decision
does not add multi-sandbox orchestration.

`agent:` may accompany `entrypoint:` to make an agent definition available
to the script's worker invocations; it does not launch the top-level agent
loop. A selected `runtime:` remains independent and may provision one
built-in runtime for worker calls through a supported invocation path. Direct
SDK or CLI calls need their own credentials and egress configuration and do
not automatically receive that runtime's tool hooks. A workflow using
multiple providers must configure each explicitly; this decision does not
add multi-runtime bootstrap.

## Consequences

- Existing deterministic Bash and Python orchestration can run in Fullsend
  without requiring a rewrite as prompts or runtime-specific workflow scripts.
  Script authors own worker scheduling, per-run state, retries, and
  idempotency. Run chaining and durable state across runs are outside this
  decision. Multi-hour, multi-stage rounds like the strat-creator batches
  remain for a run-chaining follow-up; `entrypoint:` does not address them.
- Workers share the entrypoint's sandbox policy and filesystem. This trades
  per-worker isolation for sequencing within one run; the effective policy
  must cover the combined work.
- Child runtime calls must use a supported path to receive that runtime's
  configured hooks and integrity checks. Direct SDK calls do not use runtime
  tool hooks. OpenShell policy still applies to all processes in the sandbox.
- A generic entrypoint has no inferred agent telemetry. A selected
  `stream_format` can parse one supported stream, but does not aggregate
  usage, status, or costs across multiple worker calls.
- Adding `entrypoint:` changes the harness contract. Version-aware loaders
  must reject unsupported schema versions so older consumers do not silently
  ignore the field ([ADR 0127](0127-harness-schema-versioning-and-field-types.md)).
- The execution contract must define resource packaging, task inputs and
  outputs, mixed-provider credentials, timeout/retry behavior, and how
  scripts report worker results. These details belong in the implementation
  specification and must be validated against both motivating workloads.
- This capability is experimental and opt-in; it may change, be disabled,
  or be removed in a future release.
