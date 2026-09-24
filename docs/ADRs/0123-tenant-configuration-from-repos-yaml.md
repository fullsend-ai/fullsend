---
title: "123. GitOps-managed tenant configuration for Fullsend"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - platform
  - tenants
  - deployment
---

# 123. Tenant configuration from repos.yaml

Date: 2026-09-24

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

Defining the components of a centrally manageable fullsend system to address
those needs is out of scope for this document.

What is in scope is the data model. An enterprise operator could require a new
level of abstraction on top of the existing fullsend model - a "tenant" of the
platform that is larger than a single repo and may be larger than an org.

The `repos.yaml` manifest already describes a spec for how fullsend should be
installed and operating across multiple repos.

See also the [agent infrastructure problem
document](../problems/agent-infrastructure.md).

## Decision

Future components that make up a centrally manageable Fullsend system will
re-use the repos manifest as the method for end users to define their tenant.

We anticipate that enterprise administrators manage those files in git,
although this is not strictly required.

Future central service components may load and validate them from configured HTTPS
sources, or alternatively from a mounted kubernetes ConfigMap.

The human-managed format remains `repos.yaml`

Central service components should use a new internal `TenantConfig` model built
from the validated manifest, rather than rely directly on `repos.Manifest`,
where we can store extra metadata as needed.

```
type TenantConfig struct {
    TenantID      string
    Manifest      *repos.Manifest
}
```

Self-managed per-repository installations remain first-class and keep their
existing manifest and CLI behavior.

### Note about the mint tenant

This platform-level use of **tenant** differs from the mint ADRs' use of the
term for an org or mint trust boundary:
[ADR 0029](0029-central-token-mint-secretless-fullsend.md) describes a
self-managed tenant as a single organization, [ADR
0059](0059-public-mint-mode-with-wildcard-allowlists.md) distinguishes tight
single-tenant and public multi-tenant mint profiles, and [ADR
0068](0068-public-community-mint-architecture.md) distinguishes self-managed
tenant mints from the hosted community mint. Those mint boundaries remain
relevant to credential and authorization scope; this ADR does not redefine
them.

## Consequences

- This is the smallest, simplest incremental decision for exploring what an enterprise-managed Fullsend installation could look like. No new API.
- A custom REST API or Kubernetes-native tenant resource can be explored later if GitOps manifests prove unsuitable as requirements evolve.
- We may find that we have overloaded repos.yaml with too many competing use cases. Watch out for this as we go forwards.
- For enterprise administrators, git history and review can provide an auditable change and rollback path for each tenant's repository configuration.
- Follow-up decisions will define other components of a centrally manageable fullsend system.
