# Experiment: OpenShell Sandbox Evaluation

**Date:** 2026-03-27
**Status:** Complete
**Repo:** <https://github.com/NVIDIA/OpenShell>

## Goal

Evaluate NVIDIA OpenShell as a sandboxed runtime for autonomous AI agents. Specifically: can we run it locally, does the network policy enforcement work as advertised, and can we do container builds inside a sandbox?

## What is OpenShell

OpenShell provides sandboxed execution environments for AI agents with declarative YAML policy enforcement. It runs a K3s cluster inside a single Docker container and enforces filesystem, network, process, and inference controls. Policies are hot-reloadable without restarting sandboxes.

## Setup

**Environment:** DigitalOcean droplet, Ubuntu 24.04, 8GB RAM, 2 vCPUs.

Fedora (our local dev environment) is not in OpenShell's support matrix — only Debian/Ubuntu and macOS are listed. We tried setting up a local KVM/QEMU Ubuntu VM via `virt-manager` and `virt-install` with cloud-init autoinstall, but this proved to be more friction than it was worth. A cloud Ubuntu VM was the path of least resistance.

```bash
# Install
apt-get install -y docker.io
curl -LsSf https://raw.githubusercontent.com/NVIDIA/OpenShell/main/install.sh | sh
openshell gateway start
```

## Experiment 1: Network Policy Enforcement (L7)

Reproduced the [sandbox-policy-quickstart](https://github.com/NVIDIA/OpenShell/tree/main/examples/sandbox-policy-quickstart) example.

### Steps

1. Created a sandbox with default-deny networking
2. Attempted `curl https://api.github.com/zen` — **blocked** (`403 CONNECT tunnel failed`)
3. Applied a read-only GitHub API policy (hot-reload, no restart)
4. Retried `curl https://api.github.com/zen` — **allowed** (returned a GitHub zen quote)
5. Attempted `curl -X POST https://api.github.com/repos/octocat/hello-world/issues` — **blocked at L7** (`POST not permitted by policy`)

### Policy used

```yaml
network_policies:
  github_api:
    name: github-api-readonly
    endpoints:
      - host: api.github.com
        port: 443
        protocol: rest
        tls: terminate
        enforcement: enforce
        access: read-only
    binaries:
      - { path: /usr/bin/curl }
```

### Results

All three states worked exactly as documented:

| State | Behavior |
|-------|----------|
| Default deny | All outbound traffic blocked |
| L7 read-only | GET allowed, POST blocked |
| Audit trail | Every request logged with method, path, and decision |

Logs showed full audit trail including denied connection reasons, L7 decisions, and policy names. Hot-reload worked in seconds.

## Experiment 2: Fine-Grained Path-Based L7 Policy

Wrote a custom policy to allow reading from any HTTPS endpoint but only permit POST to a single GitHub issue's comments endpoint.

### Policy

```yaml
network_policies:
  read_everything:
    name: read-everything
    endpoints:
      - host: "**"
        port: 443
        protocol: rest
        enforcement: enforce
        access: read-only
    binaries:
      - { path: "**" }

  github_one_issue:
    name: github-one-issue
    endpoints:
      - host: api.github.com
        port: 443
        protocol: rest
        enforcement: enforce
        rules:
          - allow:
              method: POST
              path: "/repos/myorg/myrepo/issues/42/comments"
    binaries:
      - { path: "**" }
```

### Key findings

- L7 rules support explicit `method` + `path` glob patterns, not just `access` presets
- Path patterns use glob syntax with `/` delimiter — `*` doesn't cross path boundaries, `**` does
- The `binaries` field restricts which executable can use the policy (defense-in-depth)
- Policies can be scoped to specific binaries (e.g., only `curl` or `gh`) or opened to all with `{ path: "**" }`

## Experiment 3: Git Branch Push Restrictions

Investigated whether OpenShell can restrict which git branches an agent pushes to.

### Findings

**Git over HTTPS:** Not possible at the branch level. A `git push` does `POST /{repo}.git/git-receive-pack` — the branch name is in the binary pack data in the request body, not in the URL path. OpenShell's L7 rules match on HTTP method and URL path only. You can block `git push` entirely but cannot distinguish branches.

**Git over SSH:** Not possible. SSH is an opaque encrypted tunnel. OpenShell can allow or block the SSH connection to `github.com:22` entirely but cannot inspect the session content.

**Recommendation:** Use server-side branch protection rules (GitHub rulesets) for branch-level restrictions. Combine with OpenShell network policy for endpoint-level control.

## Experiment 4: Container Builds Inside a Sandbox

Attempted to run `podman build` inside an OpenShell sandbox.

### Setup

Built a custom sandbox image based on the official OpenShell base image, adding podman, fuse-overlayfs, uidmap, and slirp4netns. Used `docker save | ctr import` to load the image directly into the K3s containerd (the `--from` image push mechanism OOM-killed on a 4GB VM; worked after upgrading to 8GB but the pipe approach is more memory-efficient).

### Key learnings about custom images

- Custom images must be based on the official OpenShell base image (`ghcr.io/nvidia/openshell-community/sandboxes/base:latest`) — the sandbox supervisor binary is side-loaded at runtime
- The sandbox user is `sandbox:998:998` (not 1000)
- `--from` with a Dockerfile path requires the file to be named `Dockerfile` in a directory, not a direct path to a `Containerfile`
- Loading images via `docker save | docker exec -i <cluster> ctr -n k8s.io images import -` is more reliable than the `--from` push mechanism for large images
- Images tagged `:latest` trigger `imagePullPolicy: Always` in K3s — use a specific tag (`:v1`, `:v2`) for locally-loaded images

### Result

**`podman build` does not work inside an OpenShell sandbox.** The sandbox's seccomp/user-namespace restrictions prevent `newuidmap` from operating:

```
running `/usr/bin/newuidmap 81 0 998 1 1 100000 65536`:
newuidmap: write to uid_map failed: Operation not permitted
Error: cannot set up namespace using "/usr/bin/newuidmap": exit status 1
```

This is a fundamental conflict: rootless podman requires creating new user namespaces with subordinate UID mappings, which is exactly what OpenShell's security model blocks. This is by design — allowing arbitrary user namespace manipulation would undermine the sandbox's isolation guarantees.

**The intended workflow is:** build images outside the sandbox (on the host or in CI), then run them inside the sandbox via `--from`.

## Summary

| Capability | Works? | Notes |
|-----------|--------|-------|
| Network default-deny | Yes | All outbound blocked by default |
| L7 read-only enforcement | Yes | GET allowed, POST blocked per-endpoint |
| Fine-grained path rules | Yes | Can restrict to specific API paths |
| Binary-scoped policies | Yes | Can limit which executables access which endpoints |
| Hot-reload policies | Yes | No sandbox restart needed |
| Audit logging | Yes | Full method/path/decision trail |
| Git branch restrictions | No | Branch name is in request body, not URL path |
| Container builds inside sandbox | No | User namespace restrictions block rootless podman |
| Custom sandbox images | Yes | Base from official image, load via ctr import |
| Fedora host support | No | Ubuntu/Debian or macOS required |

## Recommendations

1. **OpenShell is production-ready for network policy enforcement.** The L7 inspection, hot-reload, and audit logging work well. Worth adopting for agents that need controlled internet access.
2. **Use server-side controls for git branch protection.** OpenShell cannot enforce this at the network level.
3. **Don't expect container-in-container.** Build images in CI or on the host, not inside sandboxes.
4. **Run on Ubuntu.** Fedora is not supported and local VM setup adds unnecessary friction.
5. **Allocate at least 8GB RAM** for the host if using custom sandbox images.
