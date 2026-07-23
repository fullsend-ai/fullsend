# Escalation ladder for changing agent behavior

Before replacing or deriving from a core agent, prove that lighter options
cannot meet your needs. This page defines the escalation path — start at
Level 1 and move up only when you can show why the current level is
insufficient.

This ladder applies to **core agent roles that fullsend already ships**
(triage, code, review, fix, retro, prioritize). If you are building an agent
for a job fullsend does not cover — a release-notes generator, a
compliance checker, a deployment orchestrator — skip ahead to
[Bring Your Own Agent](../../guides/user/bring-your-own-agent.md). There is
no existing default to exhaust first.

## The four levels

```
Level 1 — Configure           Least invasive. No code leaves your repo.
Level 2 — Contribute          Fix the gap in the default agent for everyone.
Level 3 — Derive              Inherit the default, replace identity components.
Level 4 — Replace             Build a parallel agent for the same role.
```

Each level increases maintenance burden. A derived agent inherits upstream
improvements via `base` but must track breaking changes in the parent
harness. A replacement agent inherits nothing — every upstream improvement
must be replicated independently.

## Level 1: Configure the default agent

Use the extension points the default agent was designed for. These keep you
in [configured-default territory](default-vs-custom.md) and require no
harness changes at all.

| Extension point | What it does | Guide |
|---|---|---|
| `AGENTS.md` | Project-wide instructions for all agents — code style, test commands, architecture rules, domain context | [Customizing with AGENTS.md](../../guides/user/customizing-with-agents-md.md) |
| Repo skills (`.agents/skills/`) | Domain-specific knowledge for individual agents — linting rules, deployment checklists, label glossaries | [Customizing with Skills](../../guides/user/customizing-with-skills.md) |
| Documented env vars | Per-agent tuning knobs (e.g., `REVIEW_FINDING_SEVERITY_THRESHOLD`) | Each agent's [reference page](../) |
| `env:` in harness | Add environment variables without changing the agent's identity | [Harness field reference](../../guides/user/bring-your-own-agent.md#harness-field-reference) |
| `skills:` in harness | Add skills via `base` composition — concatenated with the base agent's skill list | [Configuring existing agents](../../guides/user/bring-your-own-agent.md#configuring-existing-agents) |
| `plugins:` in harness | Add language-server plugins | [Harness field reference](../../guides/user/bring-your-own-agent.md#harness-field-reference) |
| `host_files:` in harness | Inject additional files into the sandbox | [Harness field reference](../../guides/user/bring-your-own-agent.md#harness-field-reference) |
| Sandbox image layers | Base your image on the default, add tools | [Customizing agents](../../guides/user/customizing-agents.md#customization-examples) |

**Evidence to escalate:** show that no combination of these extension points
produces the behavior you need. Concrete evidence includes:

- A failing test case or eval run where the agent consistently gets the
  wrong answer despite correct `AGENTS.md` instructions
- A skill that cannot influence the agent's behavior because the gap is in
  the system prompt, not in domain knowledge
- An env var or config knob that does not exist for the behavior you need to
  change

## Level 2: Contribute to the default agent

If Level 1 cannot close the gap, check whether the gap is general — would
other users benefit from the same change? If yes, the fix belongs in the
default agent, not in a private fork.

Contributions include:

- **New extension points** — a new env var, a new skill hook, a new
  documented configuration knob
- **Prompt improvements** — clarifications, better instructions, additional
  decision criteria in the agent definition
- **Script changes** — pre/post script logic that handles a broader set of
  cases
- **Skill improvements** — better procedures, additional steps, broader
  coverage in a built-in skill

**How to contribute:** file an issue or open a PR against the default agent.
If the change is in the [fullsend-ai/fullsend](https://github.com/fullsend-ai/fullsend)
repo (harness, scripts, skills, agent definitions), contribute there. If the
agent definition or harness lives in
[fullsend-ai/agents](https://github.com/fullsend-ai/agents), contribute to
that repo instead.

**Evidence to escalate:** show that the improvement you need is specific to
your team's workflow and would not benefit other users. Concrete evidence
includes:

- A proposed upstream change that was reviewed and rejected as too
  org-specific
- A behavior change that contradicts the default agent's documented charter
  (e.g., you need the review agent to auto-merge, but the default review
  agent is explicitly read-only)
- A workflow that requires proprietary integrations (e.g., posting to an
  internal Slack channel, querying a private API)

## Level 3: Derive from the default agent

Inherit the default harness via `base` and replace only the components that
must differ. This is a [derived agent](default-vs-custom.md) — it tracks
upstream improvements for everything you did not override, but the
components you replaced are now your responsibility.

Derived agents use `base` composition to inherit the default harness and
override identity-defining fields:

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/fullsend/<sha>/internal/scaffold/fullsend-repo/harness/code.yaml#sha256=abc...

# Override identity-defining components:
agent: agents/my-code-agent.md          # Custom system prompt
post_script: scripts/post-my-code.sh    # Custom post-processing
slug: my-org-code                       # Custom identity
```

See [Configuring existing agents](../../guides/user/bring-your-own-agent.md#configuring-existing-agents)
for the full pattern and
[Classification by harness field](default-vs-custom.md#classification-by-harness-field)
for which fields cross the derived threshold.

**What you maintain:**

- Your custom system prompt, scripts, or validation loop
- Compatibility with upstream `base` harness changes (field additions,
  schema changes, script interface changes)
- Your own testing and evaluation for the overridden components

**Evidence to escalate:** show that `base` inheritance cannot support your
use case. Concrete evidence includes:

- A fundamental incompatibility with the base harness's script interface
  (e.g., the base post-script expects an output format your agent does not
  produce, and you cannot adapt either side)
- A need to control the full execution pipeline including sandbox policy,
  image, and provider configuration that `base` field merge rules do not
  support

## Level 4: Replace the role entirely

Build a custom agent from scratch that fills the same role as a default
agent. This is the heaviest option — you inherit nothing from upstream and
must maintain the full harness, agent definition, scripts, skills, and
testing independently.

See [Bring Your Own Agent](../../guides/user/bring-your-own-agent.md) for
the end-to-end guide.

**When this is appropriate:**

- The default agent's architecture is fundamentally incompatible with your
  requirements (not just its prompt or scripts)
- You have passed through Levels 1–3 and documented why each is
  insufficient
- You have the capacity to maintain a parallel implementation long-term

Register your replacement agent with the same role name in `config.yaml` —
config-registered agents take precedence over built-in agents on name
collision. See
[Registering your agent](../../guides/user/bring-your-own-agent.md#registering-your-agent).

## Prove-it checklist

Before creating a derived or replacement agent for a core role, confirm:

- [ ] **Level 1 exhausted.** You tried `AGENTS.md`, repo skills, env vars,
  harness `skills:` / `plugins:` / `host_files:`, and documented config
  knobs. None of them address the gap.
- [ ] **Level 2 considered.** The improvement is too org-specific to
  contribute upstream, or an upstream contribution was proposed and
  rejected.
- [ ] **Level 3 evaluated** (if jumping to Level 4). `base` inheritance
  cannot support your use case, and you can articulate why.
- [ ] **Evidence documented.** You have specific examples — failed eval
  runs, rejected upstream PRs, architectural constraints — not just a
  preference for a different approach.
- [ ] **Maintenance plan.** You have a plan for keeping the derived or
  custom agent up to date as the default evolves.

## See also

- [Default, derived, and custom agents](default-vs-custom.md) — how to
  classify your agent after making changes
- [Bring Your Own Agent](../../guides/user/bring-your-own-agent.md) —
  building and registering custom agents
- [Customizing with AGENTS.md](../../guides/user/customizing-with-agents-md.md)
  — Level 1: project-wide instructions
- [Customizing with Skills](../../guides/user/customizing-with-skills.md)
  — Level 1: agent-specific domain knowledge
- [Customizing agents](../../guides/user/customizing-agents.md) — harness
  configuration and layered content resolution
