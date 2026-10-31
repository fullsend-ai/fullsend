---
title: "154. Agent entries may overlay harness fields"
status: Accepted
relates_to:
  - agent-infrastructure
  - governance
topics:
  - configuration
  - harness
  - overlays
---

# 154. Agent entries may overlay harness fields

Date: 2026-10-30

Amends: [ADR 0049](0049-agent-configuration-env-var-convention.md), [ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md), [ADR 0081](0081-reserve-workflow-env-for-infra-plumbing.md), [ADR 0091](0091-per-agent-runtime-model-effort.md)

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

An agent's tuning knobs are variables in its harness `env.runner` and
`env.sandbox`. [ADR 0049](0049-agent-configuration-env-var-convention.md),
[ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md) and
[ADR 0081](0081-reserve-workflow-env-for-infra-plumbing.md) leave a repo one
way to change one: compose a harness through `base:` (or copy it).
[ADR 0091](0091-per-agent-runtime-model-effort.md) lets an `agents:` entry
set `runtime`, `model` and `effort` but keeps `config.yaml` from composing a
harness; entries have since gained `subagents`
([ADR 0104](0104-per-persona-model-resolution.md)) and `trigger` (#8726). A
`base:` harness pins an agents commit that nothing moves forward
automatically (#5802), so a repo that changes one value stops getting agent
updates. That has blocked `REVIEW_FINDING_SEVERITY_THRESHOLD` (#6822, #8143),
`GITHUB_SIGNED_COMMITS` (#8903, closed in favor of #8143) and a longer
code-agent timeout (#7223).

The discussion on #7719 asked for one general way to override harness fields
from `config.yaml`, not a config field per setting. Harness overlays already
merge a partial harness onto a resolved one
([`mergeForgeConfig`](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/internal/harness/forge.go#L364)),
and [ADR 0112](0112-overlays-may-set-any-harness-field.md) decides that
overlays may set any harness field (not yet implemented). `config.yaml` comes
from the same ref as a local harness file: on GitHub, the PR base SHA for
`pull_request_target` and `pull_request_review` and `github.sha` otherwise
([`reusable-dispatch.yml`](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/.github/workflows/reusable-dispatch.yml#L1271),
the same expression in every agent job); on GitLab, the default branch
([`run-agent-job.sh`](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/run-agent-job.sh#L1014-L1016)).

This ADR aims to let a repository change an agent's knob from `config.yaml`
without composing and pinning a harness, and without adding a config field
per setting.

## Options

- **Harness fields directly on the entry**, such as a bare `env:` (as
  #8143's triage shows) or `timeout_minutes` (#7223). Each needs its own
  placement and merge rules and shares a namespace with the entry's own
  fields, such as `model`.
- **Keep `base:` and automate pin updates** (#5802). A repo still keeps a
  harness file per change; this complements the decision.
- **A file overlay like `customized/`**
  ([ADR 0035](0035-layered-content-resolution.md)), which
  [ADR 0064](0064-deprecate-customized-directory-overlay.md) removed as
  redundant with `base:` composition.
- **A `harness:` block on the entry.** Chosen: it reuses the overlay merge
  and keeps harness fields apart from the entry's own.

## Decision

We let an `agents:` entry carry a `harness:` block that overrides harness
`env` values, so a repo can change an agent's knob without a `base:` harness.

```yaml
agents:
  - name: review
    harness:
      env:            # runner and sandbox are set independently
        runner: { REVIEW_FINDING_SEVERITY_THRESHOLD: medium }
        sandbox: { REVIEW_FINDING_SEVERITY_THRESHOLD: medium }
```

1. **Fields.** The block takes only `env`, where every knob above lives. Any
   other harness field, including those ADR 0112 lets overlays set, joins
   only through a later decision. It may sit on any enabled entry.
2. **When.** `fullsend run` and `fullsend lint` read the entry before harness
   composition (today `fullsend run` reads per-agent settings
   [after it](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/internal/cli/run.go#L982))
   and merge the block with the overlay merge after `base:`, `forge:` and the
   harness's overlays, before validation. Dispatch and `fullsend lock` do not
   merge it. Values get the harness's `${VAR}` expansion and checks. Each key
   replaces the harness value, or adds it when the `forge:` block or overlay
   that declares it did not apply, and an empty string sets it empty, as an
   overlay's `env` does.
3. **Precedence.** A per-run override where the variable has one, then the
   block, then the harness, then the default, as in ADR 0091.
4. **Layers.** Block keys merge per key across `config.base.yaml` and
   `config.yaml`, `config.yaml` winning. A `~` in `config.yaml` drops a key
   `config.base.yaml` set, so the harness value applies again, as `~` does
   for [`subagents`](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/internal/config/interfaces.go#L215);
   unlike in a harness file, `~` is not an empty value.
5. **Checks.** Mistakes fail loudly, as #8143 asks. Agent-entry validation
   ([`ValidateAgentEntries`](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/internal/config/config.go#L508),
   applied by `fullsend run`, `fullsend lint` and dispatch) rejects an unknown
   key in the block and a name the
   [sandbox gate](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/internal/cli/run.go#L3424)
   reserves, in either section. `fullsend run` and `fullsend lint` reject a
   name the harness does not declare in that section, naming the agent and
   variable. A name is declared if any layer of the harness, the file or a
   `base:` ancestor, sets it in that section at top level, in a `forge:` block
   or in an overlay, whatever the forge or event; composition records these
   names before it resolves `forge:` blocks and overlays. Run and lint then
   agree on a variable such as `GITHUB_SIGNED_COMMITS` that only a forge
   overlay declares. Adding an undeclared name still takes `base:`. Strict
   decoding of the rest of the entry stays with #7223.
6. **Settings.** A non-empty block counts as a setting
   ([`HasSettings`](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/internal/config/config.go#L71)
   and the dispatch workflow's
   [entry check](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/.github/workflows/reusable-dispatch.yml#L896)),
   so a block-only entry stays override-only and registers no harness.

This amends the override convention of ADR 0049, ADR 0080 and ADR 0081: a
knob stays one harness variable, settable in the harness or through the
block, so ADR 0080's "one settable location" becomes one name with two
places. It loosens ADR 0091's rule that `config.yaml` does not compose a
harness, for `env` only.

## Forge scope

Forge-neutral: the CLI merges the block on every forge.

- **GitHub:** the reusable dispatch workflow's entry check must count the
  block.
- **GitLab:** no workflow-side entry check exists; nothing else changes.
- **Forgejo:** fullsend has no Forgejo support yet; nothing to change.

## Consequences

- A repo sets `REVIEW_FINDING_SEVERITY_THRESHOLD` or `GITHUB_SIGNED_COMMITS`
  in one entry and keeps getting agent updates; a longer timeout (#7223)
  needs a later decision that adds `timeout_minutes` to the block.
- Whoever changes `config.yaml`, `config.base.yaml` or the `repos.yaml` that
  renders them reaches as far as a `base:` harness's `env`, which ADR 0112
  accepts from config as a trusted source: expanding a runner variable into
  the sandbox, setting a declared `GH_TOKEN` to another runner variable, or
  emptying `REVIEW_PROTECTED_PATHS`.
- ADR 0122's safety gate does not compare block values, so a `repos.yaml`
  change that empties `REVIEW_PROTECTED_PATHS` is not flagged; extending the
  gate is follow-up work.
- The plan output names `config.yaml` as the source, `fullsend agent set`
  gains a way to write the block, and docs here and in fullsend-ai/agents
  that name `base:` as the only override path are updated.
- An older workflow pin or CLI
  [rejects](https://github.com/fullsend-ai/fullsend/blob/46cbad968f14700e03bcbb3f000a0a60c5f4de62/internal/config/config.go#L573)
  a block-only entry, and an older CLI ignores the block beside another
  setting, so repos update both.
