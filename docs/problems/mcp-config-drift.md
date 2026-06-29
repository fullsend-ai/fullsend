# MCP Configuration Drift

Detecting and responding to unauthorized changes in MCP (Model Context Protocol) server configurations. MCP configs define what external tools and services an agent can access; silent modification is an escalation vector that bypasses all other security controls.

**Related:**
- [security-threat-model.md](security-threat-model.md) — Threat 1 (persistent injection via externally editable resources), cross-cutting security principles
- [agent-architecture.md](agent-architecture.md) — agent roles and trust model
- [ADR 0017](../ADRs/0017-credential-isolation-for-sandboxed-agents.md) — credential isolation

## The problem

MCP configuration files (`.mcp.json`, tool server manifests, and similar declarative configs) define the tool surface available to an agent. An agent configured with an MCP server has access to every tool that server exposes. These configs are:

1. **Stored as plain files in the repository or workspace**, subject to the same modification vectors as any other file
2. **Consumed at agent startup**, meaning changes take effect on the next run without any approval step
3. **Not monitored for integrity** between runs

This creates an attack surface that the existing threat model partially identifies under [Threat 1: Persistent injection via externally editable resources](security-threat-model.md#persistent-injection-via-externally-editable-resources), but does not fully address for MCP specifically.

### Attack scenarios

**Scenario 1: Malicious MCP server injection.** An attacker adds a new MCP server entry pointing to an attacker-controlled endpoint. The agent now has access to attacker-defined tools that can intercept data, return manipulated results, or expose capabilities the agent should not have. The agent trusts the tools because they are declared in its configuration.

**Scenario 2: Server endpoint replacement.** An attacker modifies an existing MCP server entry to point to a different endpoint (e.g., replacing an internal service URL with an external proxy). All tool calls the agent makes through that server now pass through the attacker's infrastructure, enabling data interception and response manipulation.

**Scenario 3: Permission escalation via tool surface expansion.** An attacker modifies the MCP config to add tools or capabilities to an existing server entry, expanding the agent's effective permissions beyond what was originally intended. The agent gains access to destructive operations, data sources, or APIs it was not designed to use.

**Scenario 4: Gradual drift without adversarial intent.** MCP configs evolve organically as teams add integrations. Without a baseline, there is no way to distinguish an intentional configuration change from an unauthorized one. Over time, agents accumulate tool access that no one explicitly approved, violating the principle of least privilege.

### Why existing defenses are insufficient

- **CODEOWNERS** can guard MCP config files, but only in repos that explicitly configure this. Many repos treat config files as low-sensitivity and do not require human approval for changes.
- **The immutable agent policy principle** (cross-cutting security principle 6) states that agent rules cannot be modified through the channels agents operate on. MCP configs are agent rules (they define tool access), but they are stored in files that agents may be able to modify or that PRs can change.
- **Credential isolation** ([ADR 0017](../ADRs/0017-credential-isolation-for-sandboxed-agents.md)) keeps secrets out of the sandbox, but MCP server endpoints themselves are not secrets. A malicious server URL passes all credential checks because the credential is in the server, not the config.
- **The tool allowlist hook** (`tool_allowlist_pretool.py`) operates on tool names, not server endpoints. An attacker who replaces the endpoint behind a trusted tool name bypasses the allowlist entirely.

## Defense considerations

### Approach 1: Baseline and diff at session start

At the beginning of each agent run, the harness hashes all MCP configuration files and compares against a stored baseline. Any deviation triggers an alert or blocks the run.

**Implementation:**
- First run: compute SHA-256 of each MCP config file, store as baseline (in a harness-controlled location outside the sandbox)
- Subsequent runs: recompute hashes, compare to baseline
- On mismatch: log the diff, block the run, notify the repository owner

**Trade-offs:**
- Simple to implement (file hashing, no external infrastructure)
- Catches any modification, including legitimate changes (requires a workflow to update the baseline)
- Does not detect changes to what the MCP server *serves* (the config may be unchanged, but the server's tool surface may have changed)
- Baseline storage location matters: if the baseline is in the repo, it can be modified alongside the config
- **Trust-on-first-use (TOFU) bootstrapping risk:** if the first run occurs against an already-compromised config, the baseline captures the malicious state and all subsequent runs pass. The baseline should be established from a known-good state or reviewed by a human before being trusted

### Approach 2: MCP config as immutable harness input

Treat MCP configurations as harness-level inputs (like agent system prompts) rather than workspace files. The harness injects the MCP config into the sandbox at startup from a trusted source (e.g., the org config repo, a central policy store), and the agent cannot modify it during the run.

**Trade-offs:**
- Strongest isolation (the agent never sees the config file, only the resolved tool surface)
- Requires centralized MCP config management, which adds operational complexity
- Makes per-repo tool customization harder
- Aligns with the existing pattern of harness-level control ([ADR 0016](../ADRs/0016-unidirectional-control-flow.md))

### Approach 3: Content-aware validation

Beyond hashing, parse the MCP config and validate its contents against a policy:
- All server endpoints must be on an allowlist of trusted domains
- Tool surface must be a subset of the approved tools for the agent's role
- No new server entries without explicit approval in the policy

**Trade-offs:**
- Catches semantic threats that hash-based diffing misses (e.g., a config change that adds a new tool to an existing server)
- Requires maintaining an allowlist of trusted MCP servers and approved tool surfaces per agent role
- More complex to implement than simple hashing
- Policy maintenance burden scales with the number of MCP integrations

## Relationship to existing security hooks

The SSRF validator blocks connections to private networks and metadata endpoints for Bash and WebFetch tool calls. However, MCP server connections are established by the runtime's MCP client, which may not flow through the tool-call hook mechanism. This means SSRF validation provides partial coverage for MCP endpoints, not complete coverage. MCP config drift detection operates at a different layer: it validates the *configuration* before any connections are attempted, rather than relying on runtime request-level blocking. Both are needed, but drift detection is the primary defense for MCP specifically because it prevents a malicious endpoint from entering the config in the first place.

## Relationship to other problem areas

- **[Security Threat Model](security-threat-model.md):** MCP config drift is a specific instance of [Threat 1: Persistent injection via externally editable resources](security-threat-model.md#persistent-injection-via-externally-editable-resources). The threat model identifies the general pattern; this doc applies it specifically to MCP configurations, which define the agent's tool surface rather than just influencing its behavior through text.
- **[Governance](governance.md):** Who controls MCP config policy? If per-repo teams can add arbitrary MCP servers, the org loses visibility into what tools agents can access. Governance determines whether MCP configs are repo-level decisions or org-level policy.
- **[Agent Architecture](agent-architecture.md):** MCP configs relate to agent roles and trust boundaries. Different agent roles should have different tool surfaces, and MCP configs are the mechanism that defines those surfaces. Drift in one agent's config can expand its effective authority beyond its intended role.

## Open questions

- Should MCP config drift detection be a harness-level check (runs before the agent starts) or a hook (runs within the agent's execution)?
- How do we handle legitimate MCP config changes? Is the right model a manual baseline update, or an approval workflow integrated with the PR process?
- Should the baseline include only the config file contents, or also the resolved tool surface (what tools each server actually exposes at runtime)?
- How does this interact with dynamic MCP server discovery, where agents may connect to servers not declared in static config files?
- Should MCP config files be added to CODEOWNERS by default when fullsend is installed in a repo?
- What is the right response to drift detection: block the run, alert and continue, or degrade to a reduced tool surface?
