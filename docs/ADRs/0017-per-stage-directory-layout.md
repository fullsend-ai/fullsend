---
title: "0017. Harness definitions and shared directory layout"
status: Proposed
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - configuration
  - sandbox
---

# 0017. Harness definitions and shared directory layout

Date: 2026-04-07

## Status

Proposed

<!-- Once this ADR is Accepted, its content is frozen. Do not edit the Context,
     Decision, or Consequences sections. If circumstances change, write a new
     ADR that supersedes this one. Only status changes and links to superseding
     ADRs should be added after acceptance. -->

## Context

Each agent invocation requires configuration that ties together several moving
parts:

1. **Which agent to run** — a single agent definition (`.md` file following the
   Claude sub-agent standard).
2. **Which sandbox policy to apply** — a full policy file covering network
   access (L4/L7), filesystem access, SSRF protection, and process isolation.
3. **Pre-script** — deterministic setup that runs **outside** the sandbox
   (clone, checkout, token generation, gathering data the sandbox cannot access).
4. **Post-script** — deterministic teardown that runs **outside** the sandbox
   for privileged operations agents must not perform (push, PR creation, label
   transitions).
5. **Skills** — skill definitions the agent needs, provisioned into the sandbox.
6. **Tool servers** — host-side REST proxy servers that hold credentials and
   enforce scoping (e.g. GitHub proxy, Jira proxy).
7. **Environment variables** — available to scripts and the agent runtime.
8. **Timeout** — a hard kill enforced by the runner.
9. **Validation loop** — an optional deterministic script that checks agent
   output and re-runs the agent with feedback on failure.

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
│     (clone, checkout, gather context)                     │
│  2. Provision sandbox (policy, mounts)                    │
│  3. Start tool servers on host                            │
│  4. Copy agent definition, skills, tools into sandbox     │
│  5. Launch agent runtime inside sandbox                   │
│  6. Wait for agent to exit (or timeout)                   │
│  7. If validation_loop defined:                           │
│     a. Run validation script                              │
│     b. If non-zero, re-run agent with feedback appended   │
│     c. Repeat up to max_iterations                        │
│  8. Tear down sandbox and tool servers                    │
│  9. Run post_script OUTSIDE sandbox                       │
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

The harness definition is the input to harness assembly
([#173](https://github.com/fullsend-ai/fullsend/issues/173)). It connects to
`config.yaml` ([ADR 0011](../normative/admin-install/v1/adr-0011-org-config-yaml/SPEC.md)),
`agent-dispatch-v1.yaml` ([ADR 0012](../normative/admin-install/v1/adr-0012-fullsend-repo-files/SPEC.md)),
and the agent wrapper concept ([#101](https://github.com/fullsend-ai/fullsend/issues/101)).

## Options

### Option A: Per-stage directories with co-located files

```
stages/
  code/
    code.yaml
    firewall.yaml
    code-pre.sh
    code-post.sh
  triage/
    triage.yaml
    triage-pre.sh
    triage-post.sh
```

Everything about a stage is in one directory. The stage YAML includes a key
pointing to companion files (e.g. `sandbox.firewall_file: firewall.yaml`).
Layered overrides target individual files within the directory.

**Pros:**
- `ls stages/code/` shows everything about the code stage — high
  discoverability.
- Script paths are relative to the stage directory, simplifying co-location.

**Cons:**
- Resources that should be shared (policies, skills, tools) are duplicated
  across stage directories or require awkward cross-references.
- The layout is stage-centric, but the unit of execution is a single agent.
  Moving to one-agent-per-sandbox makes "stage" an overloaded term.
- Adding a new shared resource (e.g. a tool server) means touching every
  stage directory that uses it.

### Option B: Shared directories with per-agent harness files

```
policies/           # Sandbox policy files (OpenShell format)
  readonly.yaml
  readonly-with-web.yaml
  triage-write.yaml
  code-write.yaml

agents/             # Agent definitions (.md, following Claude standard)
  triage.md
  code.md
  review.md

skills/             # Skill definitions (SKILL.md, following AgentSkills standard)
  triage-coordination/SKILL.md
  detect-duplicates/SKILL.md
  assess-completeness/SKILL.md

tools/              # Binaries or pointers to downloadable binaries
  ruff/
  claude/

api-servers/        # REST tool server implementations (credential proxies)
  gh-server/
  jira-server/

scripts/            # Pre/post scripts, validation scripts
  triage-pre.sh
  triage-post.sh
  code-pre.sh
  code-post.sh
  validate-lint.sh

harness/            # Per-agent harness configs — the glue
  triage.yaml
  code.yaml
  review.yaml
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

# The agent definition file (Claude sub-agent standard .md with frontmatter).
# Model is specified in the agent definition frontmatter, not here.
agent: agents/<agent>.md

# Full sandbox policy file covering network, filesystem, SSRF, process isolation.
# Start with OpenShell format; introduce a translation layer if backends change.
policy: policies/<policy>.yaml

# Skills to provision into the sandbox alongside the agent definition.
skills:
  - skills/<skill-name>

# Tool binaries or downloadable assets needed inside the sandbox.
# When tools are fetched before launch, sha256 digests should be checked.
tools_binaries:
  - name: <tool>
    source: PATH                    # or a URL to a downloadable binary
    sha256: <digest>                # verified before or after sandbox launch

# Host-side REST proxy servers spawned before the agent starts, torn down after.
api_servers:
  - name: <server-name>
    script: api-servers/<server>/<script>
    port: <port>
    env:
      <VAR>: ${{secrets.<SECRET>}}

# Scripts that run OUTSIDE the sandbox, before and after the agent.
pre_script: scripts/<pre>.sh
post_script: scripts/<post>.sh

# Optional validation loop. After the agent exits, the runner executes the
# validation script. If it exits non-zero, the agent re-runs with the
# script's stdout/stderr appended as additional context.
# The validation script may be a simple deterministic check (linter, tests)
# or it may invoke another agent (e.g. `fullsend run review`) — see open
# questions below.
validation_loop:
  script: scripts/<validate>.sh     # exit 0 = pass, non-zero = retry
  max_iterations: 3                 # how many times the agent can retry
  feedback_mode: append             # append validation output to agent prompt

# Environment variables available to pre/post scripts and the agent runtime.
env:
  <KEY>: <value>

# Hard timeout enforced by the runner. The sandbox is killed after this.
timeout_minutes: 30
```

### Example: triage harness (simple)

A single agent, no code changes, no push, no PR:

```yaml
# harness/triage.yaml
agent: agents/triage.md
policy: policies/readonly-with-web.yaml

skills:
  - skills/triage-coordination
  - skills/detect-duplicates

tools_binaries:
  - name: claude
    source: PATH

api_servers:
  - name: github-proxy
    script: api-servers/gh-server/gh_server.py
    port: 8081
    env:
      GH_TOKEN: ${{secrets.GITHUB_TOKEN}}

pre_script: scripts/triage-pre.sh
post_script: scripts/triage-post.sh

env:
  ISSUE_NUMBER: ${{event.issue.number}}
  REPO_FULL_NAME: ${{event.repository.full_name}}

timeout_minutes: 30
```

The triage agent's policy (`readonly-with-web.yaml`) allows outbound HTTPS to
the model provider and the GitHub proxy, but no filesystem writes outside the
workspace.

### Example: code harness (with validation loop)

A code agent that writes code, validated by a deterministic lint/test script.
If validation fails, the agent re-runs with the failure output as context:

```yaml
# harness/code.yaml
agent: agents/code.md
policy: policies/code-write.yaml

skills:
  - skills/code-implementation
  - skills/testing-conventions

tools_binaries:
  - name: claude
    source: PATH
  - name: ruff
    source: https://github.com/astral-sh/ruff/releases/download/v0.9.0/ruff-x86_64-unknown-linux-gnu.tar.gz
    sha256: abc123...

api_servers:
  - name: github-proxy
    script: api-servers/gh-server/gh_server.py
    port: 8081
    env:
      GH_TOKEN: ${{secrets.GITHUB_TOKEN}}

pre_script: scripts/code-pre.sh
post_script: scripts/code-post.sh

validation_loop:
  script: scripts/validate-lint.sh
  max_iterations: 3
  feedback_mode: append

env:
  TIMEOUT_MINUTES: 90
  BRANCH_NAME: ${{event.branch_name}}

timeout_minutes: 120
```

The code harness's policy (`code-write.yaml`) would include repo-specific
egress (e.g. `pypi.org`, `proxy.golang.org`) alongside the baseline model
provider endpoints.

The code→review pattern (run the code agent, then a review agent as a gate,
then loop back if review fails) can be expressed through the `validation_loop`
mechanism: Whether `validation_loop` can invoke other agents
is an open question — see Consequences. Pipeline-level orchestration in CI (GitHub Actions / Tekton) can sequence
separate harness invocations.

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
- **Inheritance applies per-directory.** Each shared directory
  (policies/, skills/, etc.) follows the
  [ADR 0003](0003-org-config-repo-convention.md) layering
  (fullsend defaults → org `.fullsend` → per-repo) independently.
- **Model stays in the agent definition.** The harness YAML does not specify or
  override the model — that belongs in the agent `.md` frontmatter per the
  Claude sub-agent standard.
- A JSON Schema for the harness YAML format is a natural follow-on.
- Open: whether the harness `skills` list restricts or merely guides the agent
  runtime, and whether the runner should load the union of org-level and
  repo-level skills automatically
  ([experiment needed](https://github.com/fullsend-ai/fullsend/issues/127#issuecomment-4201527352)).
  Loading repo-level skills has prompt injection implications
  ([#48](https://github.com/konflux-ci/fullsend/pull/48)).
- Open: which fields are protected vs. freely overridable at the org/repo layer
  (policy rules should likely be additive only — repos cannot weaken
  org-level policies).
- Open: whether the `validation_loop` script can invoke another agent (e.g.
  `fullsend run review`). If yes, the code→review→code pattern is expressible
  within a single harness and does not require CI-level orchestration. If the
  validation step is restricted to deterministic scripts only (linters, tests),
  then multi-agent patterns require CI pipeline sequencing. The answer affects
  whether the validation step runs inside or outside the sandbox, and has
  transcript/observability implications.
- Open: whether tool provisioning should use declared lists with sha256 digests
  (transparent, auditable) or pre-built container images (simpler runner, less
  auditable), or support both.
- Open: whether harness definitions need a `version` field for schema
  evolution.
