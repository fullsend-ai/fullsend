---
title: "0017. Credential Isolation for Sandboxed Agents"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - credentials
  - sandbox
  - security
---

# 0017. Credential Isolation for Sandboxed Agents

Date: 2026-04-01

## Status

Accepted (credential delivery tiers extended by [ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md))

## Context

When sandboxed agents need to perform operations requiring credentials (e.g. reading or writing GitHub issues), the credential must be kept away from the agent process. A compromised agent with access to a credential can exfiltrate it — once the credential leaves the sandbox, the attacker can use it without any sandbox constraints.

This decision was informed by PoC work on the [agent-scoped-tools triage experiment](https://github.com/fullsend-ai/experiments/tree/main/agent-scoped-tools-triage), where sandboxed subagents need read access to GitHub issues and a top-level agent needs write access. The threat model prioritizes external injection and compromised credentials (see [security-threat-model.md](../problems/security-threat-model.md)).

## Options

### Option 1: One server per permission set, different ports

Run separate host-side servers per capability (e.g. `:8083` for read-only issues, `:8084` for write). Each server holds the credential and only exposes its specific operations. Sandbox network policy allows/denies ports.

**Trade-offs:** Strongest isolation — port-level enforcement is simple and hard to misconfigure. But requires managing multiple server processes and ports per capability set.

### Option 2: Single host-side REST API with L7 network policy enforcement

One server on the host holds the credential and exposes all operations as REST endpoints. Sandbox network policies enforce per-agent access at L7 (HTTP method + path). Agents call the server via `curl` — no credential, no SDK, no client library.

**Trade-offs:** Single process, simple to operate. L7 policy enforcement is already proven in OpenShell. Agents need instructions on how to call the API. Relies on correct policy authoring for path-level restrictions.

### Option 3: Scoped tokens per agent (GitHub App installation tokens)

Generate a short-lived token per agent scoped to only the permissions that agent needs. Pass the token into the sandbox.

**Trade-offs:** Uses GitHub's native permission model. But GitHub token granularity is coarse — you can scope to "read issues" but not to "read only issue #3 in repo X." Minimum TTL is 1 hour, far exceeding typical agent lifetime (~6 min). A compromised agent can exfiltrate the token and use it for the full TTL across any repo the installation covers. The [Model Armor experiment](https://github.com/fullsend-ai/experiments/tree/main/model-armor-vs-agent-triage) demonstrated that pre-scan injection defenses catch only 25% of payloads — meaning tokens inside a sandbox *will* eventually be exfiltrated if the agent processes attacker-controlled input.

### Option 4: MCP server with per-agent tool filtering

One MCP server runs outside the sandbox, holds the credential, and exposes all tools. Generate per-agent MCP configs that only list the server if the agent uses `mcp__` tools. Rely on runtime tool scoping (agent `tools` field) to restrict which tools each agent can call.

**Trade-offs:** No credential in sandbox. But runtime tool scoping is the only barrier — the MCP server itself accepts any call that reaches it. A compromised agent that crafts raw HTTP requests to the MCP server can call any tool, bypassing the runtime restriction. Additionally, MCP clients in current runtimes (e.g. Claude Code) have internal timeouts (~30-60s) that cannot be reliably configured ([anthropics/claude-code#7575](https://github.com/anthropics/claude-code/issues/7575)). Long-running operations (such as sandbox creation + agent execution) cause the client to close the connection before the response arrives, resulting in broken pipe errors. This makes MCP unsuitable for operations that take longer than the client's internal timeout. [service-gator](https://github.com/LobsterTrap/service-gator) is a related MCP tool that addresses the tool-scoping bypass by enforcing permissions at the server level, but it is limited to forge and issue tracker interactions (GitHub, GitLab, Forgejo, JIRA) rather than being a general-purpose credential proxy, and the MCP timeout limitation still applies.

### Option 5: Copy tools (scripts) into the sandbox

Bundle credential-bearing scripts into each sandbox. The script makes the API call with embedded credentials.

**Trade-offs:** Self-contained, no network dependency on host. But credentials exist inside the sandbox (in the script or its HTTP traffic). A compromised agent can read the script source, inspect `/proc` for environment variables, or sniff outbound traffic to extract the credential.

### Option 6: Sidecar proxy inside the sandbox

A proxy process runs inside the sandbox, holds the credential, and only exposes specific operations on localhost. The agent calls the proxy without auth.

**Trade-offs:** Clean agent interface. But the credential is in the sandbox's process space — a compromised agent with elevated access can extract it from memory or `/proc/<pid>/environ`.

### Option 7: Prefetch + post-process (no runtime credential access)

A deterministic pre-script runs before the agent, fetching all data the agent will need (issue descriptions, referenced files, linked issue content) and writing it to the sandbox filesystem. The agent runs with no network access to credential-bearing services — it reads from the filesystem and writes its output (e.g., a triage decision, a comment body) to a well-known output path. A post-script reads the agent's output and performs the credentialed actions (posting the comment, applying labels).

**Trade-offs:** Strongest isolation — zero credentials and zero credential-bearing network access during the agent run. The agent cannot exfiltrate credentials because none exist anywhere in its environment. But prefetching requires knowing in advance *every piece of data* the agent will need, and this set must be fully determined by the triggering event alone. In practice, even seemingly simple agents like triage need to follow unpredictable reference chains — an issue links to an external repo's issue, which references files in another repo, which point to documentation elsewhere. Prefetching either misses references the agent would have followed dynamically (incomplete) or pulls in too much (slow startup, increased data-on-disk risk). This makes prefetch viable only for agents with a closed, fully enumerable set of inputs (e.g., a linter that only needs the diff, or a labeling agent that reads only the issue title and body with no link-following). Prefetched data from private sources also persists on the runner's filesystem — a risk if the runner dies or is shared, since the data outlives the agent process. Contrast with the REST server model where data stays in server-process memory.

## Decision

**Default: full isolation (option 7, prefetch + post-process).** Agents run with zero network access to credential-bearing services. Inputs are pre-fetched by a deterministic pre-step; outputs are applied by a deterministic post-step. This is the expected model for any new agent. The first engineering question for a new agent is: *"Can this agent run with zero access to credential-bearing services?"* If yes — no proxy, no L7 policies, no policy maintenance, no new attack surface. Agents that cannot complete their work with prefetched data should stop early or produce a partial result rather than requiring runtime credential access by default.

**Fallback: host-side REST API with L7 enforcement (option 2).** When an agent must interact with external services mid-run — following unpredictable reference chains, multi-turn iteration, or accessing data that cannot be enumerated from the triggering event alone — a host-side REST server holds credentials and exposes scoped endpoints. L7 network policy enforces per-agent access. This model carries ongoing engineering and maintenance cost (proxy endpoints, L7 policies, input validation, output scanning) and should be adopted only when full isolation is not viable.

Both models share the same principle: **credentials never enter the sandbox.**

Options 3–6 were rejected. Scoped tokens (option 3) were rejected because prompt injection cannot be reliably prevented in principle — no detection rate is sufficient, so any credential present in the sandbox must be assumed exfiltrable. The [Model Armor experiment](https://github.com/fullsend-ai/experiments/tree/main/model-armor-vs-agent-triage) illustrates this empirically (25% detection rate), but the argument does not depend on any specific percentage: the attack is fundamentally asymmetric, and a single successful injection exfiltrates the token for the remainder of its TTL across any repo the installation covers. MCP with tool filtering (option 4) was rejected because MCP tool scoping is only enforced at the runtime level, not the server level — a compromised agent can bypass it, and MCP clients have internal timeouts that make them unsuitable for long-running operations. Per-port servers (option 1) were rejected because L7 enforcement provides equivalent security with less operational overhead.

**Why a REST server instead of passing tokens directly.** A host-side REST server in the same GitHub Actions workflow receives the token via the same mechanism (environment/secrets) as any other step. The security boundary is not *where the token lives in the workflow* but *what the agent can do with it*. A raw token in the sandbox has the full scope of whatever GitHub App installation or PAT it was minted from. The REST server restricts the agent to specific operations (HTTP method + path) enforced by L7 policy — the agent can call `GET /repos/{owner}/{repo}/issues/{number}` but cannot call `DELETE /repos/{owner}/{repo}` even though the underlying token would permit it. The REST server is a *capability reducer*, not merely a *credential holder*.

**Agent configuration drives server lifecycle.** Which REST servers to spawn and which pre/post scripts to run is declared in the agent's configuration file, stored in a well-known directory in the repo. The entry point receives only the agent name; it looks up the agent's YAML configuration to determine what infrastructure to provision. This keeps the entry point generic and makes adding new agents a configuration change rather than a code change.

This decision has been validated by two subsequent experiments:

- The [triage subagents PoC](https://github.com/fullsend-ai/experiments/tree/main/agent-scoped-tools-triage) demonstrated the full pattern end-to-end: a host-side GitHub REST server on `:8081`, per-agent OpenShell network policies (read-only for subagents, write for the orchestrator), and zero credentials in any sandbox.
- The [OpenShell sandbox evaluation](https://github.com/fullsend-ai/experiments/blob/main/openshell-sandbox-evaluation.md) confirmed that L7 path-level enforcement is production-ready, with hot-reloadable policies and full audit trails.

## Consequences

- Agents have zero credentials in their environment — nothing to exfiltrate.
- Agent skills must document how to call the REST API (curl commands, endpoints, expected responses).
- Sandbox policy authoring is security-critical — incorrect path patterns can over-permit access. Note that L7 path-level enforcement cannot restrict operations encoded in the request body (e.g. git push branch names) — use server-side controls (GitHub branch rulesets) for those cases. See the [OpenShell evaluation](https://github.com/fullsend-ai/experiments/blob/main/openshell-sandbox-evaluation.md) for details.
- The host-side server is a single point of failure; if it goes down, all agents lose access.
- This pattern generalizes beyond GitHub to any credential-bearing external service.
- A centralized gateway (e.g. [Agent Gateway](../landscape.md#agent-gateway)) could sit in front of host-side servers to add cross-agent rate limiting, audit logging, and CEL-based RBAC — a complementary control, not a replacement for per-sandbox L7 policy.
- The admin CLI's secrets layer stores agent private keys in the `.fullsend` config repo (as repo secrets), keeping credential distribution aligned with this decision: credentials flow through host-side infrastructure, never into agent environments.
- **Host-side REST servers must be secured against unintended access within the same job.** On GitHub-hosted runners, each job gets a fresh ephemeral VM ([GitHub docs: about GitHub-hosted runners](https://docs.github.com/en/actions/using-github-hosted-runners/about-github-hosted-runners)). Other jobs — even from the same workflow — run on separate VMs and cannot reach the REST server. The threat surface is within the same job: other workflow steps share the same VM and localhost, so if the REST server is left running as a background process, a subsequent step could call it. Mitigations: (1) start and stop the REST server within the same step as the sandbox, so it is not exposed to other steps; (2) use a per-run bearer token (a random UUID generated at sandbox creation, passed to both the server and the sandbox) so that even if timing overlaps, unintended callers are rejected; (3) per-sandbox tokens additionally let the server identify which sandbox is calling and enforce policy server-side (defense-in-depth over relying solely on OpenShell L7). **Exception:** single-CPU GitHub-hosted runners run in containers on a shared VM rather than in dedicated VMs — on those runners, container network namespace isolation determines whether other containers can reach localhost, making the per-run token more important.
- **Prefetched data on disk outlives the agent process.** When using the prefetch model, data from private repositories or restricted sources is written to the runner's filesystem. If the runner crashes or is not properly cleaned up, this data persists. Post-run cleanup scripts must be treated as security-critical. The REST server model avoids this because data stays in server-process memory and is lost when the process exits.
- **The REST server and L7 policies are the security boundary, and their maintenance cost is non-trivial.** Every operation an agent needs must be implemented as a proxy endpoint with input validation, output sanitization, and credential scanning. Every agent role needs L7 policy rules matching those endpoints. Both must be maintained as upstream service APIs change and new agent roles are added. Each new external service (Jira, GitLab, Slack) multiplies the proxy surface — new endpoints, new validation rules, new credential patterns to scan for, new L7 policy paths. The ownership model for this security-critical code is an open question: centralizing it in the fullsend repo makes the fullsend team a bottleneck for every new integration; distributing it to enrolled repos adds operational burden that fullsend's shim-workflow model is designed to avoid. This cost reinforces the decision to make full isolation the default — agents that can run without runtime credential access avoid the proxy maintenance cost entirely.
- **Agent configuration is the source of truth for sandbox infrastructure.** Each agent's YAML configuration file declares what REST servers, pre-scripts, and post-scripts it requires. The sandbox framework reads this configuration and provisions accordingly. This means adding a new agent or changing an agent's credential access is a configuration change reviewed via the normal PR process — no changes to the entry point or workflow files required. The agent configuration directory should be CODEOWNERS-guarded since it determines what credentials and operations each agent can access.
