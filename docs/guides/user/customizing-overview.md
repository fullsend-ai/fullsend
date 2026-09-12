# Customizing Agents

Fullsend agents work out of the box, but most teams want to tailor them — add
coding conventions, domain knowledge, extra skills, or entirely new agent
roles. This page lists every customization approach from lightest to heaviest
so you can pick the right one.

## Quick decision guide

| Goal | Approach | Effort |
|------|----------|--------|
| Teach agents your coding style, test commands, or architecture rules | [AGENTS.md](#agentsmd) | Low |
| Give an agent domain-specific knowledge or a new capability | [Skills](#skills) | Low |
| Change model, timeout, image, or add env vars to an existing agent | [Harness configuration](#harness-configuration) | Medium |
| Build a completely new agent with its own trigger, scripts, and schema | [Bring Your Own Agent](#bring-your-own-agent) | Medium with `fullsend agent new`, high by hand |

Start at the top and move down only when a lighter option doesn't cover your
needs. Most teams only need AGENTS.md and perhaps a skill or two.

## AGENTS.md

The simplest way to influence agent behavior. Add an `AGENTS.md` file to your
repository with conventions that apply to all contributors — human and agent
alike:

```markdown
# AGENTS.md

## Testing
- Always run `make test` before committing.

## Code style
- Use structured logging via `slog`. Do not use `log.Printf`.
```

Every agent reads `AGENTS.md` automatically. No fullsend configuration
changes needed.

**Best for:** coding conventions, test commands, architecture rules, domain
context.

**Guide:** [Configuring with AGENTS.md](customizing-with-agents-md.md)

## Skills

Skills are self-contained markdown documents that teach an agent how to
perform a specific task. Place them in `.agents/skills/` in your repo and
symlink `.claude/skills` to make them discoverable:

```
your-repo/
  .agents/skills/
    deployment-checks/
      SKILL.md
  .claude/skills -> ../.agents/skills
```

Skills extend what agents know — linting rules, deployment checklists,
customer data sources — without changing any fullsend configuration.

**Best for:** agent-specific domain knowledge, helper scripts, extension
points, replacing built-in skills.

**Guide:** [Configuring with Skills](customizing-with-skills.md)

## Harness configuration

To change how an existing agent runs — its model, timeout, sandbox image,
environment variables, or skills — use `base:` harness composition. Create
a thin harness that inherits from the upstream agent and overrides only what
differs:

```yaml
# .fullsend/harness/code.yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

model: sonnet
timeout_minutes: 45
skills:
  - skills/my-custom-linting
env:
  sandbox:
    JIRA_PROJECT: "MYPROJ"
```

Register the agent in `.fullsend/config.yaml`:

```yaml
agents:
  - name: code
    source: harness/code.yaml
```

**Best for:** changing model or timeout, adding environment variables,
adding skills via harness, extending the sandbox image, disabling agents.

**Guides:**
- [Configuring Agent Behavior](customizing-agents.md) — harness
  composition, status notifications, disabling agents
- [Harness Field Reference](../../reference/harness-reference.md) — complete field reference,
  merge rules, and advanced configuration

## Bring Your Own Agent

When you need a completely new agent — with its own trigger, scripts, and
output schema — start with the generator:

```bash
fullsend agent new my-agent --fullsend-dir .fullsend --role triage
```

It writes the files below, registers the agent in `config.yaml`, and checks
that the result loads. You then edit `agents/my-agent.md`, the instructions the
agent follows; everything else is ready to run. See
[`fullsend agent new`](../../cli/agent.md#agent-new) for the flags, the role
table, and a validated walkthrough.

```
.fullsend/
  harness/my-agent.yaml         # Execution config, including the trigger
  agents/my-agent.md            # Agent prompt — the file you edit
  schemas/my-agent-result.schema.json  # What the agent must produce
  scripts/post-my-agent.sh      # Turns the result into one comment
  policies/base.yaml            # Sandbox policy (written when absent)
  providers/vertex-ai.yaml      # Network access the role needs (written when absent)
  providers/github-ro.yaml
  profiles/fullsend-vertex-ai.yaml
  profiles/fullsend-github-ro.yaml
```

The provider and profile pair depends on `--role`; the one shown is for
`triage`. See the [role table](../../cli/agent.md#agent-new) for the others.

The agent runs automatically when matching events arrive. It runs on the
hosted mint as long as it keeps a built-in `role:`; a distinct GitHub App
identity requires your own mint (see
[Custom Agent Identity](custom-agent-identity.md)).

**Best for:** entirely new agent roles, custom triggers, specialized
output schemas.

**Guide:** [Bring Your Own Agent](bring-your-own-agent.md)

## See also

- [Default, derived, and custom agents](../../agents/topics/default-vs-custom.md) — when does configuration cross into custom agent territory?
- [Escalation ladder](../../agents/topics/escalation-ladder.md) — prove-it path before deriving or replacing a core agent
- [Bugfix Workflow](bugfix-workflow.md) — how agents work together end to end
