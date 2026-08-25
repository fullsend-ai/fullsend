# Glossary

Shared vocabulary for the fullsend project. Terms are defined in the context of fullsend's architecture, workflow, and security model. Each entry includes a brief definition and a pointer to the relevant document for deeper context.

This is a living document. PRs that introduce new terminology should add to this glossary as part of the change.

## Customization vocabulary

When someone says they "customized an agent," ask which of these they mean — colloquial **"customized agent"** is ambiguous; prefer a precise term below.

**Agent classification** (pick the most specific that applies — these are categories, not ordered steps):

- [Default agent](#default-agent) — unmodified shipped agent
- [Configured default agent](#configured-default-agent) — same identity; allowed configuration only
- [Derived agent](#derived-agent) — `base:` lineage from a default, but identity-defining fields replaced
- [Custom agent](#custom-agent) — no default `base` lineage (built from scratch)

**Skills** (how instructions are added or replaced):

- [Additive skill](#additive-skill) vs [Skill override](#skill-override)
- [Always-on skill](#always-on-skill) vs [On-demand skill](#on-demand-skill) (load modes; see [ADR 0092](ADRs/0092-always-on-harness-skills.md))
- [Built-in skill](#built-in-skill), [Repo skill](#repo-skill), [Extension point](#extension-point)

**Scripts** (host-side pre/post hooks):

- [Pre-script](#pre-script) / [Post-script](#post-script) changes are [script overrides](#script-override) — replace, not add

**Platform:**

- [BYOA](#byoa) is the capability to bring your own agents and config — **not** a synonym for [custom agent](#custom-agent)

| Goal | Resource |
|------|----------|
| Classify your change | [Default, derived, and custom agents](agents/topics/default-vs-custom.md) |
| Configure a harness | [Configuring agents](guides/user/customizing-agents.md) |
| Bring your own agent | [Bring Your Own Agent](guides/user/bring-your-own-agent.md) |
| Add skills | [Configuring with skills](guides/user/customizing-with-skills.md) |
| Project-wide instructions | [Configuring with AGENTS.md](guides/user/customizing-with-agents-md.md) |
| `base:` merge rules | [Base composition](#base-composition), [ADR 0045](ADRs/0045-forge-portable-harness-schema.md) |
| Always-on skill load mode | [ADR 0092](ADRs/0092-always-on-harness-skills.md) |

---

## A

### Additive Skill

A [skill](#skill) that **extends** an agent's skill set without replacing a [built-in skill](#built-in-skill). Typical paths: list a new unique name under harness `skills:` via [base composition](#base-composition), or add a uniquely named [repo skill](#repo-skill). The default agent's identity is unchanged — this yields a [configured default agent](#configured-default-agent), not a [custom agent](#custom-agent). Example pattern: a team brevity or house-style skill composed alongside existing skills.
See [Configuring with skills](guides/user/customizing-with-skills.md) and [Default, derived, and custom agents](agents/topics/default-vs-custom.md).

### Agent Infrastructure

The compute and orchestration layer that runs agent workloads — provisioning, scheduling, scaling, and lifecycle management of agent execution environments. This is the "where do agents physically run" question. Options include GitHub Actions, Tekton pipelines, OpenShift AI (KServe), or purpose-built platforms.
See [architecture.md](architecture.md) and [agent-infrastructure.md](problems/agent-infrastructure.md).

### Agent Registry

The catalog of available agent roles and their configurations. It bridges the abstract roles defined in the agent architecture (triage, code, review) and the concrete runtime configurations the harness uses to instantiate each agent. Fullsend provides a base set; adopting organizations extend it via their `.fullsend` repository.
See [architecture.md](architecture.md).

### Agent Runtime

The agent itself in execution — the LLM, its tool-use loop, and the interface to the model provider. This is the thing that actually reasons and acts; everything else in the architecture exists to support, constrain, or coordinate it. Claude Code is the default runtime; [pi](https://github.com/earendil-works/pi) is available as an opt-in second runtime (`runtime: pi`), and OpenCode is a stub. See [runtimes.md](runtimes.md).
See [architecture.md](architecture.md) and [agent-infrastructure.md](problems/agent-infrastructure.md).

### AGENTS.md

Project-wide instructions for humans and agents (conventions, testing, architecture). Fullsend agents operating on a checked-out target repo read it there ([Configuring with AGENTS.md](guides/user/customizing-with-agents-md.md)). Using `AGENTS.md` keeps you in [configured default](#configured-default-agent) territory; it is not a [custom agent](#custom-agent). Prefer `AGENTS.md` for rules that apply to every agent; use a [skill](#skill) when behavior is agent- or task-specific.

### Always-on Skill

A harness [skill](#skill) load mode decided in [ADR 0092](ADRs/0092-always-on-harness-skills.md): frontmatter `metadata.apply: always` (legacy top-level `apply: always` also accepted) opts the skill into always-on. Under the Claude Code runtime, after upload, bootstrap injects a short directive naming those skills so the model opens each with the Skill tool before relying on them — it does not paste `SKILL.md` bodies into the agent file; that path requires `Skill` in the agent's `tools:`. (pi has no Skill tool — skills are prompt-driven via `read` of `SKILL.md`; see [runtimes](runtimes.md).) Adding such a skill via `skills:` on a thin `base:` wrapper keeps a [configured default](#configured-default-agent); replacing `agent:` just to name the skill would make it [derived](#derived-agent). Contrast with [on-demand skill](#on-demand-skill).
See [Configuring with skills](guides/user/customizing-with-skills.md) and [#6380](https://github.com/fullsend-ai/fullsend/issues/6380).

### Automerge

The end-state goal where PRs that pass all agent review and CI checks are merged to the target branch without human intervention. Automerge is gated by the [autonomy spectrum](problems/autonomy-spectrum.md) — most workflows start with human-in-the-loop approval and graduate toward automerge as confidence increases. The team has explicitly decided not to implement automerge in the MVP; agents will comment that they approve, but a human must merge.
See [autonomy-spectrum.md](problems/autonomy-spectrum.md).

## B

### Base Composition

The mechanism for customizing an agent's harness: a thin harness file sets `base:` to a local path or URL pointing at an upstream harness, then declares only the fields that differ. Scalars override the base value; `skills` merges with deduplication by basename (child overrides); `plugins`, `providers`, `openshell.profiles`, and `api_servers` concatenate (base + child, no dedup at composition time); `env.runner` and `env.sandbox` merge as independent maps (child keys win); `runner_env` is deprecated in favor of `env.runner`. Replaced the deprecated [`customized/` directory](#customized-directory) overlay, which required copying and maintaining an entire upstream YAML file to change a single field. Same mechanism as colloquial "harness composition."
See [ADR 0045](ADRs/0045-forge-portable-harness-schema.md), [ADR 0055](ADRs/0055-unified-env-var-delivery.md), [ADR 0064](ADRs/0064-deprecate-customized-directory-overlay.md), and [Configuring Agent Behavior](guides/user/customizing-agents.md).

### Blast Radius

The scope of damage a compromised or misbehaving agent can cause. A core design constraint: every architectural decision about sandboxing, identity scoping, and network policy is evaluated by asking "what is the blast radius if this agent is compromised?" Minimizing blast radius is the primary goal of the sandbox layer.
See [security-threat-model.md](problems/security-threat-model.md) and [architecture.md](architecture.md).

### Built-in Skill

A [skill](#skill) that ships with a [default agent](#default-agent) (for example `code-implementation`, `code-review`, `issue-labels`). Listed in that agent's harness and documented under [Agents reference](agents/). Teams typically [add](#additive-skill) alongside built-ins; replacing one by basename via [base composition](#base-composition) or the historical overlay is a [skill override](#skill-override).
See [Configuring with skills](guides/user/customizing-with-skills.md#built-in-skills).

### BYOA

**Bring Your Own Agent** — the platform capability to register, compose, and run agents and harness config you own (config.yaml registration and `base:` [base composition](#base-composition)). BYOA covers [configured default](#configured-default-agent), [derived](#derived-agent), and [custom](#custom-agent) agents. Saying "we use BYOA" does **not** mean "we built a custom agent"; many BYOA users only add skills or env on a default harness.
See [Bring Your Own Agent](guides/user/bring-your-own-agent.md), [ADR 0058](ADRs/0058-agent-registration.md), and [ADR 0045](ADRs/0045-forge-portable-harness-schema.md).

## C

### Configured Default Agent

A [default agent](#default-agent) whose behavior was adjusted **without** changing identity-defining harness fields (`agent:`, [pre-script](#pre-script) / [post-script](#post-script), slug, validation loop). Allowed paths include documented [extension points](#extension-point), [additive skills](#additive-skill), [skill overrides](#skill-override), [AGENTS.md](#agentsmd), env vars, plugins, host files, sandbox image layers, and policy composition. The slug normally stays too — treat a slug change as [derived](#derived-agent) unless that agent's own docs recommend a specific slug override for a stated purpose. Still recognizably the same agent (for example "our triage, with team skills").
See [Default, derived, and custom agents](agents/topics/default-vs-custom.md).

### Custom Agent

An agent whose `base` chain does **not** trace back to a default agent harness in `fullsend-ai/agents`, or that has no `base` at all. Built from scratch — even if it resembles triage, code, or review. Adding skills to triage is not a custom agent; that is a [configured default](#configured-default-agent). Contrast with [derived agent](#derived-agent) (starts from a default via `base:` but replaces identity).
See [Default, derived, and custom agents](agents/topics/default-vs-custom.md) and [Bring Your Own Agent](guides/user/bring-your-own-agent.md).

### Customized Agent

Colloquial phrase — avoid in precise discussion. It may mean a [configured default](#configured-default-agent), a [derived agent](#derived-agent), or a [custom agent](#custom-agent). Prefer one of those terms (see [Customization vocabulary](#customization-vocabulary)).

### Customized Directory

**Removed.** A per-org (`customized/`) or per-repo (`.fullsend/customized/`) directory whose contents were overlaid on top of upstream defaults at runtime, replacing any upstream file with a matching name. The overlay was file-level replacement only — customizing a single harness field required copying and maintaining the entire upstream YAML file. Superseded by [base composition](#base-composition) for harnesses, URL-based references for skills/agents/plugins/policies, and config-based agent registration.
See [ADR 0035](ADRs/0035-layered-content-resolution.md) (original mechanism) and [ADR 0064](ADRs/0064-deprecate-customized-directory-overlay.md) (deprecation).

## D

### Debouncing

Collapsing rapid-fire events on the same issue or PR into a single agent invocation. Without debouncing, a burst of edits to an issue body could trigger multiple redundant triage runs. The [webhook + dispatch service](ADRs/0002-initial-fullsend-design.md#1-webhook--dispatch-service) is responsible for deduplicating flapping events before dispatching work to agents. On GitHub this uses real-time webhooks; on GitLab the cron poller provides watermark-based deduplication at 5–60 minute intervals, which is functionally analogous but operates on a coarser time scale (see [ADR 0067](ADRs/0067-gitlab-cron-polling-event-dispatch.md)).
See [architecture.md](architecture.md) (building block 1).

### Default Agent

An agent shipped by fullsend: harness files and agent definitions in `fullsend-ai/agents`. Unmodified, it is simply a default agent. Documented configuration yields a [configured default agent](#configured-default-agent). Replacing identity-defining fields on a `base:` of that harness yields a [derived agent](#derived-agent). An agent with no default `base` lineage is a [custom agent](#custom-agent).
See [Agents reference](agents/) and [Default, derived, and custom agents](agents/topics/default-vs-custom.md).

### Derived Agent

An agent that uses `base` [base composition](#base-composition) from a [default agent](#default-agent) but replaces identity-defining components — system prompt (`agent:`), [pre-script](#pre-script) / [post-script](#post-script), slug (unless that agent's docs recommend a specific override), or validation loop — beyond documented [extension points](#extension-point). It reuses default lineage but is no longer recognizably that default. Example: changing the post-script so the agent can call a forge API the stock script does not support. Contrast with [configured default](#configured-default-agent) and [custom agent](#custom-agent).
See [Default, derived, and custom agents](agents/topics/default-vs-custom.md).

## E

### Entry Point

The single deterministic component that receives forge events and decides which agent combination to run. On GitHub, events arrive via webhooks; on GitLab, via cron-polled scheduled pipelines (see [ADR 0067](ADRs/0067-gitlab-cron-polling-event-dispatch.md)). Previously called **wrapper** — the rename was adopted to avoid confusion with the sandbox/wrapping layer (see [#101](https://github.com/fullsend-ai/fullsend/issues/101) for the terminology evolution). The entry point is non-AI: it is a conventional program (currently Go) that parses events, enforces ACLs on slash commands, validates label transitions, and dispatches to agent runtimes. It does not make LLM calls.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 1 and [#101](https://github.com/fullsend-ai/fullsend/issues/101).

### Escalation

Stopping automated processing and routing to a human. Escalation is triggered when agents cannot reach consensus (flapping), when trust violations are detected, when loop limits are exceeded, or when the work falls outside the authorized scope (e.g., a change that looks like a feature when only bug fixes are authorized). The escalation queue is the "dead letter queue" — the place humans monitor for items the system could not resolve autonomously.
See [autonomy-spectrum.md](problems/autonomy-spectrum.md) and [agent-architecture.md](problems/agent-architecture.md).

### Eval Measurement

The concept of **scoring traces** — a score, judge, or metric applied to agent (or agent-chain) behavior. Example signals: cost per run, whether the code agent later passes review, or whether a review agent recommends merge and a human still intervenes. Measurements are not the inputs under test; they are an [OTEL derived product](#otel-derived-products) computed from [OTEL primary facts](#otel-primary-facts). Online / trend scoring of wild-run traces is decided in [ADR 0087](ADRs/0087-eval-measurements-online-trace-scoring.md) (`fullsend eval-measure`, `eval-measurements.jsonl`) and is [fail-open](#fail-open). The same measurement *idea* can also be applied to curated [eval scenarios](#eval-scenario), but those PR-gate fixtures are a separate path ([ADR 0051](ADRs/0051-agent-eval-harness-for-test-infrastructure.md)). Prefer this term (or synonyms *eval score* / *eval judge*) over the bare word "evals," which is ambiguous with [eval scenarios](#eval-scenario).
See [Eval Measurements](guides/infrastructure/eval-measurements.md), [testing-agents.md](problems/testing-agents.md), and [Observability](#observability).

### Eval Scenario

A fixed, reproducible test case — a concrete input with an expected outcome that you re-run when an agent changes. Example: triage is presented with an issue asking to add a cheeseburger to the README and is expected to reject and close it. Scenarios are maintained like tests: if intentional agent behavior changes, update the scenario expectations. They answer "did this change make the agent better or worse on known cases?" and can later grow by promoting interesting production cases from telemetry into the curated set. Distinct from [eval measurements](#eval-measurement) (online/trend scores on wild traces, or judges applied to a scenario). Prefer this term over the bare word "evals." Also called a *functional eval fixture* in agent CI.
See [ADR 0051](ADRs/0051-agent-eval-harness-for-test-infrastructure.md) and [testing-agents.md](problems/testing-agents.md) (golden-set evaluation).

### Evergreen

A workflow concept where a repository automatically stays up-to-date with dependency updates (e.g., Renovate PRs) by automerging changes that consist solely of known-safe dependency bumps. Named by analogy with evergreen browsers that silently self-update. Proposed as a stretch-goal supplementary workflow.

### Extension Point

A documented hook on a [default agent](#default-agent) that teams are expected to use — for example a named optional skill (`customer-research` on prioritize) or a published configuration variable (`REVIEW_FINDING_SEVERITY_THRESHOLD` on review). Using an extension point keeps the result a [configured default agent](#configured-default-agent). Each agent lists its extension points in [`docs/agents/<agent>.md`](agents/).
See [Default, derived, and custom agents](agents/topics/default-vs-custom.md) and [Configuring with skills](guides/user/customizing-with-skills.md#extension-points).

## F

### Fail-Open

When a step's error or a `fail` score must not fail the surrounding job or block delivery. Eval measurements are fail-open: a missing manifest, a scorer `fail`/`skip` label, or a measure-step IO error never fails the agent run. Contrast with fail-closed gates (auth, kill switch) where an error must stop the run. In scripts, fail-open is acceptable for non-critical steps (logging, metrics) and dangerous for gates.
See [ADR 0087](ADRs/0087-eval-measurements-online-trace-scoring.md), [Eval Measurements](guides/infrastructure/eval-measurements.md), and [Shell scripting](contributing/shell-scripting.md).

### Flapping

When agents enter a cycle of conflicting feedback that prevents convergence. Example: the security review agent rejects what the code agent produces to satisfy the correctness review agent, and vice versa, creating an oscillating loop. Flapping is a primary trigger for [escalation](#escalation) — after a configurable number of cycles, the system stops and routes to humans.
See [autonomy-spectrum.md](problems/autonomy-spectrum.md).

## H

### Harness

The configuration and context layer that prepares an agent for its task. The harness assembles skills, system prompts, codebase context, tool definitions, and behavioral instructions — it is what transforms a generic LLM into a specific agent with a specific role. "Harness engineering" is a relatively new term in the industry (emerging early 2026); in fullsend, the harness is a distinct architectural layer between the sandbox and the agent runtime.
See [architecture.md](architecture.md).

## I

### Identity

A distinct GitHub App installation representing a specific agent role (e.g., triage, coder, reviewer). Each agent role gets its own identity so that actions are attributable and permissions can be scoped per-role. Identity is not the same as trust — an agent's identity lets it authenticate; trust derives from repository permissions and CODEOWNERS, not from which credentials the agent holds.
See [architecture.md](architecture.md) and [agent-architecture.md](problems/agent-architecture.md).

## L

### Label State Machine

The set of valid label transitions on issues and PRs that encode workflow state. Labels like `ready-for-triage`, `ready-to-code`, and `ready-for-review` drive agent dispatch; others such as `ready-for-merge` and `requires-manual-review` encode review outcomes. Every routing label carries the `ready-` prefix, but not every `ready-` label routes: `ready-for-merge` is a review outcome that reaches dispatch without matching a stage. Per-org shims filter on this prefix, while per-repo shims allow arbitrary labels for BYOA harness agents. In per-repo installs, `ready-for-review` on a PR also triggers review; applying it to a standalone issue does not. The label state machine guard validates that transitions are legal and enforces mutual exclusion — for example, starting a triage run clears downstream labels so stale state does not carry forward.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 3 and [Bring Your Own Agent — routing label convention](guides/user/bring-your-own-agent.md).

## M

### MCP Server (Model Context Protocol)

An external process that exposes tools to an agent via the Model Context Protocol. In fullsend, MCP servers are used as controlled access points outside the sandbox — for example, an MCP server that wraps the `gh` CLI can provide GitHub access while keeping credentials out of the agent's environment. MCP servers are preferred where necessary (particularly for mediating writes that the sandbox cannot natively constrain), while direct API calls or skills are preferred for static, deterministic processes to avoid performance overhead.
See [#101](https://github.com/fullsend-ai/fullsend/issues/101) and [security-threat-model.md](problems/security-threat-model.md).

### Model Armor

Google's API for prompt injection detection and defense. Referenced in the security threat model as a potential defense layer that can be placed in front of every agent to detect and block prompt injection attempts in inputs. The team is working to obtain access for evaluation.
See [security-threat-model.md](problems/security-threat-model.md).

## O

### Observability

The logging, tracing, and audit layer for agent actions. Every agent action must be attributable, traceable, and reviewable — both for debugging failures and for security auditability. In practice, this includes capturing agent JSONL logs (including "thinking" traces), converting them to human-readable format, and uploading them as artifacts. Observability is a cross-cutting concern that touches every other component.
See [architecture.md](architecture.md).

### OTEL Derived Products

Values **computed from** a run's OpenTelemetry trace after the fact — scores, fitness checks, later quality signals. They are not a second copy of what happened. First-ship example: `eval-measurements.jsonl` from `fullsend eval-measure` ([eval measurements](#eval-measurement) are the concept of scoring traces). Derived products sit beside telemetry as sibling files; they never replace [OTEL primary facts](#otel-primary-facts).
See [ADR 0087](ADRs/0087-eval-measurements-online-trace-scoring.md) and [Eval Measurements](guides/infrastructure/eval-measurements.md).

### OTEL Primary Facts

What **actually happened** on an agent run, recorded as OpenTelemetry (OTEL) spans. The local source of truth is `run-telemetry.jsonl`; when `OTEL_EXPORTER_OTLP_*` is set, the same spans also export live over OTLP ([ADR 0050](ADRs/0050-distributed-tracing-instrumentation.md)). Agent identity, work item, tokens, cost, span tree, and `exit_code` belong here. Sibling files (including [eval measurements](#eval-measurement)) must not become a second source of run truth.
See [Distributed Tracing](guides/infrastructure/distributed-tracing.md) and [ADR 0087](ADRs/0087-eval-measurements-online-trace-scoring.md).

### On-demand Skill

A [skill](#skill) load mode: uploaded and listed for the run, but under the Claude Code runtime the full body loads only when the model opens it (Skill tool). Default when frontmatter omits `metadata.apply` / `apply`, or sets `on-demand` ([ADR 0092](ADRs/0092-always-on-harness-skills.md)). Contrast with [always-on skill](#always-on-skill).

## P

### Policy Store

Where agent behavioral rules live — autonomy levels, review requirements, allowed operations, and escalation rules. Policy is distinct from the harness (which configures *how* an agent works) and from intent (which defines *what* work is authorized). Policy defines the *boundaries* of agent behavior — what an agent is allowed to do regardless of what it's asked to do. The adopting organization's `.fullsend` repository is the natural home for policy configuration.
See [architecture.md](architecture.md) and [governance.md](problems/governance.md).

### Post-script

A host-side script declared as `post_script` in the harness. Runs **outside** the sandbox **after** the agent exits, with credentials, to apply untrusted agent output (labels, comments, pushes, board updates). Credential isolation depends on this boundary ([ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md)). Changing `post_script` on a default `base:` is a [script override](#script-override) and makes the agent [derived](#derived-agent) — skills cannot replace post-script API actions the stock script does not perform.
See [ADR 0024](ADRs/0024-harness-definitions.md) and [Default, derived, and custom agents](agents/topics/default-vs-custom.md).

### Pre-script

A host-side script declared as `pre_script` in the harness. Runs **outside** the sandbox **before** the agent starts, with credentials, to fetch inputs and write them into the workspace for the sandbox. Changing `pre_script` on a default `base:` is a [script override](#script-override) and makes the agent [derived](#derived-agent).
See [ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md), [ADR 0024](ADRs/0024-harness-definitions.md), and [Default, derived, and custom agents](agents/topics/default-vs-custom.md).

## R

### Ready to Code

A label indicating an issue has passed triage and is cleared for the code agent to begin work. It is a key transition point in the [label state machine](#label-state-machine) — the triage agent sets it after confirming the issue is not a duplicate, is reproducible (if applicable), is a bug (not a feature, unless features are in scope), and has sufficient detail for the code agent. The code agent watches for this label as its trigger to begin work.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md).

### Repo Skill

A [skill](#skill) committed under the target repo (typically `.agents/skills/`, often symlinked as `.claude/skills`). Under the Claude Code runtime, discovered for agents on that repo. Novel names are [additive](#additive-skill). A repo skill whose name matches a [built-in skill](#built-in-skill) is **shadowed**: built-ins upload to the personal-level config dir (`CLAUDE_CONFIG_DIR/skills/`), while repo skills stay at the project level (`.claude/skills/`); Claude Code's personal-over-project precedence silently ignores the repo copy — there is no bootstrap error. That silent shadowing is not a [skill override](#skill-override). Use a unique name, or replace intentionally via [base composition](#base-composition) (same basename on the child harness `skills:` list). With `runtime: pi`, project trust is disabled (`--no-approve` / `defaultProjectTrust: never`), so repo-committed skills under `.agents/skills` are not discovered at all — see [runtimes](runtimes.md).
See [Configuring with skills — Skill precedence](guides/user/customizing-with-skills.md#skill-precedence).

### Rework Rate

A quality metric measuring how many review-fix cycles a PR goes through before reaching approval. Visible on PRs when review happens post-submission (requested changes → fix → re-review). If review moves to a pre-PR inner loop, rework rate becomes harder to measure from the PR alone and must be extracted from agent logs.

## S

### Sandbox

The isolation boundary around a running agent. Responsible for filesystem access control and network regulation — ensuring an agent can only reach what it's authorized to reach and cannot affect other agents or systems outside its boundary. The sandbox is a **security primitive**, not the entire execution environment. Its job is containment: if an agent is compromised, the blast radius is limited to what the sandbox permits. Do not confuse with the broader execution environment (which also includes the harness and runtime). [NVIDIA/OpenShell](https://github.com/NVIDIA/OpenShell) is the current leading candidate for sandbox implementation.
See [architecture.md](architecture.md) and [security-threat-model.md](problems/security-threat-model.md).

### Sidecar

An external process running alongside (but outside) the agent's sandbox that mediates access to resources the sandbox cannot natively constrain. Example: an ephemeral Git server that receives `git push` from the agent and forwards it only to the one branch the agent is authorized to write to. Unlike an MCP server (which the agent explicitly calls as a tool), a sidecar can be transparent — the agent may not know it's interacting with a mediator rather than the real service.
See [architecture.md](architecture.md) and [#101](https://github.com/fullsend-ai/fullsend/issues/101).

### Script Override

Setting `pre_script` or `post_script` on a child harness so it **replaces** the base script. Under [base composition](#base-composition) these are scalars: override only — not additive concatenation (unlike unique skill names). Replacing scripts on a default `base:` yields a [derived agent](#derived-agent). Details: [ADR 0045](ADRs/0045-forge-portable-harness-schema.md), [Default, derived, and custom agents](agents/topics/default-vs-custom.md), [Configuring agents](guides/user/customizing-agents.md).

### Skill

A directory containing a `SKILL.md` file and optional companions (scripts, sub-agents, references, assets) that gives an agent scoped instructions for a task. Skills are assembled by the [harness](#harness). A skill may declare tools it is authorized to use; approving the skill implicitly authorizes those tools. Skills change *how* an agent reasons; they do not replace host-side [pre-script](#pre-script) / [post-script](#post-script) forge actions. Adding skills → usually [configured default](#configured-default-agent); see also [additive skill](#additive-skill), [skill override](#skill-override), [always-on](#always-on-skill), [on-demand](#on-demand-skill).
See [architecture.md](architecture.md), [codebase-context.md](problems/codebase-context.md), and [Configuring with skills](guides/user/customizing-with-skills.md).

### Skill Override

Intentionally **replacing** a [built-in skill](#built-in-skill) so the agent does not load the shipped version. Distinct from an [additive skill](#additive-skill) (new unique name). Under [base composition](#base-composition), `skills` merges with **deduplication by basename** — a child entry with the same basename overrides the base. Historically also done via `customized/skills/` ([ADR 0035](ADRs/0035-layered-content-resolution.md)), now deprecated ([ADR 0064](ADRs/0064-deprecate-customized-directory-overlay.md) / [Customized Directory](#customized-directory)). Classification: still [configured default](#configured-default-agent) when you only replace the skill (not `agent:` or scripts). Do not rely on a same-named [repo skill](#repo-skill) for override — that path is silently shadowed (see [Repo Skill](#repo-skill) / [Skill precedence](guides/user/customizing-with-skills.md#skill-precedence)). Fail-fast on duplicate basenames applies only when two harness-listed skills collide in `SkillDirs()`, not to repo-vs-built-in collisions.
See [Base composition](#base-composition), [ADR 0045](ADRs/0045-forge-portable-harness-schema.md), and [Default, derived, and custom agents](agents/topics/default-vs-custom.md).

### Stage

A higher-level workflow component in the fullsend pipeline (e.g., triage, code, review). The team formally chose "stage" over "phase" to avoid overloading the general SDLC use of "phase" and to maintain distinct vocabulary from Tekton's pipeline/task/step hierarchy, since fullsend may run on Tekton infrastructure. Each stage contains one or more [steps](#step).
See [ADR 0002](ADRs/0002-initial-fullsend-design.md).

### Step

A discrete unit of work within a [stage](#stage). For example, the triage stage may include steps for duplicate detection, reproducibility checking, and label assignment. "Stages and steps" is the agreed-upon workflow hierarchy for fullsend.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md).

### Slash Command

A GitHub comment in the form `/fs-triage`, `/fs-code`, `/fs-review`, etc., that manually triggers an agent workflow. The `/fs-` prefix namespaces fullsend commands to avoid collisions with other AI tools. Slash commands are parsed by the entry point and gated by an ACL — not every user can invoke every command. They provide an explicit human-initiated trigger alongside the automatic label-based triggers.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 2.

## T

### Trigger

What initiates an agent run. Could be a forge event (issue filed, label applied, comment posted, PR/MR opened, check completed), a [slash command](#slash-command), or a scheduled action. The term is used loosely in discussions — sometimes meaning the raw forge event (GitHub webhook or GitLab cron-polled change), sometimes meaning the processed signal that actually starts an agent after debouncing and validation. In fullsend's architecture, triggers flow through the [entry point](#entry-point), which normalizes and dispatches them.
See [architecture.md](architecture.md) (building block 1).

### Triage

In fullsend, triage means routing, deduplicating, assessing completeness, and checking reproducibility — **not** fixing. The triage agent reads the issue, determines if it is a duplicate, assesses whether it is a bug or a feature (and denies if features are not in scope), checks if the issue has enough detail for the code agent, and optionally attempts reproduction. The scope of triage has been a recurring discussion point, particularly around whether reproducibility and test generation belong in triage or implementation.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 4 and [#86](https://github.com/fullsend-ai/fullsend/issues/86).

### Trust

In fullsend, trust is not a single concept — it appears in at least three distinct contexts:

1. **Identity trust** — Can we verify who is making a request? Addressed by agent identities and GitHub App installations.
2. **Content trust** — Can we trust the content of inputs (issue bodies, comments, PR descriptions)? The answer is always **no** under the zero-trust model; all inputs are sanitized regardless of source.
3. **Execution trust** — Can we trust that an agent will do what it's supposed to? Addressed by sandboxing, scoped permissions, and the principle that trust derives from repository permissions, not agent identity.

The overloading of "trust" across these contexts has been a recurring source of confusion in design discussions.
See [security-threat-model.md](problems/security-threat-model.md) and [agent-architecture.md](problems/agent-architecture.md).

## W

### Work Coordinator

The mechanism that assigns work to agents and prevents conflicts. The existing design principle is that the **repo is the coordinator** — branch protection, CODEOWNERS, status checks, and forge events provide coordination without a central orchestrator. The work coordinator may be just the glue connecting forge events to agent infrastructure, or it may need to be more (e.g., a claim/lock system to prevent two code agents from picking up the same issue).
See [architecture.md](architecture.md) and [#77](https://github.com/fullsend-ai/fullsend/issues/77).

## Z

### Zero Trust

In fullsend's agent-to-agent model, zero trust means **nothing is trusted implicitly based on identity alone**. It does **not** mean "accept zero inputs" or "block everything." Every agent assumes every other agent — and every external input — could be compromised. The code agent assumes the triage output may contain prompt injection. The review agent assumes the submitted PR is designed to trick it. Defense is layered: input sanitization, scoped permissions, sandbox containment, and output validation all work together.
See [security-threat-model.md](problems/security-threat-model.md) (Threat 5) and [#102](https://github.com/fullsend-ai/fullsend/issues/102).
