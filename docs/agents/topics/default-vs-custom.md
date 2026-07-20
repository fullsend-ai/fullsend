# Default, derived, and custom agents

Fullsend ships a set of default agents in
[fullsend-ai/agents](https://github.com/fullsend-ai/agents). Each can be
configured and extended.
At some point, enough modification turns a configured default into something
different. This document defines three tiers:

1. **Configured default agent** — uses only documented extension points
   (env vars, skills, `AGENTS.md`, plugins, host files, sandbox image layers).
   Still recognizably the same default agent.
2. **Derived agent** — starts from a default via `base` inheritance but
   replaces identity-defining components (system prompt, scripts, slug, or
   validation loop). It re-uses parts of a default but is no longer
   recognizably that agent.
3. **Custom agent** — its `base` chain does not trace back to a default agent
   harness, or it has no `base` at all. Built from scratch.

## Why the distinction matters

We want two things simultaneously:

1. **Encourage derived and custom agents.** The harness, sandbox, and `base`
   composition system exist so teams can build agents tailored to their
   workflows.
2. **Encourage contribution to default agents.** When a change improves a
   default agent for everyone, it should flow upstream rather than live in a
   private fork.

Knowing whether you are running a derived agent or a custom agent helps you
decide: should this change be contributed back, or does it only make sense for
my team? Clear language helps us all communicate about this.

**Default to contributing.** If your modification would benefit other users,
contribute it to the default agent's definition. Build a custom agent when
your needs genuinely diverge from the default agent's charter.

## The rule

> If a modification uses a documented extension point for that agent, or a
> general-purpose harness field that does not alter the agent's identity, the
> result is still a **configured default agent**. If it replaces
> identity-defining components, the result is a **derived agent**.

Each default agent documents its extension points in
[`docs/agents/<agent>.md`](../). The review agent, for example, documents
`REVIEW_FINDING_SEVERITY_THRESHOLD` as a configuration variable and
`issue-labels` as an overloadable skill. Using those mechanisms produces a
configured review agent, not a derived one.

## The `base` lineage test

The `base` field in a harness YAML
([ADR 0045](../../ADRs/0045-forge-portable-harness-schema.md)) is the first
thing to check. If a harness's `base` chain — through one or more levels of
inheritance — traces back to a default agent harness in `fullsend-ai/fullsend`,
the harness *started from* a default agent. What you override on top of that
base determines whether the result is still a configured default or has crossed
into derived territory.

If the `base` chain does **not** trace back to a default agent harness, the
agent is custom by definition — regardless of how similar it looks.

## Classification by harness field

| Modification | Classification | Rationale |
|---|---|---|
| Set a documented configuration variable (e.g., `REVIEW_FINDING_SEVERITY_THRESHOLD`) | Configured default | Documented extension point. The agent was designed for this. |
| Add environment variables via `env:` | Configured default | Env vars augment behavior without changing identity. |
| Add skills via `skills:` | Configured default | Skills extend knowledge. The agent's core behavior is unchanged. |
| Add repo-level skills in `.agents/skills/` | Configured default | Repo skills are discovered automatically; no harness change needed. |
| Add project instructions via `AGENTS.md` | Configured default | All agents read `AGENTS.md`. This is the standard customization path. |
| Override a built-in skill via `customized/skills/` | Configured default | Documented extension point ([Customizing with Skills](../../guides/user/customizing-with-skills.md#overriding-built-in-skills)). |
| Replace the sandbox image with one based on the default image | Configured default | The agent's behavior is unchanged; the environment is augmented. |
| Add plugins via `plugins:` | Configured default | Plugins extend tooling without changing the agent's identity. |
| Add host files via `host_files:` | Configured default | Additional data for the sandbox. The agent itself is unchanged. |
| Change the sandbox policy (`policy:`) | Configured default | Policy composition lets you augment an agent's policy without changing its identity. |
| **Replace the agent system prompt** (`agent:`) | **Derived** | The system prompt (sometimes called the subagent definition file) provides the primary instructions for the agent. Replacing it creates a different agent. |
| **Replace pre or post scripts** (`pre_script:`, `post_script:`) | **Derived** | Scripts control the agent's integration with external systems. Different scripts mean different behavior at the pipeline boundary. |
| **Replace the app role slug** (`slug:`) | **Derived**\* | The slug determines who the agent authenticates as. A different identity is a different agent. |
| **Replace the validation loop** (`validation_loop:`) | **Derived** | The validation loop defines the contract between the agent and the harness. Changing it changes what the agent is expected to produce. |

\* Replacing the slug is acceptable in limited cases we may document in the
future — for example, granting the review agent merge rights via a different
GitHub App. When a specific agent's documentation recommends a slug override
for a stated purpose, that override does not make the agent derived.

## See also

- [Escalation ladder](escalation-ladder.md) — prove-it path before
  deriving or replacing a core agent
- [Agents reference](../) — default agent documentation and extension points
- [Customizing agents](../../guides/user/customizing-agents.md) — harness
  configuration and layered content resolution
- [Customizing with AGENTS.md](../../guides/user/customizing-with-agents-md.md)
  — project-wide instructions for all agents
- [Customizing with skills](../../guides/user/customizing-with-skills.md) —
  extending or replacing built-in skills
- [Bring Your Own Agent](../../guides/user/bring-your-own-agent.md) —
  building custom agents and configuring existing ones
- [ADR 0045](../../ADRs/0045-forge-portable-harness-schema.md) — `base`
  composition and harness inheritance
