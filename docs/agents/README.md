# Default Agents

Reference documentation for the default agents shipped by fullsend.
All agents below are enabled by default. The set of default agents is defined by
the YAML files in [`internal/scaffold/fullsend-repo/harness/`](../../internal/scaffold/fullsend-repo/harness/).

| Agent | Summary |
|-------|---------|
| [Triage](triage.md) | Inspects new issues and produces structured triage decisions |
| [Prioritize](prioritize.md) | Scores issues using the RICE framework for project board ranking |
| [Code](code.md) | Implements fixes and features from triaged issues |
| [Review](review.md) | Reviews pull requests for correctness, security, and intent alignment |
| [Fix](fix.md) | Addresses review feedback on open PRs |
| [Retro](retro.md) | Analyzes completed workflows and proposes system improvements |

## Customization

All agents can be customized by adding instructions and skills to your
repository. Changes to `AGENTS.md` affect every agent; skills let you tune how
a specific agent performs a specific task. See
[Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).

## Custom Agents

Support for adding your own custom agents to the fullsend pipeline is coming
soon.
