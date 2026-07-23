# Agent-Driven Evaluation Tools

How fullsend's own problem areas appear in agent-driven tools that evaluate agent configurations (or "who watches the watchmen").

## Context

Agent setup evaluation tools (linters, security scanners for skills/commands/agents/hooks) are themselves agent-driven systems. They face a subset of the same challenges fullsend faces, viewed from the tooling side rather than the platform side.

## Relevant problem areas

### [Testing agents](../../testing-agents.md)

An evaluation tool's own agent setup (skills, commands, hooks) needs the same static analysis it provides to others. This is the "who watches the watchmen" problem. The tool must dogfood its own checks, and its CI must gate on its own lint and security rules. Failure to do this means the tool's own configuration can drift into the patterns it flags for others.

### [MCP configuration drift](../../mcp-config-drift.md)

Evaluation tools that integrate with MCP servers (for LLM-based review) face config drift when new MCP servers are added or removed. The tool's own cross-component analysis (phantom MCP detection) directly addresses this problem for downstream users, but the tool itself must also keep its own MCP config current.

### [Tool call risk assessment](../../tool-call-risk-assessment.md)

Evaluation tools that run security scans must not themselves become attack vectors. A malicious skill under evaluation could contain patterns designed to influence the evaluator's behavior (anti-jailbreak patterns, evaluator-targeted prompt injection). The tool needs its own defense against adversarial inputs, which is distinct from the defenses it provides to users.

### [Trustworthiness evidence](../../trustworthiness-evidence.md)

For an evaluation tool, trustworthiness evidence takes a specific form: false positive rate, false negative rate, and rule accuracy over time. If the tool flags too many false positives, teams disable it. If it misses real issues, teams lose trust. Tracking these metrics is how the tool earns continued adoption.

## Unique considerations

- **Recursive evaluation:** the tool must be able to evaluate its own setup without circular dependency issues
- **Rule accuracy feedback loop:** users who suppress findings or override verdicts generate signal about rule quality
- **Multi-tool support:** unlike fullsend (which targets a specific platform), evaluation tools must handle multiple AI assistants (Claude Code, Cursor, Copilot, Gemini, OpenCode) with different configuration formats
