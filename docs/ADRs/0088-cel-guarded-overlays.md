---
title: "88. CEL-guarded overlays in the harness schema"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - overlays
  - cel
  - forge
  - configuration
---

# 88. CEL-guarded overlays in the harness schema

Date: 2026-08-14

## Status

Accepted

> **Living reference:** The overlay resolution pipeline, CEL environment,
> and merge rules described here are maintained in
> [Harness Field Reference](../contributing/harness-fields.md). Update
> that document (not this ADR) when overlay behavior evolves.

## Context

[ADR 0045](0045-forge-portable-harness-schema.md) added a `forge:` block to
the harness schema, keyed by platform (`github`, `gitlab`), so a single
harness file can carry platform-specific overrides for `pre_script`,
`post_script`, `skills`, `host_files`, `runner_env`, and other fields.
`ResolveForge(platform)` merges the selected platform's block into the
harness's top-level fields and nils out the map.

This design is rigid in two ways:

1. **Fixed conditioning axis.** The only thing you can condition on is the
   forge platform — a value detected from the CI environment or passed via
   `--forge`. There is no way to condition config on the event source
   system (e.g. JIRA vs GitHub), the event type, or any other property of
   the triggering event. This blocks
   [#2264](https://github.com/fullsend-ai/fullsend/issues/2264) and
   [#5989](https://github.com/fullsend-ai/fullsend/issues/5989), where a
   code agent needs JIRA-specific setup scripts when triggered by a JIRA
   issue but GitHub-specific scripts when triggered by a GitHub issue —
   and in both cases writes to the same forge.

2. **Closed key set.** Adding a new conditioning value (e.g. `jira`)
   requires modifying `validForgeKeys` in Go code, even though JIRA is
   not a forge and doesn't belong in `forge:`. Any future conditioning
   axis (event type, repo language, deployment target) would need its own
   top-level block and its own parallel resolution/merge/compose code.

The harness already has a CEL expression engine for `trigger:` expressions
([ADR 0061](0061-harness-cel-dispatch.md)), evaluated against
`normevent.Event` (`internal/normevent`) — a typed struct with fields like
`Source.System` (`SourceSystem`), `Entity.Kind` (`EntityKind`),
`Transition.Kind` (`TransitionKind`), and others. A general
conditional-config mechanism can reuse this infrastructure.

### Related work

- [ADR 0045](0045-forge-portable-harness-schema.md): Forge-portable harness
  schema — established the forge block, merge rules, and base composition.
- [ADR 0061](0061-harness-cel-dispatch.md): Harness CEL dispatch — CEL
  trigger expressions and `NormalizedEvent` schema.

## Decision

Add an `overlays:` list field to the harness schema. Each entry
has a `when:` CEL expression (same environment as `trigger:`, evaluated
against the `event` variable) and the same override fields as
`ForgeConfig`. At resolution time, the **first** entry whose `when`
evaluates to true is merged into the harness using the same merge
semantics as `mergeForgeConfig` (ADR 0045). Remaining entries are not
evaluated.

```yaml
overlays:
- when: event.source.system == "github"
  pre_script: scripts/pre-gh.sh
  skills:
    - skills/github-issue-triage
  runner_env:
    GH_TOKEN: ${GH_TOKEN}
- when: event.source.system == "jira"
  pre_script: scripts/pre-jira.sh
  skills:
    - skills/jira-issue-read
  runner_env:
    JIRA_TOKEN: ${JIRA_TOKEN}
```

First-match-wins keeps the mental model simple: exactly one overlay (or
none) applies to any given event. When an agent needs config from
multiple concerns (e.g. JIRA-specific scripts *and* GitHub-specific
runner env), the harness author creates a combined entry for that
scenario:

```yaml
overlays:
- when: event.source.system == "jira" && runtime.forge == "github"
  pre_script: scripts/pre-jira.sh
  skills:
    - skills/jira-issue-read
  runner_env:
    GH_TOKEN: ${GH_TOKEN}
    JIRA_TOKEN: ${JIRA_TOKEN}
- when: event.source.system == "github"
  pre_script: scripts/pre-gh.sh
  skills:
    - skills/github-issue-triage
  runner_env:
    GH_TOKEN: ${GH_TOKEN}
```

More specific entries go first; broader fallbacks go last.

`forge:` and `overlays:` must not coexist in the same harness —
`Validate()` rejects a harness that declares both. `forge:` continues to
work unchanged as a deprecated feature. `Lint()` emits a deprecation
warning when `forge:` is present, recommending migration to
`overlays:`.

### Resolution pipeline

> **Update (2026-08-21):** The empty-event semantics described below have
> evolved since this ADR was written. See
> [Harness Field Reference](../contributing/harness-fields.md) for the
> current behavior (nil event → empty map substitution).

`LoadWithOpts` and `LoadWithBase` gain `Event normevent.Event` and
`Config map[string]any` fields in their options structs. The pipeline
becomes:

```
Unmarshal → validateForge → validateOverlays →
ResolveForge(platform) → ResolveOverlays(event, config) → Validate
```

`ResolveOverlays(event, config)` evaluates each entry's `when`
against the CEL environment (see below). The first entry whose
`when` returns true is merged; remaining entries are skipped. Like
`ResolveForge`, it nils out the field after resolution (consumed).

### CEL environment

The overlay `when` expressions are evaluated in the same CEL
environment as `trigger:` ([ADR 0061](0061-harness-cel-dispatch.md)),
extended with additional variables:

| Variable | Type | Source |
|---|---|---|
| `event` | `normevent.Event` | The triggering event — same typed struct from `internal/normevent` with fields like `source.system` (`SourceSystem`), `entity.kind` (`EntityKind`), `transition.kind` (`TransitionKind`), etc. |
| `runtime.forge` | `string` | The effective forge platform, resolved with precedence: (1) `--forge` CLI flag, (2) `config.forge` from config.yaml, (3) CI env vars (`GITHUB_ACTIONS`, `GITLAB_CI`). Today `detectForgePlatform()` only checks (1) and (3); this ADR adds (2) so `runtime.forge` always reflects the configured platform whether or not the agent runs in CI. |
| `config` | `map[string]any` | The full per-repo config from `config.yaml` (`perRepoConfig`). Available for overlays that need to condition on repo-level settings beyond forge. |

This means an overlay can condition on the event origin, the runtime
platform, repo-level settings, or any combination. Most overlay
`when` expressions should reference `runtime.forge` and/or `event`
fields — `config` is available but typically not needed in `when`
expressions since `runtime.forge` already incorporates `config.forge`.

### Base composition

`mergeBaseIntoChild` concatenates `overlays` lists (base entries
first, child entries appended), the same way it handles `plugins`,
`providers`, and `api_servers`. With first-match-wins, a child entry
that matches shadows all base entries — child entries go last in the
concatenated list, but more-specific child `when` expressions can be
ordered before base fallbacks by the harness author.

The mutual exclusion between `forge:` and `overlays:` applies to the
**post-merge** result — the harness as seen by `Validate()` after
`mergeBaseIntoChild` runs. This means a base harness using `forge:`
and a child using `overlays:` (or vice versa) would produce a merged
harness containing both, which `Validate()` rejects. To migrate
incrementally, the base harness must convert from `forge:` to
`overlays:` before any child can adopt `overlays:`. This is a
deliberate constraint: mixing the two mechanisms in a composed
harness would create ambiguous resolution order between
`ResolveForge` and `ResolveOverlays`.

### Validation

Each `overlays` entry requires:

- A non-empty `when` field that compiles as a CEL expression returning
  bool (same rules as `trigger:`).
- Valid override fields, checked by the same validation logic as
  `validateForge` (script paths are local, URL fields have integrity
  hashes, etc.).

### Deprecation of `forge:`

`forge:` is not removed by this ADR. It continues to work, is validated
and resolved exactly as before, and existing harnesses are unaffected.
The deprecation is advisory:

- `Lint()` warns when `forge:` is present.
- New harnesses should use `overlays:` instead.
- A future ADR may remove `forge:` once all harnesses have migrated.

A `forge:` block like:

```yaml
forge:
  github:
    pre_script: scripts/pre-gh.sh
```

maps to an overlay that conditions on the forge platform:

```yaml
overlays:
- when: runtime.forge == "github"
  pre_script: scripts/pre-gh.sh
```

The mapping is mechanical — each forge key becomes a `when` expression
checking `runtime.forge` — but note the conditioning axis differs from
`event.source.system`. `runtime.forge` reflects the effective forge
platform (from `--forge` flag, `config.forge`, or CI env vars — see
the CEL environment table above), while
`event.source.system` identifies the event origin. These diverge for
cross-system events: a JIRA issue triggering work on GitHub Actions has
`runtime.forge == "github"` but `event.source.system == "jira"`.

To support this, the overlay CEL environment exposes `runtime.forge`
alongside the existing `event` variable, so overlays can faithfully
replicate `forge:` conditioning when needed.

## Consequences

- Harness authors can condition config on any event property, not just
  the forge platform — enabling JIRA-triggered agents, event-type-specific
  setup, and future conditioning axes without schema changes.
- `forge:` is deprecated but remains functional, so existing harnesses
  need no immediate migration.
- The CEL expression engine is already present (`trigger.go`,
  `github.com/google/cel-go`); `overlays` reuses it rather than
  introducing a new expression language or matching mechanism.
- First-match-wins means exactly one overlay (or none) applies per
  event, making the resolved harness easy to predict. Cross-concern
  scenarios (e.g. JIRA-triggered agent on GitHub) require a dedicated
  combined entry rather than implicit layering.
- The harness composition guide (`docs/contributing/harness-composition.md`)
  gains `validateOverlays`, `ResolveOverlays`, and the
  `overlays` concatenation in `mergeBaseIntoChild` as new
  counterpart functions to keep in sync.
