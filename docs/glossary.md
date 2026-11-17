# Glossary

Shared vocabulary for the fullsend project. Terms are defined in the context of fullsend's architecture, workflow, and security model. Each entry includes a brief definition and a pointer to the relevant document for deeper context.

This is a living document. PRs that introduce new terminology should add to this glossary as part of the change.

<a id="customization-vocabulary"></a>
**Customization vocabulary**

When someone says they "customized an agent," ask which of these they mean — colloquial **"customized agent"** is ambiguous; prefer a precise term below.

**Agent classification** (each class is an [agent](#agent); pick the most specific that applies — these are categories, not ordered steps):

- [Default agent](#default-agent) — unmodified shipped agent
- [Configured default agent](#configured-default-agent) — same identity; allowed configuration only
- [Derived agent](#derived-agent) — `base:` lineage from a default, but identity-defining fields replaced
- [Custom agent](#custom-agent) — no default `base` lineage (built from scratch)

**Skills** (how instructions are added or replaced):

- [Additive skill](#additive-skill) vs [Skill override](#skill-override)
- [Always-on skill](#always-on-skill) vs [On-demand skill](#on-demand-skill) (load modes; see [#6681](https://github.com/fullsend-ai/fullsend/issues/6681) / [#6682](https://github.com/fullsend-ai/fullsend/issues/6682))
- [Built-in skill](#built-in-skill), [Repo skill](#repo-skill), [Extension point](#extension-point)

**Scripts** (host-side pre/post hooks):

- [Pre-script](#pre-script) / [Post-script](#post-script) changes are [script overrides](#script-override) — replace, not add

**Platform:**

- [BYOA](#byoa) is the capability to bring your own agents and config — **not** a synonym for [custom agent](#custom-agent)

| Goal | Resource |
|------|----------|
| Classify your change | [Default, derived, and custom agents](guides/user/default-vs-custom.md) |
| Configure a harness | [Configuring agents](guides/user/customizing-agents.md) |
| Bring your own agent | [Bring Your Own Agent](guides/user/bring-your-own-agent.md) |
| Add skills | [Configuring with skills](guides/user/customizing-with-skills.md) |
| Project-wide instructions | [Configuring with AGENTS.md](guides/user/customizing-with-agents-md.md) |
| `base:` merge rules | [Base composition](#base-composition), [ADR 0045](ADRs/0045-forge-portable-harness-schema.md) |
| Always-on skill load mode | [#6681](https://github.com/fullsend-ai/fullsend/issues/6681) (frontmatter injection; [#6859](https://github.com/fullsend-ai/fullsend/pull/6859)), optional mode [#6682](https://github.com/fullsend-ai/fullsend/issues/6682) |

---

## A

### Additive Skill

A [skill](#skill) that **extends** an agent's skill set without replacing a [built-in skill](#built-in-skill). Typical paths: list a new unique name under harness `skills:` via [base composition](#base-composition), or add a uniquely named [repo skill](#repo-skill). The default agent's identity is unchanged — this yields a [configured default agent](#configured-default-agent), not a [custom agent](#custom-agent). Example pattern: a team brevity or house-style skill composed alongside existing skills.
See [Configuring with skills](guides/user/customizing-with-skills.md) and [Default, derived, and custom agents](guides/user/default-vs-custom.md).

### Agent

The unit that fullsend registers, triggers and runs to do one job on one work
item, such as triaging an issue, writing a change or reviewing a pull request.
An agent is more than one file or one process. It is everything that one
[harness](#harness) declares, together with the platform parts that the harness
connects it to:

- **Name.** The name that registers and runs the agent: an `agents:` entry in
  `.fullsend/config.yaml`, and `fullsend run <name>`. Fullsend resolves the
  built-in agents (triage, prioritize, code, review, fix and retro) without an
  entry.
- **[Agent definition](#agent-definition).** The prompt, tools and model that
  the LLM starts with (`agent:`).
- **[Skills](#skill) and plugins.** Instructions and tooling that the agent can
  load (`skills:`, `plugins:`).
- **[Pre-script](#pre-script) and [post-script](#post-script).** Host-side
  scripts that run with credentials, outside the sandbox. The pre-script
  fetches the inputs before the run. The post-script applies the output after
  the run.
- **[Validation loop](#validation-loop).** The check that the agent's output
  must pass (`validation_loop:`).
- **[Role](#role).** The forge identity that the agent acts as and, on GitHub,
  the most permissions it can hold (`role:`, `privilege_levels:`).
- **[Sandbox](#sandbox) image, policy and providers.** The environment that
  the LLM runs in, what it can read, and which hosts it can reach (`image:`,
  `policy:`, `providers:`).
- **[Trigger](#trigger).** The events that start the agent (`trigger:`).
- **[Agent runtime](#agent-runtime) and model.** The LLM loop that executes the
  agent, and the model it calls (`runtime:` in `.fullsend/config.yaml`,
  `model:`).

A harness must set `agent`, `role` and `policy`. The other parts are optional.
[Default](#default-agent), [configured default](#configured-default-agent),
[derived](#derived-agent) and [custom](#custom-agent) agents are all agents in
this sense. The classes differ in where the parts come from.

Use "agent" for the whole unit, and a narrower term for one part:

- The agent definition is only the prompt file.
- The agent runtime is only the LLM loop inside the sandbox.
- A role is an identity that agents can share. The `code` and `fix` agents
  both run as the `coder` role.
- A sub-agent is a helper that a skill starts inside one agent's run, such as
  the `security` sub-agent in the `pr-review` skill.

See [Agents reference](agents/),
[Harness field reference](reference/harness-reference.md), and
[Custom agent identity](guides/user/custom-agent-identity.md).

### Agent Definition

The Markdown file that the harness `agent:` field names. Its frontmatter sets
the name, description, tools, model and skills. Its body is the system prompt
that the LLM starts with. The default agents keep theirs under `agents/` in
`fullsend-ai/agents`. The agent definition is one part of an [agent](#agent),
not the whole agent. Replacing it on a default `base:` makes a
[derived agent](#derived-agent).
See [Harness field reference](reference/harness-reference.md) and
[Default, derived, and custom agents](guides/user/default-vs-custom.md).

### Agent Infrastructure

The compute and orchestration layer that runs agent workloads — provisioning,
scheduling, scaling, and lifecycle management of agent execution environments.
This is the "where do agents physically run" question. Today agents run as
GitHub Actions jobs and GitLab CI/CD jobs, and `fullsend run` runs one agent on
a local machine. The problem document explores other options.
See [architecture.md](architecture.md) and
[agent-infrastructure.md](problems/agent-infrastructure.md).

### Agent Registry

The catalog of available [agents](#agent) and their configurations. It
connects the agents that the agent architecture describes (triage, code,
review) to the harnesses that fullsend loads to run each one. Fullsend
provides a base set in `fullsend-ai/agents`. A repository adds, replaces or
disables agents with the `agents:` list in its `.fullsend/config.yaml`.
See [architecture.md](architecture.md#agent-registry) and
[ADR 0058](ADRs/0058-agent-registration.md).

### Agent Runtime

The program that runs the LLM loop for an [agent](#agent) inside the sandbox:
the model calls, the tool-use loop, and the interface to the model provider.
This is the part that reasons and acts; the rest of the architecture supports,
constrains or coordinates it. Claude Code is the default runtime.
[pi](https://github.com/earendil-works/pi) and
[Codex](https://github.com/openai/codex) are opt-in runtimes (`runtime: pi`,
`runtime: codex`). [OpenCode](https://opencode.ai) is an experimental opt-in
runtime (`runtime: opencode`). Use it only for read-only agents until the
security-hook adapter lands; fullsend does not enforce that limit. Other
projects call this layer a [harness](#harness); see that entry for the naming
difference.
See [runtimes.md](runtimes.md), [architecture.md](architecture.md) and
[agent-infrastructure.md](problems/agent-infrastructure.md).

### AGENTS.md

Project-wide instructions for humans and agents (conventions, testing, architecture). Fullsend agents operating on a checked-out target repo read it there ([Configuring with AGENTS.md](guides/user/customizing-with-agents-md.md)). Using `AGENTS.md` keeps you in [configured default](#configured-default-agent) territory; it is not a [custom agent](#custom-agent). Prefer `AGENTS.md` for rules that apply to every agent; use a [skill](#skill) when behavior is agent- or task-specific.

### Agent Spec

The preferred docs term for the per-agent [harness](#harness) YAML: skills, env, providers, sandbox profiles, and `base:` composition. It avoids confusion with the industry sense of "harness", which fullsend calls the [agent runtime](#agent-runtime). This is a docs preference, not a rename: the YAML schema, field names, the `internal/harness/` package, and ADR titles still say "harness".
See [Harness](#harness), [Base Composition](#base-composition), and [harness-reference.md](reference/harness-reference.md).

### Always-on Skill

A harness [skill](#skill) load mode based on declaring every harness-listed skill in the agent definition: Claude bootstrap injects those skills into the agent definition's `skills:` frontmatter (harness-listed only; repo-discovered skills remain [#237](https://github.com/fullsend-ai/fullsend/issues/237)). The pi and other runtimes retain their own skill-loading behavior. Adding such a skill via `skills:` on a thin `base:` wrapper keeps a [configured default](#configured-default-agent); replacing `agent:` just to name the skill would make it [derived](#derived-agent). Contrast with [on-demand skill](#on-demand-skill) (planned per-skill optional mode).

> **Status:** Frontmatter injection for harness-listed skills is active for the
> Claude runtime. Whether the injected declaration causes activation without an
> explicit Skill tool call still requires empirical validation. Do not use the
> abandoned `metadata.apply: always` and soft Skill-tool directive designs.

See [Configuring with skills](guides/user/customizing-with-skills.md).

### Automerge

Merging a PR without a human pressing merge, once the required reviews and CI
checks pass. The [autonomy spectrum](problems/autonomy-spectrum.md) gates
automerge: most workflows start with human approval and move toward automerge
as confidence grows. Automerge is opt-in and off by default. When
`CODE_AUTO_MERGE` is `"true"`, the code agent turns on the forge's native
auto-merge for the PRs and merge requests it opens, so the forge merges them
when its merge requirements are met. A dedicated auto-merge agent for a narrow
class of low-risk work is on the [roadmap](roadmap.md).
See [autonomy-spectrum.md](problems/autonomy-spectrum.md) and
[Progressive auto-merge](guides/user/adoption.md#progressive-auto-merge).

## B

### Base Composition

The mechanism for customizing an agent's harness: a thin harness file sets `base:` to a local path or URL pointing at an upstream harness, then declares only the fields that differ. Scalars override the base value; `skills` merges with deduplication by skill identity (declared `SKILL.md` name when on disk, otherwise basename — child overrides a matching base entry; nested forge skills that share a leaf directory name are kept); `plugins`, `providers`, `openshell.profiles`, and `api_servers` concatenate (base + child, no dedup at composition time); `env.runner` and `env.sandbox` merge as independent maps (child keys win); `runner_env` is deprecated in favor of `env.runner`. Replaced the removed [`customized/` directory](#customized-directory) overlay, which required copying and maintaining an entire upstream YAML file to change a single field. Same mechanism as colloquial "harness composition."
See [ADR 0045](ADRs/0045-forge-portable-harness-schema.md), [ADR 0055](ADRs/0055-unified-env-var-delivery.md), [ADR 0064](ADRs/0064-deprecate-customized-directory-overlay.md), and [Configuring Agent Behavior](guides/user/customizing-agents.md).

### Blast Radius

The scope of damage a compromised or misbehaving agent can cause. A core design constraint: every architectural decision about sandboxing, identity scoping, and network policy is evaluated by asking "what is the blast radius if this agent is compromised?" Minimizing blast radius is the primary goal of the sandbox layer.
See [security-threat-model.md](problems/security-threat-model.md) and [architecture.md](architecture.md).

### Built-in Skill

A [skill](#skill) listed in a [default agent](#default-agent)'s harness in `fullsend-ai/agents`. Documented under [Agents reference](agents/) (each agent page carries a `Source` link to its harness YAML). Teams typically [add](#additive-skill) alongside built-ins; replacing one by skill identity via [base composition](#base-composition) is a [skill override](#skill-override).
See [Configuring with skills](guides/user/customizing-with-skills.md#built-in-skills).

### BYOA

**Bring Your Own Agent** — the platform capability to register, compose, and run agents and harness config you own (config.yaml registration and `base:` [base composition](#base-composition)). BYOA covers [configured default](#configured-default-agent), [derived](#derived-agent), and [custom](#custom-agent) agents. Saying "we use BYOA" does **not** mean "we built a custom agent"; many BYOA users only add skills or env on a default harness.
See [Bring Your Own Agent](guides/user/bring-your-own-agent.md), [ADR 0058](ADRs/0058-agent-registration.md), and [ADR 0045](ADRs/0045-forge-portable-harness-schema.md).

## C

### Configured Default Agent

A [default agent](#default-agent) whose behavior was adjusted **without** changing identity-defining harness fields (`agent:`, [pre-script](#pre-script) / [post-script](#post-script), `role:`, `validation_loop`). Allowed paths include documented [extension points](#extension-point), [additive skills](#additive-skill), [skill overrides](#skill-override), [AGENTS.md](#agentsmd), env vars, plugins, host files, sandbox image layers, and policy composition. Changing install-time `slug:` alone stays a configured default (the mint never reads it). Replacing `role:` is normally [derived](#derived-agent), except when that agent's docs recommend a specific role override for a stated purpose. Still recognizably the same agent (for example "our triage, with team skills").
See [Default, derived, and custom agents](guides/user/default-vs-custom.md).

### Custom Agent

An agent whose `base` chain does **not** trace back to a default agent harness in `fullsend-ai/agents`, or that has no `base` at all. Built from scratch — even if it resembles triage, code, or review. Adding skills to triage is not a custom agent; that is a [configured default](#configured-default-agent). Contrast with [derived agent](#derived-agent) (starts from a default via `base:` but replaces identity).
See [Default, derived, and custom agents](guides/user/default-vs-custom.md) and [Bring Your Own Agent](guides/user/bring-your-own-agent.md).

### Customized Agent

Colloquial phrase — avoid in precise discussion. It may mean a [configured default](#configured-default-agent), a [derived agent](#derived-agent), or a [custom agent](#custom-agent). Prefer one of those terms (see [Customization vocabulary](#customization-vocabulary)).

### Customized Directory

**Removed.** A per-org (`customized/`) or per-repo (`.fullsend/customized/`) directory whose contents were overlaid on top of upstream defaults at runtime, replacing any upstream file with a matching name. The overlay was file-level replacement only — customizing a single harness field required copying and maintaining the entire upstream YAML file. Superseded by [base composition](#base-composition) for harnesses, URL-based references for skills/agents/plugins/policies, and config-based agent registration.
See [ADR 0035](ADRs/0035-layered-content-resolution.md) (original mechanism) and [ADR 0064](ADRs/0064-deprecate-customized-directory-overlay.md) (deprecation).

## D

### Debouncing

Collapsing rapid-fire events on the same issue or PR so they do not produce
redundant agent work. On GitHub, each built-in agent's job runs in a
concurrency group for that agent and that issue or PR, with
`cancel-in-progress: true`. A newer run of the same agent on the same subject
cancels the run in progress. On GitLab, dispatch is hybrid under
[ADR 0125](ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md): webhook
fast-path events deduplicate against poll-detected events through
concurrency-safe per-mode dispatch state, and the cron poller's
watermark-based deduplication
([ADR 0067](ADRs/0067-gitlab-cron-polling-event-dispatch.md)) remains the
reconciliation backstop. The installed schedules poll for slash commands every
5 minutes and for events every 15 minutes.
[ADR 0106](ADRs/0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
additionally adopts preserve-and-coalesce scheduling at the execution-platform
layer: after implementation, an active run finishes while later matching
events collapse into one pending follow-up run.
See [architecture.md](architecture.md) (building block 1).

### Default Agent

An agent shipped by fullsend: harness files and agent definitions in `fullsend-ai/agents`. Unmodified, it is simply a default agent. Documented configuration yields a [configured default agent](#configured-default-agent). Replacing identity-defining fields on a `base:` of that harness yields a [derived agent](#derived-agent). An agent with no default `base` lineage is a [custom agent](#custom-agent).
See [Agents reference](agents/) and [Default, derived, and custom agents](guides/user/default-vs-custom.md).

### Derived Agent

An agent that uses `base` [base composition](#base-composition) from a [default agent](#default-agent) but replaces identity-defining components — system prompt (`agent:`), [pre-script](#pre-script) / [post-script](#post-script), mint `role:` (unless that agent's docs recommend a specific role override), or `validation_loop` — beyond documented [extension points](#extension-point). Changing install-time `slug:` alone does **not** make the agent derived. It reuses default lineage but is no longer recognizably that default. Example: changing the post-script so the agent can call a forge API the stock script does not support. Contrast with [configured default](#configured-default-agent) and [custom agent](#custom-agent).
See [Default, derived, and custom agents](guides/user/default-vs-custom.md).

### Direct OIDC Trust

The inference gateway's `auth: oidc` mode: the gateway validates the job's
forge OIDC token itself, with no token exchange. It is not
[WIF](#wif-workload-identity-federation). It works on GitHub Actions; the
gateway does not accept GitLab ID tokens yet.
See [How fullsend authenticates](guides/user/how-fullsend-authenticates.md).

## E

### Entry Point

The deterministic, non-AI layer that receives forge events and decides which
agents to run. It makes no LLM calls. The initial design describes it as a
single component. Previously called **wrapper** — the rename was adopted to
avoid confusion with the sandbox/wrapping layer (see
[#101](https://github.com/fullsend-ai/fullsend/issues/101) for the terminology
evolution). Today several parts do this work:

- On GitHub, the shim workflow in each repository forwards events to the
  reusable dispatch workflow. That workflow routes the built-in agents by
  stage and checks the commenter's permission before it runs a
  [slash command](#slash-command).
- `fullsend dispatch` evaluates the [trigger](#trigger) expressions of the
  registered agents.
- On GitLab, a webhook fast path starts a pipeline when an event arrives, and
  scheduled `fullsend poll` jobs pick up missed events (see
  [ADR 0125](ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md),
  [ADR 0067](ADRs/0067-gitlab-cron-polling-event-dispatch.md)).

The architecture document calls this layer the
[Agent Dispatch and Coordination Layer](architecture.md#agent-dispatch-and-coordination-layer).

See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 1 and
[#101](https://github.com/fullsend-ai/fullsend/issues/101).

### Escalation

Stopping automated processing and routing to a human. Escalation is triggered
when agents cannot reach consensus (flapping), when trust violations are
detected, when loop limits are exceeded, or when the work falls outside the
authorized scope (for example, a change that looks like a feature when only
bug fixes are authorized). Agents escalate on the issue or PR itself, with
labels that humans can filter on: the review agent applies
`requires-manual-review` when a review needs human judgment, and the fix agent
applies `needs-human` as it nears its iteration cap. Fullsend has no separate
escalation queue.
See [autonomy-spectrum.md](problems/autonomy-spectrum.md) and [agent-architecture.md](problems/agent-architecture.md).

### Eval Measurement

The concept of **scoring traces** — a score, judge, or metric applied to agent (or agent-chain) behavior. Example signals: cost per run, whether the code agent later passes review, or whether a review agent recommends merge and a human still intervenes. Measurements are not the inputs under test; they are an [OTEL derived product](#otel-derived-products) computed from [OTEL primary facts](#otel-primary-facts). Online / trend scoring of wild-run traces is decided in [ADR 0087](ADRs/0087-eval-measurements-online-trace-scoring.md) (`fullsend eval-measure`, `eval-measurements.jsonl`) and is [fail-open](#fail-open). The same measurement *idea* can also be applied to curated [eval scenarios](#eval-scenario), but those PR-gate fixtures are a separate path ([ADR 0051](ADRs/0051-agent-eval-harness-for-test-infrastructure.md)). Prefer this term (or synonyms *eval score* / *eval judge*) over the bare word "evals," which is ambiguous with [eval scenarios](#eval-scenario).
See [Eval Measurements](guides/infrastructure/eval-measurements.md), [testing-agents.md](problems/testing-agents.md), and [Observability](#observability).

### Eval Scenario

A fixed, reproducible test case — a concrete input with an expected outcome that you re-run when an agent changes. Example: triage is presented with an issue asking to add a cheeseburger to the README and is expected to reject and close it. Scenarios are maintained like tests: if intentional agent behavior changes, update the scenario expectations. They answer "did this change make the agent better or worse on known cases?" and can later grow by promoting interesting production cases from telemetry into the curated set. Distinct from [eval measurements](#eval-measurement) (online/trend scores on wild traces, or judges applied to a scenario). Prefer this term over the bare word "evals." Also called a *functional eval fixture* in agent CI.
See [ADR 0051](ADRs/0051-agent-eval-harness-for-test-infrastructure.md) and [testing-agents.md](problems/testing-agents.md) (golden-set evaluation).

### Extension Point

A documented hook on a [default agent](#default-agent) that teams are expected to use — for example a named optional skill (`customer-research` on prioritize) or a published configuration variable (`REVIEW_FINDING_SEVERITY_THRESHOLD` on review). Using an extension point keeps the result a [configured default agent](#configured-default-agent). Each agent lists its extension points in [`docs/agents/<agent>.md`](agents/).
See [Default, derived, and custom agents](guides/user/default-vs-custom.md) and [Configuring with skills](guides/user/customizing-with-skills.md#extension-points).

## F

### Fail-Open

When a step's error or a `fail` score must not fail the surrounding job or block delivery. Eval measurements are fail-open: a missing manifest, a scorer `fail`/`skip` label, or a measure-step IO error never fails the agent run. Contrast with fail-closed gates (auth, kill switch) where an error must stop the run. In scripts, fail-open is acceptable for non-critical steps (logging, metrics) and dangerous for gates.
See [ADR 0087](ADRs/0087-eval-measurements-online-trace-scoring.md), [Eval Measurements](guides/infrastructure/eval-measurements.md), and [Shell scripting](contributing/shell-scripting.md).

### Flapping

When agents enter a cycle of conflicting feedback that prevents convergence. Example: the security review agent rejects what the code agent produces to satisfy the correctness review agent, and vice versa, creating an oscillating loop. Flapping is a primary trigger for [escalation](#escalation) — after a configurable number of cycles, the system stops and routes to humans.
See [autonomy-spectrum.md](problems/autonomy-spectrum.md).

## H

### Harness

In fullsend, the configuration and context layer that prepares an agent for its task. It assembles skills, system prompts, codebase context, tool definitions, and behavioral instructions, turning a generic LLM into an agent with a specific role. Concretely, it is the per-agent YAML between the sandbox and the [agent runtime](#agent-runtime): skills, env, providers, sandbox profiles, and `base:` composition. New docs call it the [agent spec](#agent-spec).

Elsewhere in the industry (Anthropic's "agent harness" writing, meta-harness projects such as Omnigent), "harness" means the agent CLI itself — Claude Code, Codex, or pi — which fullsend calls the agent runtime. In those terms, fullsend is a [meta-harness](#meta-harness).
See [architecture.md](architecture.md).

## I

### Identity

The forge account that an agent acts as. On GitHub, each [role](#role) (for
example `triage`, `coder` or `review`) has its own GitHub App installation, so
actions are attributable and permissions are scoped per role. Agents can share
a role: the `code` and `fix` agents both act as `coder`. On GitLab, stored
per-role tokens provide the identities; the built-in roles are Poller, Analyst
and Coder. Identity is not the same as trust — an agent's identity lets it
authenticate; trust derives from repository permissions and CODEOWNERS, not
from which credentials the agent holds.
See [architecture.md](architecture.md),
[agent-architecture.md](problems/agent-architecture.md) and
[Custom agent identity](guides/user/custom-agent-identity.md).

### Installation Token

A short-lived GitHub App token that the [token mint](#token-mint) issues in
exchange for the job's OIDC token. Agents use it for forge writes such as
comments and pull requests. It exists on GitHub only: GitLab forge writes use
stored per-role tokens.
See [How fullsend authenticates](guides/user/how-fullsend-authenticates.md).

## L

### Label State Machine

The labels on issues and PRs that encode workflow state, and the transitions
between them. Labels like `ready-for-triage`, `ready-to-code`, and
`ready-for-review` drive dispatch of the built-in agents; others such as
`ready-for-merge` and `requires-manual-review` encode review outcomes. The
built-in routing labels carry the `ready-` prefix, but not every `ready-`
label routes: `ready-for-merge` is a review outcome that reaches dispatch
without matching a stage. Custom agents route on any label through a CEL
[trigger](#trigger); no prefix is required. `ready-for-review` on a PR also
triggers review; applying it to a standalone issue does not. No single
component validates transitions. The agents' host-side scripts add and remove
the labels for their own stage — for example, a triage run clears downstream
labels so stale state does not carry forward. The "label state machine guard"
in ADR 0002 is the initial design for a central check.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 3 and [Bring Your Own Agent — routing label convention](guides/user/bring-your-own-agent.md).

## M

### MCP Server (Model Context Protocol)

An external process that exposes tools to an agent via the Model Context
Protocol. Early design discussions proposed MCP servers as controlled access
points outside the sandbox, for example one that wraps the `gh` CLI.
[ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md) rejected
that option: only the agent runtime enforces MCP tool scoping, so a
compromised agent can bypass it. Instead, pre-scripts fetch inputs and
post-scripts apply outputs on the host. Where an agent needs a service during
the run, OpenShell providers give the sandbox a [placeholder](#placeholder),
and the proxy swaps in the real credential on the way out
([ADR 0025](ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md)).
See [ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md),
[#101](https://github.com/fullsend-ai/fullsend/issues/101) and
[security-threat-model.md](problems/security-threat-model.md).

### Meta-harness

A platform that configures, sandboxes, and orchestrates agent harnesses — [agent runtimes](#agent-runtime) in fullsend terms — rather than being one itself. Fullsend is a meta-harness: it assembles the [agent spec](#agent-spec) for each role, provisions the [sandbox](#sandbox), and dispatches to a runtime such as Claude Code, Codex, or pi. Omnigent is another example.
See [architecture.md](architecture.md).

### Model Armor

Google's API for prompt injection detection and defense. The team evaluated it
as a defense layer in front of agents. In fullsend's experiments it caught at
most 25% of the injection payloads, and
[ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md) concludes
that no detection rate is high enough to make a credential inside the sandbox
safe. Fullsend does not call Model Armor. Its content scans use a local
prompt-injection classifier.
See [ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md) and
[platform-nativeness.md](problems/platform-nativeness.md).

## O

### Observability

The logging, tracing, and audit layer for agent actions. Every agent action
must be attributable, traceable, and reviewable — both for debugging failures
and for security auditability. In practice, fullsend extracts the agent's JSONL
transcripts (including "thinking" traces) from the sandbox and stores them with
the run's output, and it records each run as OpenTelemetry spans
([OTEL primary facts](#otel-primary-facts)). Observability is a cross-cutting
concern that touches every other component.
See [architecture.md](architecture.md).

### On-demand Skill

> **Planned:** A per-skill optional / on-demand mode (syntax undecided) is tracked in [#6682](https://github.com/fullsend-ai/fullsend/issues/6682). Harness-listed skills currently receive frontmatter injection on the Claude runtime; activation behavior still requires empirical runtime validation. See [#6681](https://github.com/fullsend-ai/fullsend/issues/6681).

A [skill](#skill) load mode where the skill is available on the run but is not forced active by default. Harness-listed skills are uploaded and shown in the runtime skill list; on the Claude runtime, fullsend also injects them into agent frontmatter, but actual activation without an explicit Skill tool call still requires empirical validation. Contrast with [always-on skill](#always-on-skill).

### OTEL Derived Products

Values **computed from** a run's OpenTelemetry trace after the fact — scores, fitness checks, later quality signals. They are not a second copy of what happened. First-ship example: `eval-measurements.jsonl` from `fullsend eval-measure` ([eval measurements](#eval-measurement) are the concept of scoring traces). Derived products sit beside telemetry as sibling files and, when `OTEL_EXPORTER_OTLP_*` is configured, also export as span events on the agent-trace OTLP path; they never replace [OTEL primary facts](#otel-primary-facts).
See [ADR 0087](ADRs/0087-eval-measurements-online-trace-scoring.md) and [Eval Measurements](guides/infrastructure/eval-measurements.md).

### OTEL Primary Facts

What **actually happened** on an agent run, recorded as OpenTelemetry (OTEL) spans. The local source of truth is `run-telemetry.jsonl`; when `OTEL_EXPORTER_OTLP_*` is set, the same spans also export live over OTLP ([ADR 0050](ADRs/0050-distributed-tracing-instrumentation.md)). Agent identity, work item, tokens, cost, span tree, and `exit_code` belong here. Sibling files (including [eval measurements](#eval-measurement)) must not become a second source of run truth.
See [Distributed Tracing](guides/infrastructure/distributed-tracing.md) and [ADR 0087](ADRs/0087-eval-measurements-online-trace-scoring.md).

## P

### Placeholder

A stand-in value the sandbox holds instead of a real credential. The OpenShell proxy swaps in the real credential on the way out, so the credential never enters the sandbox.
See [How fullsend authenticates](guides/user/how-fullsend-authenticates.md).

### Policy Store

Where agent behavioral rules live — autonomy levels, review requirements,
allowed operations, and escalation rules. Policy is distinct from the harness
(which configures *how* an agent works) and from intent (which defines *what*
work is authorized). Policy defines the *boundaries* of agent behavior — what
an agent is allowed to do regardless of what it's asked to do. Each target
repository's `.fullsend/` directory holds policy configuration, governed by the
repository's CODEOWNERS and review process
([ADR 0033](ADRs/0033-per-repo-installation-mode.md)). The harness `policy:`
field is a separate thing: it names the sandbox policy file.
See [architecture.md](architecture.md) and
[governance.md](problems/governance.md).

### Post-script

A host-side script declared as `post_script` in the harness. Runs **outside** the sandbox **after** the agent exits, with credentials, to apply untrusted agent output (labels, comments, pushes, board updates). Credential isolation depends on this boundary ([ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md)). Changing `post_script` on a default `base:` is a [script override](#script-override) and makes the agent [derived](#derived-agent) — skills cannot replace post-script API actions the stock script does not perform.
See [ADR 0024](ADRs/0024-harness-definitions.md) and [Default, derived, and custom agents](guides/user/default-vs-custom.md).

### Pre-script

A host-side script declared as `pre_script` in the harness. Runs **outside** the sandbox **before** the agent starts, with credentials, to fetch inputs and write them into the workspace for the sandbox. Changing `pre_script` on a default `base:` is a [script override](#script-override) and makes the agent [derived](#derived-agent).
See [ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md), [ADR 0024](ADRs/0024-harness-definitions.md), and [Default, derived, and custom agents](guides/user/default-vs-custom.md).

## R

### Ready to Code

A label indicating that an issue has passed triage and is cleared for the code
agent. It is a key transition point in the
[label state machine](#label-state-machine). With the default settings, the
triage post-script applies it when the triage agent finds the issue fully
specified and the issue's category is on the auto-promotion list
(`TRIAGE_AUTO_CODE_CATEGORIES`; `bug`, `documentation` and `performance` by
default). Other fully specified issues, such as feature requests, receive
`triaged` and wait for a human. The label triggers the code agent.
See [Triage agent](agents/triage.md) and
[Ready-to-code promotion](https://github.com/fullsend-ai/agents/blob/main/docs/triage.md#ready-to-code-promotion).

### Repo Skill

A [skill](#skill) committed under the target repo (typically `.agents/skills/`, often symlinked as `.claude/skills`). Discovered under the Claude Code and Codex runtimes (Codex keeps project trust untrusted, Claude Code parity — see [Codex](runtimes/codex.md)). Novel names are [additive](#additive-skill). A repo skill whose name matches a [built-in skill](#built-in-skill) is **shadowed**: built-ins upload to the personal-level config dir (`CLAUDE_CONFIG_DIR/skills/`), while repo skills stay at the project level (`.claude/skills/`); Claude Code's personal-over-project precedence ignores the repo copy — there is no bootstrap error or fail-fast, but on Claude Code fullsend emits a warning when a repo skill's identity (declared `SKILL.md` name, or basename) collides with a harness-listed skill's identity. That shadowing is not a [skill override](#skill-override). Use a unique name, or replace intentionally via [base composition](#base-composition) (matching skill identity — declared `SKILL.md` name, or basename — on the child harness `skills:` list). With `runtime: pi`, project trust is disabled (`--no-approve` / `defaultProjectTrust: never`), so repo-committed skills under `.agents/skills` are not discovered at all — see [runtime implementation](contributing/runtime-implementation.md).
See [Configuring with skills — Skill precedence](guides/user/customizing-with-skills.md#skill-precedence).

### Rework Rate

A quality metric measuring how many review-fix cycles a PR goes through before reaching approval. Visible on PRs when review happens post-submission (requested changes → fix → re-review). If review moves to a pre-PR inner loop, rework rate becomes harder to measure from the PR alone and must be extracted from agent logs.

### Role

The harness field (`role:`) that sets which forge identity an agent
authenticates as. A role is separate from the agent's name, and agents can
share one: the `code` and `fix` agents both run as `coder`. On GitHub, the
role also sets the most permissions the agent can hold. The hosted
[token mint](#token-mint) serves a fixed set of built-in roles: `triage`,
`coder`, `review`, `retro`, `prioritize` and `fullsend`. A mint that you run
yourself can also serve custom roles. On GitLab, fullsend maps the agent's
name, or else its role, to one of the stored per-role tokens. Those tokens
carry the same access, so on GitLab the role sets only the identity. Replacing
`role:` on a default agent normally makes a [derived agent](#derived-agent).
See [Custom agent identity](guides/user/custom-agent-identity.md) and
[Identity](#identity).

## S

### Sandbox

The isolation boundary around a running agent. Responsible for filesystem
access control and network regulation — ensuring an agent can only reach what
it's authorized to reach and cannot affect other agents or systems outside its
boundary. The sandbox is a **security primitive**, not the entire execution
environment. Its job is containment: if an agent is compromised, the blast
radius is limited to what the sandbox permits. Do not confuse with the broader
execution environment (which also includes the harness and runtime). Fullsend
implements the sandbox with
[NVIDIA/OpenShell](https://github.com/NVIDIA/OpenShell)
([ADR 0036](ADRs/0036-agent-execution-sandbox.md)).
See [architecture.md](architecture.md) and
[security-threat-model.md](problems/security-threat-model.md).

### Script Override

Setting `pre_script` or `post_script` on a child harness so it **replaces** the base script. Under [base composition](#base-composition) these are scalars: override only — not additive concatenation (unlike unique skill names). Replacing scripts on a default `base:` yields a [derived agent](#derived-agent).
See [ADR 0045](ADRs/0045-forge-portable-harness-schema.md), [Default, derived, and custom agents](guides/user/default-vs-custom.md), and [Configuring agents](guides/user/customizing-agents.md).

### Sidecar

An external process running alongside (but outside) the agent's sandbox that
mediates access to resources the sandbox cannot natively constrain. Early
design discussions gave the example of an ephemeral Git server that receives
`git push` from the agent and forwards it only to the one branch the agent may
write to. Fullsend does not ship that sidecar: the code agent's post-script
pushes the branch from the host. Unlike an MCP server (which the agent
explicitly calls as a tool), a sidecar can be transparent — the agent may not
know it is interacting with a mediator rather than the real service.
See [ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md) and
[#101](https://github.com/fullsend-ai/fullsend/issues/101).

### Signed Commit

A commit carrying a cryptographic signature the forge can verify; GitHub shows it as **Verified**, and repository rulesets can require one. The scaffold commits `fullsend repos install` creates on GitHub are signed by GitHub itself, which creates them on the operator's behalf through its API — no key on the operator's machine is involved. The exception is a vendored install, whose binary goes through an API GitHub signs only for App tokens. Distinct from the DCO `Signed-off-by` trailer, which is message text.
See [Signed commits](cli/repos.md#signed-commits) and [ADR 0140](ADRs/0140-sign-github-scaffold-commits-via-createcommitonbranch.md).

### Skill

A directory containing a `SKILL.md` file and optional companions (scripts, sub-agents, references, assets) that gives an agent scoped instructions for a task. Skills are assembled by the [harness](#harness). A skill may declare tools it is authorized to use; approving the skill implicitly authorizes those tools. Skills change *how* an agent reasons; they do not replace host-side [pre-script](#pre-script) / [post-script](#post-script) forge actions. Adding skills → usually [configured default](#configured-default-agent); see also [additive skill](#additive-skill), [skill override](#skill-override), [always-on](#always-on-skill), [on-demand](#on-demand-skill).
See [architecture.md](architecture.md), [codebase-context.md](problems/codebase-context.md), and [Configuring with skills](guides/user/customizing-with-skills.md).

### Skill Override

Intentionally **replacing** a [built-in skill](#built-in-skill) so the agent does not load the shipped version. Distinct from an [additive skill](#additive-skill) (new unique name). Under [base composition](#base-composition), `skills` merges with **deduplication by skill identity** (declared `SKILL.md` name when on disk, otherwise basename) — a child entry with the same identity overrides the base. Nested forge skills that share a leaf directory (`skills/issue-labels/github` vs `skills/pr-review/github`) are not treated as the same skill. Historically also done via `customized/skills/` ([ADR 0035](ADRs/0035-layered-content-resolution.md)), now removed ([ADR 0064](ADRs/0064-deprecate-customized-directory-overlay.md) / [Customized Directory](#customized-directory)). Classification: still [configured default](#configured-default-agent) when you only replace the skill (not `agent:` or scripts). Do not rely on a same-named [repo skill](#repo-skill) for override — that path is shadowed without fail-fast (see [Repo Skill](#repo-skill) / [Skill precedence](guides/user/customizing-with-skills.md#skill-precedence)). Fail-fast on duplicate sandbox names applies only when two harness-listed skills collide in `SkillDirs()`, not to repo-vs-built-in collisions. To change only some files of a skill instead of replacing all of it, use per-file overrides on its `skills:` entry ([Overriding single files in a skill](guides/user/customizing-with-skills.md#overriding-single-files-in-a-skill)).
See [Base composition](#base-composition), [ADR 0045](ADRs/0045-forge-portable-harness-schema.md), and [Default, derived, and custom agents](guides/user/default-vs-custom.md).

### Slash Command

A comment in the form `/fs-triage`, `/fs-code`, `/fs-review`, and so on, that
manually starts an agent. Slash commands work in comments on GitHub issues and
PRs, on GitLab, and on Jira issues. The `/fs-` prefix namespaces fullsend
commands to avoid collisions with other AI tools. The dispatch layer (see
[entry point](#entry-point)) parses slash commands and checks the commenter's
permission, so not every user can invoke every command. They provide an
explicit human-initiated trigger alongside the automatic label-based triggers.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 2 and
[ADR 0042](ADRs/0042-fs-prefix-for-slash-commands.md).

### Stage

A higher-level workflow component in the fullsend pipeline (e.g., triage, code, review). The team formally chose "stage" over "phase" to avoid overloading the general SDLC use of "phase" and to maintain distinct vocabulary from Tekton's pipeline/task/step hierarchy, since fullsend may run on Tekton infrastructure. Each stage contains one or more [steps](#step).
See [ADR 0002](ADRs/0002-initial-fullsend-design.md).

### Step

A discrete unit of work within a [stage](#stage). For example, the triage stage may include steps for duplicate detection, completeness assessment, and label assignment. "Stages and steps" is the agreed-upon workflow hierarchy for fullsend.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md).

## T

### Token Mint

The service that issues short-lived forge tokens on GitHub. It checks the
job's OIDC token, then returns a GitHub App
[installation token](#installation-token) for the [role](#role) that the
harness names. The GitHub App private keys stay in the mint; the repository
stores only the mint URL. The mint is GitHub-only: GitLab forge writes use
stored per-role tokens.
See [How fullsend authenticates](guides/user/how-fullsend-authenticates.md) and
[Mint service administration](guides/infrastructure/mint-administration.md).

### Triage

In fullsend, triage means routing, deduplicating and assessing completeness —
**not** fixing. The triage agent reads the issue and the repository context.
It determines whether the issue is a duplicate, classifies it (for example as
a bug, a feature request, documentation or a question), and checks whether it
has enough detail for the code agent. It does not attempt reproduction. Its
only output is a structured result. The post-script applies that result as
labels and a comment: for example `needs-info` with clarifying questions,
`duplicate`, `not-planned`, `triaged` for work that waits for a human, or
[`ready-to-code`](#ready-to-code). The scope of triage has been a recurring
discussion point, particularly around whether reproducibility and test
generation belong in triage or implementation.
See [Triage agent](agents/triage.md),
[ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 4 and
[#86](https://github.com/fullsend-ai/fullsend/issues/86).

### Trigger

What initiates an agent run: a forge or tracker event (issue filed, label
applied, comment posted, PR/MR opened), a [slash command](#slash-command), or
a scheduled action. In a harness, `trigger:` is a CEL expression over the
normalized event; fullsend runs the agent when the expression matches. On
GitHub, the dispatch workflow routes the built-in agents by stage, and harness
triggers start the other registered agents. A `trigger:` on the `agents:`
entry for review, retro, triage or prioritize decides whether that agent's
built-in job starts. Jira polling also evaluates harness triggers. GitLab uses
its own built-in router and does not evaluate `trigger` expressions. In
discussions the term sometimes means the raw forge event instead.
See [CEL Triggers Reference](guides/user/cel-triggers-reference.md) and
[architecture.md](architecture.md) (building block 1).

### Trust

In fullsend, trust is not a single concept — it appears in at least three distinct contexts:

1. **Identity trust** — Can we verify who is making a request? Addressed by
   agent [identities](#identity): GitHub App installations on GitHub, and
   stored role tokens on GitLab.
2. **Content trust** — Can we trust the content of inputs (issue bodies, comments, PR descriptions)? The answer is always **no** under the zero-trust model; all inputs are sanitized regardless of source.
3. **Execution trust** — Can we trust that an agent will do what it's supposed to? Addressed by sandboxing, scoped permissions, and the principle that trust derives from repository permissions, not agent identity.

The overloading of "trust" across these contexts has been a recurring source of confusion in design discussions.
See [security-threat-model.md](problems/security-threat-model.md) and [agent-architecture.md](problems/agent-architecture.md).

## V

### Validation Loop

The harness block (`validation_loop:`) that names a script to check the
agent's output, for example against a JSON schema. When the check fails,
fullsend runs the agent again, until the agent has run `max_iterations` times
in total. The default is 1, so the agent runs once. With
`feedback_mode: append`, fullsend appends the validation feedback to the
prompt on a retry. The validation loop is the contract between the agent and
the harness for what a run must produce. Replacing it on a default agent makes
a [derived agent](#derived-agent).
See [Harness field reference](reference/harness-reference.md).

## W

### WIF (Workload Identity Federation)

A token exchange in which a provider trusts the job's OIDC token and returns
its own short-lived access token. Fullsend uses GCP WIF for Vertex AI on GitHub
Actions and GitLab CI, and OpenAI Workload Identity for OpenAI models on GitHub
Actions only; the inference gateway's [direct OIDC trust](#direct-oidc-trust)
is not WIF.
See [How fullsend authenticates](guides/user/how-fullsend-authenticates.md).

### Work Coordinator

The mechanism that assigns work to agents and prevents conflicts. The design
principle is that the **repo is the coordinator** — branch protection,
CODEOWNERS, status checks, and forge events provide coordination without a
central orchestrator. The architecture document describes this part as the
[Agent Dispatch and Coordination Layer](architecture.md#agent-dispatch-and-coordination-layer).
[ADR 0106](ADRs/0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
decides that fullsend serializes agent runs on the same issue or PR. That
decision is not implemented yet: the GitHub workflows still cancel the run in
progress (see [Debouncing](#debouncing)).
See [architecture.md](architecture.md) and [#77](https://github.com/fullsend-ai/fullsend/issues/77).

## Z

### Zero Trust

In fullsend's agent-to-agent model, zero trust means **nothing is trusted implicitly based on identity alone**. It does **not** mean "accept zero inputs" or "block everything." Every agent assumes every other agent — and every external input — could be compromised. The code agent assumes the triage output may contain prompt injection. The review agent assumes the submitted PR is designed to trick it. Defense is layered: input sanitization, scoped permissions, sandbox containment, and output validation all work together.
See [security-threat-model.md](problems/security-threat-model.md) (Threat 5) and [#102](https://github.com/fullsend-ai/fullsend/issues/102).
