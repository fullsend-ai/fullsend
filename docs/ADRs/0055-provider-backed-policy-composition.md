---
title: "55. Provider-backed policy composition"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - policy
  - providers
  - composition
  - sandbox
---

# 55. Provider-backed policy composition

Date: 2026-06-22

## Status

Accepted

## Context

[ADR 0024](0024-harness-definitions.md) established per-agent harness
files with a `policy` field pointing to an OpenShell policy YAML.
Each policy file contains filesystem, process, and network
restrictions for one agent.
[ADR 0038](0038-universal-harness-access.md) introduced URL-based
access for harness resources, and
[ADR 0045](0045-forge-portable-harness-schema.md) defined
forge-portable harness composition with `base:` inheritance. Together
they establish that harness dependencies should be referenceable and
composable — but neither addresses how provider or profile definitions
are resolved when a base harness is remote (see [#2672](https://github.com/fullsend-ai/fullsend/issues/2672)).

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

## Options considered

### Option 1: Monolithic per-agent policies (legacy approach)

Each agent has its own policy YAML with all network rules inline.
Agent designers specify every endpoint and binary in a single file.

**Pros:**
- Self-contained — one file per agent, easy to read in isolation.
- No dependency on providers v2 or OpenShell version.
- Full control per agent.

**Cons:**
- Duplicated network blocks across agents.
- Maintenance burden grows with agent count and service count.
- Easy to drift — one agent's policy gets an endpoint fix, others
  don't.

### Option 2: Composable policies with agent designer freedom

Provide composable provider profiles as an option. Agent designers
choose whether to use providers for network access or keep inline
network rules. A shared `base.yaml` covers filesystem, landlock, and
process restrictions. Agents can mix: use providers for common services
and inline rules for agent-specific endpoints.

**Pros:**
- Flexible — agent designers pick the approach that fits.
- Incremental adoption — no big-bang migration needed.
- Lowest friction to introduce — no convention change required.

**Cons:**
- Two ways to do the same thing — harder to reason about effective
  policy.
- Duplicated endpoints may reappear if designers don't adopt providers.
- No single source of truth for a service's endpoints.

### Option 3: All network access through providers (chosen)

Provider profiles are the single mechanism for granting network access.
Policy files define only non-composable sandbox restrictions:
filesystem access, landlock, and process identity. A single
`base.yaml` replaces per-agent policy files for these non-network
concerns. Harnesses declare which providers they need — each
contributes network rules via composition.

**Pros:**
- Single source of truth per service — endpoint changes update one
  profile.
- Policy files are small and uniform (no network section).
- Clear separation: profiles own network access, policies own sandbox
  restrictions.
- New agents get network access by declaring providers, not copy-pasting
  endpoint blocks.

**Cons:**
- Requires OpenShell >= v0.0.37 and `providers_v2_enabled`.
- Agent designers must understand the provider model.
- Credential-less providers require a workaround
  ([NVIDIA/OpenShell#1978](https://github.com/NVIDIA/OpenShell/issues/1978)).

## Decision

**Adopt Option 3 as a best practice for fullsend scaffold agents.**

The scaffold ships with provider profiles for shared services and a
single `base.yaml` policy. Scaffold harnesses declare providers instead
of inline network rules.

This is a best practice, not an enforcement. Custom agents with inline
network policies continue to work — composition is additive, so
duplicated rules between providers and inline policies are redundant
but harmless. Agent designers who bring their own agents can choose
whichever approach fits their needs. The scaffold serves as the
reference implementation.

Six custom profiles ship with the scaffold:

| Profile ID | Service | Access |
|---|---|---|
| `fullsend-vertex-ai` | Anthropic API, Google Cloud APIs | read-write |
| `fullsend-github` | GitHub API, Git transport | read-write |
| `fullsend-github-ro` | GitHub API, Git transport | read-only |
| `fullsend-package-registries` | npm, PyPI, Go modules | read-only |
| `fullsend-gitleaks` | GitHub releases for gitleaks | read-only |
| `fullsend-github-artifacts` | GitHub Actions artifact download | read-only |

Custom profiles are used instead of OpenShell built-ins because they
bundle endpoint combinations specific to fullsend (e.g.,
fullsend-vertex-ai combines Anthropic API + GCP APIs).

Provider definitions use dummy credentials as a workaround for
[NVIDIA/OpenShell#1978](https://github.com/NVIDIA/OpenShell/issues/1978)
— providers exist for network policy contribution only. Credential
delivery continues via host_files ([ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md) tier 4).

The `providers_v2_enabled` setting and profile imports are managed
automatically by `fullsend run`.

## Consequences

- Scaffold agents use provider profiles for all network access. Policy
  files contain only filesystem, landlock, and process restrictions.
- Network rules for each service are defined once in a profile YAML.
  Adding or changing endpoints updates one file.
- All scaffold agents share a single `policies/base.yaml`. Per-agent
  policy files are eliminated from the scaffold.
- Custom agents are not required to adopt this pattern. Inline network
  policies continue to work, but the scaffold and guides recommend
  providers as the preferred approach.
- The building-custom-agents guide is updated to show providers as
  the recommended way to grant network access.
- Requires OpenShell >= v0.0.37 and the `providers_v2_enabled` gateway
  setting (set automatically).
- GitHub access is split into read-write (`fullsend-github`) and
  read-only (`fullsend-github-ro`) profiles, matching the access
  levels agents had under the previous per-agent policy model.
  Review and retro agents use the read-only variant.
- Provider and profile definitions are currently resolved from local
  directories only. Portability for URL-referenced base harnesses
  requires harness-level declaration and URL-based resolution ([#2672](https://github.com/fullsend-ai/fullsend/issues/2672)).
