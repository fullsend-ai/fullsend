---
title: "55. Harness CEL triggers and fullsend dispatch drivers"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - dispatch
  - harness
  - cel
  - forge
---

# 55. Harness CEL triggers and fullsend dispatch drivers

Date: 2026-06-23

## Status

Accepted (partially supersedes static stage routing from
[ADR 0041](0041-synchronous-workflow-call-event-dispatch.md); preserves its
synchronous `workflow_call` execution model)

## Context

Custom agents should be easy to author and portable across forges. Today, adding
one means editing shared dispatch bash in GitHub Actions, adding per-stage
workflow files, and re-implementing routing per install mode — work that is
GitHub-specific and not co-located with the harness
([gitlab-implementation.md](../problems/gitlab-implementation.md#event-mapping),
[ADR 0045](0045-forge-portable-harness-schema.md),
[ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md)).

A second constraint is **security allow-listing**: token mint and inference APIs
trust only explicit `job_workflow_ref` values
([ADR 0029](0029-central-token-mint-secretless-fullsend.md)). Each new agent
workflow file requires operational updates in both surfaces, which discourages
org-specific agents.

Colocating CEL `trigger` rules on harness files and routing through
**`fullsend dispatch`** lets orgs drop in harness YAML without new allow-listed
workflows. Routing nuance (slash commands, labels, actor ACLs, fork gates) lives
in portable expressions over a forge-neutral **`NormalizedEvent`**
([normative v1 spec](../normative/normalized-event/v1/)).

## Options

### Option A: Keep routing in workflow bash (status quo)

- Proven behavior; remains forge-coupled and hard to test.

### Option B: Central `config.yaml` routing table

- One audit file; rules drift from harness definitions.

### Option C: CEL `trigger` on harness files

- Self-describing agents; requires `fullsend dispatch` and input/output drivers.

## Decision

Adopt **Option C**.

- **`NormalizedEvent`:** routing input with forge-neutral field names
  ([`docs/normative/normalized-event/v1/`](../normative/normalized-event/v1/),
  [ADR 0015](0015-normative-specifications-directory.md)). **v1 normative scope
  is GitHub Actions** (`gha-event` driver); other forges are documented as
  future illustrations only. Examples and projection rules live in the normative
  tree — not duplicated here.
- **Authorization:** `fullsend dispatch` enforces
  [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md) as a
  **platform-level gate** after the input driver normalizes the event and
  **before** CEL trigger evaluation. Authorization is not delegated to per-harness
  CEL expressions.
- **Harness `trigger`:** optional CEL boolean with root variable `event`. No
  `trigger` → manual `fullsend run` only. Multiple harnesses may match (parallel
  fan-out).
- **`fullsend dispatch`:** input driver → **authorize** → evaluate harness CEL →
  project execution ref (unchanged `fullsend run` contract) → output driver
  (`gha-matrix`, `json`, etc.). Drivers are flagged or auto-detected
  (`GITHUB_EVENT_PATH` → `gha-event`).
- **Workflow integration:** installations may replace bash stage routing with
  `fullsend dispatch --output-driver gha-matrix` and a dynamic job matrix.
  Reintroduces dynamic agent discovery via CEL (superseding ADR 0041's static
  `workflow_call` stage list) while keeping synchronous matrix-job execution.
  Deprecate `# fullsend-stage:` markers and duplicated bash routers where
  dynamic routing is adopted.
- **Coexistence with explicit workflows:** implementing this ADR does not
  preclude hand-written workflows that invoke a particular harness file directly
  (today's per-agent or per-stage `workflow_call` pattern). CEL-based dispatch
  and explicit harness invocation may run side by side in the same installation —
  for example, default agents routed by `fullsend dispatch` while org-specific
  agents remain on dedicated workflows that call `fullsend run` with a fixed
  harness path.

## Consequences

- Custom agents ship as harness files with `trigger`; no per-agent workflow or
  dispatch bash edits.
- Mint and inference allow-lists can trust a small set of generic runner
  workflows instead of every agent-specific workflow file.
- Authorization remains centralized per ADR 0054; CEL triggers express routing
  only, not permission policy.
- Routing is unit-testable via `NormalizedEvent` fixtures without GitHub
  Actions; input adapters own forge-specific mapping including comment
  `command`/`instruction` extraction (from downstream workflow steps such as
  `reusable-fix.yml`).
- CEL linting, documentation, and eval fixtures are required
  ([testing-agents.md](../problems/testing-agents.md)); sequential multi-agent
  chaining remains out of scope ([ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md)).
