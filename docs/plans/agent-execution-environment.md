# Agent Execution Environment

How do fullsend agents execute on CI runners, what does the sandbox environment contain, and how does it work across GitHub Actions and GitLab CI?

**Note:** This is an implementation plan companion to [ADR-0036](../ADRs/0036-agent-execution-sandbox.md). It provides detailed implementation guidance for the chosen sandbox architecture, structured for iterative evolution as the design is validated in production. Once the architecture stabilizes, operational content may migrate to `docs/guides/` per ADR-0023.

## Table of Contents

1. [Container Image Build Pipeline](#container-image-build-pipeline)
2. [OpenShell Configuration](#openshell-configuration)
3. [Resource Limits and Timeouts](#resource-limits-and-timeouts)
4. [Privileged Container Requirements](#privileged-container-requirements)
5. [GitLab Runner Configuration](#gitlab-runner-configuration)
6. [Host-Side REST Server in Containers](#host-side-rest-server-in-containers)
7. [Image Signing and Verification](#image-signing-and-verification)
8. [Upgrade and Rollback](#upgrade-and-rollback)
9. [Platform-Specific Considerations](#platform-specific-considerations)

## Container Image Build Pipeline

The agent sandbox container image is the primary artifact that defines the execution environment. It is built, tested, signed, and distributed via a CI/CD pipeline.

### Dockerfile Structure

The Dockerfile is organized in layers to optimize for cache reuse and minimize image size:

```dockerfile
# Builder stage: Compile fullsend CLI binary
FROM golang:1.23 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -o fullsend ./cmd/fullsend

# Base: Ubuntu 22.04 LTS (OpenShell requires glibc)
FROM ubuntu:22.04 AS base

# System dependencies and OpenShell installation
RUN apt-get update && apt-get install -y \
    ca-certificates \
    curl \
    git \
    jq \
    && rm -rf /var/lib/apt/lists/*

# Install OpenShell (version pinned for reproducibility)
ARG OPENSHELL_VERSION=0.0.37-dev
RUN curl -L https://github.com/NVIDIA/OpenShell/releases/download/${OPENSHELL_VERSION}/openshell-linux-amd64 -o /usr/local/bin/openshell \
    && chmod +x /usr/local/bin/openshell

# Language runtimes layer (can be cached independently)
FROM base AS runtimes
# Note: golang-1.23 is not available in default Ubuntu 22.04 repos.
# Use official golang image for builder stage (as shown above) or add PPA/manual download for runtime.
# Example: COPY --from=golang:1.23 /usr/local/go /usr/local/go
RUN apt-get update && apt-get install -y \
    python3.11 \
    python3-pip \
    nodejs \
    npm \
    && rm -rf /var/lib/apt/lists/*
COPY --from=golang:1.23 /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"

# Tools layer
FROM runtimes AS tools
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli.list > /dev/null \
    && apt-get update \
    && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

# Install yq (YAML processor)
ARG YQ_VERSION=4.35.1
RUN curl -L https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_amd64 -o /usr/local/bin/yq \
    && chmod +x /usr/local/bin/yq

# Agent harness layer
FROM tools AS harness
COPY --from=builder /app/fullsend /usr/local/bin/fullsend
RUN chmod +x /usr/local/bin/fullsend

# Provider and policy templates
FROM harness AS final
COPY policies/ /opt/fullsend/policies/
COPY providers/ /opt/fullsend/providers/

# OpenShell gateway runs as PID 1 (per ADR-0030)
ENTRYPOINT ["/usr/local/bin/openshell", "gateway", "start"]
```

### Build Pipeline (GitHub Actions)

```yaml
name: Build Agent Sandbox Image

on:
  push:
    branches: [main]
    paths:
      - 'internal/sandbox/Dockerfile'
      - 'internal/sandbox/**'
  pull_request:
    paths:
      - 'internal/sandbox/Dockerfile'
      - 'internal/sandbox/**'

env:
  REGISTRY: ghcr.io
  IMAGE_NAME: ${{ github.repository }}/agent-sandbox

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
      id-token: write  # For Sigstore signing

    steps:
      - uses: actions/checkout@v4

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to Container Registry
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract metadata
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          tags: |
            type=sha,prefix={{branch}}-
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}

      - name: Build and push image
        id: build
        uses: docker/build-push-action@v5
        with:
          context: internal/sandbox
          push: ${{ github.event_name != 'pull_request' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max

      - name: Install cosign
        if: github.event_name != 'pull_request'
        uses: sigstore/cosign-installer@v3

      - name: Sign image with Sigstore
        if: github.event_name != 'pull_request'
        run: |
          cosign sign --yes ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}@${{ steps.build.outputs.digest }}

      - name: Verify image signature
        if: github.event_name != 'pull_request'
        run: |
          cosign verify \
            --certificate-identity-regexp=https://github.com/${{ github.repository }} \
            --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
            ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}@${{ steps.build.outputs.digest }}
```

### Image Tagging Strategy

- **Git SHA tags**: `main-abc1234` for every commit to main (immutable, traceable to source)
- **Semver tags**: `v1.2.3`, `v1.2`, `v1` for releases (following semantic versioning)
- **Latest tag**: Not used (prevents accidental version drift across enrolled repos)

Config repo templates reference explicit semver tags: `ghcr.io/fullsend-ai/agent-sandbox:v1.2.3`

## OpenShell Configuration

> **⚠️ Important:** The OpenShell interaction details in this section are illustrative and based on design-phase exploration. **[ADR-0030](../ADRs/0030-openshell-sandbox-interaction-model.md) (Accepted)** decides the actual interaction model: CLI-based sandbox creation (`openshell sandbox create`), SSH for command execution via HTTP CONNECT tunnels, SCP for file delivery during bootstrap, and provider-based credential delivery via `openshell provider create`. Files like agent definitions and skills are SCP'd during bootstrap, not baked into the image. **Implementation must follow ADR-0030's decisions.** The API endpoints, configuration file formats, and workflow described below may not match the actual OpenShell CLI/API.

OpenShell runs as the container entrypoint (PID 1). When `fullsend run` is invoked, it communicates with the OpenShell gateway to create nested sandboxes for individual agent processes.

### Gateway Configuration

OpenShell gateway configuration is embedded in the container image at `/etc/openshell/gateway.yaml`:

```yaml
# OpenShell gateway configuration
api:
  listen: 127.0.0.1:8080  # Gateway API (sandbox creation, policy management)

proxy:
  listen: 0.0.0.0:3128    # HTTP proxy for sandbox egress (L7 policy enforcement)

logging:
  level: info
  format: json
  output: stdout

policies:
  directory: /opt/fullsend/policies  # Policy templates
  reload: true  # Hot-reload policies without restarting gateway

providers:
  directory: /opt/fullsend/providers  # Provider definitions
```

### Sandbox Creation Flow

1. **Agent job starts**: CI runner pulls and starts the container image. OpenShell gateway starts as PID 1.
2. **Fullsend harness invokes**: `fullsend run triage` (or other agent name) executes inside the container.
3. **Load agent config**: Harness reads `/opt/fullsend/agents/<agent-name>/config.yaml` to determine required policies and providers.
4. **Create sandbox via API**: Harness calls `POST http://127.0.0.1:8080/v1/sandboxes` with policy and provider configuration.
5. **OpenShell creates namespace**: Gateway creates a new Linux namespace (network, mount, PID, IPC) for the agent process.
6. **Apply L7 policies**: Gateway configures iptables rules to route all sandbox egress through the proxy (port 3128), applies HTTP method + path restrictions.
7. **Inject providers**: Gateway configures provider placeholders (opaque tokens) that the proxy will swap for real credentials at runtime.
8. **Execute agent**: Harness executes the agent binary (Claude Code) inside the sandbox. Agent sees isolated filesystem and network.
9. **Enforce policies**: All agent HTTP requests go through the proxy. Proxy enforces L7 policies, swaps provider placeholders for credentials, logs all requests.
10. **Sandbox terminates**: Agent completes, harness reads output, sandbox namespace is destroyed.

### Policy Definition Example

Agent-specific L7 network policies are stored in `/opt/fullsend/agents/<agent-name>/policies/`:

```yaml
# /opt/fullsend/agents/triage/policies/github-read.yaml
# L7 policy for triage agent: read-only GitHub API access

name: github-read
description: Read-only access to GitHub issues and pull requests

rules:
  # Allow reading issues
  - endpoint: "https://api.github.com/repos/*/*/issues/*"
    methods: [GET]
    binaries: [gh, curl]  # Only gh and curl can call this endpoint

  # Allow listing issues
  - endpoint: "https://api.github.com/repos/*/*/issues"
    methods: [GET]
    binaries: [gh, curl]

  # Deny all other GitHub API calls
  - endpoint: "https://api.github.com/**"
    methods: [GET, POST, PUT, PATCH, DELETE]
    action: deny
```

Binary-level enforcement (`binaries: [gh, curl]`) prevents the agent from crafting raw HTTP requests to bypass intended tool usage. The gateway walks `/proc/<pid>/exe` to identify the calling binary.

### Provider Configuration Example

Providers inject credentials as opaque placeholders that the proxy swaps at runtime:

```yaml
# /opt/fullsend/providers/github.yaml
# GitHub API token provider

name: github
type: header
config:
  header: Authorization
  value_template: "Bearer {{GITHUB_TOKEN}}"
  placeholder: "Bearer __GITHUB_TOKEN__"

# The agent sees: Authorization: Bearer __GITHUB_TOKEN__
# The proxy sends: Authorization: Bearer ghp_realtoken123...
```

## Resource Limits and Timeouts

Agent jobs must have resource limits to prevent runaway processes and control costs.

### GitHub Actions

GitHub Actions applies runner-level limits (VM size: 2 CPU, 7 GB RAM, 14 GB SSD for Linux runners). Per-job timeouts are set in the workflow:

```yaml
jobs:
  run-agent:
    runs-on: ubuntu-latest
    timeout-minutes: 15  # Maximum 15 minutes per agent run
    steps:
      - name: Run agent
        run: |
          docker run --rm \
            --memory=4g --cpus=1.5 \
            -e AGENT_NAME=${{ inputs.agent_name }} \
            -e EVENT_PAYLOAD=${{ inputs.event_payload }} \
            ghcr.io/fullsend-ai/agent-sandbox:v1.2.3
```

### GitLab CI (Docker Executor)

GitLab runner with Docker executor supports container resource limits via runner configuration:

```toml
# /etc/gitlab-runner/config.toml
[[runners]]
  name = "fullsend-agent-runner"
  executor = "docker"

  [runners.docker]
    image = "ghcr.io/fullsend-ai/agent-sandbox:v1.2.3"
    privileged = true  # Required for OpenShell network namespace manipulation
    cpus = "1.5"
    memory = "4g"
    memory_swap = "4g"  # Prevent swap usage
```

Per-job timeout in `.gitlab-ci.yml`:

```yaml
run-agent:
  image: ghcr.io/fullsend-ai/agent-sandbox:v1.2.3
  timeout: 15 minutes
  script:
    - fullsend run $AGENT_NAME
```

### GitLab CI (Kubernetes Executor)

Kubernetes executor uses pod resource requests and limits:

```toml
# /etc/gitlab-runner/config.toml
[[runners]]
  name = "fullsend-k8s-runner"
  executor = "kubernetes"

  [runners.kubernetes]
    image = "ghcr.io/fullsend-ai/agent-sandbox:v1.2.3"
    namespace = "fullsend-agents"
    privileged = true

    cpu_request = "1"
    cpu_limit = "1.5"
    memory_request = "2Gi"
    memory_limit = "4Gi"

    service_cpu_request = "0.1"
    service_memory_request = "128Mi"
```

### Recommended Limits

Based on experiments with Claude Code agents (see [experiments repo](https://github.com/fullsend-ai/experiments)):

| Agent Type | CPU | Memory | Timeout | Rationale |
|------------|-----|--------|---------|-----------|
| Triage | 1.0 | 2 GB | 5 min | Lightweight, mostly API calls and text processing |
| Review | 1.5 | 4 GB | 15 min | Larger context windows, multiple file reads |
| Code | 2.0 | 8 GB | 30 min | May clone repos, run tests, compile code |
| Fix | 1.5 | 4 GB | 15 min | Similar to code but typically smaller scope |

## Privileged Container Requirements

OpenShell requires privileged container access (or at minimum `CAP_NET_ADMIN` capability) to manipulate network namespaces for L7 policy enforcement. This is the most significant security trade-off in the architecture.

### Why Privileged is Required

OpenShell creates network namespaces for each sandbox and uses iptables to route sandbox traffic through the proxy. Network namespace creation requires `CAP_NET_ADMIN` (or full privileged mode).

### Alternatives to Privileged Mode

#### User Namespace Remapping (Rootless Docker)

Run the container as a non-root user inside a user namespace. The user appears as root inside the namespace but is unprivileged on the host.

**GitHub Actions:**
```yaml
- name: Run agent with rootless Docker
  run: |
    dockerd-rootless.sh &
    until docker info >/dev/null 2>&1; do sleep 1; done
    docker run --rm \
      -e AGENT_NAME=triage \
      ghcr.io/fullsend-ai/agent-sandbox:v1.2.3
```

**GitLab Runner (Docker executor with user namespaces):**
```toml
[[runners]]
  executor = "docker"
  [runners.docker]
    userns_mode = "host"  # User namespace remapping
    cap_add = ["CAP_NET_ADMIN"]  # Grant only network capability
    privileged = false
```

**Status**: Experimental. OpenShell has not been tested extensively with user namespaces. Requires kernel support (CONFIG_USER_NS=y) and may have compatibility issues with certain Linux distributions.

#### eBPF-Based Policy Enforcement

Replace iptables-based L7 enforcement with eBPF programs that hook into the network stack without requiring privileged containers.

**Status**: Not yet implemented in OpenShell. This would require an upstream feature request for eBPF-based L7 enforcement.

#### Accept Privileged Requirement

Document that fullsend agents require privileged containers and provide guidance for security hardening:

1. **Dedicated runner pools**: Run agent workloads on isolated runners, not shared with other CI/CD jobs.
2. **Network segmentation**: Agent runners on separate VLANs with egress restrictions.
3. **Regular image scans**: Use Trivy, Grype, or Snyk to scan the agent sandbox image for vulnerabilities.
4. **Minimal base image**: Use distroless or minimal Ubuntu to reduce attack surface.
5. **PodSecurityPolicy exemptions** (Kubernetes): Create PSP exceptions for the fullsend-agents namespace with audit logging.

### Kubernetes PodSecurityPolicy Configuration

For organizations using Kubernetes, configure pod security via Pod Security Standards (Kubernetes 1.25+):

**Pod Security Standards (recommended - K8s 1.25+):**
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: fullsend-agents
  labels:
    pod-security.kubernetes.io/enforce: privileged
    pod-security.kubernetes.io/audit: restricted
    pod-security.kubernetes.io/warn: restricted
```

Namespace-level exemption with audit logging ensures privileged pods are allowed but logged for security review.

## GitLab Runner Configuration

GitLab runners must be configured to support the agent sandbox container image. Two executor types are supported: Docker and Kubernetes.

### Docker Executor Setup

**Install GitLab Runner:**
```bash
curl -L "https://packages.gitlab.com/install/repositories/runner/gitlab-runner/script.deb.sh" | sudo bash
sudo apt-get install gitlab-runner
```

**Register runner:**

> **Note:** GitLab deprecated registration tokens in favor of runner authentication tokens (starting with `glrt-`) in GitLab 15.10. The `--registration-token` flag was removed in GitLab 17.0. For GitLab 15.10+, create a runner authentication token in the GitLab UI (Settings → CI/CD → Runners → New runner) and use `--token` instead.

```bash
# For GitLab 15.10+ (recommended):
sudo gitlab-runner register \
  --url https://gitlab.com \
  --token $RUNNER_AUTHENTICATION_TOKEN \
  --executor docker \
  --description "fullsend-agent-runner" \
  --docker-image "ghcr.io/fullsend-ai/agent-sandbox:v1.2.3" \
  --docker-privileged

# For GitLab < 15.10 (legacy):
# sudo gitlab-runner register \
#   --url https://gitlab.com \
#   --registration-token $REGISTRATION_TOKEN \
#   --executor docker \
#   --description "fullsend-agent-runner" \
#   --docker-image "ghcr.io/fullsend-ai/agent-sandbox:v1.2.3" \
#   --docker-privileged

# Note: --docker-privileged is required for OpenShell network namespace manipulation.
# Do NOT mount /var/run/docker.sock as this would bypass sandbox isolation.
```

**Configure resource limits** (`/etc/gitlab-runner/config.toml`):
```toml
[[runners]]
  name = "fullsend-agent-runner"
  executor = "docker"
  [runners.docker]
    image = "ghcr.io/fullsend-ai/agent-sandbox:v1.2.3"
    privileged = true
    disable_cache = false
    volumes = ["/cache"]
    cpus = "1.5"
    memory = "4g"
    shm_size = 0
```

### Kubernetes Executor Setup

**Install GitLab Runner as a Kubernetes deployment:**

> **Note:** For GitLab 15.10+, create a runner authentication token in the GitLab UI (Settings → CI/CD → Runners → New runner) and use `runnerToken` instead of the deprecated `runnerRegistrationToken` (removed in GitLab 17.0).

```bash
helm repo add gitlab https://charts.gitlab.io

# For GitLab 15.10+ (recommended):
helm install gitlab-runner gitlab/gitlab-runner \
  --namespace fullsend-agents \
  --set runnerToken=$RUNNER_AUTHENTICATION_TOKEN \
  --set rbac.create=true \
  --set runners.privileged=true

# For GitLab < 15.10 (legacy):
# helm install gitlab-runner gitlab/gitlab-runner \
#   --namespace fullsend-agents \
#   --set runnerRegistrationToken=$REGISTRATION_TOKEN \
#   --set rbac.create=true \
#   --set runners.privileged=true
```

**Configure executor** (values.yaml):
```yaml
runners:
  config: |
    [[runners]]
      [runners.kubernetes]
        namespace = "fullsend-agents"
        image = "ghcr.io/fullsend-ai/agent-sandbox:v1.2.3"
        privileged = true
        cpu_request = "1"
        cpu_limit = "1.5"
        memory_request = "2Gi"
        memory_limit = "4Gi"
```

### Registry Authentication

The agent sandbox image is public on ghcr.io, so no authentication is required for pulls. For organizations hosting private forks:

**GitLab CI/CD variable:**
```yaml
# .gitlab-ci.yml
run-agent:
  image: registry.example.com/fullsend/agent-sandbox:v1.2.3
  # Note: The job's base image is pulled by the runner using its configured credentials
  # (CI_REGISTRY_USER/PASSWORD, image_pull_secrets, or runner config). Agents inside
  # the sandbox do NOT have Docker daemon access - they are isolated by OpenShell.
  script:
    - fullsend run $AGENT_NAME
```

**Kubernetes image pull secret:**
```bash
kubectl create secret docker-registry regcred \
  --docker-server=registry.example.com \
  --docker-username=$USERNAME \
  --docker-password=$PASSWORD \
  --namespace=fullsend-agents
```

Reference in runner config:
```yaml
runners:
  config: |
    [[runners]]
      [runners.kubernetes]
        image_pull_secrets = ["regcred"]
```

## Host-Side REST Server in Containers

ADR-0017 describes the host-side REST server pattern for credential isolation: a server runs outside the agent sandbox, holds credentials, and exposes scoped endpoints. L7 network policies enforce per-agent access.

In the containerized architecture, "host-side" means **outside the nested OpenShell sandbox, but inside the same container**. The container contains:

1. **OpenShell gateway** (PID 1): Manages sandbox lifecycle, enforces L7 policies
2. **Host-side REST server** (background process): Holds credentials, exposes API
3. **Agent sandbox** (isolated namespace): Runs the agent (Claude Code), has network policies restricting access to REST server endpoints

### Lifecycle

The fullsend harness (`fullsend run`) starts the REST server before creating the sandbox:

```bash
# Inside the container
fullsend run triage --event-payload "$EVENT_PAYLOAD"

# Harness steps:
# 1. Read agent config (/opt/fullsend/agents/triage/config.yaml)
# 2. Start REST server if required:
#    /opt/fullsend/servers/github-rest-server --port 8081 --token $GITHUB_TOKEN &
#    SERVER_PID=$!
# 3. Create OpenShell sandbox with L7 policy allowing http://127.0.0.1:8081/repos/*/*/issues (GET only)
# 4. Execute agent inside sandbox
# 5. Wait for agent completion
# 6. Kill REST server (kill $SERVER_PID)
# 7. Clean up sandbox namespace
```

The REST server and agent sandbox are isolated by network policy, not by separate VMs (as on GitHub-hosted runners). This is acceptable because:

- L7 policy enforcement is the security boundary, not VM isolation
- The container itself is ephemeral (destroyed after the job)
- No other jobs run in the same container (GitLab Docker/Kubernetes executors create fresh containers per job)

### REST Server Authentication

Even though the REST server is on localhost inside the container, it must authenticate requests to prevent:

1. **Timing overlap**: If the REST server startup or shutdown timing is off, another sandbox in the same container could call it (low risk, but defense-in-depth).
2. **Compromised gateway**: If OpenShell has a vulnerability allowing sandbox escape, the REST server should still require authentication.

**Per-run bearer token pattern:**
```bash
# Harness generates a random token for this run
RUN_TOKEN=$(uuidgen)

# Start server with token
/opt/fullsend/servers/github-rest-server --port 8081 --token $GITHUB_TOKEN --bearer-token $RUN_TOKEN &

# Pass token to sandbox via environment variable
openshell sandbox create \
  --policy /opt/fullsend/agents/triage/policies/github-read.yaml \
  --env BEARER_TOKEN=$RUN_TOKEN \
  -- claude code /opt/fullsend/agents/triage/instructions.md
```

Agent calls REST server with bearer token:
```bash
curl -H "Authorization: Bearer $BEARER_TOKEN" http://127.0.0.1:8081/repos/org/repo/issues/123
```

REST server validates the bearer token before processing requests.

## Image Signing and Verification

Container image signing ensures supply chain integrity: the image running on CI runners is the same image built by the official pipeline, without tampering.

### Signing with Sigstore Cosign

Sigstore cosign provides keyless signing using OIDC identity:

```bash
# Sign (done by CI pipeline)
cosign sign --yes ghcr.io/fullsend-ai/agent-sandbox:v1.2.3

# Verify (done by runners before execution)
cosign verify \
  --certificate-identity-regexp=https://github.com/fullsend-ai/fullsend \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
  ghcr.io/fullsend-ai/agent-sandbox:v1.2.3
```

The signature is stored in the container registry alongside the image. No private key management required (uses GitHub Actions OIDC token).

### Verification in CI Workflows

**GitHub Actions:**
```yaml
- name: Verify image signature
  run: |
    cosign verify \
      --certificate-identity-regexp=https://github.com/fullsend-ai/fullsend \
      --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
      ghcr.io/fullsend-ai/agent-sandbox:${{ inputs.image_tag }}

- name: Run agent
  run: docker run --rm ghcr.io/fullsend-ai/agent-sandbox:${{ inputs.image_tag }}
```

**GitLab CI:**
```yaml
run-agent:
  before_script:
    - apt-get update && apt-get install -y cosign
    - cosign verify \
        --certificate-identity-regexp=https://github.com/fullsend-ai/fullsend \
        --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
        ghcr.io/fullsend-ai/agent-sandbox:$IMAGE_TAG
  image: ghcr.io/fullsend-ai/agent-sandbox:$IMAGE_TAG
  script:
    - fullsend run $AGENT_NAME
```

### Policy Enforcement

Organizations can enforce image signature verification at the runner level:

**Docker Content Trust (GitHub Actions):**
```bash
export DOCKER_CONTENT_TRUST=1
export DOCKER_CONTENT_TRUST_SERVER=https://notary.docker.io
```

**Kubernetes admission controller (Kyverno):**
```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: verify-images
spec:
  validationFailureAction: enforce
  rules:
    - name: verify-fullsend-agent-sandbox
      match:
        resources:
          kinds:
            - Pod
      verifyImages:
        - imageReferences:
            - "ghcr.io/fullsend-ai/agent-sandbox:*"
          attestors:
            - entries:
                - keyless:
                    subject: "https://github.com/fullsend-ai/fullsend/.github/workflows/build-agent-sandbox.yml@*"
                    issuer: "https://token.actions.githubusercontent.com"
```

## Upgrade and Rollback

Upgrading the agent sandbox image requires coordination across the config repo and all enrolled repos.

### Upgrade Process

1. **Build and test new image**: CI pipeline builds `ghcr.io/fullsend-ai/agent-sandbox:v1.3.0`, runs integration tests.
2. **Sign and publish**: Image is signed with Sigstore and pushed to registry.
3. **Update config repo templates**: PR to `.fullsend` repo updates image tag in workflow templates.
4. **Propagate to enrolled repos**: Renovate bot (or manual PRs) updates image tags in enrolled repo workflows.
5. **Monitor rollout**: Observe agent success rates, error logs, resource usage.

### Automated Template Updates (Renovate)

Renovate bot can automatically create PRs to update image tags:

```json
// .fullsend/.github/renovate.json
{
  "extends": ["config:base"],
  "dockerfile": {
    "enabled": true
  },
  "regexManagers": [
    {
      "fileMatch": ["^\\.github/workflows/.*\\.ya?ml$", "^\\.gitlab/ci/.*\\.ya?ml$"],
      "matchStrings": ["image:\\s+ghcr\\.io/fullsend-ai/agent-sandbox:(?<currentValue>.*?)\\s"],
      "datasourceTemplate": "docker",
      "depNameTemplate": "ghcr.io/fullsend-ai/agent-sandbox"
    }
  ]
}
```

Renovate will:
1. Detect new image tags in ghcr.io
2. Create PRs to update workflow files in enrolled repos
3. Auto-merge if CI passes (optional, controlled by Renovate config)

### Rollback

If a new image version causes issues:

1. **Immediate**: Revert the config repo template PR, restore previous image tag.
2. **Enrolled repos**: Renovate creates rollback PRs automatically when config repo downgrades image tag.
3. **Manual override**: Enrolled repos can pin a specific image tag locally until the issue is resolved.

### Version Skew Tolerance

Enrolled repos may run different image versions during rollout. The architecture must tolerate version skew:

- **Agent harness protocol**: Breaking changes to `fullsend run` CLI interface require major version bump.
- **OpenShell API**: Gateway API must maintain backward compatibility for sandbox creation.
- **L7 policy syntax**: Policy file format changes should support old and new syntax during transition periods.

## Platform-Specific Considerations

### GitHub Actions VM Runners

GitHub Actions Linux runners provide ephemeral VMs with Docker pre-installed. Each job gets a fresh VM.

**Advantages:**
- Strong isolation (job-to-job isolation is VM boundary)
- Docker available by default, no runner setup required
- Host-side REST server in separate workflow step shares localhost but is on separate VM from other jobs

**Disadvantages:**
- Container-in-VM overhead (nested virtualization for Docker)
- Slower than bare-metal container execution
- GitHub Actions timeout limits (6 hours max, 360 minutes, far above agent needs but affects very long-running jobs)

**Configuration:**
```yaml
jobs:
  run-agent:
    runs-on: ubuntu-latest  # Ephemeral VM with Docker
    steps:
      - name: Run agent
        run: |
          docker run --rm --privileged \
            -e AGENT_NAME=triage \
            -e EVENT_PAYLOAD='${{ toJSON(github.event) }}' \
            ghcr.io/fullsend-ai/agent-sandbox:v1.2.3
```

### GitLab Docker Executor

GitLab runner with Docker executor runs containers directly on the runner host (Linux VM or bare metal).

**Advantages:**
- No nested virtualization, faster than GitHub Actions VM approach
- Native container execution
- Can reuse Docker layer cache across jobs (faster image pulls)

**Disadvantages:**
- Jobs on the same runner share the Docker daemon (potential for interference, requires runner isolation strategy)
- Privileged containers have more host access than GitHub Actions VMs
- Runner registration and configuration required (not provided by GitLab SaaS for free tier)

**Configuration:**
See [GitLab Runner Configuration](#gitlab-runner-configuration) section above.

### GitLab Kubernetes Executor

GitLab runner with Kubernetes executor creates pods for each job.

**Advantages:**
- Native Kubernetes resource limits (CPU, memory, ephemeral storage)
- Pod security policies and network policies for additional security controls
- Service mesh integration (Istio, Linkerd) for observability and traffic control
- Autoscaling via cluster autoscaler

**Disadvantages:**
- Kubernetes cluster required (not available on GitLab SaaS free tier by default)
- Pod scheduling latency (slower than direct Docker execution)
- Privileged pod requirement may conflict with organizational security policies

**Configuration:**
See [GitLab Runner Configuration](#gitlab-runner-configuration) section above.

### Container Runtime Alternatives

**Podman (rootless):**
OpenShell 0.0.37-dev+ supports Podman. Rootless Podman can run containers without privileged access on the host.

```bash
# Install Podman
sudo apt-get install -y podman

# Run agent with rootless Podman
podman run --rm \
  -e AGENT_NAME=triage \
  ghcr.io/fullsend-ai/agent-sandbox:v1.2.3
```

**Status**: Experimental. Requires OpenShell validation in rootless mode. May have performance or compatibility issues.

**Kata Containers (microVM isolation):**
Kata Containers provide VM-level isolation for containers using lightweight VMs (Firecracker, QEMU).

```yaml
# Kubernetes RuntimeClass for Kata Containers
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: kata
handler: kata

---
# Use Kata runtime for agent pods
apiVersion: v1
kind: Pod
metadata:
  name: fullsend-agent
spec:
  runtimeClassName: kata
  containers:
    - name: agent
      image: ghcr.io/fullsend-ai/agent-sandbox:v1.2.3
```

**Advantages**: Stronger isolation than standard containers (VM boundary).
**Disadvantages**: Higher overhead, slower startup, requires Kata-enabled Kubernetes nodes.

## Open Questions

### OpenShell Performance in Nested Container Environments

**Problem**: OpenShell creates nested Linux namespaces for sandboxes. Running OpenShell inside a container (which is already a namespace) creates three levels: VM/host → container → sandbox. Does this nesting impact performance or compatibility?

**Testing needed**:
- Benchmark agent run time: native Linux vs Docker vs Kubernetes pod
- Validate L7 policy enforcement in all three environments
- Test network namespace creation limits (how many concurrent sandboxes can one gateway manage?)

**Status**: Limited production data. Initial experiments (see [experiments repo](https://github.com/fullsend-ai/experiments)) show acceptable performance (<5% overhead for Docker-in-VM vs native), but large-scale production testing needed.

### Builder Services for Container Image Builds

**Problem**: Code agents that need to build container images (validate Dockerfiles, test image builds) cannot use Docker-in-Docker without privileged nested containers (severe security risk).

**Options**:
1. **External Kaniko service**: Deploy Kaniko or Buildkit as a separate service, agent submits build requests via API.
2. **Prohibit container builds**: Document that agents cannot build images, use static Dockerfile analysis only.
3. **Sidecar Kaniko pod** (Kubernetes only): Spawn ephemeral Kaniko sidecar for each agent run, agent communicates via shared volume.

**Status**: No current agents require container builds. Defer until concrete use case emerges. If needed, external builder service is the architecturally sound option.

### Multi-Architecture Support (arm64)

**Problem**: The container image is currently built for linux/amd64 only. Some organizations use ARM-based runners (AWS Graviton, Apple Silicon for macOS GitHub Actions runners).

**Options**:
1. **Multi-arch image**: Build and publish linux/amd64 and linux/arm64 variants.
2. **Architecture-specific images**: Separate image tags for each architecture.
3. **No ARM support**: Document amd64-only requirement.

**Status**: OpenShell supports ARM64. Buildx can build multi-arch images. Requires testing on ARM runners and updating build pipeline. Defer until ARM runner adoption is significant.

### Image Size Optimization

**Problem**: Full runtime image is ~1.5-2GB. First pull on a new runner takes 30-60 seconds.

**Options**:
1. **Multi-stage build optimization**: Remove build-time dependencies from final image.
2. **Distroless base**: Use distroless or minimal base image (Alpine) to reduce OS layer size.
3. **Layer caching**: Ensure runner Docker cache is persistent across jobs.
4. **Per-agent images**: Ship minimal base image, per-agent images add only required tools.

**Status**: Current Dockerfile uses multi-stage builds. Distroless may conflict with OpenShell requirements (glibc, shell for scripts). Layer caching works on GitLab Docker executor but not GitHub Actions (ephemeral VMs). Per-agent images violate single-image architecture decision. Monitor image pull latency in production, optimize if it becomes a bottleneck.

## References

- [ADR 0036: Agent Execution Sandbox Architecture](../ADRs/0036-agent-execution-sandbox.md)
- [ADR 0017: Credential Isolation for Sandboxed Agents](../ADRs/0017-credential-isolation-for-sandboxed-agents.md)
- [ADR 0025: Provider Credential Delivery](../ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md)
- [ADR 0028: GitLab Support Architecture](../ADRs/0028-gitlab-support.md) (superseded by [ADR 0043](../ADRs/0043-gitlab-support-via-webhook-bridge.md))
- [agent-infrastructure.md](../problems/agent-infrastructure.md): Infrastructure layer exploration
- [OpenShell Documentation](https://docs.nvidia.com/openshell/)
- [Sigstore Cosign](https://docs.sigstore.dev/cosign/overview/)
- [GitLab Runner Documentation](https://docs.gitlab.com/runner/)
