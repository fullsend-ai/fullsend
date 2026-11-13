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

Repositories can inherit shared configuration through `config.base.yaml`
([ADR 0069](0069-ready-made-configuration-presets.md)). [ADR
0162](0162-agent-entries-may-carry-harness-overrides.md) accepts a `harness:`
block on an agent entry, composed as a child harness. Together, these decisions
provide a way for a shared config to register agents and add skills or plugins
to selected agents. The remaining need is to reuse complete config documents
across repositories.

The motivating use case is an organization publishing a curated config that
registers several agents and adds security capabilities to selected agents.
Consuming repositories should be able to adopt that configuration from a URL,
with every update pinned and reviewed locally. Related issues include
[#430](https://github.com/fullsend-ai/fullsend/issues/430),
[#631](https://github.com/fullsend-ai/fullsend/issues/631), and
[#7228](https://github.com/fullsend-ai/fullsend/issues/7228). This ADR aims to
enable that reuse without introducing a separate bundle schema or precedence
model.

## Decision

We use ordered imports of complete config documents so repositories can reuse
agent sets and per-agent harness configuration through the existing config
model, without a separate bundle schema or precedence rules.

For example, a repository can import a pinned config that adds a skill to its
review agent:

```yaml
# The consuming repository's .fullsend/config.yaml
imports:
  - https://raw.githubusercontent.com/acme/agent-config/<commit>/config.yaml#sha256=<digest>

# Local values are applied after the import.
agents:
  - name: review
```

```yaml
# The acme org's agent-config file
agents:
  - name: review
    harness:
      skills:
        - https://github.com/acme/agent-config/tree/<commit>/skills/security#sha256=<digest>
      env:
        sandbox:
          REVIEW_FINDING_SEVERITY_THRESHOLD: medium
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
   rules ([Layered Config Reference](../guides/infrastructure/layered-config-reference.md)).

3. **Pins and updates.** Every remote import must use HTTPS, identify an
   immutable source revision, and include a `#sha256=<64-hex-character digest>`
   fragment. Remote resources referenced by an imported config remain pinned
   under their existing rules, and relative resource paths resolve relative to
   the imported config's source. Renovate proposes source revision and digest
   updates; a repository adopts an update by reviewing and merging that change.

4. **Trust.** An import is a full config layer. It can affect every field that
   participates in config inheritance, including agent registration, runtime,
   roles, and remote-resource allowlists. Repository owners review the imported
   document and its pin with the same care as local config; repository-local
   fields retain their current scope.

## Forge scope

This decision is forge-neutral. GitHub, GitLab, and Forgejo repositories use
the same import syntax, pin verification, and merge order; imported documents
are fetched over HTTPS independently of the target repository's forge.

## Consequences

- Repositories can reuse complete agent configurations and extend a specific
  agent through its `harness:` block without a second bundle format.
- Immutable source revisions and digests make imports reviewable and updates
  addressable through repository changes.
- Imported configs can affect global settings under the existing merge rules;
  import cycles and pin mismatches are errors.
- Organizations can curate and share best practices across their teams.
