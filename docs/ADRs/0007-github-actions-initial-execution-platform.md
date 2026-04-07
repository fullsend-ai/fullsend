---
title: "7. GitHub Actions as initial execution platform"
status: Proposed
relates_to:
  - agent-infrastructure
  - agent-architecture
  - security-threat-model
topics:
  - infrastructure
  - execution
  - portability
---

# 7. GitHub Actions as initial execution platform

Date: 2026-03-27

## Status

Proposed

## Context

Fullsend needs an execution platform — somewhere to run agent workloads when
triggered by events. The architecture doc identifies this as the "Agent
Infrastructure" component, and
[agent-infrastructure.md](../problems/agent-infrastructure.md) explores three
directions: adopt a 3rd party solution, use existing internal infrastructure,
or build our own.

Two concurrent ADRs shape this decision:

- **[ADR 0005](0005-unidirectional-control-flow.md)** establishes that the
  execution stack has unidirectional control flow: Trigger → Infrastructure →
  Sandbox → Harness → Runtime. The infrastructure layer must be swappable
  without affecting layers below it.
- **[ADR 0006](0006-forge-abstraction-layer.md)** establishes a forge
  abstraction layer so that agents do not call GitHub APIs directly.

We need to choose an initial platform that lets us start running agents quickly
while preserving the ability to change platforms later.

## Options

### Option 1: GitHub Actions

Use GitHub Actions for both the trigger layer and the infrastructure layer.
Workflows respond to GitHub events (issues, PRs, labels) and provision runners
that invoke fullsend's platform-agnostic entry point.

**Pros:**
- Zero additional infrastructure to provision or operate. Orgs already have it.
- GitHub's event system provides the trigger layer for free.
- Runners provide compute with built-in secret management.
- The `.github/workflows/` mechanism is well-understood.
- Experiment #67 demonstrated that GitHub App token generation and scoped
  `GH_TOKEN` passing work with Claude Code. (The experiment ran locally, not on
  GitHub Actions runners — validating the full GH Actions environment remains
  open work.)
- Fastest path to a working implementation.

**Cons:**
- Couples the trigger and infrastructure layers (both are GitHub Actions).
- Runner resource limits: 6-hour job timeout, 20 concurrent jobs per org on the
  free tier (jobs beyond this limit are **dropped, not queued**), 2,000
  minutes/month included. These limits are org-wide across all repos.
- Cost at scale — GitHub-hosted runners are billed per minute.
- Vendor lock-in risk — mitigated by ADR 0005's unidirectional rule and the
  entry point contract described below.
- Self-hosted runners can relax resource and concurrency limits but add
  operational burden. The initial implementation targets GitHub-hosted runners;
  self-hosted runners are a viable optimization for orgs that hit limits.

### Option 2: Kubernetes from day one

Provision a Kubernetes cluster with an operator/controller that watches forge
events and runs agent workloads as pods.

**Pros:**
- Full control over compute, isolation, and scaling.
- No vendor coupling at the infrastructure layer.

**Cons:**
- Requires cluster provisioning, operator development, webhook ingestion, and
  secret management — months of work before an agent runs.

### Option 3: Hybrid from day one (GitHub Actions triggers, Kubernetes runs)

GitHub Actions responds to events and dispatches work to a Kubernetes cluster
that runs the actual agent workloads.

**Pros:**
- Easy triggers from GitHub's event system, flexible compute from Kubernetes.

**Cons:**
- Premature complexity. Two systems to operate before we know what the workload
  looks like.

## Decision

GitHub Actions is the initial execution platform for fullsend. It serves as
both the trigger layer and the infrastructure layer for the first
implementation.

### Critical constraint: no GitHub Actions coupling below the infrastructure layer

GitHub Actions is the infrastructure. It is NOT the sandbox, the harness, or
the runtime. The workflow YAML is infrastructure configuration — it provisions
compute and launches the sandbox. Nothing below the infrastructure layer should
know it is running on GitHub Actions.

Concretely:

- The workflow file passes the raw forge event to a **platform-agnostic entry
  point**. The entry point consults `.fullsend` config, selects a sandbox
  policy, harness configuration, and agent runtime — then orchestrates their
  launch. The infrastructure layer (GitHub Actions) is not involved in those
  choices.
- All configuration comes from the `.fullsend` repo (see
  [ADR 0003](0003-org-config-repo-convention.md)), not from workflow YAML.
- GitHub Actions secrets bootstrap credentials (e.g., a GitHub App private
  key), but credential issuance (generating ephemeral tokens) is handled by
  the identity provider component, not by GitHub Actions-specific mechanisms.

### The entry point contract

The boundary between "infrastructure" and "everything below" is a
platform-agnostic entry point. On GitHub Actions, a workflow step invokes it.
On Kubernetes, a pod's entrypoint invokes it. The entry point receives the
**raw forge event** — e.g., "issue #123 was labeled with `agent-ready`" — not
a pre-processed task description.

The entry point is responsible for:

1. **Interpreting the event** — determining what happened and whether it
   requires agent action.
2. **Consulting `.fullsend` configuration** — reading the org's `.fullsend`
   repo to determine which sandbox policy, harness configuration, and agent
   runtime to use for this event type.
3. **Orchestrating the launch** — setting up the sandbox with the selected
   policy, assembling the harness with the selected agent definition, and
   invoking the agent runtime.

Control still flows strictly downward per
[ADR 0005](0005-unidirectional-control-flow.md) — the entry point configures
each layer top-down, and no layer can influence layers above it. The entry
point is the same regardless of execution platform.

How bootstrap credentials (e.g., for fetching `.fullsend` config or issuing
ephemeral tokens) are provided to the entry point is an open question. On
GitHub Actions, the reusable workflow in `.fullsend` has access to secrets and
can pass them to the entry point. On Kubernetes, a mounted secret or service
account may serve the same role. The right approach will emerge from
implementation.

### Future execution platforms

The architecture anticipates additional execution platforms beyond GitHub
Actions:

- **Kubernetes** — clusters with an independent trigger layer (e.g., a
  controller/operator that watches forge events via webhooks or polling).
- **GitLab CI** — GitLab's native CI/CD runners, using GitLab CI pipeline
  definitions as the trigger and infrastructure layer.
- **Forgejo Runners** — Forgejo's runner infrastructure, analogous to GitHub
  Actions but for Forgejo-hosted organizations.

In each case, the same principle applies:

- The trigger layer changes to the platform's native event system.
- The infrastructure changes to the platform's native compute.
- Everything below (sandbox, harness, runtime) stays the same because of
  ADR 0005's unidirectional rule.
- The forge abstraction layer (ADR 0006) means the entry point works unchanged.

### Workflow file design

The `.github/workflows/` file in enrolled repos should be as thin as possible —
a stub that calls a [reusable workflow](https://docs.github.com/en/actions/sharing-automations/reusing-workflows)
defined in the org's `.fullsend` repo. The reusable workflow in `.fullsend`
contains the actual entry point invocation, secret references, and sandbox
launch logic. The enrolled repo's workflow is just a `workflow_call` reference.

This design has two benefits:

- **Credential isolation.** The GitHub App private key and other secrets live
  only in the `.fullsend` repo. Enrolled repos never have direct access to
  these secrets — they invoke the reusable workflow, which has access.
- **Centralized updates.** Changing the entry point, sandbox image, or launch
  logic requires updating only the `.fullsend` repo, not every enrolled repo.

### Workflow file protection

The fullsend workflow file in each enrolled repo **must be listed in
CODEOWNERS as human-owned.** If an agent could modify its own workflow file via
a PR, it would be modifying its own trigger and infrastructure layer —
violating [ADR 0005](0005-unidirectional-control-flow.md)'s unidirectional
rule. This is the same principle that makes CODEOWNERS itself always
human-owned: agents cannot modify their own guardrails.

Which layer is responsible for verifying this? The agent dispatch and
coordination layer should perform a **pre-flight check** before launching
agent work: confirm that the enrolled repo's fullsend workflow file is
CODEOWNERS-protected. If it is not, the dispatch layer refuses to run and
surfaces the misconfiguration to humans. This keeps enforcement in the
topmost layer, consistent with unidirectional control flow — lower layers
don't need to worry about it because the dispatch layer has already verified
it.

## Consequences

- **Enrolled repos get a workflow file.** Each enrolled repo gets a
  `.github/workflows/` file that invokes fullsend's entry point on relevant
  events. This file is infrastructure, not application code.
- **The entry point is platform-agnostic.** It is a script or container, not a
  GitHub Action. This is the portability boundary.
- **Kubernetes migration is additive.** When we add Kubernetes support, we
  implement a new trigger layer and a new way to invoke the same entry point.
  Nothing below the infrastructure layer changes.
- **Resource limits constrain initial agent capabilities.** GitHub Actions'
  6-hour job timeout, 20 concurrent jobs per org (free tier), and runner specs
  limit what agents can do initially. Critically, jobs that exceed the
  concurrency limit are dropped, not queued — a burst of events (e.g., many
  issues labeled simultaneously) will lose work. The dispatch layer must handle
  this, either by rate-limiting event processing or by retrying dropped work.
- **The fullsend workflow file in enrolled repos must be CODEOWNERS-protected.**
  If agents could modify their own workflow file, they would be modifying their
  own trigger and infrastructure layer, violating
  [ADR 0005](0005-unidirectional-control-flow.md). The dispatch layer performs
  a pre-flight check to verify this.
- **Trigger and infrastructure coupling is a known trade-off.** Both layers
  are GitHub Actions initially. ADR 0005's layering ensures this coupling does
  not leak below the infrastructure layer, so decoupling them later requires
  no changes to the sandbox, harness, or runtime.
