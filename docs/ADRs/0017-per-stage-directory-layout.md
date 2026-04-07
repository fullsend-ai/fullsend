---
title: "0017. Per-stage directory layout for stage definitions"
status: Proposed
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - stages
  - configuration
---

# 0017. Per-stage directory layout for stage definitions

Date: 2026-04-07

## Status

Proposed

<!-- Once this ADR is Accepted, its content is frozen. Do not edit the Context,
     Decision, or Consequences sections. If circumstances change, write a new
     ADR that supersedes this one. Only status changes and links to superseding
     ADRs should be added after acceptance. -->

## Context

Each agent invocation requires a **stage definition** that ties together
several moving parts:

1. **Which agents to run, and in what order** — a stage may chain agents
   sequentially (e.g. code agent then review agent), sharing the same sandbox.
2. **Which sandbox profile to apply** — container image, network egress
   allowlist, filesystem mounts.
3. **Pre-script** — deterministic setup (clone, checkout, token generation).
4. **Post-script** — deterministic teardown for privileged operations agents
   must not perform (push, PR creation, label transitions).
5. **Runtime parameters** — model, retry limits, timeouts, iteration caps.

Today these are scattered across workflow files, CLI arguments, and unspecified
conventions. There is no single file that ties a stage together.

A **stage runner** (part of the entry point from [#125](https://github.com/fullsend-ai/fullsend/issues/125))
reads the definition and executes a deterministic sequence:

```
┌─────────────────────────────────────────────────┐
│  Stage runner reads stages/code/code.yaml       │
├─────────────────────────────────────────────────┤
│  1. Provision sandbox (image, firewall, mounts) │
│  2. Run pre_script inside sandbox               │
│  3. For each agent in agents[] (in order):      │
│     a. Assemble harness (agent def + skills)    │
│     b. Launch agent runtime                     │
│     c. Wait for agent to exit                   │
│     d. If non-zero exit, stop — skip remaining  │
│        agents and post_script                   │
│  4. Run post_script inside sandbox              │
│     (has access to everything agents produced)  │
└─────────────────────────────────────────────────┘
```

The stage runner is deterministic code, not an LLM. Agents are LLM sessions;
the orchestration between them is a script or binary. Each agent runs on the
same sandbox filesystem, so later agents see earlier agents' work.

The stage definition is the input to harness assembly
([#173](https://github.com/fullsend-ai/fullsend/issues/173)). It connects to
`config.yaml` ([ADR 0011](../normative/admin-install/v1/adr-0011-org-config-yaml/SPEC.md)),
`agent-dispatch-v1.yaml` ([ADR 0012](../normative/admin-install/v1/adr-0012-fullsend-repo-files/SPEC.md)),
and the agent wrapper concept ([#101](https://github.com/fullsend-ai/fullsend/issues/101)).

## Options

### Option A: Flat `stages/` directory

```
stages/
  triage.yaml
  code.yaml
  scripts/
    code-pre.sh
    code-post.sh
```

Follows [ADR 0003](0003-org-config-repo-convention.md) inheritance
(fullsend < org `.fullsend` < per-repo). Scripts, firewall rules, and stage
YAML are siblings with no grouping per stage.

### Option B: Per-stage directory with co-located files

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

The stage YAML includes a key pointing to companion files (e.g.
`sandbox.firewall_file: firewall.yaml`). Everything about a stage is in one
directory. Layered overrides target individual files within the directory.

### Option C: Inline in `config.yaml`

All stage definitions under a `stages:` key in `config.yaml`. Single source of
truth, but the file grows large and per-repo overrides require replacing the
entire stages block.

## Decision

Adopt per-stage directories (Option B). Each stage is a directory under
`stages/` containing:

- **`<stage>.yaml`** — the stage definition
- **`firewall.yaml`** — egress allowlist, referenced by key from the stage YAML
- **Pre/post scripts** — co-located in the same directory

The inheritance model from [ADR 0003](0003-org-config-repo-convention.md)
applies at the file level: fullsend ships defaults, the org `.fullsend` repo
can overlay or add stages, and per-repo `.fullsend/stages/<stage>/` can
override individual files.

### Example: triage stage (simple)

A single agent, no code changes, no push, no PR:

```yaml
# stages/triage/triage.yaml
agents:
  - name: triage
    definition: agents/triage.md

sandbox:
  image: registry.access.redhat.com/ubi9/ubi-minimal:latest
  firewall_file: firewall.yaml
  filesystem:
    workspace: /home/agent/workspace
    readonly:
      - /home/agent/.config

runtime:
  model: claude-sonnet-4-20250514
  max_retries: 1
  timeout_minutes: 30

pre_script: triage-pre.sh
post_script: triage-post.sh
```

### Example: code stage (multi-agent)

Two agents run sequentially in the same sandbox. The code agent implements the
fix and commits locally, then the review agent evaluates the branch as a
mandatory pre-push security gate. The post-script pushes and creates the PR.

```yaml
# stages/code/code.yaml
agents:
  - name: code
    definition: agents/code.md
  - name: review
    definition: agents/review.md

sandbox:
  image: registry.access.redhat.com/ubi9/ubi:latest
  firewall_file: firewall.yaml
  filesystem:
    workspace: /home/agent/workspace
    readonly:
      - /home/agent/.config

runtime:
  model: claude-sonnet-4-20250514
  max_retries: 2
  timeout_minutes: 120

pre_script: code-pre.sh
post_script: code-post.sh
```

The code stage's `firewall.yaml` would include repo-specific egress (e.g.
`pypi.org`, `proxy.golang.org`) alongside the baseline GitHub and model
provider endpoints.

## Consequences

- `ls stages/code/` shows everything about the code stage — high discoverability.
- Firewall rules, scripts, and the stage YAML can be overridden independently at each inheritance layer.
- The entry point resolves a stage by convention: `fullsend entrypoint code` reads `stages/code/code.yaml`.
- Script paths in the YAML become relative to the stage directory, simplifying co-location.
- A JSON Schema for the stage YAML format is a natural follow-on.
- Open: whether the `skills` list in a stage definition restricts or merely guides the agent runtime ([experiment needed](https://github.com/fullsend-ai/fullsend/issues/127#issuecomment-4201527352)).
- Open: which fields are protected vs. freely overridable at the org/repo layer (firewall rules should likely be additive only).
