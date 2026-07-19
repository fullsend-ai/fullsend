---
title: "73. Named mint privilege levels under agent roles"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
topics:
  - mint
  - least-privilege
  - harness
  - roles
---

# 73. Named mint privilege levels under agent roles

Date: 2026-07-19

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

The mint maps each agent role to a single hardcoded permission set ([ADR 0007](0007-per-role-github-apps.md)). Every token minted for a role gets the same ceiling. The threat model establishes least privilege as a cross-cutting principle (see [security-threat-model.md](../problems/security-threat-model.md)), but the current model cannot differentiate between a phase that only needs read access and one that needs write — both receive write-level tokens.

[#2821](https://github.com/fullsend-ai/fullsend/issues/2821) (human-gated permission adjustments) and the least-privilege topic ([#5312](https://github.com/fullsend-ai/fullsend/issues/5312)) depend on a general mechanism for minting tokens at differentiated privilege levels within a role. Without this mechanism, downstream work risks encoding use-case-specific designs instead of a reusable model.

## Decision

### Role levels

Each role defines an ordered set of **named privilege levels**. Each level's permissions are a superset of all preceding levels. The first level is always `read` — generally read-only — and is the default when no level is requested.

The `write` level is defined as the permission set currently granted by each built-in role. For roles where `write` is not explicitly defined, requesting `write` falls back to `read` automatically.

### Mint API

Token requests accept an optional `level` field:

```json
{"role": "coder", "level": "write"}
```

When `level` is omitted, the mint defaults to `read`. The mint validates that the requested level exists for the role and returns an error for unknown levels.

### Custom roles

`CUSTOM_ROLE_PERMISSIONS` continues to accept the current JSON shape — a flat role-to-permissions map — interpreted as the `read` permissions for each role:

```json
{"my-role": {"contents": "read", "issues": "write"}}
```

An alternate shape specifies multiple named levels per role. The mint auto-detects the format by checking whether each role's value contains a `levels` key:

```json
{"my-role": {"levels": {"read": {"contents": "read"}, "write": {"contents": "read", "issues": "write"}}}}
```

### Clients

`mintclient` and CLI paths that mint tokens accept an optional `level` parameter, defaulting to `read` when unset. The harness passes the level derived from the `privilege_levels` configuration (see below) to the mint client for each phase.

### Acquisition semantics

The harness is the sole caller that selects privilege levels. It determines the level for each run phase from the `privilege_levels` configuration and requests the corresponding token from the mint before that phase begins. Agents and scripts within a phase do not choose or escalate their own level — they receive the token the harness minted for their phase.

### Harness `privilege_levels` flag

Harness YAML supports a `privilege_levels` field mapping run phases to levels:

```yaml
privilege_levels:
  pre_script: write
  runtime: read
  post_script: write
```

A `default` key specifies the level for phases not explicitly listed. When `privilege_levels` is omitted entirely, the harness behaves as if:

```yaml
privilege_levels:
  default: write
```

This preserves backward compatibility — existing harnesses continue to receive write-level tokens for all phases.

## Consequences

- Agents running in the LLM sandbox can receive read-only tokens, reducing blast radius if the sandbox is compromised.
- The mint API remains backward-compatible; omitting `level` produces the same tokens as today.
- `CUSTOM_ROLE_PERMISSIONS` supports both the existing flat format and the new multi-level format without a breaking change.
- Harnesses that omit `privilege_levels` default to `write` for all phases, so existing configurations are unaffected.
- Implementation of role levels in the mint, clients, and harness is tracked in [#2823](https://github.com/fullsend-ai/fullsend/issues/2823) and [#2826](https://github.com/fullsend-ai/fullsend/issues/2826).
- [#2821](https://github.com/fullsend-ai/fullsend/issues/2821) (human-gated permission adjustments) is unblocked and can build on this mechanism.
