---
title: "123. fullsend-ai/platform"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - platform
  - tenants
  - deployment
---

# 123. fullsend-ai/platform

Date: 2026-09-21

## Status

Accepted

## Context

Self-managed per-repository Fullsend installations are useful, but they
require each repository owner to operate event delivery, scheduling,
execution, credentials, and supporting infrastructure. This fits small,
medium, and large open source projects, as well as proprietary environments
of a similar scale.

At enterprise or foundation scale, an organization may instead need to
operate those capabilities across many repositories, forge organizations, and
work trackers. When the organization also needs site-wide controls, the
distributed operational responsibility of self-managed per-repository
installations becomes an anti-feature.

See also the [agent infrastructure problem
document](../problems/agent-infrastructure.md).

## Decision

Fullsend gains an optional **platform** subsystem implemented independently
from the core Fullsend runner in a new
[fullsend-ai/platform](https://github.com/fullsend-ai/platform) repository.

The platform introduces a **tenant** as its primary administrative boundary.
For the `fullsend-ai/platform` deployment, the platform owns the tenant API,
polling, webhook ingress, queueing, dispatch, and workload orchestration.
Self-managed per-repository installations retain `fullsend poll` and
`fullsend dispatch` behavior ([ADR 0063](0063-polling-based-work-discovery.md),
[ADR 0061](0061-harness-cel-dispatch.md)). The existing
`fullsend-ai/fullsend` repository owns `fullsend run`, configuration and
trigger semantics, the normalized entity API, and runtime integrations
reusable by both the platform and forge-native deployments.

A tenant may span multiple forge organizations, repositories, and Jira
projects. It is therefore not equivalent to Fullsend's deprecated
per-organization installation mode.

In this ADR, **tenant** names the `fullsend-ai/platform` administrative
boundary. This is distinct from the mint ADRs' use of tenant for an org or
mint trust boundary: [ADR 0029](0029-central-token-mint-secretless-fullsend.md)
describes a self-managed tenant as a single organization, [ADR
0059](0059-public-mint-mode-with-wildcard-allowlists.md) distinguishes tight
single-tenant and public multi-tenant mint profiles, and [ADR
0068](0068-public-community-mint-architecture.md) distinguishes self-managed
tenant mints from the hosted community mint. Those mint boundaries remain
relevant to credential and authorization scope; this ADR does not redefine
them.

Self-managed per-repository installation remains a first-class deployment
model.

## Consequences

- The platform implementation can evolve independently while this repository continues to define the reusable runner and core execution contracts.
- Enterprise platform teams can reduce per-tenant and per-repository operational toil, and tenants can process repository-less entities without inventing a fake repository owner.
- Site and tenant administrators can eventually impose configuration on repository executions, although the specific mechanism remains open.
- Platform and tenant concepts should remain absent from the `fullsend-ai/fullsend` CLI, while platform integration develops API boundaries around the existing `tracker.Client` and poller implementations; exact public interface names remain to be designed.
