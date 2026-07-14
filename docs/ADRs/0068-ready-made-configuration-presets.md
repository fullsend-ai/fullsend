---
title: "68. Ready-made configuration presets for simplified installation"
status: Accepted
relates_to:
  - agent-infrastructure
  - governance
  - security-threat-model
topics:
  - configuration
  - github-setup
  - installation
---

# 68. Ready-made configuration presets for simplified installation

Date: 2026-06-29

## Status

Accepted

## Context

`fullsend github setup` today spreads installation decisions across many CLI
flags (`--mint-url`, `--inference-project`, `--inference-region`, and others)
and separate enrollment steps: operators run `fullsend mint enroll` to register
repos with the token mint, and the installer provisions inference WIF
infrastructure via the inference layer
([ADR 0033](0033-per-repo-installation-mode.md),
[ADR 0029](0029-central-token-mint-secretless-fullsend.md)). The all-in-one
`fullsend admin install` command is deprecated in favor of `fullsend github
setup`.

Per-repo configuration lives in `.fullsend/config.yaml` within the target
repository ([ADR 0033](0033-per-repo-installation-mode.md)), but key runtime
settings (mint endpoint, inference backend) are only partially represented
there; much of the effective configuration still comes from flags and ephemeral
provisioning. That makes repeatable, vendor-curated installs harder than they
need to be.

[ADR 0064](0064-deprecate-customized-directory-overlay.md) deprecates the
`customized/` directory overlay; `config.base.yaml` is the successor mechanism
for distributing a shared baseline into each target repo's `.fullsend/`.

This ADR applies only to **per-repo** installation. Per-org installation via a
dedicated `<org>/.fullsend` config repo is deprecated and out of scope
([ADR 0044](0044-deprecate-per-org-installation-mode.md)).

[ADR 0029](0029-central-token-mint-secretless-fullsend.md) already treats
`job_workflow_ref` as the trust binding for mint authorization. Shared-infrastructure
mint workflow pinning and `job_workflow_ref` validation are decided in
[ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md). The same
pattern may authorize inference backends without per-adopter enrollment when
the caller's workflow is pinned to definitions in `fullsend-ai/fullsend`;
inference-backend validation of `job_workflow_ref` remains open for a follow-on ADR.

## Options

- **Flags only (status quo):** Flexible for advanced operators, but every
  adopter must understand mint and inference provisioning details.
- **Single monolithic `config.yaml`:** Simpler than flags, but mixes
  vendor-provided defaults with repo-specific overrides and complicates upgrades
  of the preset layer.
- **Layered base + overlay files (chosen):** Separate vendor baseline
  (`.fullsend/config.base.yaml`) from repo overrides (`.fullsend/config.yaml`),
  resolved through accessor methods. Supports preset distribution and clean
  upgrades.

## Decision

**1. Move installation settings into configuration files.** Mint URL,
inference provider and backend parameters, and other values currently
supplied via CLI flags belong in the per-repo configuration under `.fullsend/`.
The installer reads configuration instead of reconstructing it from flags.
Decision 1 applies to `fullsend github setup` (single-repo install). The
[ADR 0057](0057-repos-management.md) bulk manifest path (`fullsend repos
install` / `sync`) remains a distinct operator mechanism using GitHub Secrets
and Variables until a follow-on change migrates it to per-repo config files.

**2. Layered configuration with accessor-based lookup.** Configuration is
stored in the target repository as:

- `.fullsend/config.base.yaml` — the base layer (vendor preset or repo baseline).
- `.fullsend/config.yaml` — the user overlay for repo-specific customization.

**Relationship to the three-tier model.** [ADR 0003](0003-org-config-repo-convention.md)
and `docs/architecture.md` describe configuration inheritance as upstream
defaults, then org `.fullsend`, then per-repo overrides. Per-repo installation
is the sole supported deployment model; the dedicated org config repo is
deprecated ([ADR 0044](0044-deprecate-per-org-installation-mode.md)).
`config.base.yaml` in each target repo fills the org tier's former
configuration role — not a revival of per-org installs. A vendor preset
committed as `config.base.yaml` can be reused across repos in one org or
distributed unchanged across org boundaries without a separate `<org>/.fullsend`
repository. `config.yaml` remains the per-repo overlay. Lookup order is overlay
→ base → **code defaults** in `internal/config` (and related packages): values
not set in either file still resolve from compiled-in defaults, as today.
Accessor methods implement that full chain; direct struct field access does not.

All runtime and installer lookups go through methods on a configuration
accessor (for example `MintURL()`, `InferenceProvider()`), not direct struct
field access. Each accessor implements its own merge and fallback rules across
layers (scalar override, deep merge, or required-in-overlay semantics as
appropriate). The design must allow additional file layers beyond base + overlay
in the future without changing call sites.

**3. `--config` install flag for ready-made presets.** `fullsend github setup`
accepts `--config <path-or-url>` and optional `--config-hash <sha256>`. The installer:

1. Fetches or reads the preset document. When `--config-hash` is supplied,
   the installer validates fetched content against that hash; signing and
   preset URL allowlisting are deferred to a follow-on ADR.
2. Commits it as `.fullsend/config.base.yaml` in the target repository.
3. Writes a stub `.fullsend/config.yaml` containing only comments and empty or
   minimal override fields for the adopter to customize.

Presets may be local files or HTTPS URLs. The flag is optional; advanced
installs that assemble configuration manually remain supported.

**4. Omit per-adopter mint and inference enrollment (target state).**
When a preset targets shared infrastructure authorized via `job_workflow_ref`
to workflows in `fullsend-ai/fullsend`, the installer does not run mint
enrollment or inference WIF provisioning. Trust is established by the
workflows the preset references, not by registering each repo with backend
operators at install time.

Workflow pinning and mint-side `job_workflow_ref` validation for shared
infrastructure are decided in [ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md).
Inference-backend validation of `job_workflow_ref` and compatibility for
self-managed mint or inference paths that still require explicit enrollment
remain open for follow-on ADRs. Until inference follow-on ADRs land,
preset-based installs continue requiring inference enrollment steps where
applicable.

## Consequences

- Common installs become a single command with a preset URL instead of a long
  flag list plus separate mint and inference enrollment steps.
- The `internal/config` package gains a layered accessor API; direct field reads
  outside that package become a lint or review violation.
- Preset upgrades can refresh `.fullsend/config.base.yaml` while preserving
  repo edits in `.fullsend/config.yaml`, provided merge semantics are
  documented per field.
- Existing installations without `config.base.yaml` remain valid — accessors
  treat a missing base file as an empty layer, falling through to code defaults.
- Preset URLs are a supply-chain trust surface; signing and preset URL
  allowlisting are follow-on concerns (`--config-hash` validation is in scope
  for `--config` installs).
- Self-managed and air-gapped deployments keep working via hand-authored
  configuration or flags that bypass shared presets.
- Mint operators shift from per-repo onboarding to backend policy (workflow
  allowlists per [ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md));
  inference operators follow when inference-backend policy is decided.
  Security review moves to preset curation and `job_workflow_ref` pinning
  rather than install-time enrollment calls where shared infrastructure applies.
