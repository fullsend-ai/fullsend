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

# 123. GitOps-managed tenant configuration for Fullsend

Date: 2026-09-24

## Status

Accepted

## Context

The `repos.yaml` manifest already describes desired Fullsend installations
across GitHub and GitLab, including shared defaults and per-repository
settings. `fullsend repos install` can load a manifest from a local file or an
HTTPS URL and reconcile the declared repositories ([repo management
reference](../cli/repos.md)).

An enterprise-managed Fullsend offering may need a centrally managed view of
the repositories associated with each tenant. Before choosing its services or
deployment model, we can use a Git repository as the source of truth and build
on the existing manifest format. Whether to offer that centralized service
remains open; self-managed per-repository installs continue to serve local
ownership and operation.

See also the [agent infrastructure problem
document](../problems/agent-infrastructure.md).

## Decision

Any centrally managed Fullsend offering will use one existing `repos.yaml`
version 1 manifest per tenant as its GitOps desired state. Enterprise
administrators keep those files in Git; central service components load and
validate them from configured HTTPS sources. The human-managed format remains
`repos.yaml`; this decision does not introduce a new public tenant YAML schema.

Central service components will use an internal `TenantConfig` model built
from the validated manifest and trusted service-side tenant and source
revision metadata. Tenant identity and revision do not come from new fields
in `repos.yaml`.

A **tenant** names the centrally managed configuration boundary; each
manifest describes that tenant's GitHub and/or GitLab repositories. This is
not equivalent to Fullsend's deprecated per-organization installation mode.

Self-managed per-repository installations remain first-class and keep their
existing manifest and CLI behavior. This ADR decides only how tenant
repository configuration is authored and consumed. It does not decide whether
the centralized service will be offered, its public management API or
Kubernetes resources, its dispatcher or launcher, its infrastructure, or a
repo-less execution context. Those needs can be addressed in follow-up
decisions.

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

- This is the smallest, simplest incremental decision for exploring what an enterprise-managed Fullsend installation could look like: it reuses the existing manifest and Git review workflow.
- Git history and review can provide an auditable change and rollback path for each tenant's repository configuration.
- Central service components will need to fetch and validate each manifest and build an internal `TenantConfig` carrying the parsed configuration, tenant identity, and source revision.
- A custom REST API or Kubernetes-native tenant resource can be explored later if GitOps manifests prove unsuitable as requirements emerge.
- Follow-up decisions will define the centralized service offering, dispatcher and launcher, and any repo-less execution context; self-managed per-repository installs remain supported.
