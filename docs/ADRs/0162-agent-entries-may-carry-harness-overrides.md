---
title: "162. Agent entries may carry harness overrides"
status: Accepted
relates_to:
  - agent-infrastructure
  - governance
topics:
  - configuration
  - harness
---

# 162. Agent entries may carry harness overrides

Date: 2026-11-06

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

Most of an agent's settings live in its harness. To change one, such as a
variable in its `env`, a repo writes its own harness that names the agent's
harness in `base:`
([ADR 0049](0049-agent-configuration-env-var-convention.md),
[ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md),
[ADR 0081](0081-reserve-workflow-env-for-infra-plumbing.md)). That `base:`
pins an agents commit that nothing moves forward automatically (#5802), so
the repo stops getting agent updates. This has blocked
`REVIEW_FINDING_SEVERITY_THRESHOLD` (#6822, #8143), `GITHUB_SIGNED_COMMITS`
(#8903, closed in favor of #8143) and a longer code-agent timeout (#7223).

[ADR 0045](0045-forge-portable-harness-schema.md) kept per-agent overrides
out of `config.yaml`. [ADR 0091](0091-per-agent-runtime-model-effort.md)
narrowed that to let an `agents:` entry set `runtime`, `model` and `effort`,
and entries have since gained `subagents`
([ADR 0104](0104-per-persona-model-resolution.md)) and `trigger` (#8726).
The discussion on #7719 asked for one general way to override harness fields
from `config.yaml`, not a config field per setting. `config.yaml` is read from
the same trusted ref as a local harness file: on GitHub, the pull request's
base commit for `pull_request_target` and `pull_request_review` events and the
triggering commit otherwise; on GitLab, the default branch.

This ADR aims to let a repository change an agent's harness settings from
`config.yaml`, without writing and pinning a harness file.

## Options

- **Harness fields directly on the entry.** Rejected: an entry also holds
  fields that are not harness fields, such as `enabled`, and some names
  already mean something else on an entry, such as `trigger`.
- **A block limited to `env`** (the first draft of this ADR). Rejected in
  review: every other field would need its own decision.
- **Keep `base:` and automate pin updates** (#5802). A repo still keeps a
  harness file per change; this complements the decision.
- **A `harness:` block that works as a derived harness.** Chosen.

## Decision

We let an `agents:` entry carry a `harness:` block that works as a harness
derived from the agent's harness, so a repo can change harness settings
without writing and pinning a harness file.

```yaml
agents:
  - name: review
    harness:
      env:
        runner: { REVIEW_FINDING_SEVERITY_THRESHOLD: medium }
        sandbox: { REVIEW_FINDING_SEVERITY_THRESHOLD: medium }
  - name: code
    harness:
      timeout_minutes: 90
```

1. **What it is.** The block is a partial harness, applied as if it named the
   agent's harness in `base:`, with nothing pinned: it builds on whatever
   harness the agent resolves to, from its `source` or the built-in one. It
   merges as a child harness does, by the rules of `base:` composition (see
   the [harness field reference](../contributing/harness-fields.md)). It may
   sit on any enabled entry.
2. **Fields.** The block takes every harness field except:
   - `base:`, since the entry's `source` already does that job;
   - `forge` and `runner_env`, both deprecated:
     [ADR 0088](0088-cel-guarded-overlays.md) replaces `forge` with
     `overlays`, and [ADR 0055](0055-unified-env-var-delivery.md) replaces
     `runner_env` with `env.runner`, so a new place to set harness fields
     starts without them. `runner_env` would also get around point 5: the run
     merges it into the runner environment beside `env.runner`, so the block
     could add runner variables the harness does not declare. For the same
     reason, the block's `overlays` cannot set `runner_env` either. A harness
     that still uses either field works with a block: names in its `forge:`
     blocks count as declared (point 5), and the block's `overlays` can test
     `runtime.forge`. A variable a harness sets only through `runner_env`
     cannot be changed through the block until the harness moves it to
     `env.runner`; no fleet harness uses `runner_env` today.

   An unknown key fails. Like any layer, the block may set `schema_version`
   ([ADR 0127](0127-harness-schema-versioning-and-field-types.md)): absent
   means `1`, and a block whose version differs from the agent's harness
   fails to load.
3. **Layers.** The blocks in `config.base.yaml` and `config.yaml` are two more
   layers of the chain: the agent's harness, then the `config.base.yaml`
   block, then the `config.yaml` block. Harness rules apply between them, so
   `config.yaml` can replace a value from `config.base.yaml` but not remove
   it, and `~` on an `env` value sets it empty. Entry fields outside the
   block, such as `subagents`, keep their current layering.
4. **Who reads it.** Everything that composes the agent's harness composes
   the block too: `fullsend run`, `fullsend lint`, dispatch (including the
   Jira poller) and `fullsend lock`, so they all see the same harness. Lock
   treats a change to the block like a change to the harness file. It still
   does not lock built-in agents, as ADR 0091 decides.
5. **Env names.** Unlike a harness file, the block cannot add a variable to
   `env`. An `env` name that the agent's harness does not declare in that
   section fails, naming the agent and the variable. A name is declared if
   any layer of the agent's harness, the file or a `base:` ancestor, sets it
   in that section at top level, in a `forge:` block or in an overlay,
   whatever the forge or event. Adding a variable still takes a harness file
   with `base:`. A name fullsend reserves in the sandbox
   ([ADR 0146](0146-sandbox-environment-contract-manifest.md)) fails in
   either section. This keeps per-persona switches such as
   `REVIEW_UX_ENABLED` from returning through `env`: turning personas on and
   off belongs to `subagents` entries (#8969).
6. **Fields the entry already has.** Setting `model`, `effort` or `trigger` on
   both an agent entry and its `harness` block fails.

   Built-in agents are `review`, `retro`, `triage`, `prioritize`, `code` and
   `fix`, whether or not the entry has a `source`. Until all built-in agents
   are routed by CEL alone (#2902):
   - An entry `trigger` on `review`, `retro`, `triage` or `prioritize` opts
     that agent into CEL routing, with workflow routing as fallback.
   - An entry `trigger` on `code` or `fix` routes nothing; they keep workflow
     routing until ported.
   - A `harness.trigger` on any built-in agent is not evaluated; the
     configuration loads with a warning naming the agent.

   These checks apply to the entry after `config.base.yaml` and `config.yaml`
   are merged.
7. **Precedence.** A per-run override (flag or environment variable) where
   the setting has one, then the block, then the harness, then the default,
   as ADR 0091 orders entry fields.
8. **Settings.** A non-empty block counts as a setting, so a block-only entry
   tunes a built-in agent and registers no harness of its own.

This amends the override convention of ADR 0049, ADR 0080 and ADR 0081: a
knob stays one harness variable, settable in a harness or through the block.
It amends ADR 0091's rule that `config.yaml` does not compose a harness: the
block is a harness layer.

## Forge scope

Forge-neutral: the CLI composes the block on every forge.

- **GitHub:** the reusable dispatch workflow's check of `agents:` entries must
  count the block.
- **GitLab:** no workflow-side entry check exists; nothing else changes.
- **Forgejo:** fullsend has no Forgejo support yet; nothing to change.

## Consequences

- A repo changes an agent setting in one entry and keeps getting agent
  updates (#8143, #8903, #7223).
- Whoever can change `config.yaml`, `config.base.yaml` or the `repos.yaml`
  that renders them can change what a local `base:` harness can, including
  the image, policy, security settings and scripts, and `role` and
  `human_min_role`, which set who may trigger the agent
  ([ADR 0151](0151-harness-human-role-minimums.md)). ADR 0122's safety gate
  does not inspect harness settings, so it does not compare block values;
  extending it is follow-up work.
- A shared preset whose block names a variable that a repo's harness does not
  declare fails that repo's runs, as an unmatched `subagents` key does
  ([ADR 0104](0104-per-persona-model-resolution.md)).
- The review agreed that entry fields which override harness fields should
  give way to the block. Retiring the entry's `trigger` comes first; that
  work decides how built-in agents are routed and removes the `trigger` rule
  in point 6. The other fields are decided in follow-up work.
- An older workflow pin or CLI rejects a block-only entry, and an older CLI
  ignores the block beside another setting, so repos update both.
