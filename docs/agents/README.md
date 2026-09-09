---
description: Reference for the agents fullsend ships and how to register custom agents — resolved at runtime from the fullsend-ai/agents repository or your org and repo config.
---

# Agents

Reference documentation for the agents shipped by fullsend.
The default agents are defined in the
[`fullsend-ai/agents`](https://github.com/fullsend-ai/agents) repository
and resolved at runtime via config entries or agents-repo fallback.
Custom agents can be registered via the `agents:` field in org or per-repo
config (see [Architecture](../architecture.md#agent-registry) for details on the registration model).

| Agent | Summary |
|-------|---------|
| [Triage](triage.md) | Inspects new issues and produces structured triage decisions |
| [Prioritize](prioritize.md) | Scores issues using the RICE framework for project board ranking |
| [Code](code.md) | Implements fixes and features from triaged issues |
| [Review](review.md) | Reviews pull requests for correctness, security, and intent alignment |
| [Fix](fix.md) | Addresses review feedback on open PRs |
| [Retro](retro.md) | Analyzes completed workflows and proposes system improvements |

## Configuration

All agents can be configured by adding instructions and skills to your
repository. Changes to `AGENTS.md` affect every agent; skills let you tune how
a specific agent performs a specific task. See
[Configuring with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Configuring with Skills](../guides/user/customizing-with-skills.md).

At some point, enough configuration turns a configured default agent into a
derived agent. See [Default, derived, and custom agents](topics/default-vs-custom.md)
for where that line is and why it matters.

## Custom Agents

Custom agents can be added to the fullsend pipeline via the `agents:` field in
your org-level or per-repo `config.yaml`. Each entry is either a local path
(relative to the fullsend directory) or a pinned HTTPS URL with an integrity
hash. Config entries are looked up directly by name; when absent, a runtime
fallback resolves known first-party agents from `fullsend-ai/agents`.
See [Bring Your Own Agent](../guides/user/bring-your-own-agent.md)
for the complete guide to building and registering custom agents.
