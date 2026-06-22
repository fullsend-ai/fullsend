---
title: "46. Provider-backed policy composition"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - policy
  - providers
  - composition
  - sandbox
---

# 46. Provider-backed policy composition

Date: 2026-06-22

## Status

Accepted

## Context

ADR 0024 established per-agent harness files with a `policy` field
pointing to an OpenShell policy YAML. Each policy file contains
filesystem, process, and network restrictions for one agent.

In practice, every policy file duplicates the same network rule blocks
for shared services: Vertex AI inference endpoints, GitHub API access,
package registries (npm, PyPI, Go modules), and gitleaks binary
downloads. Six agents × four service groups = the same endpoints and
binaries repeated in every combination.

This duplication creates maintenance burden: when a service adds or
changes endpoints, every policy file must be updated independently.
The fix.yaml policy comments "Identical to the code agent policy,"
making the redundancy explicit.

OpenShell v0.0.37 introduced provider-backed policy composition
(NVIDIA/OpenShell#1037). When a provider is attached to a sandbox and
has a registered profile, the gateway merges the profile's network
rules into the effective policy at fetch time under reserved
`_provider_*` keys. This is additive-only and keeps provider rules
isolated from user/agent rules.

## Decision

**Policy files must not contain network_policies.** All network access
is provided through provider profiles — this is the single mechanism
for granting network access to sandboxed agents. Policy files define
only non-composable sandbox restrictions: filesystem access, landlock,
and process identity.

Define custom provider profiles for each service and import them into
the gateway during `fullsend run`. Harnesses declare which providers
they need — each contributes network rules automatically via
composition. Because all agents share identical non-network policy
sections, a single `policies/base.yaml` replaces the per-agent
policy files.

Five custom profiles:

| Profile ID | Service | Access |
|---|---|---|
| `fullsend-vertex-ai` | Anthropic API, Google Cloud APIs | read-write |
| `fullsend-github` | GitHub API, Git transport | read-write |
| `fullsend-package-registries` | npm, PyPI, Go modules | read-only |
| `fullsend-gitleaks` | GitHub releases for gitleaks | read-only |
| `fullsend-github-artifacts` | GitHub Actions artifact download | read-only |

Custom profiles are used instead of OpenShell built-ins because our
profiles bundle endpoint combinations specific to fullsend (e.g.,
fullsend-vertex-ai combines Anthropic API + GCP APIs into one profile).

Provider definitions reference these profiles via the `type` field.
No credentials are defined — providers exist for network policy
contribution only. Credential delivery continues via host_files
(ADR 0025 tier 4).

The `providers_v2_enabled` gateway setting and profile imports are
managed automatically by `fullsend run`, consistent with how it
already manages provider creation.

## Consequences

- **Network access is exclusively provider-managed.** Policy files
  never contain `network_policies` — network rules live in provider
  profiles and are composed at fetch time. This eliminates the
  duplication that motivated this change and prevents it from
  recurring.
- Network rules for each service are defined once in a profile YAML.
  Adding or changing endpoints updates one file.
- All agents share a single `policies/base.yaml` for non-composable
  restrictions (filesystem, landlock, process). Per-agent policy
  files are eliminated.
- No schema changes to harness YAML or ProviderDef. Existing custom
  agents with inline network rules keep working — duplicated rules are
  redundant but harmless (composition is additive), but should be
  migrated to providers over time.
- Requires OpenShell >= v0.0.37 and the `providers_v2_enabled` gateway
  setting (set automatically).
- Single profile per service means all agents get the broadest access
  level (read-write for GitHub). If per-agent access differentiation
  is needed later, split into separate profiles (e.g.,
  fullsend-github-rw, fullsend-github-ro).
