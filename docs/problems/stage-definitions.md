# Stage Definitions

How does the system know what to run for each agent stage — which agent, which sandbox, which pre/post scripts, and what runtime parameters?

## Context

The fullsend pipeline has several moving parts per agent invocation:

- **Workflow** — the GitHub Actions workflow (or equivalent) that catches the event and routes it ([PR #164](https://github.com/fullsend-ai/fullsend/pull/164))
- **Sandbox** — the container image, network policy, and filesystem mounts that isolate the agent (Story 7, [#130](https://github.com/fullsend-ai/fullsend/issues/130))
- **Agent definition** — the persona, tool constraints, and behavioral instructions (e.g. `agents/code.md`)
- **Skills** — step-by-step procedures the agent follows (e.g. `skills/code-implementation/SKILL.md`)
- **Pre-script** — deterministic setup that runs before the agent (clone repo, configure git identity, generate tokens)
- **Post-script** — deterministic teardown that runs after the agent (push branch, create/update PR, apply labels, post comments)
- **Runtime parameters** — model, retry limits, timeouts, iteration caps

Today these are scattered: the workflow file hardcodes the stage name, the `fullsend entrypoint <stage>` CLI resolves the agent, and sandbox configuration is unspecified. There is no single file that ties a stage together.

The entry point (Story 2, [#125](https://github.com/fullsend-ai/fullsend/issues/125)) "assembles the agent harness — system prompt, skills, codebase context, tool definitions, and behavioral instructions — from layered config." But the actual assembly mechanics are unspecified ([#173](https://github.com/fullsend-ai/fullsend/issues/173)). This document explores what that assembly input looks like.

## What a stage definition needs to express

At minimum, the system needs to know:

1. **Which agents to run, and in what order** — a stage may run multiple agents sequentially (e.g. code agent then review agent). Each agent has its own definition (persona, tools, constraints), but they share the same sandbox and filesystem.
2. **Which sandbox profile to apply** — network policy (firewall rules / egress allowlist), container image, filesystem mounts
3. **What runs before the agents** — path to a deterministic pre-script for setup (clone, checkout, token generation)
4. **What runs after the agents** — path to a deterministic post-script for actions agents must not perform (push, PR creation, label transitions, comment posting)
5. **Runtime parameters** — model, retry limits, timeouts, iteration caps
6. **Which skills are available** — explicit skill paths, or "all skills in the repo" (see [open question on skill discoverability](#does-the-skills-list-restrict-or-guide))

## Execution model

The stage definition is a declarative file — it doesn't run anything itself. A **stage runner** (part of the entry point from Story 2) reads the file and executes the following sequence:

```
┌─────────────────────────────────────────────────┐
│  Stage runner reads stages/code.yaml            │
├─────────────────────────────────────────────────┤
│  1. Provision sandbox (image, firewall, mounts) │
│  2. Run pre_script inside sandbox               │
│  3. For each agent in agents[] (in order):      │
│     a. Assemble harness (agent def + skills)    │
│     b. Launch agent runtime (e.g. claude code)  │
│     c. Wait for agent to exit                   │
│     d. If non-zero exit, stop — skip remaining  │
│        agents and post_script                   │
│  4. Run post_script inside sandbox              │
│     (has access to everything agents produced)  │
└─────────────────────────────────────────────────┘
```

The stage runner is deterministic code — not an LLM. It reads the YAML, provisions the environment, and calls each agent CLI in sequence. The agents themselves are LLM sessions (e.g. `claude --agent agents/code.md`), but the orchestration between them is a script or binary that the entry point controls.

Each agent runs in the same sandbox filesystem, so later agents see earlier agents' work (commits, modified files). The pre-script sets up the workspace; agents do their work; the post-script handles privileged operations (push, PR creation) that agents are not allowed to perform.

## Examples

Mock stage definitions live in [`stages/examples/`](../../stages/examples/). These are not functional — they are design mocks showing what the format could look like.

- **[`triage.yaml`](../../stages/examples/triage.yaml)** — The simplest case: a single agent, no code changes, no push, no PR. The triage agent reads the issue and posts triage output.
- **[`code.yaml`](../../stages/examples/code.yaml)** — A more complex stage: two agents run sequentially in the same sandbox. The code agent implements the fix and commits locally, then the review agent evaluates the branch as a mandatory pre-push security gate. The post-script pushes and creates the PR. Incorporates decisions from Story 4 ([#127](https://github.com/fullsend-ai/fullsend/issues/127)) and the code agent discussion (Apr 7, 2026).

Each stage definition references its pre/post scripts by path (e.g. `stages/scripts/code-pre.sh`). The scripts live alongside the stage definitions in [`stages/examples/scripts/`](../../stages/examples/scripts/).

## Where stage definitions live

### Option A: In the fullsend repo (defaults) with org overrides

Fullsend ships default stage definitions in `stages/`. The adopting org's `.fullsend` repo can override any stage by placing a file at the same relative path. Per-repo overrides layer on top.

```
fullsend (upstream defaults)
  └── stages/
        ├── triage.yaml
        ├── code.yaml
        ├── review.yaml
        └── fix.yaml

<org>/.fullsend (org overrides)
  └── stages/
        └── code.yaml              # overrides model, adds custom firewall rules

<repo> (per-repo overrides)
  └── .fullsend/
        └── stages/
              └── code.yaml        # overrides retry limit for this repo
```

This follows the existing inheritance model: fullsend defaults < org `.fullsend` config < per-repo overrides ([ADR 0003](../ADRs/0003-org-config-repo-convention.md)).

**Trade-off:** Clean layering, but three potential locations means merge logic is needed. Which fields are overridable vs. protected?

### Option B: Per-agent directory with co-located config

Instead of a separate `stages/` directory, put the stage definition alongside the agent definition:

```
agents/
  ├── code.md             # agent persona
  ├── code.stage.yaml     # stage definition (sandbox, scripts, params)
  ├── triage.md
  ├── triage.stage.yaml
  ├── review.md
  └── review.stage.yaml
```

**Trade-off:** Co-location is discoverable — everything about an agent is in one place. But it conflates the agent definition (portable across stages) with stage-specific config (sandbox, scripts). An agent could participate in multiple stages with different configurations.

### Option C: Inline in `config.yaml`

Extend the existing `config.yaml` schema to include stage definitions directly:

```yaml
version: "2"
stages:
  code:
    agent: agents/code.md
    sandbox:
      image: ubi9
      firewall: ...
    pre_script: scripts/code-pre.sh
    post_script: scripts/code-post.sh
    runtime:
      model: claude-sonnet-4-20250514
```

**Trade-off:** Single source of truth, but `config.yaml` grows large. Harder to override individual stages at the per-repo level without replacing the entire stages block.

## Relationship to existing components

- **`config.yaml`** ([ADR 0011](../normative/admin-install/v1/adr-0011-org-config-yaml/SPEC.md)) defines org-level settings (roles, repos, defaults). Stage definitions reference the same role names but add operational detail.
- **`agent-dispatch-v1.yaml`** ([ADR 0012](../normative/admin-install/v1/adr-0012-fullsend-repo-files/SPEC.md)) is the reusable workflow that receives events. It calls `fullsend entrypoint <stage>`, which would load the stage definition to know what to do.
- **Per-stage GitHub workflows** ([PR #164](https://github.com/fullsend-ai/fullsend/pull/164)) are the event-catching layer. Each workflow maps to one stage and calls the entry point. The stage definition is what the entry point reads.
- **Agent wrappers** ([#101](https://github.com/fullsend-ai/fullsend/issues/101)) — the pre/post script concept is essentially what this issue calls "wrappers." Stage definitions formalize where wrapper logic is declared.
- **Harness assembly** ([#173](https://github.com/fullsend-ai/fullsend/issues/173)) — the stage definition is the input to harness assembly. The harness reads the stage file to know which agent, skills, and runtime params to assemble.

## Open questions

- **Schema and validation.** Should stage definitions have a JSON Schema like `config.yaml` does? Probably yes — Greg suggested "must have a schema."
- **Does the `skills` list restrict or guide?** If a stage definition lists specific skills, does the agent runtime treat that as an allowlist (only these skills) or a recommendation (prefer these, but others are available)? This affects all agent definitions. ([Experiment needed](https://github.com/fullsend-ai/fullsend/issues/127#issuecomment-4201527352) — do agents use unlisted skills?)
- **Protected fields.** Which fields can an org or repo override, and which are locked by fullsend defaults? Firewall rules seem like they should only be additive (orgs can allow more endpoints, not remove fullsend's defaults). Model choice seems freely overridable.
- **One file per stage or one file for all stages?** Option C puts everything in one file. Options A and B split by stage. Which is easier to reason about when debugging a failed agent run?
- **How does `fullsend entrypoint <stage>` discover the file?** Convention-based path lookup (`stages/<stage>.yaml`)? A registry in `config.yaml`? The entry point CLI needs a resolution strategy.
