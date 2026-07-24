# Agents

Reference documentation for the agents shipped by fullsend.
The default agents below are defined by the YAML files in
[`internal/scaffold/fullsend-repo/harness/`](../../internal/scaffold/fullsend-repo/harness/).
Custom agents can be registered via the `agents:` field in org or per-repo
config (see [ADR 0058](../ADRs/0058-agent-registration.md)).

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
[Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).

At some point, enough configuration turns a configured default agent into a
derived agent. See [Default, derived, and custom agents](topics/default-vs-custom.md)
for where that line is and why it matters.

## Custom Agents

Custom agents can be added to the fullsend pipeline via the `agents:` field in
your org-level or per-repo `config.yaml`. Each entry is either a local path
(relative to the fullsend directory) or a pinned HTTPS URL with an integrity
hash. Config entries are looked up directly by name; when absent, a runtime
fallback resolves known first-party agents from `fullsend-ai/agents`.
See [ADR 0058](../ADRs/0058-agent-registration.md) for
details and [Bring Your Own Agent](../guides/user/bring-your-own-agent.md)
for the complete guide to building and registering custom agents.
