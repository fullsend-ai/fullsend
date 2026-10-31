---
title: "81. Reserve CI workflow env: for infrastructure plumbing, not agent behavior"
status: Accepted
relates_to:
  - agent-infrastructure
  - governance
topics:
  - configuration
  - harness
  - conventions
---

# 81. Reserve CI workflow env: for infrastructure plumbing, not agent behavior

Date: 2026-07-31

Amends: [ADR 0049](0049-agent-configuration-env-var-convention.md)

## Status

Accepted

*Amended by [ADR 0154](0154-agent-entries-may-overlay-harness-fields.md):
besides `base:` composition, an `agents:` entry's `harness:` block can
override `env.runner`/`env.sandbox` defaults. The workflow `env:` block
stays reserved for infrastructure plumbing.*

## Context

[ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md) states that a
per-repo or per-org override of an agent behavior knob means overriding
the harness (`base:` composition, per [ADR 0045](0045-forge-portable-harness-schema.md)),
not the CI workflow `env:` block. Review of that ADR found the practice
already drifting: fullsend-ai/agents#567's docs describe setting
`TRIAGE_AUTO_CODE` via the workflow `env:` block, "matching the
convention used for `REVIEW_FINDING_SEVERITY_THRESHOLD`" — but
[ADR 0055](0055-unified-env-var-delivery.md)'s own canonical example sets
`REVIEW_FINDING_SEVERITY_THRESHOLD` in the harness's `env.sandbox`, not
the workflow file. Nothing had stated the workflow-env path was out of
bounds for behavior knobs, so nothing caught the drift.

ADR 0049 lists "CI workflow injection" as one of three delivery
mechanisms for agent config vars, alongside `.env` files and
`runner_env`, without distinguishing infrastructure values (credentials,
project IDs, regions) from agent behavior knobs.

## Decision

The CI workflow `env:` block (`.github/workflows/<agent>.yml`) is
reserved for infrastructure plumbing — credentials, project IDs,
regions, and other infrastructure values sourced from CI-native inputs
(secrets, org/repo variables). Agent behavior knobs, as scoped by ADR
0080 and named per ADR 0049, are never set there. They go through harness
composition instead: `env.runner`/`env.sandbox` defaults live in the
canonical harness, and a per-repo or per-org override edits those
defaults via `base:` composition (ADR 0045).

The one exception: a value that can only be computed at CI runtime —
derived from `github.event.*`, a build matrix variable, or a secret that
cannot be expressed as static harness data — may be set in the workflow
`env:` block even if it configures agent behavior. This ADR does not try
to enumerate every such case up front; a genuine new one can be added
here by minor annotation as it turns up.

This narrows ADR 0049's "CI workflow injection" delivery mechanism: it
remains valid for infrastructure vars and CI-runtime-only values, not for
static agent behavior defaults.

## Consequences

- fullsend-ai/agents#567's docs need correcting: `TRIAGE_AUTO_CODE`'s
  override path is harness composition, not CI workflow `env:` — the
  precedent it cited was itself non-conformant. The fullsend-ai/agents
  repo's own `docs/review.md` carries the same non-conformant guidance
  for `REVIEW_FINDING_SEVERITY_THRESHOLD` that this PR fixed in
  `fullsend`'s copy, and needs the equivalent fix.
- `CODE_ALLOWED_TARGET_BRANCHES: ''` is still hardcoded in
  `reusable-code.yml`/`reusable-dispatch.yml`'s workflow `env:` block,
  even though [ADR 0053](0053-agent-driven-branch-targeting.md) already
  decided this value belongs in the harness's `runner_env`, not the
  workflow YAML. It's a pre-existing non-conformance this ADR's rule
  makes explicit; removing it from the workflow files is a follow-up,
  not part of this decision.
- New agent behavior knobs get one documented override path (harness
  `base:` composition), removing the ambiguity between three candidate
  mechanisms.
- A workflow `env:` entry that sets an agent behavior default, and isn't
  one of the CI-runtime-only exceptions, is a signal the knob was placed
  in the wrong layer.
