# GitLab Runner VM

Provisions GitLab Runner VMs on OpenShift Virtualization with a Podman custom
executor and OpenShell gateway for fullsend agent jobs.

## Architecture

Each runner VM runs:
- **gitlab-runner** (custom executor) — receives CI jobs from GitLab
- **Podman** (rootless) — creates per-job containers
- **OpenShell gateway** — provides sandbox compute for fullsend agents

Job containers use `--network=host` to reach the gateway on `127.0.0.1:17670`.
An OCI `createRuntime` hook injects the host CA trust bundle into every
container so the OpenShell supervisor can verify internal TLS endpoints.

This is a deployment variant of the container isolation model described in
ADR-0036. It uses a Podman custom executor instead of Docker/Kubernetes
executors but maintains equivalent container-level isolation for agent jobs.

See also:
- [ADR 0036: Agent Execution Sandbox](../../docs/ADRs/0036-agent-execution-sandbox.md)
- [Agent Execution Environment plan](../../docs/plans/agent-execution-environment.md)

## Quick start

```bash
# 1. Create and provision a VM (auto-numbers):
GL_TOKEN=glpat-xxx PROJECT_ID=266558 \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 \
  ./create-vm.sh

# 2. Delete a VM:
GL_TOKEN=glpat-xxx \
  GITLAB_URL=https://gitlab.example.com \
  NAMESPACE=my-namespace \
  ./delete-vm.sh fullsend-gitlab-runner-01

# 3. List VMs:
NAMESPACE=my-namespace ./delete-vm.sh --list
```

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `GL_TOKEN` | yes | — | GitLab personal access token |
| `PROJECT_ID` | yes (create) | — | GitLab project ID |
| `GITLAB_URL` | yes | — | GitLab instance URL |
| `NAMESPACE` | yes (create/delete) | — | OpenShift namespace |
| `RUNNER_IMAGE` | yes | — | Container image for jobs |
| `RUNNER_TAG` | no | `fullsend-gitlab-runner` | Runner tag for job matching |
| `OPENSHELL_VERSION` | no | `0.0.83` | OpenShell version |
| `GITLAB_RUNNER_VERSION` | no | `17.8.3` | gitlab-runner version |
| `SSH_PUBLIC_KEY` | no | `~/.ssh/id_rsa.pub` | SSH public key for VM access |
| `REGISTRATION_TOKEN` | setup only | — | GitLab runner registration token |

## Files

- `create-vm.sh` — end-to-end VM creation + runner registration + setup
- `delete-vm.sh` — VM teardown + runner deregistration
- `setup.sh` — standalone VM configuration (called by create-vm.sh)
- `vm.yaml` — KubeVirt VirtualMachine template
- `executor/prepare.sh` — custom executor prepare stage
- `executor/run.sh` — custom executor run stage
- `executor/cleanup.sh` — custom executor cleanup stage

## Security notes

- The CA trust bootstrap uses trust-on-first-use (TOFU). For higher assurance,
  provide the CA bundle out-of-band before running setup.sh.
- The OCI CA-injection hook fires for all containers on the host. It only
  copies a CA bundle file and is scoped to the `createRuntime` stage.
- Job containers share the host network namespace (`--network=host`) to reach
  the OpenShell gateway. The gateway binds to `127.0.0.1` only.
