---
title: "63. Ready-made configuration presets for simplified installation"
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

# 63. Ready-made configuration presets for simplified installation

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

[ADR 0029](0029-central-token-mint-secretless-fullsend.md) already treats
`job_workflow_ref` as the trust binding for mint authorization. The same
pattern can authorize inference backends without per-adopter enrollment, when
the caller's workflow is pinned to definitions in `fullsend-ai/fullsend`.
Concrete workflow pinning and backend policy are left to follow-on ADRs.

## Options

- **Flags only (status quo):** Flexible for advanced operators, but every
  adopter must understand mint and inference provisioning details.
- **Single monolithic `config.yaml`:** Simpler than flags, but mixes
  vendor-provided defaults with repo-specific overrides and complicates upgrades
  of the preset layer.

## Decision

**1. Move installation settings into configuration files.** Mint URL,
inference provider and backend parameters, and other values currently
supplied via CLI flags belong in the per-repo configuration under `.fullsend/`.
The installer reads configuration instead of reconstructing it from flags.

**2. Layered configuration with accessor-based lookup.** Configuration is
stored in the target repository as:

- `.fullsend/config.base.yaml` — the base layer (vendor preset or repo baseline).
- `.fullsend/config.yaml` — the user overlay for repo-specific customization.

**Relationship to the three-tier model.** [ADR 0003](0003-org-config-repo-convention.md)
and `docs/architecture.md` describe configuration inheritance as upstream
defaults, then org `.fullsend`, then per-repo overrides. Per-repo installation
drops the dedicated org config repo; `config.base.yaml` fills the org tier's
configuration role. A vendor preset committed as `config.base.yaml` can be reused across
repos in one org or distributed unchanged across org boundaries — the same
portability benefit org-wide config provided, without a separate `<org>/.fullsend`
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
accepts `--config <path-or-url>`. The installer:

1. Fetches or reads the preset document. The installer validates fetched content
   against a content hash when provided; full integrity verification (signing,
   allowlisting) is deferred to a follow-on ADR.
2. Commits it as `.fullsend/config.base.yaml` in the target repository.
3. Writes a stub `.fullsend/config.yaml` containing only comments and empty or
   minimal override fields for the adopter to customize.

Presets may be local files or HTTPS URLs. The flag is optional; advanced
installs that assemble configuration manually remain supported.

**4. Drop per-adopter mint and inference enrollment from the install path.**
When a preset targets shared infrastructure authorized via `job_workflow_ref`
to workflows in `fullsend-ai/fullsend`, the installer does not run mint
enrollment or inference WIF provisioning. Trust is established by the
workflows the preset references, not by registering each repo with backend
operators at install time.

Follow-on ADRs will specify which upstream workflows are pinned, how inference
backends validate `job_workflow_ref`, and compatibility for self-managed mint
or inference paths that still require explicit enrollment. Until those ADRs
land, preset-based installs continue requiring that enrollment steps be
performed.

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
- Preset URLs are a supply-chain trust surface; integrity verification
  (hashing, signing) is a follow-on concern.
- Self-managed and air-gapped deployments keep working via hand-authored
  configuration or flags that bypass shared presets.
- Mint and inference operators shift from per-repo onboarding to backend policy
  (workflow allowlists); security review moves to preset curation and
  `job_workflow_ref` pinning rather than install-time enrollment calls.
