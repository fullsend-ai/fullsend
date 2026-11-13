---
title: "174. Compose pinned configuration imports"
status: Accepted
relates_to:
  - agent-architecture
  - governance
topics:
  - configuration
  - agents
---

# 174. Compose pinned configuration imports

Date: 2026-11-13

## Status

Accepted

## Context

Repositories can inherit a shared baseline through `config.base.yaml` and
override it in `config.yaml` ([ADR 0069](0069-ready-made-configuration-presets.md)).
Agent entries are keyed across those layers. [ADR 0162](0162-agent-entries-may-carry-harness-overrides.md)
defines a general `harness:` block on an agent entry, composed as a child
harness. Shared config can use that block to contribute skills or plugins to a
particular agent without pinning a replacement harness. Sharing a collection
of agents and their configuration across repositories still requires each
consumer to assemble that configuration locally. Related gaps are tracked in [#430](https://github.com/fullsend-ai/fullsend/issues/430),
[#631](https://github.com/fullsend-ai/fullsend/issues/631), and
[#7228](https://github.com/fullsend-ai/fullsend/issues/7228).

This ADR aims to let repositories compose shared agent configurations while
keeping every update pinned and reviewable in the repository that uses it.

## Options

- **A separate bundle schema for agents and attachments.** Rejected because it
  would create a second configuration model and require its own precedence
  rules.
- **Imports of complete config documents.** Chosen because they reuse the
  existing config schema and field-specific merge rules.
- **Floating remote references.** Rejected because a publisher's change must
  not alter a repository's effective configuration without a reviewed update.

## Decision

We compose pinned full-config imports as ordered layers so repositories can
reuse agent sets and per-agent harness configuration through the existing
config model.

```yaml
# .fullsend/config.yaml
imports:
  - https://raw.githubusercontent.com/acme/agent-config/<commit>/config.yaml#sha256=<digest>

agents:
  - name: review
    harness:
      skills:
        - https://github.com/acme/agent-config/tree/<commit>/skills/security#sha256=<digest>
```

1. **Layer order.** The effective config is composed in this order:

   ```text
   compiled defaults
   → config.base.yaml imports, then config.base.yaml values
   → config.yaml imports, in listed order
   → config.yaml values
   ```

   An imported document's imports are composed in listed order before its own
   values. Later layers override earlier ones.
   `imports` is a resolution directive and does not appear in the effective
   config. Cycles are errors, and the same pinned document reached by multiple
   paths is applied once.

2. **Merge behavior.** Every layer uses the existing per-field config merge
   rules ([Layered Config Reference](../guides/infrastructure/layered-config-reference.md)):
   scalar values use the latest set value; agent entries merge by name;
   per-agent `harness:` blocks compose using harness rules; maps merge by key
   where currently specified; and whole-object or replace-if-set fields retain
   their current behavior. In particular, `roles` replaces the inherited list,
   `allowed_remote_resources` unions non-empty lists with explicit empty-list
   deny-all semantics, and `authorization` and `trace_context` remain
   repository-local fields.

3. **Pins and updates.** Every remote import must use HTTPS, identify an
   immutable source revision, and include a `#sha256=<64-hex-character digest>`
   fragment. Remote resources referenced by an imported config remain pinned
   under their existing rules. Renovate updates the source revision and digest
   by proposing repository changes; the repository adopts an update by
   reviewing and merging that change. A remote config's relative resource paths
   resolve relative to the imported config's source.

4. **Trust.** An import is a full config layer. It can affect every field that
   participates in config inheritance, including agent registration, runtime,
   roles, and remote-resource allowlists. Repository owners review the imported
   document and its pin with the same care as local config. Existing
   repository-local fields retain their current scope.

## Forge scope

This decision is forge-neutral. GitHub, GitLab, and Forgejo repositories use
the same import syntax, pin verification, and merge order; imported documents
are fetched over HTTPS independently of the target repository's forge.

## Consequences

- Repositories can reuse complete agent configurations and extend a specific
  agent through its `harness:` block without a second bundle format.
- Each imported config and remote resource has an integrity pin; immutable
  revisions make updates and rollbacks addressable through repository changes.
- A full config import can change global settings such as `roles` and
  `allowed_remote_resources` under their existing merge rules.
- Import graphs need deterministic cycle detection and must report invalid or
  mismatched content pins before an agent runs.
