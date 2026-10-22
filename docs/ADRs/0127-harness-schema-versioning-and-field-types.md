---
title: "127. Harness schema versioning and field types"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - configuration
  - validation
---

# 127. Harness schema versioning and field types

Date: 2026-09-24

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

The harness YAML is a cross-repository API boundary: fullsend owns the interpretation of each field (which are inline commands, which are local paths, which are fetched resources), while fullsend-ai/agents owns the content ([ADR 0047](0047-vendored-installs-with-vendor-flag.md), [ADR 0058](0058-agent-registration.md)). When the repositories split, the field-level semantic types were left implicit in fullsend's Go code, with no machine-checkable input schema. [ADR 0024](0024-harness-definitions.md) deferred schema versioning to a later decision ([#235](https://github.com/fullsend-ai/fullsend/issues/235)); it has stayed open since.

The 2026-09-21 outage made the gap concrete: a harness set `validation_loop.preflight_check: "scripts/common-preflight.sh"` expecting script-path semantics, but fullsend ran the value as a literal `sh -c` command with no working directory, so the relative path resolved against the wrong directory and at least nine runs failed before revert ([#7600](https://github.com/fullsend-ai/fullsend/issues/7600)). Today the harness carries no `version` field; only `config.yaml` does ([ADR 0045](0045-forge-portable-harness-schema.md)).

The durable fix is an explicit, versioned harness contract so field types are discoverable and checkable by both repositories.

## Options

- **Lint-only, no `schema_version`.** Rejected: a lint rule catches mistakes but gives no durable handle for when a field's type changes, which is what a version bump is for.

- **Full JSON Schema per version.** Rejected for now: it is the natural follow-on ([ADR 0024](0024-harness-definitions.md)) but is more machinery than the current need; keep it as a follow-up.

## Decision

Introduce a `schema_version` field to the harness YAML and classify each field's semantic type explicitly.

1. **Add `schema_version`.** Absent `schema_version` is interpreted as `1` (current behavior), so existing harnesses remain valid. A loader that implements versioning must reject malformed, zero, negative, or unsupported versions during parsing, before interpreting fields under a different version's semantics. This is a load failure, not a non-fatal `Harness.Lint()` finding. Validate the effective version of each raw `base` layer before composition; reject mixed-version layers unless an explicit compatibility rule exists for that combination.

2. **Classify every field by semantic type.** Distinguish inline commands (`sh -c`), local paths, resource references, scalar values (e.g. `model`, `effort`, `timeout_minutes`), and structural containers; document hybrid fields and metadata separately. The classification lives in the Harness Field Reference (`docs/contributing/harness-fields.md`).

3. **Flag type violations at load time.** Extend `Harness.Lint()` (already run from `fullsend lock` and `run`) to flag values that violate their field's declared type, at SeverityError.

Versioning declares the field-type contract; it does not validate values. Lint and runtime checks still do the work.

An older fullsend binary cannot reject a `schema_version` field it does not recognize. Upgrade and verify every pinned consumer supports the new version before advancing its harness pin to content that uses that version; a new loader cannot protect older binaries retroactively.

## Consequences

- Authors get a documented field-type contract; the "path vs command" ambiguity is resolved at the schema level. Machine-checking arrives with the `Harness.Lint()` type-flagging rule, which is not yet implemented.
- Backward compatible: harnesses without `schema_version` are treated as version 1 by version-aware loaders; invalid or unsupported versions fail load before composition.
- A `schema_version` bump is required for an incompatible field-type change and must update `harness-fields.md` in the same change. Backward-compatible field additions do not require a bump; this ADR does not set the versioning policy for other kinds of breaking schema change.
- fullsend stays the sole owner of harness interpretation; agents authors consume the published contract rather than reverse-engineering Go code.
- Follow-ups out of scope here: a policy for other breaking schema changes, per-version JSON Schema, machine-checked schema validation in agents CI, and making `Harness.Lint()` SeverityError diagnostics fail-fast in `fullsend lock`/`run`.
