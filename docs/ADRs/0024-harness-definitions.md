---
title: "24. Harness definitions and shared directory layout"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - harness
  - configuration
  - sandbox
  - security
---

# 24. Harness definitions and shared directory layout

Date: 2026-04-07

## Status

Accepted

## Context

Each agent invocation requires configuration that ties together several moving
parts:

1. **Which agent to run** — a single agent definition (`.md` file following the
   Claude sub-agent standard).
2. **Which sandbox policy to apply** — a full policy file covering network
   access (L4/L7), filesystem access, SSRF protection, and process isolation.
3. **Container image** *(optional)* — a pre-built image containing the agent
   runtime and tool binaries. The sandbox is created from this image via
   `openshell create --from <image>`, making sandboxes self-contained and
   faster to provision.
4. **Pre-script** — deterministic setup that runs **outside** the sandbox
   (clone, checkout, token generation, gathering data the sandbox cannot access).
5. **Post-script** — deterministic teardown that runs **outside** the sandbox
   for privileged operations agents must not perform (push, PR creation, label
   transitions).
6. **Skills** — skill definitions the agent needs, provisioned into the sandbox.
7. **Tool servers** — host-side REST proxy servers that hold credentials and
   enforce scoping (e.g. GitHub proxy, Jira proxy).
8. **Host files** — files on the host that must be copied into the sandbox
   during bootstrap (e.g. CA certificates, env files, configuration). Paths
   may contain `${VAR}` references expanded from the host environment. When
   the `expand` flag is set, the file *content* is also expanded — this is
   how env files with variable references get their values resolved on the
   host before being delivered to the sandbox. Credential delivery should
   use providers where possible; `host_files` is a workaround for auth flows
   incompatible with the provider placeholder model.
9. **Providers** — declarative definitions of
   [OpenShell providers](https://docs.nvidia.com/openshell/sandboxes/manage-providers).
   These protect credentials by exposing only placeholders to the agent and
   substituting the real values at runtime in headers, path params, or query
   params. For example, GitHub credentials can be injected this way. Note:
   some credential flows (e.g. Vertex AI API auth from Claude) generate
   credentials at runtime inside the agent process, which is incompatible
   with the provider placeholder model — see
   [vertex-auth-flow.md](https://github.com/fullsend-ai/experiments/blob/main/runner-hello-world/vertex-auth-flow.md)
   for details on this limitation.
10. **Required environment variables** *(planned — not yet implemented)* — the
    harness declares which env vars it needs (names only). Values are provided
    by the CI workflow at runtime from org secrets and event context, never
    hardcoded in the harness. This makes the harness a **template** that the CI
    layer **instantiates**. Currently, env var validation is handled in
    pre-scripts rather than by the runner.
11. **Runner environment variables** — key-value pairs available to pre/post
    scripts and the validation loop on the host side. Values may reference host
    variables via `${VAR}` expansion. Distinct from `required_env` (which is a
    contract) — `runner_env` carries configuration for the runner's own scripts.
12. **Timeout** — a hard kill enforced by the runner.
13. **Security scanning** — layered prompt injection defenses enforced by
    default and built into the `fullsend` CLI. Host-side scanners run before
    sandbox creation (context injection detection, SSRF validation, unicode
    normalization, secret redaction, ML-based LLM Guard). Sandbox-side
    pre/post-tool hooks are installed into the Claude configuration during
    bootstrap (Tirith terminal security, SSRF pre-tool checks, secret
    redaction post-tool). Omitting the `security` block enables all scanners
    with fail-closed semantics. Individual scanners can be toggled off
    per-harness, but there is no global kill switch.
14. **Validation loop** — an optional deterministic script that checks agent
    output and re-runs the same agent with feedback on failure. The current
    implementation supports re-running the previous agent with validation
    output appended; it does not support invoking a different agent (e.g. a
    review agent) as the validation step. Extending the loop to support
    cross-agent patterns (code→review→code) is tracked in
    [#234](https://github.com/fullsend-ai/fullsend/issues/234).

Today these are scattered across workflow files, CLI arguments, and unspecified
conventions. There is no single file — a **harness definition** — that ties
everything together for one agent invocation.

A **runner** (part of the entry point from
[#125](https://github.com/fullsend-ai/fullsend/issues/125)) reads the harness
definition and executes a deterministic sequence:

```
┌───────────────────────────────────────────────────────────┐
│  Runner reads harness/triage.yaml                         │
├───────────────────────────────────────────────────────────┤
│  1. Run pre_script OUTSIDE sandbox                        │
│     (clone, checkout, gather context, validate env)       │
│  2. Ensure providers on gateway (if declared)             │
│  3. Provision sandbox (--from image; apply policy)        │
│  4. Run security host scanners on context                 │
│  5. Bootstrap sandbox:                                    │
│     a. Copy agent definition, skills into sandbox         │
│     b. Copy host_files (expand ${VAR} in paths/content)   │
│     c. Install sandbox security hooks (Claude config)     │
│  6. Copy project code / agent_input into target dir       │
│  7. Launch Claude Code session inside sandbox              │
│  8. Wait for agent to exit (or timeout)                   │
│  9. If validation_loop defined:                           │
│     a. Run validation script (on host)                    │
│     b. If non-zero, re-run agent with feedback appended   │
│     c. Repeat up to max_iterations                        │
│ 10. Extract output files and transcripts                  │
│ 11. Tear down sandbox                                     │
│ 12. Run post_script OUTSIDE sandbox                       │
│     (push, PR creation, label transitions)                │
└───────────────────────────────────────────────────────────┘
```

The runner is deterministic code, not an LLM. The agent is the LLM session.
Each harness invocation provisions one sandbox for one agent — consistent with
the composable single-responsibility agent model
([ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md)),
where each step within a stage gets its own agent, its own harness, and its own
sandbox with policies designed for that step's responsibility.

Multi-agent sequencing — for example, running a code agent then a review agent
with a gate — belongs in the CI pipeline definition (GitHub Actions, Tekton,
GitLab CI), not in the harness YAML. The runner's job is to run one agent well.

The `fullsend run` command launches a single Claude Code session inside the
sandbox using the agent definition and model specified in the harness. The
runner controls the exact invocation — there is no user-supplied "main script"
inside the sandbox. JSONL reasoning traces produced by the agent session are
extracted after completion per the policy in
[ADR 0021](0021-jsonl-reasoning-trace-exposure.md).

Tool provisioning uses **pre-built container images** — the agent owner bakes
everything (agent runtime, tool binaries, dependencies) into a single image,
and the sandbox is created from it via `openshell create --from <image>`.
Additional files can be delivered into the sandbox via `host_files`.

The harness definition is the input to harness assembly
([#173](https://github.com/fullsend-ai/fullsend/issues/173)). It connects to
`config.yaml` ([ADR 0011](0011-admin-install-org-config-yaml-v1.md)),
`agent-dispatch-v1.yaml` ([ADR 0012](0012-admin-install-fullsend-repo-files-v1.md)),
and the agent wrapper concept ([#101](https://github.com/fullsend-ai/fullsend/issues/101)).

[PR #231](https://github.com/fullsend-ai/fullsend/pull/231) implements the
`fullsend run` command that reads harness definitions, provisions OpenShell
sandboxes, and executes agents. That implementation explored several design
choices that informed this schema — notably the `image`, `host_files`,
`providers`, and `runner_env` fields, which were proven necessary during
end-to-end experimentation. Where the implementation and this ADR diverge,
the ADR captures the team-agreed design direction; where the implementation
introduced fields the ADR had not considered, those fields have been
incorporated here.

## Options

### Option A: Per-agent directories with co-located files

```
harness/
  triage/
    deduplicator/
      deduplicator.yaml
      deduplicator.md
      fetch-issue.sh
    complete-assessment/
      complete-assessment.yaml
      complete-assessment.md
      fetch-issue-deep.sh
    priority-assessment/
      priority-assessment.yaml
      priority-assessment.md
      fetch-issue-deep.sh
    triage-summary/
      triage-summary.yaml
      triage-summary.md
      gather-triage-output.sh
      push-to-issue.sh
  code/
    coder/
      coder.yaml
      coder.md
      fetch-issue-for-code.sh
      linter.sh
      push-to-PR.sh
```

Everything about an agent — its harness config, agent definition, and
scripts — lives in one directory. Optionally grouped by stage (triage/,
code/, review/) for navigability.

**Pros:**
- `ls harness/triage/deduplicator/` shows everything about that agent — high
  discoverability.
- Script paths are relative to the agent directory, simplifying co-location.
- Multiple agents per stage are visually obvious.

**Cons:**
- Resources that should be shared (policies, skills, tools) are duplicated
  across agent directories or require awkward cross-references.
- Adding a new shared resource (e.g. a tool server or policy) means touching
  every agent directory that uses it.
- Agent definitions and scripts cannot be reused across agents without
  copying or symlinking.

### Option B: Shared directories with per-agent harness files

```
policies/           # Sandbox policy files (OpenShell format)
  readonly.yaml
  readonly-with-web.yaml
  triage-write.yaml
  code-write.yaml

agents/             # Agent definitions (.md, following Claude standard)
  deduplicator.md
  completeness-assessor.md
  priority-evaluator.md
  triage-summary.md
  coder.md
  arch-reviewer.md
  docs-reviewer.md

skills/             # Skill directories (each contains SKILL.md + companion files)
  triage-coordination/
  detect-duplicates/
  assess-completeness/
  code-implementation/
  testing-conventions/

env/                # Environment files delivered into the sandbox
  gcp-vertex.env    # May contain ${VAR} references expanded at bootstrap
  repo.env

providers/          # OpenShell provider definitions (LLM access, etc.)
  vertex-ai.yaml

api-servers/        # REST tool server implementations (credential proxies)
  gh-server/
  jira-server/

scripts/            # Pre/post scripts, validation scripts
  fetch-issue.sh
  fetch-issue-deep.sh
  gather-triage-output.sh
  push-to-issue.sh
  fetch-issue-for-code.sh
  push-to-pr.sh
  validate-lint.sh

harness/            # Per-agent harness configs — the glue
  deduplicator.yaml
  completeness-assessor.yaml
  priority-evaluator.yaml
  triage-summary.yaml
  coder.yaml
  arch-reviewer.yaml
  docs-reviewer.yaml
```

Each `harness/<agent>.yaml` is the single file the runner reads. It references
shared resources from the directories above. Multiple harnesses can share the
same policy, skills, tools, or API servers without duplication.

**Pros:**
- Reuse is natural: multiple agents share the same policy, skills, tools, or
  API servers by reference.
- The runner stays simple: `fullsend run triage` reads `harness/triage.yaml`
  and provisions everything it references.
- Each concern lives in one place: policies are reviewed in `policies/`, skills
  in `skills/`, etc. — not scattered across per-stage directories.
- Inheritance from [ADR 0003](0003-org-config-repo-convention.md) applies to
  each directory independently.

**Cons:**
- Understanding a single agent requires reading `harness/<agent>.yaml` to find
  references across multiple directories — lower discoverability compared to
  Option A.
- More directories at the top level.

### Option C: Inline in `config.yaml`

All harness definitions under a `harness:` key in `config.yaml`. Single source
of truth, but the file grows large and per-repo overrides require replacing the
entire harness block.

## Decision

Adopt shared directories with per-agent harness files (Option B). The harness
definition is the core unit: one YAML file that tells the runner everything it
needs to provision a sandbox and launch one agent.

The inheritance model from [ADR 0003](0003-org-config-repo-convention.md)
applies at the directory and file level: fullsend ships defaults, the org
`.fullsend` repo can overlay or add resources in any directory, and per-repo
`.fullsend/` can override individual files.

### Harness YAML schema

```yaml
# harness/<agent>.yaml

# Human-readable description of what this harness does.
description: <text>

# The agent definition file (Claude sub-agent standard .md with frontmatter).
agent: agents/<agent>.md

# Optional model override. When set, this takes precedence over the model
# declared in the agent definition's frontmatter. This allows the same agent
# definition to be reused across harnesses targeting different cost/latency
# profiles (e.g. opus for code, haiku for triage) without duplicating the
# agent .md file. When omitted, the model from the agent definition is used.
model: <model-name>

# Pre-built container image for the sandbox. When provided, the sandbox is
# created via `openshell create --from <image>`, with tool binaries and
# dependencies baked in. This is the preferred approach — it makes sandboxes
# self-contained and faster to provision.
image: <registry>/<image>:<tag>

# Full sandbox policy file covering network, filesystem, SSRF, process isolation.
# Start with OpenShell format; introduce a translation layer if backends change.
policy: policies/<policy>.yaml

# Skills to provision into the sandbox alongside the agent definition.
skills:
  - skills/<skill-name>

# Files on the host to copy into the sandbox during bootstrap. Primarily for
# configuration (env files, CA certs). Src paths may contain ${VAR} references
# expanded from the host environment. When expand is true, the file content is
# also expanded — use this for env files that contain variable references which
# must be resolved on the host. Credential delivery should use providers where
# possible; host_files is a workaround for auth flows incompatible with the
# provider placeholder model (e.g. GCP service account JSON for Vertex AI).
host_files:
  - src: <host-path-or-${VAR}>      # host path, supports ${VAR} expansion
    dest: <sandbox-path>             # destination inside the sandbox
    expand: false                    # expand ${VAR} in file content before copy

# OpenShell provider names required by this sandbox. The runner loads provider
# definitions from providers/ and reconciles them against the gateway before
# sandbox creation. This is how the sandbox gets LLM access (e.g. Vertex AI).
providers:
  - <provider-name>

# [PLANNED — not yet implemented] Host-side REST proxy servers spawned before
# the agent starts, torn down after. The Harness struct parses and validates
# this field, but no runtime code in fullsend currently starts, manages, or
# tears down API servers. When implemented, ${HOST_VAR} expansion in env and
# per-run bearer token authentication (per ADR-0017) will need careful scoping.
api_servers:
  - name: <server-name>
    script: api-servers/<server>/<script>
    port: <port>
    env:
      <VAR>: <value-or-${HOST_VAR}>

# Scripts that run OUTSIDE the sandbox, before and after the agent.
pre_script: scripts/<pre>.sh
post_script: scripts/<post>.sh

# Additional input files copied into the sandbox for the agent to consume.
agent_input: <directory>

# Optional validation loop (design details deferred to a separate ADR).
# After the agent exits, the runner executes the validation script on the host.
# If it exits non-zero, the agent re-runs with the script's stdout/stderr
# appended as additional context. Whether the validation script may invoke
# another agent, and where that agent runs, is an open question.
validation_loop:
  script: scripts/<validate>.sh     # exit 0 = pass, non-zero = retry
  max_iterations: 3                 # how many times the agent can retry
  feedback_mode: append             # append validation output to agent prompt

# [PLANNED — not yet implemented] Environment variables this harness requires
# at runtime. When implemented, the runner will validate all listed variables
# are present in the host environment before launch. Values are provided by
# the CI workflow from org secrets, event context, etc. — never hardcoded
# here. Currently, env var validation is handled in pre-scripts.
# required_env:
#   - <VAR_NAME>

# Key-value environment variables for host-side scripts (pre/post, validation).
# Values may reference host variables via ${VAR} expansion. Distinct from
# required_env (which is a contract the CI workflow must satisfy) — runner_env
# carries configuration for the runner's own processes.
runner_env:
  <KEY>: <value-or-${HOST_VAR}>

# Hard timeout enforced by the runner. The sandbox is killed after this.
timeout_minutes: 30

# Security scanning configuration. Controls layered prompt injection defenses
# enforced by default. Host-side scanners run before sandbox creation; sandbox-
# side pre/post-tool hooks are installed into the Claude configuration during
# bootstrap. Omitting this block enables all scanners with fail_mode: closed.
# There is no global kill switch — individual scanners can be toggled off, but
# security cannot be disabled wholesale. If a full bypass is ever needed, it
# should require an org-level override with audit logging, not a repo-level flag.
security:
  fail_mode: closed                  # "closed" (default) or "open"
  host_scanners:
    unicode_normalizer: true
    context_injection: true
    ssrf_validator: true
    secret_redactor: true
    llm_guard:
      enabled: true
      threshold: 0.92
      match_type: sentence           # "sentence" or "full"
  sandbox_hooks:
    tirith:
      enabled: true
    ssrf_pretool: true
    secret_redact_posttool: true

# Remote resource access (ADR-0038). URL-prefix allowlist for skills, agents,
# and policies fetched from HTTPS endpoints with SHA256 integrity verification.
allowed_remote_resources:
  - https://example.com/skills/
  - https://example.com/policies/

# Opt-in to runtime skill fetching. When true, the runner starts a fetch
# service that agents can call mid-run via `fullsend fetch-skill`. Requires
# at least one entry in allowed_remote_resources. Default: false.
allow_runtime_fetch: true

# Maximum number of runtime fetch requests per agent run. Requires
# allow_runtime_fetch to be true. When omitted, uses the default (10).
# Must be between 1 and 1000.
max_runtime_fetches: 10
```

### Example: triage harness (with container image)

A single agent with a pre-built image, no code changes, no push, no PR:

```yaml
# harness/triage.yaml
description: Triage incoming issues — deduplicate, assess completeness, assign priority.
agent: agents/triage.md
model: opus
image: quay.io/fullsend/triage-agent:latest
policy: policies/readonly-with-web.yaml

skills:
  - skills/triage-coordination
  - skills/detect-duplicates

providers:
  - vertex-ai

host_files:
  - src: env/gcp-vertex.env
    dest: /tmp/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/workspace/.gcp-credentials.json

api_servers:
  - name: github-proxy
    script: api-servers/gh-server/gh_server.py
    port: 8081
    env:
      GH_TOKEN: ${GH_TOKEN}

pre_script: scripts/triage-pre.sh
post_script: scripts/triage-post.sh

required_env:
  - GH_TOKEN
  - GOOGLE_APPLICATION_CREDENTIALS
  - ANTHROPIC_VERTEX_PROJECT_ID
  - CLOUD_ML_REGION
  - ISSUE_NUMBER
  - REPO_FULL_NAME

timeout_minutes: 30
```

The triage agent's policy (`readonly-with-web.yaml`) allows outbound HTTPS to
the model provider and the GitHub proxy, but no filesystem writes outside the
workspace. The `host_files` entries deliver configuration (env files) and GCP
credentials into the sandbox — the GCP credentials use `host_files` as a
workaround because Vertex AI's auth flow is incompatible with the provider
placeholder model. The `expand: true` flag means `${VAR}` references in the
env file content are expanded from the host environment before copying, so
the sandbox receives concrete values without needing the host variables set
inside it.

### Example: code harness (with validation loop)

A code agent that writes code, validated by a deterministic lint/test script.
If validation fails, the agent re-runs with the failure output as context:

```yaml
# harness/code.yaml
description: Implement code changes from an issue, with lint/test validation.
agent: agents/code.md
model: opus
image: quay.io/fullsend/code-agent:latest
policy: policies/code-write.yaml

skills:
  - skills/code-implementation
  - skills/testing-conventions

providers:
  - vertex-ai

host_files:
  - src: env/gcp-vertex.env
    dest: /tmp/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/workspace/.gcp-credentials.json

api_servers:
  - name: github-proxy
    script: api-servers/gh-server/gh_server.py
    port: 8081
    env:
      GH_TOKEN: ${GH_TOKEN}

pre_script: scripts/code-pre.sh
post_script: scripts/code-post.sh

validation_loop:
  script: scripts/validate-lint.sh
  max_iterations: 3
  feedback_mode: append

required_env:
  - GH_TOKEN
  - GOOGLE_APPLICATION_CREDENTIALS
  - ANTHROPIC_VERTEX_PROJECT_ID
  - CLOUD_ML_REGION
  - BRANCH_NAME

runner_env:
  VALIDATION_EXPECTED_FAILURES: "0"

timeout_minutes: 120
```

The code harness's policy (`code-write.yaml`) would include repo-specific
egress (e.g. `pypi.org`, `proxy.golang.org`) alongside the baseline model
provider endpoints. All tools (ruff, etc.) are pre-installed in the container
image.

### Template instantiation: same harness, different workflows

A harness is a **template**. To create different "instances" of the same agent
with different credentials or context, create separate CI workflows that
provide different values for the same `required_env` variables. You do not
need multiple harness files for the same agent — one harness, multiple
workflows:

```yaml
# .github/workflows/triage-security.yaml
name: Triage Security Issues
on:
  issues:
    types: [opened, edited]
env:
  GH_TOKEN: ${{ secrets.SECURITY_GH_TOKEN }}
  JIRA_TOKEN: ${{ secrets.SECURITY_JIRA_TOKEN }}
  ISSUE_NUMBER: ${{ github.event.issue.number }}
  REPO_FULL_NAME: ${{ github.repository }}
steps:
  - run: fullsend run triage

# .github/workflows/triage-backlog.yaml
name: Triage Backlog Issues
on:
  issues:
    types: [opened, edited]
env:
  GH_TOKEN: ${{ secrets.BACKLOG_GH_TOKEN }}
  JIRA_TOKEN: ${{ secrets.BACKLOG_JIRA_TOKEN }}
  ISSUE_NUMBER: ${{ github.event.issue.number }}
  REPO_FULL_NAME: ${{ github.repository }}
steps:
  - run: fullsend run triage
```

Same harness, same agent definition, different secret values. Duplicating a
harness to get multiple configurations is also supported — but even then,
each harness needs a CI workflow to provide its required environment variables.

### Multi-agent composition at the CI layer

The harness is the unit of execution for one agent. Multi-agent patterns —
for example, running a deduplicator then a completeness assessor then a
priority evaluator during triage — are expressed at the CI layer, not in the
harness YAML. Each agent runs in its own sandbox with its own policy:

```yaml
# .github/workflows/triage-pipeline.yaml
name: Triage Pipeline
on:
  issues:
    types: [opened]
jobs:
  deduplicate:
    runs-on: ubuntu-latest
    steps:
      - run: fullsend run deduplicator
  assess-completeness:
    needs: deduplicate
    runs-on: ubuntu-latest
    steps:
      - run: fullsend run completeness-assessor
  assign-priority:
    needs: assess-completeness
    runs-on: ubuntu-latest
    steps:
      - run: fullsend run priority-evaluator
```

This keeps the runner simple (one agent, one sandbox), gives each agent its
own security boundary, and lets the CI platform handle sequencing, parallelism,
and conditional execution.

The code→review pattern (run the code agent, then a review agent as a gate,
then loop back if review fails) can be expressed through CI-level sequencing
of separate harness invocations. The `validation_loop` mechanism supports
deterministic re-runs of the same agent but does not currently support
invoking a different agent as the validation step — extending this is tracked
in [#234](https://github.com/fullsend-ai/fullsend/issues/234).

## Consequences

- **One harness, one agent, one sandbox.** The runner has a single
  responsibility: read a harness file and execute one agent in one sandbox.
  This is the execution-level realization of the composable
  single-responsibility agent model
  ([ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md))
  and the credential isolation principle
  ([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)). JSONL
  reasoning traces extracted from the sandbox (step 11) follow the
  owner-scoped storage and credential scanning policy established by
  [ADR 0021](0021-jsonl-reasoning-trace-exposure.md).
- **Shared resources promote reuse.** Policies, skills, tools, and API servers
  live in their own directories and are referenced by multiple harnesses.
  Updating a shared policy updates every agent that uses it.
- **The runner resolves a harness by convention:** `fullsend run triage` reads
  `harness/triage.yaml`.
- **Pre/post scripts run outside the sandbox.** They handle privileged
  operations (push, PR creation) that the sandboxed agent cannot perform.
- **`validation_loop` enables structured retry.** After the agent exits, a
  deterministic validation script checks the output. Failed validation re-runs
  the same agent with the validation output appended as context. The current
  implementation does not support invoking a different agent as the validation
  step — extending this to cross-agent patterns is tracked in
  [#234](https://github.com/fullsend-ai/fullsend/issues/234).
- **`required_env` makes the harness a template** *(planned — not yet
  implemented).* The harness declares which environment variables it needs
  (names only). The CI workflow provides values from org secrets and event
  context. Different "instances" of the same agent are created by different CI
  workflows providing different secret values — not by duplicating harness
  files. Currently, env var validation is handled in pre-scripts.
- **Credentials never live in the harness YAML.** Credentials are stored in
  org secrets (GitHub Actions secrets, Vault, etc.) and injected by the CI
  workflow. The primary credential delivery mechanism is **providers** —
  OpenShell providers expose only placeholders to the agent and substitute
  real values at runtime, keeping credentials outside the sandbox. When
  provider limitations prevent this (e.g. Vertex AI API auth flow), `host_files`
  serves as a workaround to deliver credential files (like GCP service account
  JSON) into the sandbox.
- **`host_files` is for configuration delivery.** The runner copies files from
  the host into the sandbox during bootstrap. Paths support `${VAR}` expansion
  from the host environment, and the `expand` flag resolves variable references
  in file content before copying. The primary use case is env files and
  configuration; credential delivery via `host_files` is a workaround for
  cases where providers cannot handle the auth flow.
- **`runner_env` complements `required_env`.** `required_env` is the
  contract — a list of env var names the CI workflow must provide.
  `runner_env` is key-value configuration for host-side scripts (pre/post,
  validation) that may reference host variables via `${VAR}` expansion. Both
  can coexist: `required_env` validates, `runner_env` configures.
- **Inheritance applies per-directory.** Each shared directory
  (policies/, skills/, etc.) follows the
  [ADR 0003](0003-org-config-repo-convention.md) layering
  (fullsend defaults → org `.fullsend` → per-repo) independently.
- **Model default lives in the agent definition; harness can override.** The
  agent `.md` frontmatter declares the default model per the Claude sub-agent
  standard. The harness `model` field is an optional override — when set, it
  takes precedence. This lets a single agent definition be reused across
  harnesses targeting different cost/latency profiles without duplication.
- **Multi-agent orchestration lives at the CI layer, not in the runner.** The
  runner's job is one harness, one agent, one sandbox. Sequencing multiple
  agents (e.g. code then review with a gate) is expressed in CI workflow
  definitions using the platform's native primitives (`needs:` in GitHub
  Actions, task dependencies in Tekton, etc.). This keeps the runner simple
  but means orchestration logic must be maintained per CI platform.
- **CI-layer orchestration has a cross-platform maintenance cost.** Placing
  multi-agent sequencing in CI workflows means that logic must be duplicated
  or a renderer must produce platform-specific workflows from an intermediate
  representation if fullsend expands beyond GitHub Actions. An alternative (a
  single `fullsend` command that handles multi-agent orchestration internally)
  was proposed during design review but deferred in favor of getting
  single-agent execution working first. This trade-off may force revisiting
  the approach if multi-platform support becomes a requirement.
- **Pre-built container images are the tool provisioning path.** The harness
  `image` field creates the sandbox from a container image via
  `openshell create --from <image>`, with tools, agent runtime, and
  dependencies baked in. Additional files can be delivered via `host_files`.
  A `tools_binaries` field for declaring individual downloadable binaries
  was considered during design but is not supported — container images plus
  `host_files` cover the delivery use cases without the complexity of
  per-binary download, verification, and TOCTOU concerns.
- **Security scanning is per-harness, enforced by default, with no global kill
  switch.** Layered prompt injection defenses — host-side scanners (unicode
  normalization, context injection detection, SSRF validation, secret
  redaction, ML-based LLM Guard) and sandbox-side hooks (Tirith terminal
  security, SSRF pre-tool, secret redaction post-tool) — are built into the
  `fullsend` CLI and enabled with fail-closed semantics by default. Individual
  scanners can be toggled off per-harness for development, but there is no
  global `enabled: false` switch. If a full bypass is ever needed, it should
  require an org-level override with audit logging. The inheritance model
  applies: org-level security settings provide the baseline, and per-repo
  overrides are subject to the protected-fields policy
  ([#236](https://github.com/fullsend-ai/fullsend/issues/236)).
- A JSON Schema for the harness YAML format is a natural follow-on.

### Deferred to separate ADRs or issues

The following topics were discussed during the design of this ADR but
intentionally deferred to keep scope manageable:

- **Cross-agent validation and code→review→code orchestration
  ([#234](https://github.com/fullsend-ai/fullsend/issues/234)).** The basic
  `validation_loop` (deterministic script reruns the same agent) is
  implemented. The open design question is whether the validation step can
  invoke a *different* agent (e.g. `fullsend run review` as the validator
  for a code harness), where that agent runs, and the transcript/observability
  implications.
- **Schema versioning for harness definitions
  ([#235](https://github.com/fullsend-ai/fullsend/issues/235)).** Harness YAML
  files may need a `version` field for schema evolution (e.g. when fields are
  renamed or restructured). The team agreed this is important but not
  blocking — failed validation can surface schema drift without a version
  field.
- **Protected vs. freely overridable fields
  ([#236](https://github.com/fullsend-ai/fullsend/issues/236)).** At each
  inheritance layer (fullsend defaults → org `.fullsend` → per-repo), which
  fields can be overridden? Policy rules should likely be additive only (repos
  cannot weaken org-level policies). Skills might be additive. Timeout and env
  might be freely overridable.

### Open questions

- **Skills loading policy
  ([#237](https://github.com/fullsend-ai/fullsend/issues/237)).** The harness
  declares an explicit `skills:` list, but how does this interact with
  org-level and repo-level skills?

  *Approach A (explicit + org, opt-in repo):* The harness `skills:` list is
  always loaded. Org-level skills from `.fullsend/skills/` are always included
  (org-controlled, trusted). Repo-level skills from the target repo are **not**
  auto-loaded by default due to prompt injection risk
  ([#48](https://github.com/fullsend-ai/fullsend/pull/48)). To opt in, the
  harness or org config declares `allow_repo_skills: true`.

  *Approach B (all skills with scanning):* Fullsend-provided skills are
  installed in a released/versioned format. Org-level and repo-level skills are
  both available by default but scanned for injection risks at a preparation
  step before the agent launches. Repo-level skills are important for domain
  knowledge (e.g. quirks about a specific repo). Disabling repo skills would
  be the exception, not the default.

  The team has not reached consensus. Approach A is more conservative (secure
  by default, opt-in to risk). Approach B prioritizes agent effectiveness
  (skills are lazy-loaded by the agent, scanning provides the guard).
  Related: the skill installation mechanism (copy into sandbox vs.
  `claude plugin install` vs. agent-native format) also needs resolution.

- **Overridable content beyond skills.** Can users at the repo level introduce
  new agent definitions, new env requirements, new images or tools? This
  intersects with the protected-fields question above and may need its own
  design once the initial set of harness-managed resources stabilizes.
- **Provider management.** Provider definitions in `providers/` need lifecycle
  management — who creates them, when are they reconciled against the gateway,
  and how do they relate to the org-level OpenShell gateway configuration? The
  current model is that the runner reconciles declared providers before sandbox
  creation, but the detail of provider schema and gateway interaction is not
  yet specified.
