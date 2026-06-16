---
title: "47. Vendored installs with --vendor"
status: Accepted
relates_to:
  - testing-agents
topics:
  - vendor
  - layered-content
  - workflows
---

# ADR 0047: Vendored installs with `--vendor`

## Status

Accepted

## Context

Layered installs (the default) fetch reusable workflows and agent content from
`fullsend-ai/fullsend@v0` at runtime via sparse checkout. That keeps config repos
small and picks up upstream fixes automatically.

Some workflows need to run unreleased fullsend changes (forks, local workflow
edits, pre-release CI) without publishing tags. A single install flag should
vendor binary + workflow/agent assets at install time; runtime should detect
vendored files without `config.yaml` distribution settings.

## Decision

### Install-time: `--vendor`

`fullsend admin install` and `fullsend github setup` accept `--vendor` and related
flags. `fullsend github sync-scaffold` does **not** take `--vendor`; it
auto-detects vendored mode from the presence of `.defaults/action.yml` in
the config repo and rewrites scaffold files accordingly.

| Flag | Purpose |
|------|---------|
| `--vendor` | Vendor linux/amd64 binary, reusable workflows, composite actions, and agent content |
| `--fullsend-source <dir>` | Explicit fullsend checkout for content walks and binary cross-compile |
| `--fullsend-binary <path>` | Explicit Linux ELF; skips cross-compile (requires `--vendor`) |

Source resolution (shared by binary and content) in `internal/binary`:

1. `--fullsend-source` (validated checkout: `go.mod`, `cmd/fullsend/`)
2. `ModuleRoot()` when CWD is inside a checkout
3. GitHub source fetch at CLI version (released CLI only)

Without `--vendor`, install removes stale vendored binary and content paths and
renders thin callers with upstream `uses: fullsend-ai/fullsend/.../reusable-*.yml@v0`.

### Vendor manifest

`--vendor` writes `vendor-manifest.yaml` listing every vendored path plus
`binary_path`:

| Install mode | Manifest path |
|--------------|---------------|
| Per-org (`.fullsend` config repo) | `vendor-manifest.yaml` |
| Per-repo | `.fullsend/vendor-manifest.yaml` |

The manifest is committed in the same batch as vendored content. Cleanup when
`--vendor` is off reads the manifest from the target repo (via forge API) and
deletes listed paths — no local fullsend checkout required. Legacy installs
without a manifest fall back to embed-derived path enumeration.

### Analyze behavior

Scaffold and vendored assets are reported separately:

- **Workflows layer** — always checks embed-derived managed paths
  (`ManagedPaths(false)`): thin callers, shim, `customized/` gitkeeps, and
  `CODEOWNERS`. Vendored marker presence does not expand this list.
- **Vendor layer** — reports vendored binary/marker presence, manifest
  alignment (missing paths, legacy installs without manifest), and optional
  source alignment when `--fullsend-source` is passed to `fullsend admin analyze`
  (or when the CLI version can resolve a source tree).

Vendored misalignment surfaces under the **vendor** layer, not workflows.

### Runtime: file-presence detection

Reusable workflows detect vendored installs before sparse checkout:

- **All modes:** `.defaults/action.yml` in the checked-out repo (committed by `--vendor`, or populated by sparse checkout at runtime)

When present, upstream sparse checkout is skipped. Infra is referenced from
`.defaults/` (`uses: ./.defaults/.github/actions/...`, `uses: ./.defaults/`).
Layered agent content is copied from `.defaults/internal/scaffold/fullsend-repo/`
onto the workspace root at job start (inline prepare step).

Thin caller `uses:` paths are rendered at install/sync time (local `./...` when
`--vendor`, upstream `@v0` when layered).

### Trust boundary for runtime defaults

Reusable workflows gate upstream sparse checkout on `hashFiles('.defaults/action.yml',
'.fullsend/.defaults/action.yml') == ''` — when vendored markers are absent, the
job fetches defaults from `fullsend-ai/fullsend` at the configured ref.

That gate is an optimization, not a security control. Whoever can write to the
config repo (per-org `.fullsend`, or a target repo's `.fullsend/` tree in
per-repo mode) already controls which workflows and composite actions run in
enrolled repos. A writer with that access could omit or replace vendored marker
files to change which defaults are fetched — equivalent to authoring or editing
workflow YAML directly. Branch protection and CODEOWNERS on `.fullsend` (and
target-repo guardrails) remain the enforcement layer.

### What this PR removes

These existed on earlier iterations of the distribution-mode branch and are
dropped in favor of `--vendor` plus runtime marker detection:

- `distribution.mode` / `distribution.upstream.ref` in org and per-repo config
- `--distribution-mode`, `--upstream-ref` CLI flags
- `distribution_mode` workflow input
- `upstreamembed.go` (content read from resolved source tree instead)

## Consequences

- **Positive:** One flag, no config block, runtime auto-detect; dev/CI can test unreleased workflow changes.
- **Negative:** Deleting vendored files without re-install leaves broken local `uses:` paths until sync-scaffold or re-install.
- **Neutral:** Default layered behavior unchanged for installs without `--vendor`.

## References

- [Installation guide](../reference/installation.md)
- [Testing workflows](../guides/dev/testing-workflows.md)
- ADR 0031 (reusable workflows for distribution)
- ADR 0033 (per-repo installation mode)
- ADR 0035 (layered content resolution)
