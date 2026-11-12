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

Three parts:

* Introduce a new `image_mounts` field in the harness. The fullsend runner will mount those images' filesystems at the described destination inside the sandbox.
* Encourage agent authors to use a minimal image for the base sandbox `image` image, and supply their agents' system dependencies via an image mounted at `/opt/fullsend`.

Like this:

```yaml
image: registry.fedoraproject.org/fedora@sha256:<image-digest>
image_mounts:
  - image: ghcr.io/fullsend-ai/fullsend-sandbox-tools@sha256:<image-digest>
    dest: /opt/fullsend
```

* End users can now override `image` with a development image of their choice, while fullsend dependencies remain available in `/opt/fullsend`

## Forge scope

Forge-neutral: the decision applies to GitHub, GitLab, and Forgejo harnesses.
Support depends on the sandbox backend, not the forge.

## Consequences

- This ADR supplements the sandbox image model in [ADR 0030](0030-openshell-sandbox-interaction-model.md).
- Repositories can provide tools without building a Fullsend-specific composite image.
- Fullsend's sandbox image and project tool images can be maintained independently.
- Limitation! Mounted executables must be compatible with the sandbox runtime.
