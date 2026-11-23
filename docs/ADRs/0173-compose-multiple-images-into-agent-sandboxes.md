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

Accepted (amends [ADR 0032](0032-safe-push-wrapper-for-sandboxed-agents.md) and supersedes the sandbox-image requirements in [ADR 0030](0030-openshell-sandbox-interaction-model.md))

## Context

There are three layers that need different things from the agent's sandbox filesystem.

- The fullsend CLI: needs the runtime, scanners, hooks, and support tools.
- The agent author: sometimes needs additional tools, like `scan-secrets`.
- The end user: often needs repo-specific tools, compilers, libraries, and
  linters.

Today, we pack all of these things into a single sandbox `image`. The fullsend
default agents ship with a default. End users can override it, but they are
expected to build an image that inherits from the fullsend project's images if
they want things to work.

Furthermore, by being both the authors of the default agents (`code`, `review`,
etc) and the authors of the fullsend CLI, the image requirements get blurry.

This ADR aims to let Fullsend, agent authors, and end users provide their
own sandbox dependencies without requiring consumers to publish a combined
image.

## Decision

We will compose the sandbox from a harness-specified image (which we will call
the **sandbox image**) and a second image whose pullspec and digest are
embedded in the fullsend binary (which we will call the **system image**) which
will be mounted read-only at `/opt/fullsend/`.

- The fullsend CLI can know where its dependencies will be; in
  `/opt/fullsend/` provided by the system image.
- Agent authors can provide an arbitrary sandbox image that contains the tools their
  agent needs.
- End users can override that with an arbitrary sandbox image of their own that
  contains tools their project needs.

Neither agent authors nor end users can modify the system image.

When an end user overrides the sandbox image, they must take care to include
any tools that the upstream agent requires - or modify the agent further to no
longer require those tools.

### Sandbox Image Contract Changes

This ADR supersedes the sandbox-image requirements in [ADR 0030](0030-openshell-sandbox-interaction-model.md). A sandbox image no longer needs:

- A `sandbox` account or fixed UID/GID `1000660000`; Fullsend supplies a
  non-root identity at runtime.
- A writable image-defined workdir; Fullsend provides a writable workspace.
- `iproute2`; OpenShell handles network isolation outside the workload image.

### Limitations

The user cannot provide just _any_ container image.

- Both images must target Linux, and the system image must support the
  sandbox's architecture, such as `linux/amd64` or `linux/arm64`. Binaries
  built for CPU features above the architecture baseline also require hosts
  with those features.
- The system image is mounted at `/opt/fullsend`; it does not become the
  sandbox's root filesystem or select the sandbox's dynamic linker. Its
  executables run against the sandbox's Linux kernel. A dynamically linked
  executable uses the ELF interpreter named in the binary and needs compatible
  shared libraries available through its loader's search paths. A
  glibc-linked executable needs a compatible glibc loader and ABI; a
  musl-based image, such as Alpine, does not run glibc-linked system-image
  executables by default. Those tools must be built for musl, linked
  statically, or launched through a compatible loader and library set carried
  in the system image. See [`ld.so(8)`](https://man7.org/linux/man-pages/man8/ld.so.8.html)
  for ELF interpreter and library lookup behavior.

### Sandbox image for fullsend-ai/agents

The sandbox image that we will use as the authors of fullsend-ai/agents is not
decided at this time. It may vary on an agent by agent basis, but choose small
images as a rule of thumb.

The code agent in particular will need a typical development container with
`git`.

### Sandbox image for fullsend-ai project usage

Of course, the developers of fullsend are _users_ of fullsend. We will modify
our `.fullsend/` config to use a sandbox image for the code and fix agents that
contains our project dependencies (like `lychee` in addition to `git`).

### Other Options

We considered a proposal that created an open-ended way for agent authors and
end users to supply arbitrary _additional_ images that may be mounted as
read-only filesystems in `/opt/` - and we were going to do a "switcheroo" that
would move our current sandbox images to the `/opt/` directory while
encouraging users to begin overriding the sandbox `image` with whatever content
they wanted.  But, by adding to the harness schema, this created additional
unneeded feature surface. We don't really have a use case for arbitrary
additional read-only images -- we only need a way to get the fullsend system
dependencies in place.

## Forge scope

Forge-neutral: the decision applies to GitHub, GitLab, and Forgejo harnesses.
Support depends on the sandbox backend, not the forge.

## Consequences

- **Ergonomics** - Users should find that it is straightforward to supply a
  development image for agents they consume from third-parties.
- **Release process** - Today, our image build and binary build processes
  happen independently at release time. We will need to modify that to first
  perform the image build, then pass its digest-pinned pull spec to the binary
  build so that it can be included in the build.
- **Opportunity** - This ADR creates an opportunity. Today, we cram all of our
  runtimes and their dependencies into the same image. The cli is responsible
  for choosing the runtime (based on the harness). Now that the cli is also
  responsible for specifying the system image, we could decompose our system
  image into multiple system images - one per runtime - and have the cli choose
  only the necessary one for the chosen runtime. This could reduce pull times
  with a smaller image. This idea - to decompose our monolithic system image -
  is out of scope for this ADR and deferred at the time of this writing.
- **[ADR 0030](0030-openshell-sandbox-interaction-model.md)** remains the record
  of the broader OpenShell interaction model; this ADR supersedes its
  sandbox-image requirements.
- **[ADR 0032](0032-safe-push-wrapper-for-sandboxed-agents.md)** is extended by
  this ADR. `safe-push`, its policy file, and the real `git` executable move
  to the **system image** at `/opt/fullsend/usr/local/bin/safe-push`,
  `/opt/fullsend/etc/fullsend/safe-push-policy.yaml`, and
  `/opt/fullsend/usr/bin/git`, respectively.
