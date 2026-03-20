# Experiment: OpenShell for Agent Sandboxing

**Date:** 2026-03-20
**Status:** Complete
**Author:** AI agent (Claude opus-4-6) with human direction

## Goal

Explore using Linux namespace sandboxing (via bubblewrap/`bwrap`) as a mechanism to control network egress for AI coding agents. The concept is called "OpenShell" -- a sandboxed shell wrapper that can selectively permit or deny network access for agent-executed commands.

The specific questions:
1. Can we build a simple wrapper that controls network egress for agent tool calls?
2. Does it work as a positive test (egress allowed)?
3. Does it work as a negative test (egress denied)?
4. What surprising findings emerge about the architecture?

## Approach

We used [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`), a lightweight sandboxing tool that uses Linux user namespaces. It's available in the devaipod container and can create isolated network namespaces without root privileges.

### Test Components

- **`openshell.sh`** -- A bwrap wrapper that accepts `--network=allow` or `--network=deny` to control egress
- **`network-test.sh`** -- A simple script that curls httpbin.org and reports pass/fail
- **`run-tests.sh`** -- Automated test harness

### Test Matrix

| Test | What runs in sandbox | Network mode | Expected | Actual |
|------|---------------------|-------------|----------|--------|
| 1. Baseline curl | Nothing (no sandbox) | Host network | Pass | **Pass** |
| 2. curl in allow-sandbox | `curl` | `--share-net` | Pass | **Pass** |
| 3. curl in deny-sandbox | `curl` | `--unshare-net` | Fail | **Fail** (exit 6, can't resolve host) |
| 4. opencode in allow-sandbox | `opencode run --attach` | `--share-net` | Pass | **Pass** |
| 5. opencode in deny-sandbox | `opencode run --attach` | `--unshare-net` | Fail | **Fail** (can't reach server or LLM API) |
| 6. opencode + sandboxed tool | Tool call via openshell | `--unshare-net` (tool only) | Tool fails, agent works | **Pass** |
| 7. opencode webfetch (unsandboxed) | webfetch built-in tool | Host network | Pass | **Pass** (not sandboxable) |

## Results

All tests produced the expected outcomes. The sandbox correctly permits and denies network egress.

## Surprising Findings

### 1. Sandboxing the entire agent process is a non-starter

When `opencode run` is wrapped in a network-deny sandbox, it fails immediately because:
- It cannot reach `models.dev` to fetch model metadata
- It cannot reach the LLM API (the provider endpoint)
- In `--attach` mode, it cannot even reach the local opencode server on `localhost:4096` because `--unshare-net` creates a completely new network namespace where localhost refers to a different loopback interface

The error output is:
```
ERROR service=models.dev error=Unable to connect. Is the computer able to access the url? Failed to fetch models.dev
Error: Session not found
```

**Implication:** Network sandboxing must be applied at the _tool execution_ level, not at the _agent process_ level. The agent needs network access to function; it's the agent's _actions_ that need to be restricted.

### 2. The tool execution gap: in-process vs subprocess tools

OpenCode has two categories of tools that behave differently under sandboxing:

**Subprocess tools** (sandboxable via OpenShell):
- `bash` -- spawns a child process, can be wrapped with bwrap
- Any MCP server tools that execute as separate processes

**In-process tools** (NOT sandboxable via OpenShell):
- `webfetch` -- makes HTTP requests from within the opencode Node.js process
- `websearch` -- same, runs inside the agent process

This creates a **sandboxing gap**: even if all bash commands are sandboxed with `--unshare-net`, the agent can still make arbitrary network requests via the `webfetch` tool. A prompt injection could exploit this by asking the agent to use `webfetch` instead of `curl`.

**This is a significant security finding.** A complete sandboxing solution must handle both:
- OS-level sandboxing for subprocess tools (bwrap, seccomp, etc.)
- Application-level controls for in-process tools (permissions config, allowlists)

### 3. `--dev /dev` fails in unprivileged containers; `--dev-bind /dev /dev` works

The initial bwrap invocation with `--dev /dev` (which creates a minimal /dev) failed with:
```
bwrap: Can't mount devpts on /newroot/dev/pts: Permission denied
```

This is because `devpts` mounting requires `CAP_SYS_ADMIN` in the mount namespace, which unprivileged containers don't have. Using `--dev-bind /dev /dev` (bind-mount the host's /dev) works but provides less isolation. For production use, a curated device allowlist would be preferable.

### 4. The devaipod architecture already provides one layer of sandboxing

The experiment runs inside a Podman container (devaipod), which already provides:
- PID namespace isolation
- Filesystem isolation (overlay)
- Limited capabilities
- No `iptables`/`nft` access (can't modify firewall rules)

Adding bwrap _inside_ the container creates nested sandboxing. This is defense-in-depth but the outer container already restricts what the inner sandbox can access.

### 5. OpenCode's `--attach` mode reveals an architectural split

When using `opencode run --attach`, the tool execution happens on the _server_ side (PID 38, the opencode serve process), not in the `opencode run` client process. This means:
- Sandboxing the `opencode run` client has no effect on tool execution
- To sandbox tool execution, you'd need to sandbox the server or configure the server to use a sandboxed shell

This is architecturally important: the sandboxing boundary must be at the server, not the client.

## Recommendations for Konflux-CI

### Short-term: Use OpenCode's permission system

OpenCode already has a permission system that can deny specific tools:
```json
{
  "permission": {
    "webfetch": "deny",
    "bash": {
      "*": "deny",
      "git *": "allow",
      "npm test": "allow"
    }
  }
}
```

This is application-level sandboxing, not OS-level, but it's available today.

### Medium-term: Implement a sandboxed bash wrapper

Configure OpenCode to use an openshell-style wrapper as its bash executor. This could be done by:
1. Setting a custom shell in the agent's environment
2. Wrapping the tool execution in bwrap automatically
3. Using OpenCode's custom tools to create sandboxed variants of bash

### Long-term: Defense-in-depth architecture

```
┌─────────────────────────────────────────┐
│  Container (Podman/Kubernetes)          │  ← Outer sandbox
│  ┌───────────────────────────────────┐  │
│  │  Agent Process (opencode serve)   │  │  ← Needs network for LLM API
│  │  ┌─────────────────────────────┐  │  │
│  │  │  Tool Execution (bwrap)     │  │  │  ← Inner sandbox, no network
│  │  │  - bash commands            │  │  │
│  │  │  - file operations          │  │  │
│  │  │  - build commands           │  │  │
│  │  └─────────────────────────────┘  │  │
│  │  In-process tools (webfetch)      │  │  ← Needs allow-list/policy
│  └───────────────────────────────────┘  │
│  Network policy (egress allowlist)      │  ← Only LLM API endpoints
└─────────────────────────────────────────┘
```

The container-level network policy should allowlist only the LLM API endpoints. The inner bwrap sandbox should deny all network. In-process tools like webfetch should be controlled via OpenCode's permission config or disabled entirely for autonomous agents.

## Open Questions

1. **Can OpenCode's bash tool be configured to use a custom shell wrapper?** This would allow transparent sandboxing without modifying agent prompts.
2. **Should webfetch be disabled for autonomous agents?** It bypasses OS-level sandboxing. If agents need to fetch documentation, could this be pre-fetched or provided as context?
3. **How does bwrap interact with Kubernetes pod security policies?** In a Kubernetes deployment, `user_namespaces(7)` may be restricted. Would seccomp or AppArmor profiles be more appropriate?
4. **What about MCP server tool calls?** MCP servers run as separate processes and may make their own network requests. The sandboxing model needs to account for this.
5. **Performance overhead:** bwrap adds namespace setup overhead to every tool call. Is this acceptable for interactive agent workflows?

## How to Reproduce

```bash
# Run the basic tests
cd experiments/openshell-sandboxing
./run-tests.sh

# Manual tests
./openshell.sh --network=allow -- curl https://httpbin.org/get  # Works
./openshell.sh --network=deny  -- curl https://httpbin.org/get  # Fails

# Test with opencode (requires running opencode server)
opencode run --attach http://localhost:4096 \
  "Run this command: /path/to/openshell.sh --network=deny -- curl https://httpbin.org/get"
```

## Files

- `openshell.sh` -- The sandboxed shell wrapper (bwrap-based)
- `network-test.sh` -- Simple network egress test
- `run-tests.sh` -- Automated test harness
- `README.md` -- This document
