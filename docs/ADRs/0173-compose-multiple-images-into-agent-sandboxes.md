---
title: "173. Compose multiple images into agent sandboxes"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - sandbox
  - container
  - harness
---

# 173. Compose multiple images into agent sandboxes

Date: 2026-11-12

## Status

Accepted

## Context

Projects need different compilers, SDKs, and development tools. Today, they can
supply an `image` to the harness configuration to put their own image in place,
giving coding agents their tools, but they have to build a special image for
fullsend that includes both their toolchain and Fullsend's agent dependencies.
What a pain!

This ADR aims to let coding agents use project-selected images without
requiring those images to include Fullsend's dependencies.

## Decision

We adopt separate image mounts so that coding agents can use a
project-selected sandbox image while Fullsend's own dependencies arrive in a
second image, without the project rebuilding either one.

- Introduce a new `image_mounts` field in the harness. The fullsend runner will
  mount those images' filesystems at the described destination inside the
  sandbox.
- Encourage agent authors to use a minimal image for the base sandbox `image`,
  and supply their agents' system dependencies via an image mounted at
  `/opt/fullsend`. Like this:

  ```yaml
  image: registry.fedoraproject.org/fedora@sha256:<image-digest>
  image_mounts:
    - image: ghcr.io/fullsend-ai/fullsend-sandbox-tools@sha256:<image-digest>
      dest: /opt/fullsend
  ```

- End users can now override `image` with a development image of their choice,
  while fullsend dependencies remain available in `/opt/fullsend`.

### Details

**Field contract.** `image_mounts` is not treated specially; it follows the
same rules as comparable list fields such as `host_files`.

- Classification: it can appear at the harness top level and inside `forge:`
  blocks and `overlays:` entries. `image` itself stays top-level only.
- Semantic type ([ADR 0127](0127-harness-schema-versioning-and-field-types.md)):
  `image_mounts` is a structural container. `image_mounts[].image` is a scalar
  image reference, like `image`, and is never resolved as a path.
  `image_mounts[].dest` is a destination path inside the sandbox, like
  `host_files[].dest`.
- Merge: under `base:` composition and overlays, the top-level and
  forge/overlay lists are concatenated and deduplicated by `dest`, with the
  child (or forge/overlay) entry winning.
- Agent entries: an `agents:` entry's `harness:` block
  ([ADR 0162](0162-agent-entries-may-carry-harness-overrides.md)) is a harness
  layer merged by the same `base:` rules, so it needs no `image_mounts`-specific
  handling.
- `docs/contributing/harness-fields.md` records these rules when the field is
  implemented.

**Trust and collisions.**

- Image sources and destinations are configured in the harness, by whoever may
  already set `image`, `host_files` and the other fields at that layer.
  `image_mounts[].image` follows the same pinning policy as `image`: if `image`
  accepts tags, so does a mount; if `image` requires digests, so does a mount.
  The example pins digests because that is the recommended practice.
- Mount destinations are deduplicated by `dest`, as above. A destination that
  overlaps another mount's destination, or the base image's reserved paths, is
  rejected at load time rather than silently shadowing it.
- Executable resolution: project tooling is found first through normal `PATH`
  discovery. Whenever fullsend itself invokes a fullsend-provided tool, it uses
  the absolute path under the mount (for example `/opt/fullsend/bin/...`), not
  a `PATH` lookup, so a project-controlled `PATH` entry cannot substitute for
  it.
- The project-controlled base image is not a security boundary that protects
  fullsend tooling. Separate mounting keeps the images independently
  maintainable; it does not make the base image trustworthy.

**Base image and Landlock requirements.** Mounting does not change what the
base `image` must provide.

- The base image still has to meet the guest requirements of
  [ADR 0030](0030-openshell-sandbox-interaction-model.md): a `sandbox` user
  (uid/gid 1000660000), a writable workdir, and `iproute2`. Mounts supply the
  agent's dependencies only; they do not supply these.
- [ADR 0032](0032-safe-push-wrapper-for-sandboxed-agents.md) requires
  `safe-push`, its policy file and the real `git` to sit on Landlock-protected
  read-only paths, because OpenShell has no pre-Landlock bootstrap phase and
  the agent controls all writable state. A mounted image can carry these
  wrappers only if the sandbox backend attaches the mount at sandbox creation
  as a read-only path covered by the Landlock policy. Until a backend is
  confirmed to do that, agents that depend on those protections must keep them
  baked into the base image.
- Mounting a second image is a sandbox-backend capability. This ADR assumes it
  and does not name the OpenShell mechanism; the implementation must confirm
  one (or raise it with OpenShell) before relying on `image_mounts`.

## Forge scope

Forge-neutral: the decision applies to GitHub, GitLab, and Forgejo harnesses.
Support depends on the sandbox backend, not the forge.

## Consequences

- This ADR extends the sandbox image model in
  [ADR 0030](0030-openshell-sandbox-interaction-model.md) from one image to a
  base image plus mounts. The base image keeps the ADR 0030 guest requirements
  and the ADR 0032 build-time protections described above.
- Repositories can provide tools without building a Fullsend-specific
  composite image.
- Fullsend's sandbox image and project tool images can be maintained
  independently.
- Limitation! Mounted executables must be compatible with the sandbox runtime.
