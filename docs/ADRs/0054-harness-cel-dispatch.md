---
title: "54. Harness CEL triggers and fullsend dispatch drivers"
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

# 54. Harness CEL triggers and fullsend dispatch drivers

Date: 2026-06-23

## Status

Accepted

## Context

The primary motivation is to make **custom agents easy to author and portable
across forges**. Today, adding a custom agent means editing shared dispatch
bash in GitHub Actions workflows, adding a new per-stage workflow file, and
re-implementing the same routing rules for each install mode. That workflow is
GitHub-specific, not co-located with the harness, and does not travel to GitLab
or other forges ([gitlab-implementation.md](../problems/gitlab-implementation.md#event-mapping)).

A secondary motivation is **security allow-listing**. Workloads that call the
token mint or inference APIs must run from workflows explicitly trusted by those
layers. The mint validates `job_workflow_ref` against `.fullsend`, the upstream
fullsend repo, or registered per-repo repos, and against an allowed workflow
file list ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)). Inference
access via GCP Workload Identity Federation uses the same class of binding. Each new custom agent workflow file
therefore requires operational changes in two security surfaces — friction that
discourages experimentation and org-specific agents.

Agent triggering today lives in bash embedded in GitHub Actions workflows
(`reusable-dispatch.yml`, `internal/scaffold/.../dispatch.yml`). The router
reads `github.event_*` fields, applies ACL and label guards, selects a
**stage** string, and fans out to stage workflows via static `workflow_call`
jobs or `# fullsend-stage:` scanning ([ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md),
[ADR 0041](0041-synchronous-workflow-call-event-dispatch.md)).
[ADR 0045](0045-forge-portable-harness-schema.md) moved execution identity into
harness files but left **when** an agent runs in workflow bash.

Colocating CEL `trigger` rules on harness files and driving dispatch through
`fullsend dispatch` lets orgs add agents by dropping harness YAML (with triggers)
into layered config — without new allow-listed workflow files per agent. A
single generic runner workflow can be trusted once; routing nuance (slash
commands, label snapshots, actor ACLs, fork gates, review-bot fix loops) lives
in portable harness expressions evaluated against a forge-neutral event struct.

## Options

### Option A: Keep routing in workflow bash (status quo)

- No migration cost; behavior is already proven in production.
- Remains forge-coupled, duplicated, and difficult to test or extend.

### Option B: Central `config.yaml` routing table

- One file owns all rules; easy to audit centrally.
- Adding an agent still edits shared config; rules drift from harness
  definitions; poor locality for custom/org agents.

### Option C: CEL `trigger` expressions on harness files

- Each harness declares when it runs; discovery is "evaluate all harnesses."
- Expressions operate on a normalized event struct, testable without CI.
- Requires a CLI dispatch step and driver plugins for I/O.

## Decision

Adopt **Option C**. Introduce a forge-neutral **`NormalizedEvent`** as the
input to routing, CEL boolean **`trigger`** expressions on harness YAML files,
and a **`fullsend dispatch`** subcommand with pluggable **input** and **output**
drivers.

### NormalizedEvent (routing input)

A single record describing what happened plus ambient state needed for guards.
Field-level contract: [`docs/normative/normalized-event/v1/`](../normative/normalized-event/v1/)
([ADR 0015](0015-normative-specifications-directory.md)). Adapters map forge
webhooks, `GITHUB_EVENT_PATH`, or manual CLI input into this struct.

**Illustrative example only.** The normative schema
([`normalized-event.schema.json`](../normative/normalized-event/v1/normalized-event.schema.json))
and [`examples/`](../normative/normalized-event/v1/examples/) fixtures are the
authoritative source for field names, types, and requiredness. This sample
shows the shape a `gha-event` input driver would produce when triage applies
`ready-to-code` to an issue:

```json
{
  "repo": "fullsend-ai/demo",
  "entity": {
    "kind": "work_item",
    "id": 42,
    "url": "https://github.com/fullsend-ai/demo/issues/42"
  },
  "transition": {
    "kind": "label_changed",
    "label": { "name": "ready-to-code", "action": "added" }
  },
  "actor": {
    "id": "fullsend-ai-triage[bot]",
    "kind": "bot",
    "role": "none",
    "is_entity_author": false
  },
  "state": {
    "labels": ["ready-to-code", "kind/bug"]
  },
  "source": {
    "system": "github",
    "raw_type": "issues",
    "raw_action": "labeled"
  }
}
```

The code harness `trigger` expression shown below would match this event.

### Harness `trigger` field

Extend harness YAML ([ADR 0045](0045-forge-portable-harness-schema.md)) with an
optional top-level `trigger` string containing a **CEL** boolean expression.
The expression's context is the `NormalizedEvent` (convention: root variable
`event`). Example:

```yaml
trigger: |
  event.transition.kind == "label_changed" &&
  event.transition.label.name == "ready-to-code" &&
  event.entity.kind == "work_item"
```

A harness with no `trigger` is never auto-dispatched (manual `fullsend run`
only). Multiple harnesses may match one event (parallel fan-out).

### `fullsend dispatch`

New CLI subcommand:

```text
fullsend dispatch [--input-driver DRIVER] [--output-driver DRIVER] [flags]
```

**Pipeline:**

1. **Input driver** produces one or more `NormalizedEvent` values.
2. **Dispatch core** loads harness files (org/repo layered discovery per
   [ADR 0045](0045-forge-portable-harness-schema.md)), evaluates each
   `trigger` with CEL, and collects matches.
3. For each match, project an **execution ref** (`source_repo`, `event_type`,
   `event_payload`, and fix-only `trigger_source`) from the `NormalizedEvent`.
   The `fullsend run` CLI contract is unchanged; the normative schema includes
   all fields required to generate today's execution ref (see
   [execution ref projection](../normative/normalized-event/v1/README.md#execution-ref-projection)).
4. **Output driver** renders environment-specific execution plans.

**Drivers** are specified by flag or **auto-detected** from the runtime
environment (e.g. `GITHUB_EVENT_PATH` present → `gha-event` input driver).

| Driver (initial) | Role |
|------------------|------|
| `gha-event` | Read `GITHUB_EVENT_PATH` (+ optional label/PR snapshot via `gh`) → `NormalizedEvent` |
| `json` | Read events from stdin or `--input-file` (tests, replay) |
| `gha-matrix` | Emit GitHub Actions `matrix` JSON for matched harnesses + execution refs |
| `json` (output) | Print matched harness paths and execution refs to stdout |

Future drivers: GitLab webhook/trigger variables (input), Tekton
`PipelineRun` spec (output). Driver interface is stable; implementations are
pluggable.

### Workflow integration (GitHub Actions)

Replace bash stage routing in dispatch workflows with:

```yaml
- id: dispatch
  run: fullsend dispatch --output-driver gha-matrix >> "$GITHUB_OUTPUT"
- strategy:
    matrix: ${{ fromJSON(needs.dispatch.outputs.matrix) }}
  # one job per matched harness; each calls fullsend run with projected inputs
```

The shim workflow remains a thin security boundary ([ADR 0009](0009-pull-request-target-in-shim-workflows.md));
**routing logic moves out of YAML bash into harness CEL + `fullsend dispatch`.**

Static `workflow_call` stage jobs in `reusable-dispatch.yml` and
`# fullsend-stage:` markers ([ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md))
are **deprecated** by this ADR and removed during implementation.

## Consequences

- Custom agents become **drop-in harness files** with `trigger` expressions;
  no per-agent workflow files or dispatch bash edits required.
- Mint and inference allow-lists can trust a **small set of generic runner
  workflows** instead of every agent-specific workflow file.
- Trigger rules live beside the harness they activate; routing is
  unit-testable by feeding `NormalizedEvent` fixtures to CEL without GitHub
  Actions.
- Forge portability improves: only input drivers map native events to
  `NormalizedEvent`; harness CEL is shared.
- `# fullsend-stage:` workflow markers and duplicated bash routers can be
  deleted once migration completes.
- CEL in harness files requires contributor documentation, linting, and eval
  fixtures ([testing-agents.md](../problems/testing-agents.md)).
- Multi-match fan-out is explicit; sequential stage chaining remains out of
  scope ([ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md)).
