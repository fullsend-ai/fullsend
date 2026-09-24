---
title: "123. Tenant configuration from repos.yaml"
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

Self-managed per-repository Fullsend installations require each repository
owner to operate event delivery, scheduling, execution, credentials, and
supporting infrastructure. At enterprise or foundation scale, an organization
may need to operate those capabilities across many repositories, forge
organizations, and work trackers, with site-wide controls.

Defining a centrally manageable Fullsend system is out of scope. This ADR
decides its tenant configuration shape: the existing `repos.yaml` manifest
already describes how Fullsend should be installed across multiple GitHub
and/or GitLab repositories. See also the [agent infrastructure problem
document](../problems/agent-infrastructure.md).

## Decision

If a centrally manageable Fullsend system is built, end users will define each
tenant with the existing `repos.yaml` v1 manifest. This reuses the public
manifest format; it does not introduce a separate tenant configuration
schema. The source of truth and delivery mechanism are intentionally
unspecified. Git is a possible source; service components could, for example,
consume manifests from HTTPS sources or mounted Kubernetes ConfigMaps.

Central service components will wrap the validated `repos.Manifest` in an
internal `TenantConfig`, providing a place for platform metadata such as
tenant identity without duplicating the manifest's fields:

```go
type TenantConfig struct {
    TenantID string
    Manifest *repos.Manifest
}
```

Self-managed per-repository installations remain first-class and keep their
existing manifest and CLI behavior. This ADR does not decide whether the
centralized service will be offered, its management API or Kubernetes
resources, its dispatcher or launcher, its infrastructure, or a repo-less
execution context; those can be addressed in follow-up decisions.

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

- This is the smallest, simplest incremental decision for exploring an
  enterprise-managed Fullsend installation: it reuses the existing tenant
  manifest format.
- An internal wrapper can add platform metadata while reusing the validated `repos.Manifest` rather than duplicating its fields.
- If Git is chosen as a source, its history and review can provide an auditable change and rollback path for tenant configuration.
- A custom REST API or Kubernetes-native tenant resource can be explored later
  if new requirements show this simpler approach is unsuitable; follow-up
  decisions will define the rest of the service.
