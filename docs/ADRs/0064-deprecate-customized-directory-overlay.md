---
title: "64. Deprecate customized/ directory overlay"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - layering
  - customization
  - deprecation
---

# 64. Deprecate customized/ directory overlay

Date: 2026-06-30

## Status

Accepted

Supersedes [ADR 0035](0035-layered-content-resolution.md) (layered content
resolution).

## Context

[ADR 0035](0035-layered-content-resolution.md) introduced a three-tier
configuration layering model for agent customization: upstream defaults are copied into the
workspace at runtime, then files from `customized/` (per-org) or
`.fullsend/customized/` (per-repo) are overlaid on top, replacing upstream
files with matching names. The overlay is file-level replacement with no
field-level merging — customizing a single harness field requires copying the
entire upstream YAML and modifying it.

Three subsequent ADRs have introduced mechanisms that cover every
customization scenario the overlay handled, with better ergonomics:

- [ADR 0045](0045-forge-portable-harness-schema.md) added `base:`
  composition for harness files. A thin wrapper inherits an upstream harness
  by URL and overrides only the fields that differ, with proper merge
  semantics (scalars override, skills concatenate, runner_env merges).

- [ADR 0038](0038-universal-harness-access.md) added URL-based references
  for declarative resources (agents, skills, policies, schemas). Resources
  can be referenced from any trusted source without copying them into a
  local directory.

- [ADR 0058](0058-agent-registration.md) added config-based agent
  registration. Agents are discovered from `agents:` entries in config
  (URLs or local paths), not from directory scanning.

Together these make the `customized/` directory overlay redundant:

| What `customized/` did | Replacement |
|---|---|
| Override a harness | `base:` composition (ADR 0045) |
| Override an agent definition | Harness `agent:` field with path or URL (ADR 0038) |
| Add/remove agents | `agents:` list in config (ADR 0058) |
| Add custom skills | Harness `skills:` list with paths or URLs (ADR 0038); concatenated via `base:` (ADR 0045) |
| Override policies/schemas | Harness fields with paths or URLs (ADR 0038) |
| Custom scripts | `pre_script`/`post_script` in harness; inherited from `base:` (ADR 0045) |
| Custom env vars | `env:` in harness; merged via `base:` (ADR 0045) |
| Data files in `scripts/` (e.g. `.pre-commit-tools.yaml`) | L2 additive merge at repo root ([ADR 0056](0056-per-repo-precommit-tools-registry.md)); `base:` composition for harness-level overrides |

**Scripts and env constraint:**
[ADR 0038](0038-universal-harness-access.md) prohibits standalone URL
references for executable resources. All script customization must go
through `base:` harness composition (where scripts declared in the base
are fetched from the same origin). The L1 full-replacement path for
data files like `.pre-commit-tools.yaml` via `customized/scripts/`
([ADR 0056](0056-per-repo-precommit-tools-registry.md)) is replaced by
the L2 additive merge at repo root, which already covers per-repo
customization without the overlay.

The `customized/` directories currently contain only `.gitkeep` placeholders.
The overlay loop in reusable workflows runs every agent invocation but copies
zero files.

## Decision

Deprecate and remove the `customized/` directory overlay mechanism introduced
by ADR 0035.

The implementation plan is in
[docs/plans/deprecate-customized-directory-overlay.md](../plans/deprecate-customized-directory-overlay.md).

This ADR should be implemented once ADRs 0038, 0045, and 0058 are fully
implemented and in production.

## Consequences

- Users who placed files in `customized/` must migrate to `base:`
  composition, URL references, or config-based registration.
- Deprecation warnings during install and updated documentation will guide
  migration.
- The reusable workflows become simpler — no overlay loop, no
  `install_mode` branching for customization paths.
- The scaffold produces fewer files — no `.gitkeep` placeholders in
  `customized/` subdirectories.
- `fullsend admin install` no longer creates `customized/` directories.
- A single, consistent customization model replaces the split between
  directory overlay (ADR 0035) and harness composition (ADR 0045).
