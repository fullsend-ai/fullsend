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

This ADR aims to let repositories compose shared agent configurations while
keeping every update pinned and reviewable in the repository that uses it.

With remote harness references, today we have a way for a third-party to make a
really cool agent, advertise it, and have others consume it by linking to the
harness yaml remotely in their config.yaml.

What if we took this a step further, and had a way for a third-party to
construct a *bundle* of configuration -- multiple agents, configuration
overlays for existing agents, and more.  I (as the author of this ADR) want to
use this at my employer to create a config bundle that includes certain
security capability enhancements for some agents, and advertise that as a
golden configuration for those agents.

What capabilities exist that we can build on?

- Today, repositories can inherit a shared baseline through `config.base.yaml`
  and override it in `config.yaml` ([ADR 0069](0069-ready-made-configuration-presets.md)).
- Agent entries are keyed across those layers. [ADR
  0162](0162-agent-entries-may-carry-harness-overrides.md) defines a general
  `harness:` block on an agent entry, composed as a child harness.

A shared config file can use that `harness` block to declare additional skills or plugins for a
particular agent

Some related issues are
[#430](https://github.com/fullsend-ai/fullsend/issues/430),
[#631](https://github.com/fullsend-ai/fullsend/issues/631), and
[#7228](https://github.com/fullsend-ai/fullsend/issues/7228).

## Decision

We compose pinned full-config imports as ordered layers so repositories can
reuse agent sets and per-agent harness configuration through the existing
config model.

Here's a two-file example:

```yaml
# The consuming repository's .fullsend/config.yaml

# Enable vanilla review agent
agents:
  - name: review

# But layer in a remote config from a third-party
imports:
  - https://raw.githubusercontent.com/acme/agent-config/<commit>/config.yaml#sha256=<digest>
```

```yaml
# The acme org's agent-config file

# Add review agent, give it the `security` skill, and suppress "low" findings
agents:
  - name: review
    harness:
      skills:
        - https://github.com/acme/agent-config/tree/<commit>/skills/security#sha256=<digest>
      env:
        runner:
          REVIEW_FINDING_SEVERITY_THRESHOLD: medium
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
   under their existing rules.  In practice, something like renovate updates the source revision
   and digest by proposing repository changes; the repository adopts an update by
   reviewing and merging that change.

4. **Trust.** An import is a full config layer. It can affect every field that
   participates in config inheritance, including agent registration, runtime,
   roles, and remote-resource allowlists.

## Forge scope

This decision is forge-neutral. GitHub, GitLab, and Forgejo repositories use
the same import syntax, pin verification, and merge order; imported documents
are fetched over HTTPS independently of the target repository's forge.

## Consequences

- Repositories can reuse complete agent configurations and extend a specific
  agent through its `harness:` block without a second bundle format.
- Organizations can curate and share best practices across their teams.
