# Sandbox Images

Fullsend agents run inside sandboxed container images managed by
[OpenShell](https://github.com/nvidia/openshell-community).  This directory
contains the Containerfiles and supporting scripts for those images.

## Image hierarchy

```
ghcr.io/nvidia/openshell-community/sandboxes/base  (upstream, multi-arch)
  └── ghcr.io/fullsend-ai/fullsend-sandbox          (base sandbox)
        └── ghcr.io/fullsend-ai/fullsend-code        (code agent)
```

| Image | Directory | Description |
|-------|-----------|-------------|
| `fullsend-sandbox` | [`images/sandbox/`](sandbox/) | Base sandbox with Claude Code, rsync, jq, acli, gitleaks, tirith, pre-commit, gitlint, and the ProtectAI DeBERTa-v3 ONNX model for prompt injection detection. |
| `fullsend-code` | [`images/code/`](code/) | Extends `fullsend-sandbox` with Go toolchain and scan-secrets wrapper. Used by the code-implementation agent. |

Both images are built for **linux/amd64** and **linux/arm64**.

## Tags and versioning

The CI workflow (`.github/workflows/sandbox-images.yml`) produces the
following tags depending on the trigger event:

| Tag | When produced | Purpose |
|-----|---------------|---------|
| `latest` | Push to `main` branch | Production tag. Harness configs and downstream consumers reference this. |
| `dev` | Any non-PR event (push to `main`, tag push, `workflow_dispatch`) | Pre-release tag for CI and local testing. PR builds use this as the base image fallback when no digest is available. |
| `<version>` (e.g. `1.2.3`) | Tag push matching `v*` | Immutable release tag extracted from the git tag via semver. |
| `<major>.<minor>` (e.g. `1.2`) | Tag push matching `v*` | Floating minor-version tag for consumers that want automatic patch updates. |
| `<commit-sha>` (e.g. `a1b2c3d`) | Every non-PR build | Immutable per-commit reference for debugging and rollback. |

On **pull requests**, images are built but **not pushed** — this validates
the Containerfile without publishing unreleased images.

### Tag lifecycle

`:latest` is added whenever the ref is the default branch (`main`),
regardless of trigger event.

```
  workflow_dispatch ──────► :dev  :sha  (+:latest if ref=main)
  push to main ───────────► :dev  :sha  :latest
  tag push (v*) ──────────► :dev  :sha  :X.Y.Z  :X.Y
  pull_request ───────────► (build only, no push)
```

## How images are built

The workflow has two jobs that run sequentially:

1. **`build-base`** builds `fullsend-sandbox` from `images/sandbox/Containerfile`.
   On push/dispatch, it pushes the multi-arch manifest list and outputs the
   immutable digest (`@sha256:...`) for the next job.

2. **`build-code`** builds `fullsend-code` from `images/code/Containerfile`,
   passing the base image reference as `--build-arg BASE_IMAGE=<ref>`.
   On push/dispatch it uses the digest from step 1.  On PRs (where no
   digest is available because nothing was pushed), it falls back to the
   `:dev` tag on the registry.

Cross-platform builds use [QEMU](https://github.com/docker/setup-qemu-action)
for user-mode emulation and [Docker Buildx](https://github.com/docker/setup-buildx-action)
with GitHub Actions cache (`type=gha`).

### PR build and the `:dev` fallback

PR builds set `push: false`, so `build-base` produces no registry digest.
The `build-code` job needs a base image reference to build against.  It
falls back to `fullsend-sandbox:dev` — a multi-arch image that is always
kept current by non-PR builds.

This avoids a chicken-and-egg problem: if the fallback pointed to `:latest`
(which may be amd64-only before the multi-arch work lands), the arm64 build
leg would fail with `no match for platform in manifest`.

### Bootstrapping `:dev` for the first time

When adding multi-arch support to a repo that previously published
amd64-only images, the `:dev` tag does not yet exist on the registry.
Bootstrap it by triggering a manual `workflow_dispatch` run from the
feature branch:

```bash
gh workflow run sandbox-images.yml \
  --repo fullsend-ai/fullsend \
  --ref <feature-branch>
```

This builds and pushes `:dev` (and `:sha`) without touching `:latest`.
Subsequent PR builds on the feature branch can then resolve the `:dev`
base image for both platforms.

## Supply chain security

Every binary downloaded during the build is **version-pinned** and
**SHA256-verified** per architecture:

| Tool | Pinning | Verification |
|------|---------|-------------|
| OpenShell base image | Manifest list digest (`@sha256:...`) | Immutable OCI content hash |
| ONNX Runtime | `ORT_VERSION` + `ORT_SHA256_{AMD64,ARM64}` | `sha256sum -c` |
| Gitleaks | `GITLEAKS_VERSION` + `GITLEAKS_SHA256_{AMD64,ARM64}` | `sha256sum -c` |
| Tirith | `TIRITH_VERSION` + `TIRITH_SHA256_{AMD64,ARM64}` | `sha256sum -c` |
| Go toolchain | `GO_VERSION` + `GO_SHA256_{AMD64,ARM64}` | `sha256sum -c` |
| ProtectAI DeBERTa model | `PROTECTAI_MODEL_REV` + per-file SHA256 | `sha256sum -c` |
| Claude Code | Official installer script | HTTPS only (no checksum, version floats) |
| acli | `ACLI_VERSION` + `ACLI_SHA256_{AMD64,ARM64}` | `sha256sum -c` |
| pre-commit, gitlint | pip version pins | pip integrity check |

GitHub Actions are pinned to full commit SHAs (not floating tags).

### Updating pinned versions

Each tool section in the Containerfile has an update comment with the
source URL for checksums.  When bumping a version:

1. Download the new release checksums from the URL in the comment.
2. Update both `*_SHA256_AMD64` and `*_SHA256_ARM64` ARGs.
3. The base image digest can be updated by running
   `podman manifest inspect <image>:<tag>` and extracting the index digest.

## Local builds

Build for your native architecture:

```bash
podman build -t fullsend-sandbox:local \
  -f images/sandbox/Containerfile images/sandbox/

podman build -t fullsend-code:local \
  --build-arg BASE_IMAGE=fullsend-sandbox:local \
  -f images/code/Containerfile images/code/
```

Build for a specific platform (requires QEMU registration):

```bash
podman build --platform linux/arm64 -t fullsend-sandbox:arm64 \
  -f images/sandbox/Containerfile images/sandbox/
```

## Extending the base image

Per-org images that need additional tools should extend `fullsend-sandbox`
or `fullsend-code`:

```dockerfile
FROM ghcr.io/fullsend-ai/fullsend-sandbox:latest
RUN apt-get update && apt-get install -y --no-install-recommends rustc && rm -rf /var/lib/apt/lists/*
```
