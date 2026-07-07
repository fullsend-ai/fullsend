# Default agents vs. custom agents

Fullsend ships six default agents in
[fullsend-ai/agents](https://github.com/fullsend-ai/agents): triage,
prioritize, code, review, fix, and retro. Each can be configured and extended.
At some point, enough modification turns a configured default agent into a
custom agent that re-uses parts of a default one.

## Why the distinction matters

This is a bit of a
[Ship of Theseus](https://en.wikipedia.org/wiki/Ship_of_Theseus) exercise.
The question has a practical purpose, though. We want two things simultaneously:

1. **Encourage custom agents.** The harness, sandbox, and `base` composition
   system exist so teams can build agents tailored to their workflows.
2. **Encourage contribution to default agents.** When a change improves a
   default agent for everyone, it should flow upstream rather than live in a
   private fork.

Knowing whether you are running a configured default agent or a custom agent
helps you decide: should this change be contributed back, or does it only make
sense for my team? Clear language helps us all communicate about this.

**Default to contributing.** If your modification would benefit other users,
contribute it to the default agent's definition. Build a custom agent when
your needs genuinely diverge the default agent's charter.

## The rule

> If a modification uses a method documented as a recommended extension point
> for that agent, the result is still a **configured default agent**. If it
> uses any other method, the result is a **custom agent** that re-uses parts
> of a default.

Each default agent documents its extension points in
[`docs/agents/<agent>.md`](../). The review agent, for example, documents
`REVIEW_FINDING_SEVERITY_THRESHOLD` as a configuration variable and
`issue-labels` as an overloadable skill. Using those mechanisms produces a
configured review agent, not a custom one.

## The `base` lineage test

The `base` field in a harness YAML
([ADR 0045](../../ADRs/0045-forge-portable-harness-schema.md)) is the first
thing to check. If a harness's `base` chain — through one or more levels of
inheritance — traces back to a default agent harness in `fullsend-ai/fullsend`,
the harness *started from* a default agent. What you override on top of that
base determines whether the result is still a configured default or has crossed
into custom territory.

If the `base` chain does **not** trace back to a default agent harness, the
agent is custom by definition — regardless of how similar it looks. It is a
custom **from scratch** agent.

## Classification by harness field

| Modification | Still a default agent? | Rationale |
|---|---|---|
| Set a documented configuration variable (e.g., `REVIEW_FINDING_SEVERITY_THRESHOLD`) | Yes | Documented extension point. The agent was designed for this. |
| Add environment variables via `env:` | Yes | Env vars augment behavior without changing identity. |
| Add skills via `skills:` | Yes | Skills extend knowledge. The agent's core behavior is unchanged. |
| Add repo-level skills in `.agents/skills/` | Yes | Repo skills are discovered automatically; no harness change needed. |
| Add project instructions via `AGENTS.md` | Yes | All agents read `AGENTS.md`. This is the standard customization path. |
| Override a built-in skill via `customized/skills/` | Yes | Documented extension point ([Customizing with Skills](../../guides/user/customizing-with-skills.md#overriding-built-in-skills)). |
| Replace the sandbox image with one based on the default image | Yes | The agent's behavior is unchanged; the environment is augmented. |
| Add plugins via `plugins:` | Yes | Plugins extend tooling without changing the agent's identity. |
| Add host files via `host_files:` | Yes | Additional data for the sandbox. The agent itself is unchanged. |
| Change the sandbox policy (`policy:`) | Yes | Especially after [PR#2671](https://github.com/fullsend-ai/fullsend/pull/2671) merges, an agent with an augmented policy is arguably still the same agent. |
| **Replace the agent system prompt** (`agent:`) | **Custom** | The system prompt (sometimes called the subagent definition file) provides the primary instructions for the agent. Replacing it creates a different agent. |
| **Replace pre or post scripts** (`pre_script:`, `post_script:`) | **Custom** | Scripts control the agent's integration with external systems. Different scripts mean different behavior at the pipeline boundary. |
| **Replace the app role slug** (`slug:`) | **Custom**\* | The slug determines who the agent authenticates as. A different identity is a different agent. |
| **Replace the validation loop** (`validation_loop:`) | **Custom** | The validation loop defines the contract between the agent and the harness. Changing it changes what the agent is expected to produce. |

\* Replacing the slug is acceptable in limited cases we may document in the
future — for example, granting the review agent merge rights via a different
GitHub App. When a specific agent's documentation recommends a slug override
for a stated purpose, that override does not make the agent custom.

## See also

- [Agents reference](../) — default agent documentation and extension points
- [Customizing agents](../../guides/user/customizing-agents.md) — harness
  configuration and layered content resolution
- [Customizing with AGENTS.md](../../guides/user/customizing-with-agents-md.md)
  — project-wide instructions for all agents
- [Customizing with skills](../../guides/user/customizing-with-skills.md) —
  extending or replacing built-in skills
- [Building custom agents](../../guides/user/building-custom-agents.md) —
  creating a new agent from scratch
- [ADR 0045](../../ADRs/0045-forge-portable-harness-schema.md) — `base`
  composition and harness inheritance
