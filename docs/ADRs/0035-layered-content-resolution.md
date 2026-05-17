---
title: "35. Layered content resolution"
status: Proposed
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - layering
  - defaults
  - content-resolution
  - scaffold
---

# 35. Layered content resolution

Date: 2026-05-09

## Status

Proposed

## Context

[ADR 0003](0003-org-config-repo-convention.md) designed a three-tier layering
model — `fullsend defaults < org .fullsend config < per-repo overrides` — but
the runtime never implemented it. The scaffold (`internal/scaffold/scaffold.go`)
copies all ~82 files from `internal/scaffold/fullsend-repo/` into every
`.fullsend` repo via the Git Trees API, including agents, skills, harness
configs, policies, and schemas that are identical across orgs.

This causes three problems: upstream improvements require every org to re-run
`fullsend admin install`, the installer overwrites all files on upgrade
(silently reverting org customizations), and there is no visible distinction
between upstream defaults and org-specific content.

With reusable workflows (thin callers in `.fullsend` that delegate to
`fullsend-ai/fullsend` via `workflow_call`), the pipeline checks out `.fullsend`
at runtime, enabling a workspace merge that implements the layering model.

## Decision

Three coordinated changes, applied uniformly to both installation modes
(per-org and per-repo):

**A. Customizations live in `customized/`.** The scaffold installs a
`customized/` directory with empty subdirs (`agents/`, `skills/`, `schemas/`,
`harness/`, `policies/`, `scripts/`, `env/`) containing `.gitkeep` files. The
location differs by install mode:

- **Per-org** (`fullsend admin install <org>`): `customized/` in the org-level
  `<org>/.fullsend` repo. Orgs add overrides here that apply to all enrolled
  repos.
- **Per-repo** (`fullsend admin install <owner/repo>`): `.fullsend/customized/`
  in the target repo itself. The repo adds overrides that apply only to that
  repo.

In both cases the main dirs (`agents/`, `skills/`, etc.) are not committed —
they are populated at runtime from upstream.

**B. Runtime layering via reusable workflows.** Each reusable workflow adds a
"Prepare workspace" step that sparse-checkouts upstream defaults from
`fullsend-ai/fullsend@v0`, copies them into the main dirs (`agents/`, `skills/`,
etc.), then copies customizations on top so override files replace upstream
defaults. The workflow inspects `install_mode` to resolve the correct
customization base:

- `per-org`: reads from `customized/`
- `per-repo`: reads from `.fullsend/customized/`

The harness sees a single flat workspace in both modes — no changes to
`ResolveRelativeTo()` or `--fullsend-dir`.

**C. Scaffold stops writing upstream defaults.** `WalkFullsendRepo` skips files
in layered directories (`agents/`, `skills/`, `schemas/`, `harness/`,
`policies/`, `scripts/`, `env/`) and upstream-only directories (`.github/actions/`,
`.github/scripts/`). The installer writes only mode-specific files and
`customized/` gitkeeps. `CustomizedDirs()` returns paths for per-org installs;
`PerRepoCustomizedDirs()` returns `.fullsend/customized/` paths for per-repo
installs.

File categories after this change:

**Per-org install** (`<org>/.fullsend` repo):

- **Org-only** (~11 files): `dispatch.yml`, thin callers, shim template,
  `AGENTS.md` — always installed, never overwritten by upstream.
- **Org overrides** (7 `.gitkeep` files):
  `customized/{agents,skills,schemas,harness,policies,scripts,env}/` —
  scaffold creates the structure, orgs add real files.
- **Upstream defaults** (~60 files): agents, skills, schemas, harness,
  policies, scripts, env — authoritative in `fullsend-ai/fullsend`, provided
  at runtime via sparse checkout of the release tag.
- **Upstream infrastructure** (~5 files): composite actions,
  `setup-agent-env.sh` — referenced directly from upstream, never in `.fullsend`.

**Per-repo install** (target repo):

- **Repo scaffolding** (~2 files): `.github/workflows/fullsend.yaml` (shim
  workflow), `.fullsend/config.yaml` (per-repo config).
- **Repo overrides** (7 `.gitkeep` files):
  `.fullsend/customized/{agents,skills,schemas,harness,policies,scripts,env}/` —
  scaffold creates the structure, repo owners add real files.
- **Upstream defaults** and **Upstream infrastructure**: same as per-org — provided
  at runtime, never committed to the repo.

## Consequences

- Fresh per-org install produces a slim `.fullsend` repo (~24 files instead of
  ~82). Per-repo install adds only ~16 files to the target repo.
- Upgrades never overwrite customized content — the installer does not touch
  files in `customized/` (per-org) or `.fullsend/customized/` (per-repo).
- Upstream improvements to agents, skills, and schemas appear automatically at
  runtime without re-install, regardless of install mode.
- Overrides are explicit and auditable: in `customized/` for per-org, in
  `.fullsend/customized/` for per-repo.
- Requires a public upstream repo (`fullsend-ai/fullsend` is already public).
- Runtime availability: sparse checkout of upstream defaults requires
  github.com to be reachable at workflow execution time. GitHub Actions already
  depends on github.com, so this adds no new availability boundary.
- Migration for existing orgs: orgs that customized files in top-level dirs
  (e.g., `agents/triage.md`) must move them to `customized/` (e.g.,
  `customized/agents/triage.md`) before upgrading. The installer can detect
  and warn about this during `fullsend admin install`.
