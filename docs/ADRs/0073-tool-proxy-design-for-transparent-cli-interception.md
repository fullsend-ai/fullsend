---
title: "73. Tool-proxy design for transparent CLI interception"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - sandbox
  - harness
  - tool-proxy
  - credential-isolation
---

# 73. Tool-proxy design for transparent CLI interception

Date: 2026-07-17

## Status

Accepted

## Context

Sandboxed agents must interact with external services (forges, registries,
project management tools) that require host-side credentials or steering.
[ADR 0046](0046-host-side-api-server-design.md) introduced host-side API
servers (credential delivery tier 3) for operations that providers cannot
handle, but agents must learn custom API endpoints to use them. This fights the
agent's training -- LLMs have deep, reliable knowledge of popular CLI tools
(`gh`, `glab`, `jq`) but no knowledge of project-specific HTTP endpoints.

[ADR 0032](0032-safe-push-wrapper-for-sandboxed-agents.md) established the
single-tool precedent with `safe-push`: a binary wrapper that intercepts
`git push`, enforces policy, and delegates to the real binary. Tool proxies
generalize this pattern to arbitrary CLI tools via agent-runtime hooks rather
than per-tool wrapper binaries.

The [rehor project](https://github.com/OpenShift-Fleet/rehor) validated the
executor/shim pattern using gRPC-forwarded CLI proxies for credential isolation.
The [openshell-policy-bypass experiment](https://github.com/fullsend-ai/experiments/pull/5)
confirmed that OpenShell's `network_policies.binaries` field enforces
binary-to-endpoint restrictions regardless of how the agent invokes a tool.

## Options

### Interception strategy

**Hook-based interception (Claude Code `PreToolUse`, OpenCode
`tool.execute.before`):** the agent runtime's hook system intercepts tool calls
before execution, rewrites them to target the host-api server, and returns the
response transparently. No binary replacement needed -- the agent calls `gh pr
create` as trained, the hook redirects to the host-api server, and the response
looks like native CLI output. Hooks are deployed during sandbox bootstrap
alongside existing security hooks.

**Binary replacement:** replace the real CLI binary with a shim that forwards to
the host-api server. Proven by `safe-push` (ADR 0032), but requires per-tool
shim binaries baked into the container image, each handling argument parsing,
output formatting, and error mapping for a specific CLI. Scales poorly to many
tools.

Hook-based interception is preferred because it covers multiple tools with a
single mechanism, deploys at bootstrap time without image changes, and composes
with existing security hooks. OpenShell's binary-level network policy provides
the security boundary regardless -- even if the agent bypasses the hook and
calls the real binary, `network_policies.binaries` blocks unauthorized network
access.

## Decision

Introduce a `tool_proxies` field in the harness YAML schema
([ADR 0024](0024-harness-definitions.md)) that declares which CLI tools should
be transparently intercepted and routed to a host-api server.

**Harness schema addition:**

```yaml
# harness/<agent>.yaml (addition to existing schema)
tool_proxies:
  - tool: gh             # CLI tool name the agent calls
    server: github-api   # host-api server name (from api_servers)
    commands:            # optional command filter (empty = all commands)
      - pr
      - issue
```

Each entry maps a tool name to a host-api server declared in `api_servers`.
The optional `commands` field restricts interception to specific subcommands --
unmatched commands pass through to the real binary (if network policy allows).

**Interception mechanism.** The runner generates runtime-specific hooks during
sandbox bootstrap. For Claude Code, a `PreToolUse` hook intercepts Bash tool
calls matching the declared tool names, rewrites them as HTTP requests to the
mapped host-api server, and returns the response. The hook reuses the per-run
UUID bearer token already established by [ADR 0046](0046-host-side-api-server-design.md)
for authentication. For other runtimes (e.g. OpenCode), equivalent
interception uses that runtime's plugin or hook system.

**Relationship to host-api servers.** Host-api servers
([ADR 0046](0046-host-side-api-server-design.md)) remain independently usable
-- agents can call them directly via HTTP when tool-proxy transparency is not
needed. Tool proxies are an optional client-side layer that maps familiar CLI
syntax to host-api server endpoints. The server's process contract, lifecycle
management, and provider-backed network policy are unchanged.

**Policy composition.** Tool-proxy entries compose with OpenShell's
`network_policies.binaries` field:

- The real CLI binary (e.g. `/usr/bin/gh`) may be blocked from reaching
  external endpoints by the binary-level network policy -- this is the
  expected configuration when the tool proxy is active.
- The host-api server endpoint is allowed via a provider profile
  ([ADR 0046](0046-host-side-api-server-design.md)).
- If the agent bypasses the hook and invokes the binary directly, the network
  policy blocks the call. The security boundary is the network policy, not the
  hook.

**Relationship to safe-push.** `safe-push` ([ADR 0032](0032-safe-push-wrapper-for-sandboxed-agents.md))
is a binary-replacement tool for `git push` with policy enforcement at the
binary level. Tool proxies do not replace `safe-push` -- push operations require
binary-level enforcement because the security-critical distinction (force-push
vs regular push) is in the request body, not the endpoint. Tool proxies address
the transparency problem (agent uses trained CLI knowledge), while `safe-push`
addresses the mandatory policy enforcement problem (agent cannot bypass push
restrictions). Both can coexist in the same harness.

## Consequences

- The harness YAML schema ([ADR 0024](0024-harness-definitions.md)) gains a
  `tool_proxies` field. No existing fields change.
- Agents use CLI tools as trained (`gh pr create`, `glab issue list`) without
  learning project-specific HTTP APIs, reducing token overhead and improving
  reliability.
- Hook generation is runtime-specific: the runner must generate the correct
  hook format for each supported runtime (`ClaudeHooksBootstrap` for Claude
  Code, equivalent for others). Adding a new runtime requires implementing
  its hook generation.
- The security boundary remains the network policy, not the hook. A compromised
  or bypassed hook does not grant the agent unauthorized network access.
- `safe-push` remains the mechanism for push policy enforcement. Tool proxies
  complement but do not replace binary-level enforcement where request-body
  inspection is required.
