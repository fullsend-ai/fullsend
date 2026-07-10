---
title: "70. Subagent harness schema and sandboxed execution lifecycle"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - sandbox
  - harness
  - subagent
  - isolation
---

# 70. Subagent harness schema and sandboxed execution lifecycle

Date: 2026-07-10

## Status

Accepted

## Context

[ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md)
established that each agent step gets its own sandbox with tailored policies.
Today this is only implemented at the inter-stage level (triage → code → review
as separate pipeline stages). Within a stage, subagents spawned via Claude
Code's Agent tool share the parent's sandbox — filesystem, policy, providers,
and network access — violating the least-privilege principle ADR 0020 prescribes
([#3978](https://github.com/fullsend-ai/.fullsend/issues/3978)).

[ADR 0024](0024-harness-definitions.md) defines the harness schema for
top-level agent invocations. [ADR 0046](0046-host-side-api-server-design.md)
establishes the host-side API server pattern. This ADR decides how subagents
are defined, spawned, and executed in isolated sandboxes using those primitives.

## Decision

Subagents run in isolated child sandboxes via a host API server and a
sandbox-side CLI. The parent agent uses `fullsend-agent` (shipped in the
sandbox image) instead of Claude Code's native Agent tool. The CLI calls a
host-side API server (following the
[ADR 0046](0046-host-side-api-server-design.md) pattern) that spawns each
subagent as `fullsend subagent run` — a new CLI subcommand that creates a
child sandbox, bootstraps it from the subagent's harness, runs Claude Code
with the caller-supplied prompt, validates output against the declared schema,
downloads any declared output files, destroys the sandbox, and returns the
result.

**Harness field applicability.** Subagent harnesses are standalone YAML files
in `harness/`, using the same format as regular harnesses
([ADR 0024](0024-harness-definitions.md)). Most fields apply identically.
Fields that do not apply in subagent mode are validated out: `post_script`
(subagents produce structured output, not SCM mutations), `runner_env` (no
host-side scripts to configure), and `api_servers` (subagents do not spawn
their own API servers).

**New `params` field.** Spawn-time parameters (e.g., `TARGET_REPO`) declared
in the subagent harness. Values are supplied by the parent at invocation and
expand into policies, host files, and scripts via existing `${VAR}` expansion.

**New `output` field.** The result contract — a JSON schema for structured
output, optionally listing files to download from the child sandbox before
destruction. Schema validation uses the same mechanism as
`validation_loop.schema`
([ADR 0022](0022-harness-level-output-schema-enforcement.md)).

**Parent-side `subagent_harnesses` field.** A map of profile labels to harness
paths declared in the parent harness. Serves discoverability (`GET /subagents`
returns available profiles with their parameter schemas and output contracts)
and restriction (only declared harnesses can be spawned, keeping the set of
child capabilities static and auditable).

**Independent provider scoping.** Each subagent harness declares its own
providers, independent of the parent's. A subagent exploring a specific
repository gets only the credentials for that repository. Security derives
from the `subagent_harnesses` declaration being static — the parent cannot
dynamically create new profiles or escalate a subagent's provider set.

**`fullsend-agent` CLI.** Shipped in the sandbox image, this is the parent
agent's only interface for cross-sandbox subagent invocation. Two operations:
`fullsend-agent list` (discover profiles and input/output contracts) and
`fullsend-agent run <profile> --param KEY=VALUE` (spawn and block until
result). The CLI communicates with the host-side API server using the per-run
bearer token from [ADR 0046](0046-host-side-api-server-design.md).

## Consequences

- Subagents achieve the per-step isolation prescribed by
  [ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md)
  within a stage, not just between stages.
- The `subagent_harnesses` declaration is the auditable surface for what a
  parent can spawn — no dynamic capability escalation.
- Each child sandbox incurs provisioning overhead (image pull, provider setup,
  teardown). Latency-sensitive workflows may prefer bundling steps in a single
  sandbox at the cost of wider policy scope (ADR 0020 Option A).
- Implementation depends on host-side API server lifecycle
  ([#879](https://github.com/fullsend-ai/.fullsend/issues/879)) and portable
  provider resolution
  ([#2672](https://github.com/fullsend-ai/.fullsend/issues/2672)).
- In-sandbox Agent tool use remains available for intra-sandbox delegation;
  `fullsend-agent` is for cross-sandbox invocation only.
