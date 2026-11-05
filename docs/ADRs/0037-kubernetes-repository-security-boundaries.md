---
title: "37. Kubernetes repository security boundaries"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - kubernetes
  - isolation
  - identity
  - mint
---

# 37. Kubernetes repository security boundaries

Date: 2026-11-02

## Status

Accepted

## Context

We aim to enable Kubernetes as a runtime for Fullsend. Running agent workloads
there requires the repository-scoped security boundary provided by forge-hosted
execution, including a way to authenticate those workloads to the mint without
requiring each repository to enroll with it. Public mints must remain supported
as established by [ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md).

The mint currently authenticates GitHub Actions tokens and checks repository
access policy and workflow provenance; its [claims and policy functions][mint-claims]
and [JWKS verifier][mint-jwks] at `efb495b14cfeefdb136fd3f9020ba50de05b4c60`
expect GitHub-specific claims. A Kubernetes service account token identifies a
cluster, namespace, and service account, but does not identify a forge repository.

This extends the workload-identity direction of
[ADR 0029](0029-central-token-mint-secretless-fullsend.md) and the repository
boundary of [ADR 0033](0033-per-repo-installation-mode.md), while preserving
the per-agent sandbox isolation of
[ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md).
The [infrastructure problem](../problems/agent-infrastructure.md) and
[threat model](../problems/security-threat-model.md) describe the wider runtime
and containment questions. This ADR aims to preserve repository-scoped authority
when agents run on Kubernetes and authenticate to the mint with workload identity.

## Options

**Namespace metadata:** map the verified token's namespace to repository metadata
through the Kubernetes API; this preserves short names but requires protected
metadata and authenticated API access from every mint.

**Signed namespace identity:** include a strong repository hash in the namespace
name and validate it against the requested repository URL; this removes metadata
lookups and per-repository mint enrollment, at the cost of shorter readable names.

## Decision

We assign one Kubernetes namespace per repository to preserve repository-scoped
authority when Fullsend runs on Kubernetes. Each mint request carries the full
canonical repository URL. After verifying the cluster's service account token
through JWKS, the mint derives the expected namespace name from that URL and
requires an exact match with the signed namespace claim. Public mints require
no per-repository enrollment or namespace metadata lookup; forge installation,
role, permission, and explicit cross-repository grant checks still apply.

Namespace names follow `[provider]-[core_name]-[hash]`: a one- or two-character
provider ID, a sanitized readable core of at most seven characters, and the full
SHA-256 digest in 52 lowercase, unpadded base32 characters. The maximum length is
63 characters. Truncation prioritizes the repository name and removes the left
of the organization path first. Separate readable labels retain longer host,
owner/subgroup, and repository names; labels do not authorize mint requests.

Cluster issuers and namespace provisioners remain trusted: the provisioner must
verify authority to run for a repository before admitting its workloads.
Namespace RBAC, network and admission policies complement per-agent sandboxes.
The [Kubernetes repository identity v1 contract][identity-contract] defines the
naming, label, token-validation, and lifecycle rules shared by provisioners and
mints; incompatible identity changes require a new major contract version.

## Forge scope

This boundary applies to GitHub (`gh`), GitLab (`gl`, including nested groups),
and Forgejo (`fj`), including self-hosted instances distinguished by canonical
URL. It defines Kubernetes caller identity for each forge; credential issuance
still requires a forge-specific mint adapter and equivalent repository scoping,
and unsupported adapters must reject requests.

## Consequences

- Public mints can authenticate Kubernetes workloads without per-repository
  enrollment or a namespace-to-repository registry at the mint.
- Identity follows the repository URL, so v1 accepts the residual risk that old
  workload tokens can authorize access to a replacement repository at that URL
  until old token issuance stops and the remaining credentials expire.
- The full digest leaves seven characters for the namespace's readable core,
  with longer repository names available in labels.
- Repository moves require workload migration, and immediate Kubernetes
  object-revocation checks require TokenReview rather than JWKS alone.
- Namespace policy complements per-agent sandboxing but does not protect against
  a compromised cluster administrator, control plane, or shared host kernel.

[mint-claims]: https://github.com/fullsend-ai/fullsend/blob/efb495b14cfeefdb136fd3f9020ba50de05b4c60/internal/mintcore/claims.go
[mint-jwks]: https://github.com/fullsend-ai/fullsend/blob/efb495b14cfeefdb136fd3f9020ba50de05b4c60/internal/mintcore/jwks_verifier.go
[identity-contract]: ../normative/kubernetes-repository-identity/v1/README.md
