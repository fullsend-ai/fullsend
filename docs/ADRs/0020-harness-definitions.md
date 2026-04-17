---
title: "0020. Harness definitions and shared directory layout"
status: Proposed
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - configuration
  - sandbox
---

# 0020. Harness definitions and shared directory layout

Date: 2026-04-07

## Status

Proposed

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
   during bootstrap (e.g. GCP service account JSON, CA certificates, env
   files). Paths may contain `${VAR}` references expanded from the host
   environment. When the `expand` flag is set, the file *content* is also
   expanded — this is how credential env files get their values resolved on
   the host before being delivered to the sandbox.
9. **Providers** — declarative definitions of OpenShell providers (e.g. Vertex
   AI) that the runner reconciles against the gateway before sandbox creation.
   This is how the sandbox gets LLM access.
10. **Required environment variables** — the harness declares which env vars it
    needs (names only). Values are provided by the CI workflow at runtime from
    org secrets and event context, never hardcoded in the harness. This makes
    the harness a **template** that the CI layer **instantiates**.
11. **Runner environment variables** — key-value pairs available to pre/post
    scripts and the validation loop on the host side. Values may reference host
    variables via `${VAR}` expansion. Distinct from `required_env` (which is a
    contract) — `runner_env` carries configuration for the runner's own scripts.
12. **Timeout** — a hard kill enforced by the runner.
13. **Validation loop** *(deferred — see Consequences)* — an optional
    deterministic script that checks agent output and re-runs the agent with
    feedback on failure. The full design (whether validation can invoke other
    agents, sandbox boundary, observability implications) is tracked as a
    separate ADR topic.

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
│  1. Validate required_env are present                     │
│  2. Run pre_script OUTSIDE sandbox                        │
│     (clone, checkout, gather context)                     │
│  3. Ensure providers on gateway (if declared)             │
│  4. Provision sandbox (--from image, or base; policy)     │
│  5. Start tool servers on host                            │
│  6. Bootstrap sandbox:                                    │
│     a. Copy agent definition, skills into sandbox         │
│     b. Copy host_files (expand ${VAR} in paths/content)   │
│     c. Copy tools_binaries (if no image)                  │
│  7. Copy project code / agent_input into sandbox          │
│  8. Launch agent runtime inside sandbox                   │
│  9. Wait for agent to exit (or timeout)                   │
│ 10. If validation_loop defined:                           │
│     a. Run validation script (on host)                    │
│     b. If non-zero, re-run agent with feedback appended   │
│     c. Repeat up to max_iterations                        │
│ 11. Extract output files and transcripts                  │
│ 12. Tear down sandbox and tool servers                    │
│ 13. Run post_script OUTSIDE sandbox                       │
│     (push, PR creation, label transitions)                │
└───────────────────────────────────────────────────────────┘
```

The runner is deterministic code, not an LLM. The agent is the LLM session.
Each harness invocation provisions one sandbox for one agent.

Multi-agent sequencing — for example, running a code agent then a review agent
with a gate — belongs in the CI pipeline definition (GitHub Actions, Tekton,
GitLab CI), not in the harness YAML. The runner's job is to run one agent well.

Note: the "one executable" inside the sandbox could be a shell script that
invokes Claude Code multiple times with different system prompts (e.g. a
code→review→code loop). From the sandbox's perspective this is one process.
From an observability perspective it produces multiple `.jsonl` transcripts,
which complicates features like `/ci:continue-claude`. This pattern is
supported but has trade-offs that should be weighed against CI-level
orchestration.

Tool provisioning has two paths. The preferred approach is a **pre-built
container image** — the agent owner bakes everything (agent runtime, tool
binaries, dependencies) into a single image, and the sandbox is created from
it via `openshell create --from <image>`. This is simpler and faster. When a
container image is not provided, the harness falls back to a **declared list
of tool binaries** (`tools_binaries`) with optional sha256 digests that the
runner fetches and copies into the sandbox before launch.

The harness definition is the input to harness assembly
([#173](https://github.com/fullsend-ai/fullsend/issues/173)). It connects to
`config.yaml` ([ADR 0011](../normative/admin-install/v1/adr-0011-org-config-yaml/SPEC.md)),
`agent-dispatch-v1.yaml` ([ADR 0012](../normative/admin-install/v1/adr-0012-fullsend-repo-files/SPEC.md)),
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

skills/             # Skill definitions (SKILL.md, following AgentSkills standard)
  triage-coordination/SKILL.md
  detect-duplicates/SKILL.md
  assess-completeness/SKILL.md
  code-implementation/SKILL.md
  testing-conventions/SKILL.md

tools/              # Binaries or pointers to downloadable binaries
  ruff/
  claude/

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
# Model is specified in the agent definition frontmatter, not here.
agent: agents/<agent>.md

# Pre-built container image for the sandbox. When provided, the sandbox is
# created via `openshell create --from <image>`, and tool binaries baked into
# the image do not need to be listed in tools_binaries. This is the preferred
# approach — it makes sandboxes self-contained and faster to provision.
image: <registry>/<image>:<tag>

# Full sandbox policy file covering network, filesystem, SSRF, process isolation.
# Start with OpenShell format; introduce a translation layer if backends change.
policy: policies/<policy>.yaml

# Skills to provision into the sandbox alongside the agent definition.
skills:
  - skills/<skill-name>

# Tool binaries or downloadable assets needed inside the sandbox.
# Used when no container image is provided, or for tools not baked into the
# image. sha256 digests should be checked when tools are fetched before launch.
tools_binaries:
  - name: <tool>
    source: PATH                    # or a URL to a downloadable binary
    sha256: <digest>                # verified before or after sandbox launch

# Files on the host to copy into the sandbox during bootstrap. Src paths may
# contain ${VAR} references expanded from the host environment. When expand
# is true, the file content is also expanded — use this for env files that
# contain variable references which must be resolved on the host (because the
# sandbox does not have those variables set). This is the mechanism for
# delivering credentials (e.g. GCP service account JSON) and resolved env
# files into the sandbox.
host_files:
  - src: <host-path-or-${VAR}>      # host path, supports ${VAR} expansion
    dest: <sandbox-path>             # destination inside the sandbox
    expand: false                    # expand ${VAR} in file content before copy

# OpenShell provider names required by this sandbox. The runner loads provider
# definitions from providers/ and reconciles them against the gateway before
# sandbox creation. This is how the sandbox gets LLM access (e.g. Vertex AI).
providers:
  - <provider-name>

# Host-side REST proxy servers spawned before the agent starts, torn down after.
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

# Environment variables this harness requires at runtime. The runner validates
# all listed variables are present in the host environment before launch.
# Values are provided by the CI workflow from org secrets, event context,
# etc. — never hardcoded here. This makes the harness a reusable template.
required_env:
  - <VAR_NAME>

# Key-value environment variables for host-side scripts (pre/post, validation).
# Values may reference host variables via ${VAR} expansion. Distinct from
# required_env (which is a contract the CI workflow must satisfy) — runner_env
# carries configuration for the runner's own processes.
runner_env:
  <KEY>: <value-or-${HOST_VAR}>

# Hard timeout enforced by the runner. The sandbox is killed after this.
timeout_minutes: 30
```

### Example: triage harness (with container image)

A single agent with a pre-built image, no code changes, no push, no PR:

```yaml
# harness/triage.yaml
description: Triage incoming issues — deduplicate, assess completeness, assign priority.
agent: agents/triage.md
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
workspace. The `host_files` entries deliver GCP credentials and a resolved
env file into the sandbox — the `expand: true` flag means `${VAR}` references
in the env file content are expanded from the host environment before copying,
so the sandbox receives concrete values without needing the host variables
set inside it.

### Example: code harness (with validation loop)

A code agent that writes code, validated by a deterministic lint/test script.
If validation fails, the agent re-runs with the failure output as context:

```yaml
# harness/code.yaml
description: Implement code changes from an issue, with lint/test validation.
agent: agents/code.md
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
provider endpoints. Since the image includes ruff and other tools pre-installed,
`tools_binaries` is not needed here.

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

**Trade-off: CI-layer logic must be maintained per platform.** Placing
orchestration in CI workflows means that multi-agent sequencing logic lives
in GitHub Actions YAML, Tekton pipeline definitions, GitLab CI configs, etc.
If fullsend supports multiple CI platforms, this logic must be duplicated or
a renderer must produce platform-specific workflows from an intermediate
representation. Ralph advocated for keeping the CI layer minimal — just the
trigger and a single `fullsend` command — with multi-agent orchestration
handled inside the entry point so it only needs to be written once. This ADR
takes the CI-layer position because CI platforms already provide the
sequencing primitives and the runner stays simpler, but the cross-platform
maintenance cost is a real concern that may force revisiting this if fullsend
expands beyond GitHub Actions.

The code→review pattern (run the code agent, then a review agent as a gate,
then loop back if review fails) can be expressed either through CI-level
sequencing of separate harness invocations or through the `validation_loop`
mechanism. The full design of `validation_loop` — whether it can invoke other
agents and where those agents run — is deferred to a separate ADR.

## Consequences

- **One harness, one agent, one sandbox.** The runner has a single
  responsibility: read a harness file and execute one agent in one sandbox.
- **Shared resources promote reuse.** Policies, skills, tools, and API servers
  live in their own directories and are referenced by multiple harnesses.
  Updating a shared policy updates every agent that uses it.
- **The runner resolves a harness by convention:** `fullsend run triage` reads
  `harness/triage.yaml`.
- **Pre/post scripts run outside the sandbox.** They handle privileged
  operations (push, PR creation) that the sandboxed agent cannot perform.
- **`validation_loop` enables structured retry.** After the agent exits, a
  validation script checks the output. Failed validation re-runs the agent with
  feedback appended.
- **`required_env` makes the harness a template.** The harness declares which
  environment variables it needs (names only). The CI workflow provides values
  from org secrets and event context. Different "instances" of the same agent
  are created by different CI workflows providing different secret values —
  not by duplicating harness files.
- **Credentials never live in the harness YAML.** Credentials are stored in
  org secrets (GitHub Actions secrets, Vault, etc.), injected by the CI
  workflow, validated by the runner via `required_env`, and delivered to the
  sandbox via `host_files` (for credential files like GCP service account
  JSON) or used on the host to start tool servers (REST API proxies that
  hold credentials outside the sandbox).
- **`host_files` is the credential delivery mechanism.** The runner copies
  files from the host into the sandbox during bootstrap. Paths support `${VAR}`
  expansion from the host environment, and the `expand` flag resolves variable
  references in file content before copying. This is how env files with
  credential values and service account keys reach the sandbox without the
  sandbox having direct access to host secrets.
- **`runner_env` complements `required_env`.** `required_env` is the
  contract — a list of env var names the CI workflow must provide.
  `runner_env` is key-value configuration for host-side scripts (pre/post,
  validation) that may reference host variables via `${VAR}` expansion. Both
  can coexist: `required_env` validates, `runner_env` configures.
- **Inheritance applies per-directory.** Each shared directory
  (policies/, skills/, etc.) follows the
  [ADR 0003](0003-org-config-repo-convention.md) layering
  (fullsend defaults → org `.fullsend` → per-repo) independently.
- **Model stays in the agent definition.** The harness YAML does not specify or
  override the model — that belongs in the agent `.md` frontmatter per the
  Claude sub-agent standard.
- **Multi-agent orchestration lives at the CI layer, not in the runner.** The
  runner's job is one harness, one agent, one sandbox. Sequencing multiple
  agents (e.g. code then review with a gate) is expressed in CI workflow
  definitions using the platform's native primitives (`needs:` in GitHub
  Actions, task dependencies in Tekton, etc.). This keeps the runner simple
  but means orchestration logic must be maintained per CI platform. If
  fullsend expands beyond GitHub Actions, this logic will need to be
  duplicated or generated from an intermediate representation — a concern
  raised during design review. An alternative (a single `fullsend` command
  that handles multi-agent orchestration internally) was proposed but deferred
  in favor of getting single-agent execution working first.
- **Pre-built container images are the preferred tool provisioning path.** The
  harness `image` field creates the sandbox from a container image via
  `openshell create --from <image>`, with tools, agent runtime, and
  dependencies baked in. When no image is provided, `tools_binaries` declares
  individual binaries the runner fetches and copies into the sandbox — this is
  the fallback path, not the default.
- A JSON Schema for the harness YAML format is a natural follow-on.

### Deferred to separate ADRs or issues

The following topics were discussed during the design of this ADR but
intentionally deferred to keep scope manageable:

- **Validation loop and code→review→code orchestration
  ([#234](https://github.com/fullsend-ai/fullsend/issues/234)).** The
  `validation_loop` field is included in the schema as an optional mechanism,
  but the full design — whether the validation script can invoke another agent
  (e.g. `fullsend run review`), whether that agent runs inside or outside the
  sandbox, and the transcript/observability implications — was set aside to
  focus on getting single-agent output working first. Both `main.sh` (a
  shell script inside the sandbox that invokes Claude Code multiple times) and
  `validation_loop` (a runner-level retry mechanism) were proposed; neither is
  fully specified here.
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
