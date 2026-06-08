---
title: "36. Agent Execution Sandbox Architecture"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - sandbox
  - container
  - isolation
  - security
  - openshell
---

# 36. Agent Execution Sandbox Architecture

Date: 2026-05-05

## Status

Accepted

## Context

Fullsend agents execute within isolated sandboxes that enforce security boundaries: filesystem access control, network policy enforcement, and credential isolation (ADR-0017, ADR-0025). The current implementation uses OpenShell with per-agent L7 network policies and runs on GitHub Actions runners. With GitLab support proposed (ADR-0043, supersedes ADR-0028), the execution architecture needs to work on both GitHub Actions and GitLab CI runners.

The sandbox architecture has multiple concerns that need to be resolved together:

1. **Multi-platform execution**: Agents must run on GitHub Actions runners (Linux VMs) and GitLab CI runners (Docker, Kubernetes, shell executors)
2. **Container image design**: The sandbox environment (OpenShell + tools + agent harness) must be packaged for reuse across platforms
3. **Isolation model**: Security boundaries must be preserved regardless of which runner type executes the agent
4. **Resource limits**: CPU, memory, and timeout constraints need platform-independent expression
5. **OpenShell integration**: The sandbox runtime (OpenShell gateway + L7 policies + providers) must work in all executor environments

The GitLab support design (ADR-0043, supersedes ADR-0028) explicitly deferred this decision: "The agent execution environment is orthogonal to the CI/CD dispatch architecture. GitLab runner configuration, sandbox isolation, and compute architecture should be documented separately."

The forge abstraction (ADR-0005) keeps dispatch logic platform-neutral. This ADR addresses what happens *after* the dispatch pipeline triggers an agent job: how the agent actually executes.

## Options

### Option 1: Shared Container Image, Docker-First

Package the entire agent execution environment (OpenShell, agent harness, tools, language runtimes) into a single container image published to a container registry. Both GitHub Actions and GitLab CI pull and run this image.

**GitHub Actions workflow step:**
```yaml
- name: Run agent
  uses: docker/run@v1
  with:
    image: ghcr.io/fullsend-ai/agent-sandbox:v1.2.3
    options: --privileged  # OpenShell gateway needs network namespace manipulation
    env:
      AGENT_NAME: ${{ inputs.agent_name }}
      EVENT_PAYLOAD: ${{ inputs.event_payload }}
```

**GitLab CI job:**
```yaml
run-agent:
  image: ghcr.io/fullsend-ai/agent-sandbox:v1.2.3
  script:
    - fullsend run $AGENT_NAME  # GitLab CI uses script: field for commands to run inside the container
```

**Isolation mechanism:** Container runtime (Docker, Podman, GitLab's Docker executor). OpenShell creates nested sandboxes inside the container for individual agent processes.

**Advantages:**
- Single artifact to version, test, and maintain
- Consistent environment across platforms (same tools, same OpenShell version)
- Agent harness and OpenShell are pre-installed, no setup step required
- Platform differences handled by container runtime abstraction
- Clear upgrade path (bump image tag in config repo templates)

**Disadvantages:**
- Large image size (base OS + OpenShell + all language runtimes + tools agents need)
- Privileged container requirement for OpenShell may conflict with GitLab runner security policies (especially Kubernetes executors with PodSecurityPolicies)
- GitHub Actions VM-based runners add container-in-VM overhead
- Docker-in-Docker concerns if agents need to build container images (requires nested privileged, high security risk)
- GitLab shell executor cannot use this (no container runtime)

**Image composition strategy:**
- Base: OpenShell-enabled minimal Linux (Alpine or Ubuntu for Docker; Fedora/RHEL for Podman-based deployments)
- Layer 1: Language runtimes (Go, Python, Node.js) — only what fullsend's built-in agents require, not arbitrary user code. Organizations implementing "Bring Your Own Agent" can build customized images with different runtime sets (see "Image Build and Distribution" in Open Questions).
- Layer 2: Common tools (git, gh CLI, curl, jq)
- Layer 3: Agent harness (`fullsend run` CLI)
- Layer 4: Provider definitions and policy templates

**OpenShell integration:**
OpenShell gateway runs as PID 1 in the container. When `fullsend run` is invoked, it creates a sandbox via OpenShell's API, applies L7 network policies from the agent's config directory, and executes the agent (Claude Code or equivalent) inside the sandbox. The gateway intercepts all network egress from the sandbox.

### Option 2: Platform-Specific Images

Maintain separate container images or runtime environments per platform: one optimized for GitHub Actions VM runners, one for GitLab Docker executor, one for GitLab Kubernetes executor.

**GitHub**: Native VM setup (install OpenShell, tools, harness via setup steps)
**GitLab Docker**: Container image similar to Option 1
**GitLab Kubernetes**: Dedicated pod template with sidecar proxy pattern

**Advantages:**
- Each platform uses its native execution model (no VM-in-container or container-in-VM overhead)
- Can optimize for platform-specific constraints (GitHub's VM networking vs Kubernetes pod networking)
- GitLab Kubernetes executor can use pod security policies and service mesh integration

**Disadvantages:**
- Three separate codepaths to maintain, test, and version
- Inconsistent environments risk platform-specific bugs
- Harder to guarantee security boundary equivalence across platforms
- Violates "write once, run anywhere" for agent developers
- Increased testing surface (must validate each agent on each platform)

### Option 3: Kubernetes-Native with CRDs

Use Kubernetes as the universal execution layer. Deploy a custom controller (CRD) that provisions agent pods on-demand. Both GitHub Actions and GitLab CI trigger the controller via API calls.

**GitHub Actions:**
```yaml
- name: Trigger agent execution
  run: |
    kubectl create -f - <<EOF
    apiVersion: fullsend.ai/v1
    kind: AgentRun
    metadata:
      generateName: agent-triage-
    spec:
      agentName: triage
      eventPayload: ${{ toJSON(github.event) }}
    EOF
    kubectl wait --for=condition=complete agentrun/...
```

**GitLab CI:**
Similar `kubectl` calls from CI jobs.

**Advantages:**
- Kubernetes provides resource limits, pod security policies, network policies natively
- Uniform execution model regardless of CI platform
- Existing ecosystem for monitoring, logging, autoscaling
- Aligns with Kubernetes SIG Agent Sandbox (see agent-infrastructure.md evaluation)

**Disadvantages:**
- Requires Kubernetes cluster access from both GitHub and GitLab
- Custom controller is additional operational burden (vs using existing CI primitives)
- High complexity for organizations not already running Kubernetes
- Agent runs are asynchronous API calls (harder to stream logs to CI job output)
- Kubernetes SIG Agent Sandbox evaluation (agent-infrastructure.md) notes poor fit for ephemeral task-scoped execution
- Adds latency (API call + pod scheduling + image pull vs direct container start in CI executor)
- OpenShell feature parity between Kubernetes pod networking and standard Docker networking needs validation — pod network namespaces, CNI plugins, and service mesh sidecars may interact differently with OpenShell's L7 proxy than direct container networking

### Option 4: Minimal Sandbox + Dynamic Tool Installation

Ship a minimal sandbox image (OpenShell + harness only). Agents declare required tools in their config. The harness installs tools on first run and caches them.

**Image size:** ~100MB (vs ~2GB for full-stack image)
**First-run penalty:** Install tools (apt/apk/pip/npm) on demand
**Subsequent runs:** Tools cached in workspace or mounted volume

**Advantages:**
- Small image, fast distribution
- Agents only pay for tools they use
- Easy to support new tools without rebuilding base image

**Disadvantages:**
- Non-deterministic builds (tool versions change over time unless pinned)
- Installation step is untrusted code execution (npm install can run arbitrary scripts)
- Cache invalidation complexity
- Slower first run
- Tool installation requires network access, violating zero-trust during agent execution

## Decision

**Option 1: Shared Container Image, Docker-First.**

A single container image (`ghcr.io/fullsend-ai/agent-sandbox`) contains OpenShell, the fullsend agent harness, and a curated set of tools required for fullsend's built-in agents (triage, code, review, fix). Both GitHub Actions and GitLab CI pull and run this image.

**Executors supported:**
- GitHub Actions: Linux VM runners (Docker available)
- GitLab CI: Docker executor (gitlab-runner with Docker)
- GitLab CI: Kubernetes executor (GitLab Runner Kubernetes executor)

**Executors not supported:**
- GitLab shell executor (no container isolation, incompatible with sandbox security model)
- GitHub Actions Windows/macOS runners (OpenShell Linux-only, not a current fullsend target)

**Image composition:**
- Base: Ubuntu 22.04 (OpenShell requires glibc, Alpine musl incompatible; Fedora/RHEL alternative for Podman-first environments)
- OpenShell 0.0.37-dev+ with Podman support
- Language runtimes: Go 1.23, Python 3.11, Node.js 20 (LTS) — built-in agent requirements only
- Tools: git, gh CLI, curl, jq, yq
- Agent harness: `fullsend run` CLI binary
- Provider templates and L7 policy examples in `/opt/fullsend/policies/`

**Note on "Bring Your Own Agent":** The reference image contains language runtimes for fullsend's built-in agents (triage, code, review, fix). Organizations implementing custom agents with different runtime requirements can build customized images from the base Dockerfile, replacing or extending the language runtime layer. See "Image Build and Distribution" in Open Questions for per-org image build strategies.

**Why Docker-first over platform-specific (Option 2):**
Maintaining environment parity across GitHub and GitLab is a security requirement, not just operational convenience. Security boundaries (L7 policies, credential isolation, filesystem restrictions) must behave identically on both platforms. Platform-specific implementations would require separate security reviews, separate penetration testing, and continuous validation that policy enforcement is equivalent. The container abstraction provides a single implementation of the security boundary.

**Why not Kubernetes CRDs (Option 3):**
The Kubernetes SIG Agent Sandbox evaluation (agent-infrastructure.md) identified key mismatches: ephemeral task-scoped execution, poor pipeline integration, and observability gaps. Fullsend agents are triggered by SCM events (issues, PRs, reviews) and run to completion in minutes, not hours or days. This execution pattern maps naturally to CI job steps, not long-lived pod controllers. Additionally, requiring Kubernetes access from GitHub Actions workflows adds a cross-platform dependency and authentication complexity (kubeconfig management, OIDC federation) that container execution avoids. Kubernetes is an option for organizations already running it, but it should not be a mandatory dependency for the reference architecture.

**Why not minimal sandbox + dynamic tools (Option 4):**
Dynamic tool installation violates the zero-trust execution principle: agents should not perform arbitrary network operations during their run. Installing packages via `apt`, `npm`, or `pip` requires trusting upstream package repositories and running package install scripts (which can execute arbitrary code). The security model (ADR-0017, ADR-0025) prohibits agents from accessing external networks except through controlled proxies with L7 policies. Pre-baking tools into the image moves trust decisions to build time (where the image can be scanned, signed, and versioned) rather than runtime.

## Implementation Details

Detailed implementation guidance has been moved to [docs/plans/agent-execution-environment.md](../plans/agent-execution-environment.md), including:

- Container image build pipeline and versioning strategy
- OpenShell configuration for nested sandbox creation
- Resource limits and timeout enforcement per platform
- Privileged container requirements and alternatives (rootless Podman, user namespaces)
- GitLab runner configuration (Docker executor, Kubernetes executor)
- Host-side REST server lifecycle in containerized environments
- Image signing and verification (Sigstore, cosign)
- Upgrade and rollback procedures

**Note:** OpenShell configuration examples (version numbers, API endpoints, configuration syntax) are illustrative and based on design-phase exploration. These details should be validated against the actual OpenShell release used during implementation, as APIs and configuration formats may evolve.

The implementation document is structured for iterative evolution as the sandbox architecture is validated in production.

## Consequences

### Positive

- **Environment consistency**: Identical security boundaries on GitHub Actions and GitLab CI
- **Single security review surface**: Container image is the artifact to audit, scan, and sign
- **Simplified dependency management**: OpenShell version, tool versions, harness version all pinned in image tag
- **Clear upgrade path**: Bump image tag in `.fullsend` config repo templates, propagates to all enrolled repos
- **Reproducible local execution**: Developers can run the same image locally for debugging (docker run or podman run)
- **Supply chain integrity**: Image signing (Sigstore) provides attestation from build to execution

### Negative

- **Large image size**: Full runtime image ~1.5-2GB (vs minimal Alpine ~100MB), slower pull on first run
- **Privileged container requirement**: OpenShell gateway needs network namespace manipulation (`CAP_NET_ADMIN`), may conflict with restrictive PodSecurityPolicies
- **Image update lag**: Adding a new tool requires rebuilding and redeploying image, cannot be done per-agent
- **Docker-in-Docker limitation**: Agents that need to build container images require nested privileged (high security risk) or external builder services
- **GitLab shell executor excluded**: Organizations using shell executors cannot adopt fullsend without adding Docker or Kubernetes runner capacity

### Risks

- **Privileged container escape**: If OpenShell or the container runtime has a vulnerability, privileged containers can escape to the host
- **Image poisoning**: Compromised image registry or build pipeline could distribute malicious sandbox images
- **Version drift**: Enrolled repos pinning different image tags create inconsistent environments, complicate security patches

### Mitigations

- **Rootless alternatives**: Investigate rootless Podman and user namespace remapping to reduce privileged requirement (blocked on OpenShell rootless support)
- **Image signing mandatory**: All images signed with Sigstore cosign, runners verify signatures before execution
- **Automated updates**: Renovate bot or equivalent keeps image tags in config repo up to date, PRs auto-merge if CI passes
- **Builder service for Docker-in-Docker**: Code agents that need to build images delegate to external Kaniko or Buildkit services, not nested Docker

## Open Questions

### Rootless OpenShell Support

**Problem**: OpenShell currently requires privileged containers (or at minimum `CAP_NET_ADMIN` capability) to manipulate network namespaces for L7 policy enforcement. This conflicts with Kubernetes PodSecurityPolicies that prohibit privileged workloads.

**Options**:
1. **User namespace remapping**: Run OpenShell gateway as non-root inside a user namespace. Requires kernel user namespace support and runner configuration.
2. **OpenShell rootless mode**: Upstream feature request to support L7 policy enforcement without privileged containers (e.g., via eBPF or SECCOMP).
3. **Platform exemption**: Document that fullsend requires privileged containers and provide guidance for organizations to create PodSecurityPolicy exemptions for agent workloads.

**Status**: User namespace remapping is the most viable near-term path. Requires testing on GitHub Actions (Docker-in-VM) and GitLab Kubernetes executor (pod security contexts). OpenShell rootless mode is ideal long-term but depends on upstream. Note: Fedora-based base images may be considered when exploring rootless Podman support, though `fullsend run` would need compatibility testing for Fedora/RHEL environments.

### Image Build and Distribution

**Problem**: Who builds the container image, where is it stored, how is it signed, and how do runners authenticate to pull it?

**Options**:
1. **Public registry (ghcr.io)**: Image published to GitHub Container Registry, publicly readable. No authentication required. Simplest for open-source deployments.
2. **Per-org registry**: Each organization builds and signs their own image, stores in their registry (GCR, ECR, GitLab Container Registry). Runners authenticate with registry credentials. Maximum supply chain control.
3. **Hybrid**: Reference image in public registry (ghcr.io/fullsend-ai/agent-sandbox), organizations can build customized forks.

**Status**: Public registry for reference implementation. Organizations with strict supply chain requirements should fork the Dockerfile and build internally. Document registry authentication setup for GitLab CI (CI_REGISTRY_USER, CI_JOB_TOKEN) and GitHub Actions (GITHUB_TOKEN).

### Builder Services for Docker-in-Docker Use Cases

**Problem**: Code agents may need to build container images as part of their work (e.g., testing a Dockerfile change, validating image build). Docker-in-Docker (running Docker inside the agent container) requires privileged nested containers, which is a severe security risk.

**Options**:
1. **External builder service**: Agent submits build requests to a remote Kaniko or Buildkit API. Builder runs in a separate, controlled environment. Agent receives build result (success/failure, logs) but never has Docker socket access.
2. **Prohibit container builds**: Document that fullsend agents cannot build container images. Code agents validate Dockerfiles via static analysis only.
3. **Ephemeral builder pods**: For Kubernetes executor, spawn a sidecar Kaniko pod per agent run. Pod has its own isolation boundary, agent communicates via shared volume or API.

**Status**: External builder service is the architecturally sound option but requires deploying and operating the builder infrastructure. Prohibiting container builds is the safe default for initial implementation. This should be revisited when a concrete use case emerges (e.g., a code agent that needs to validate changes to Dockerfiles or image builds).

## References

- ADR-0005: Forge abstraction layer (dispatch is platform-neutral, execution must also be)
- ADR-0017: Credential isolation for sandboxed agents (zero credentials in sandbox)
- ADR-0025: Provider credential delivery (OpenShell providers for credential injection)
- [ADR-0043: GitLab per-repo support](0043-gitlab-per-repo-support.md) (supersedes [ADR-0028](0028-gitlab-support.md); dispatch pipelines, explicitly deferred agent execution environment)
- ADR-0030: OpenShell sandbox interaction model (defines the agent-harness communication protocol)
- [agent-infrastructure.md](../problems/agent-infrastructure.md): Infrastructure layer exploration, SIG Agent Sandbox evaluation
- [OpenShell](https://github.com/NVIDIA/OpenShell): Sandbox runtime with L7 network policy enforcement
