---
title: "110. Add a managed tenant platform"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - platform
  - tenants
  - deployment
---

# 110. Add a managed tenant platform

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
installations becomes an anti-feature. The [agent infrastructure problem
document](../problems/agent-infrastructure.md) describes the broader trade-offs;
the [security threat model](../problems/security-threat-model.md) continues to
apply to either deployment model.

## Decision

Fullsend gains an optional **platform** subsystem implemented independently
from the core Fullsend runner in a new
[fullsend-ai/platform](https://github.com/fullsend-ai/platform) repository.

The platform introduces a **tenant** as its primary administrative boundary.
The platform owns the tenant API, polling, webhook ingress, queueing,
dispatch, and workload orchestration. The existing
`fullsend-ai/fullsend` repository owns `fullsend run`, configuration and
trigger semantics, the normalized entity API, and runtime integrations
reusable by both the platform and forge-native deployments.

A tenant may span multiple forge organizations, repositories, and Jira
projects. It is therefore not equivalent to Fullsend's deprecated
per-organization installation mode.

Self-managed per-repository installation remains a first-class deployment
model.

## Consequences

- The platform implementation can evolve independently while this repository continues to define the reusable runner and core execution contracts.
- Enterprise platform teams can reduce per-tenant and per-repository operational toil, and tenants can process repository-less entities without inventing a fake repository owner.
- Site and tenant administrators can eventually impose configuration on repository executions, although the specific mechanism remains open.
- Platform and tenant concepts should remain absent from the `fullsend-ai/fullsend` CLI, while its public Go API grows to support the platform, notably with `IssueTracker` and `Poller` interfaces and likely other integrations.
