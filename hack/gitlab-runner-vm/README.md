# GitLab Runner VM

Provisions GitLab Runner VMs on OpenShift Virtualization or GCE (Google
Compute Engine) with a Podman custom executor and OpenShell gateway for
fullsend agent jobs.

## Architecture

Each runner VM runs:
- **gitlab-runner** (custom executor) — receives CI jobs from GitLab
- **Podman** (rootless) — creates per-job containers
- **OpenShell gateway** — provides sandbox compute for fullsend agents

Job containers use `--network=host` to reach the gateway. An OCI
`createRuntime` hook injects the host CA trust bundle into every container
(Debian and RHEL-family layouts) so jobs can verify internal TLS endpoints.
Gateway mTLS credentials are mounted read-only from the runner user's
OpenShell config.

This is a deployment variant of the container isolation model described in
ADR-0036. It uses a Podman custom executor instead of Docker/Kubernetes
executors but maintains equivalent container-level isolation for agent jobs.

See also:
- [ADR 0036: Agent Execution Sandbox](../../docs/ADRs/0036-agent-execution-sandbox.md)

## Quick start — OpenShift Virtualization

> **Requires:** `oc`, `virtctl` (client >= 1.5 — ships with OpenShift Virtualization >= 4.19),
> `python3`, and a local `ssh` binary (virtctl wraps it via ProxyCommand).

```bash
# 1. Create and provision a VM — group-scoped runner (recommended):
GL_TOKEN=glpat-xxx GROUP_ID=12345 \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-openshift-vm.sh

# Or project-scoped runner (for single-project deployments):
GL_TOKEN=glpat-xxx PROJECT_ID=12345 \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-openshift-vm.sh

# 2. Delete a VM:
GL_TOKEN=glpat-xxx \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  ./delete-openshift-vm.sh fullsend-gitlab-runner-01

# 3. List VMs:
NAMESPACE=my-namespace ./delete-openshift-vm.sh --list
```

## Quick start — GCE (Google Compute Engine)

> **Requires:** `gcloud` CLI (authenticated), `python3`, `curl`.
> By default, VMs have no public IP — SSH access is tunneled via [IAP](https://cloud.google.com/iap/docs/using-tcp-forwarding)
> (Cloud IAP API must be enabled; operator needs `roles/iap.tunnelResourceAccessor`).
> The VPC subnet must have [Cloud NAT](https://cloud.google.com/nat/docs/overview)
> configured — without it, VMs created with `--no-address` cannot reach
> package mirrors or container registries and `dnf install` will fail.
> Set `GCP_USE_IAP=false` to create the VM with an external IP and SSH directly.

```bash
# 1. Create and provision a VM — group-scoped runner (recommended):
GL_TOKEN=glpat-xxx GROUP_ID=12345 \
  GITLAB_URL=https://gitlab.example.com \
  GCP_PROJECT=my-gcp-project \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-gcp-vm.sh

# Or project-scoped runner (for single-project deployments):
GL_TOKEN=glpat-xxx PROJECT_ID=12345 \
  GITLAB_URL=https://gitlab.example.com \
  GCP_PROJECT=my-gcp-project \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-gcp-vm.sh

# 2. Delete a VM:
GL_TOKEN=glpat-xxx \
  GITLAB_URL=https://gitlab.example.com \
  GCP_PROJECT=my-gcp-project \
  ./delete-gcp-vm.sh fullsend-gitlab-runner-01

# 3. List VMs:
GCP_PROJECT=my-gcp-project ./delete-gcp-vm.sh --list
```

## Environment variables

### Shared

| Variable | Required | Default | Description |
|---|---|---|---|
| `GL_TOKEN` | yes | — | GitLab PAT (Owner role on the target group or project, scopes: `create_runner` + `manage_runner` + `api`) |
| `PROJECT_ID` | yes (create)¹ | — | GitLab project ID — registers a project-scoped runner (`locked=true`) |
| `GROUP_ID` | yes (create)¹ | — | GitLab group ID — registers a group-scoped runner (`locked=false`). Recommended for platform-service deployments |
| `GITLAB_URL` | yes | — | GitLab instance URL |
| `RUNNER_IMAGE` | yes (create) | — | Image pre-pulled as a warm cache; jobs must still set `image:` in `.gitlab-ci.yml` |
| `RUNNER_TAG` | no | `fullsend-gitlab-runner` | Runner tag for job matching |
| `RUNNER_ACCESS_LEVEL` | no | `not_protected` | `ref_protected` restricts the runner to protected branches and tags, so merge-request pipelines on unprotected source refs never match and sit `pending`. In project mode, `not_protected` means any job on any branch of the project can run; in group mode, any tag-matched job on any branch of any project invited into the group tree runs on this VM (see Security below) |
| `OPENSHELL_VERSION` | no | from `.github/scripts/openshell-version.sh` | OpenShell version (Renovate-tracked) |
| `GITLAB_RUNNER_VERSION` | no | `19.2.1` | gitlab-runner version |
| `REGISTRATION_TOKEN` | setup only | — | GitLab runner registration token |

¹ Exactly one of `PROJECT_ID` or `GROUP_ID` must be set (mutually exclusive).

### OpenShift-specific

| Variable | Required | Default | Description |
|---|---|---|---|
| `NAMESPACE` | yes (create/delete) | — | OpenShift namespace |
| `VM_USER` | no | `fedora` | Cloud-image login user (`cloud-user` on RHEL/CentOS Stream images) |
| `SSH_PUBLIC_KEY` | no | contents of `~/.ssh/id_rsa.pub` or `id_ed25519.pub` | SSH public key contents (not a path) |

### GCE-specific

| Variable | Required | Default | Description |
|---|---|---|---|
| `GCP_PROJECT` | yes | — | GCP project ID |
| `GCP_ZONE` | no | `us-east1-b` | GCE zone |
| `GCP_MACHINE_TYPE` | no | `e2-standard-4` | GCE machine type (4 vCPU, 16 GB — closest to the 4 CPU / 14 GiB KubeVirt spec) |
| `GCP_NETWORK` | no | `gitlab-runners` | VPC network (must have IAP ingress and egress firewall rules) |
| `GCP_SUBNET` | no | — | VPC subnet (required for custom-mode VPCs; omit for auto-mode) |
| `GCP_USE_IAP` | no | `true` | Use IAP tunneling for SSH. Set to `false` to create the VM with an external IP and SSH directly. |
| `GCP_IMAGE_FAMILY` | no | `fedora-cloud-43` | GCE image family |
| `GCP_IMAGE_PROJECT` | no | `fedora-cloud` | GCE image project |

## Files

- `create-openshift-vm.sh` — end-to-end VM creation on OpenShift + runner registration + setup
- `delete-openshift-vm.sh` — OpenShift VM teardown + runner deregistration
- `create-gcp-vm.sh` — end-to-end VM creation on GCE + runner registration + setup
- `delete-gcp-vm.sh` — GCE VM teardown + runner deregistration
- `setup.sh` — standalone VM configuration (called by create-openshift-vm.sh / create-gcp-vm.sh)
- `gitlab-runner-version.sh` — central pin for the gitlab-runner version
- `vm.yaml` — KubeVirt VirtualMachine template (OpenShift only)
- `executor/prepare.sh` — custom executor prepare stage
- `executor/run.sh` — custom executor run stage
- `executor/cleanup.sh` — custom executor cleanup stage

## Security notes

- By default (`GCP_USE_IAP=true`), GCE VMs are created with `--no-address`
  (no public IP) and SSH access is tunneled through IAP, which requires the
  operator to have `roles/iap.tunnelResourceAccessor` and authenticates via
  GCP IAM — mirroring the OpenShift model where SSH is tunneled through the
  K8s API server. When `GCP_USE_IAP=false`, the VM gets an external IP and
  SSH connects directly with trust-on-first-use host-key verification
  (`StrictHostKeyChecking=accept-new`) — the first connection accepts the key
  and subsequent connections within the same run reject changes.
  The script prints a command to remove the external IP afterward.
- The CA trust bootstrap uses trust-on-first-use (TOFU). For higher assurance,
  provide the CA bundle out-of-band before running setup.sh.
- The OCI CA-injection hook fires for all containers on the host. It only
  copies a CA bundle file and is scoped to the `createRuntime` stage.
- Job containers share the host network namespace (`--network=host`) to reach
  the OpenShell gateway. The gateway binds to `0.0.0.0` (required for the
  Podman compute driver — sandbox containers register via
  `host.containers.internal`). mTLS protects the endpoint.
- Job containers receive read-only access to the runner's gateway mTLS
  credentials (`~/.config/openshell`). This is required for the fullsend
  agent inside job containers to authenticate to the gateway. Credential
  exposure depends on the runner scope:
  - **Project mode** (`PROJECT_ID`): the runner is scoped to one project by
    `runner_type=project_type` and `locked=true`, so only jobs from that
    project can access these credentials.
  - **Group mode** (`GROUP_ID`): the runner is scoped to the group by
    `runner_type=group_type`, so any project invited into the group tree can
    run tag-matched jobs on this VM and access the mounted gateway credentials.
    **This is a wider trust boundary than project mode** — credential access
    extends from a single project to every project in the group tree. For
    group-scoped runners, consider setting `RUNNER_ACCESS_LEVEL=ref_protected`
    as a compensating control to restrict jobs to protected branches and tags.
  In both modes, access is narrowed by `run_untagged=false` (only jobs tagged
  with the runner's tag are matched) and optionally by `ref_protected`
  (restricting to protected branches/tags). If job-scoped credential minting
  is added to the gateway, this mount should be replaced with short-lived
  per-job tokens.
