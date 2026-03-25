---
title: "3. Org-level configuration lives in a conventional repo"
status: Proposed
relates_to:
  - governance
  - codebase-context
  - intent-representation
  - architectural-invariants
  - agent-architecture
  - agent-infrastructure
topics:
  - configuration
  - adoption
  - convention
---

# 3. Org-level configuration lives in a conventional repo

Date: 2026-03-25

## Status

Proposed

## Context

An organization adopting fullsend must configure it: the intent repo, the
architecture documentation, runtime and model, infrastructure, sandbox
defaults, org-specific agents and skills, and per-repo overrides. (See issue
#75.)

Today no conventional location exists. Without one, every tool must be told
where to find config, or each tool invents its own convention.

## Options

### Option 1: `<org>/.fullsend` repo

Each adopting organization creates a repo named `.fullsend` in their org or
group (GitHub org, GitLab group, Forgejo org, or equivalent) — the single
entry point for all fullsend configuration.

The dot-prefix mirrors `.github` for org-level configuration, signaling
"infrastructure/meta" rather than application code. The name avoids collisions
with other tooling.

**Pros:**
- Clean, memorable convention. Tooling looks in exactly one place.
- Org-owned — the adopting org controls configuration, permissions, and review.
- Decoupled from fullsend's release cycle.
- Dot-prefix follows an established pattern (`.github`, `.gitlab`).
- CODEOWNERS enforces appropriate approval on configuration changes.

**Cons:**
- Dot-prefixed repos sort differently and sometimes hide in forge UIs.
- Adopters must learn a new convention.
- Dot-prefix visibility varies across platforms.

### Option 2: `<org>/fullsend-config` repo

Same as Option 1, without the dot-prefix.

**Pros:**
- Appears in normal alphabetical listings on all forges.
- Name is self-documenting.

**Cons:**
- Lacks the dot-prefix signal for "meta/infra."
- Slightly more generic name; collision unlikely but possible.

### Option 3: Configuration inside an existing platform meta-repo

Put fullsend configuration in a subdirectory of the org's existing platform
meta-repo (GitHub's `.github` or equivalent). GitLab and Forgejo lack an
equivalent convention — itself a problem with this option.

**Pros:**
- No new repo on platforms that already have a meta-repo.
- Permissions may already exist.

**Cons:**
- Mixes concerns. Meta-repos serve issue templates, CI config, and community
  health files; adding agent configuration overloads their purpose.
- Tooling must look inside a subdirectory of a multi-purpose repo.
- CODEOWNERS for fullsend config must coexist with other CODEOWNERS rules.
- Growing fullsend config (agents, skills, workflows) would dominate the
  meta-repo.
- Not portable: GitHub's `.github` has no equivalent on GitLab or Forgejo.

### Option 4: External configuration management system

Use a runtime configuration store (Consul, etcd, HashiCorp Vault, AWS
Parameter Store) as the source of truth for org-level config.

**Pros:**
- Purpose-built for configuration: dynamic updates, access control, secret
  storage.
- Some organizations already run these systems.

**Cons:**
- Breaks the "everything auditable in version control" principle — no PR
  review, no CODEOWNERS, no merge history.
- Introduces infrastructure dependencies that vary across environments.
- Each store has its own API, auth model, and operational requirements.
- Config changes bypass the review and governance processes fullsend relies
  on.
- Structural configuration (agents, workflows, intent repo location) belongs
  in version control; only secrets and dynamic runtime values belong in
  systems like Vault.

### Option 5: Forge-native org/group settings

Store fullsend configuration in the forge's own org-level settings (GitHub
org settings API, GitLab group variables, Forgejo org settings).

**Pros:**
- No additional repo or system.

**Cons:**
- Opaque: no version control, no merge-request review, no audit trail
  beyond platform logs.
- Each platform exposes different settings APIs; a model that works on
  GitHub may not map to GitLab or Forgejo.
- Key-value or flat structures lack the expressiveness for agent
  definitions, workflow overrides, or layered inheritance.
- Platform admin permissions govern changes, not CODEOWNERS-style
  path-level review.

### Option 6: Hosted control plane / SaaS

A hosted web service where orgs configure fullsend via a UI or API.

**Pros:**
- Could offer a polished experience with validation, previews, and guided
  setup.

**Cons:**
- Introduces a central dependency the org does not control.
- Moves configuration out of version control — no PR review, no CODEOWNERS,
  no git history.
- Creates a high-value attack target: compromising the control plane
  compromises every org.
- Contradicts fullsend's design philosophy: the repo is the coordinator,
  not a service.

### Option 7: Configuration inside the fullsend repo itself

Each adopting org gets a directory in the fullsend repo (e.g., `orgs/nonflux/`).

**Pros:**
- Everything in one place; maintainers see all adopters.

**Cons:**
- Couples org config to fullsend's release cycle and permissions.
- Orgs cannot modify config without a PR to fullsend.
- Violates the principle that org config is org-owned.
- Conflates the framework with its instances.

## Decision

Adopting organizations create a **`<org>/.fullsend`** repo as the conventional
location for all org-level fullsend configuration.

This repo is the root of the dependency graph for fullsend in an org. All
tooling — harness, trigger layer, agent runtimes, drift scanners — starts here.
The convention is:

1. **Tooling looks for `<org>/.fullsend`** to bootstrap. If the repo exists,
   the org has adopted fullsend. If not, there is nothing to configure.
2. **The `.fullsend` repo points to everything else:** the intent repo,
   architecture repo, infrastructure config, agent definitions, workflow
   definitions, sandbox profiles, and per-repo overrides.
3. **The adopting org governs the `.fullsend` repo.** Its CODEOWNERS, branch
   protection, and review requirements follow the org's governance model.
   Changes here are governance-level changes (see
   [governance.md](../problems/governance.md) — configuration security).
4. **Agents cannot modify this repo.** The `.fullsend` repo defines agent
   behavior; agents must not modify their own configuration. Enforcement:
   exclude bot/service accounts from write access and require human approval
   via CODEOWNERS on all paths. This aligns with the principle that CODEOWNERS
   files are always human-owned.

### Repo structure (initial)

```
.fullsend/
  config.yaml              # Top-level org configuration
  guardrails.yaml          # Org-wide guardrails (separate file for CODEOWNERS)
  agents/                  # Org-specific agent definitions (extends base set)
  skills/                  # Org-specific skills (extends base set)
  workflows/               # Workflow overrides/extensions
  repos/                   # Per-repo configuration overrides
    <repo-name>.yaml
```

The `config.yaml` contains pointers and org-wide defaults. This repo holds
structural configuration only; secrets (API keys, credentials) are managed
separately via the org's secret management system (Vault, sealed secrets, etc.).

```yaml
version: 1                          # Schema version for future evolution

# Where to find org-specific resources
intent_repo: <org>/features         # or <org>/intent
architecture_repo: <org>/architecture

# Agent runtime defaults
runtime:
  harness: claude-code              # or opencode
  model: claude-sonnet-4-6

# Infrastructure
infrastructure:
  platform: kubernetes              # or github-actions, etc.
  # platform-specific config follows

# Sandbox defaults
sandbox:
  network_policy: restricted
  filesystem: ephemeral
```

Per-repo overrides in `repos/<repo-name>.yaml` can override org defaults
(within the bounds of org-wide guardrails that cannot be weakened).

The exact schema will evolve. The decision here is about the convention and
location, not the schema details.

### Inheritance model

Base fullsend provides default agents, skills, and workflows. The `.fullsend`
repo extends or overrides them for the org. Per-repo config in
`repos/<repo-name>.yaml` further overrides for specific repos. The layering is:

```
fullsend defaults < org .fullsend config < per-repo overrides
```

Org config can add agents, skills, and workflows. It can override defaults. It
cannot weaken org-wide guardrails. Guardrails live in a separate
`guardrails.yaml` file so that CODEOWNERS can enforce stricter review on that
file specifically (CODEOWNERS operates on file paths, not YAML sections).

**Limitation:** per-repo overrides in `repos/<repo-name>.yaml` use the same
layering mechanism as org-over-upstream. Without additional enforcement, a repo
override could weaken org-level guardrails. The exact mechanism for protecting
org guardrails from repo-level override is tracked as a follow-up (see issue
#84).

## Consequences

- **Single, discoverable configuration root.** No ambiguity about where
  config lives.
- **Org-owned.** The adopting org controls permissions, review, and release
  cadence for its own config — no PRs to fullsend needed.
- **Security-critical asset.** The `.fullsend` repo defines agent behavior
  and must be protected accordingly: restricted write access, required
  reviews, audit logging. (See [governance.md](../problems/governance.md) —
  configuration security.)
- **Stable convention for tooling.** Harness assembly, trigger layer, and
  CLI tooling all assume `.fullsend` exists and follow pointers from there.
- **Centralized per-repo overrides.** All org configuration is auditable in
  one place rather than scattered across repos.
- **`docs/problems/applied/` stays for problem analysis**, not operational
  config.
- **Natural bootstrap for adoption.** A fullsend installer's first step is
  creating the `.fullsend` repo and populating initial config. Everything
  else flows from that repo existing.
- **Guardrail override gap.** The inheritance model does not yet define how
  an org protects specific settings from being weakened by per-repo overrides.
  Separating guardrails into their own file solves the org-level CODEOWNERS
  problem, but the repo-level override boundary needs further design (issue
  #84).
