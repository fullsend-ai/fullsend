# Running agents locally

This guide walks through running agents with fullsend on your machine. It
sets the base to help you run any agent, default or custom. Both macOS and
Linux are supported with Podman as the container runtime.

> For building fullsend from source or contributing to the CLI, see [Local development](../dev/local-dev.md).

## Prerequisites

| Requirement | macOS | Linux |
|-------------|-------|-------|
| Container runtime | Podman Desktop with a running machine | Podman |
| [OpenShell](https://github.com/NVIDIA/OpenShell) | 0.0.38 | 0.0.38 |
| GCP project | [Agent Platform API](https://console.cloud.google.com/apis/library/aiplatform.googleapis.com) enabled with [Claude models](https://console.cloud.google.com/vertex-ai/model-garden) enabled | Same |
| GCP credentials | Service account key (see section below) | Same |
| GitHub PAT | Classic PAT with `repo` scope (see section below) | Same |

## Download the fullsend CLI

Download the latest release from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases).
Pick the archive matching your platform:

| Platform | Archive |
|----------|---------|
| macOS (Apple Silicon) | `fullsend_{version}_darwin_arm64.tar.gz` |
| macOS (Intel) | `fullsend_{version}_darwin_amd64.tar.gz` |
| Linux (x86_64) | `fullsend_{version}_linux_amd64.tar.gz` |
| Linux (arm64) | `fullsend_{version}_linux_arm64.tar.gz` |

Extract and move to a directory in your PATH:

```bash
tar xzf fullsend_{version}_darwin_arm64.tar.gz
mv fullsend_{version}_darwin_arm64/fullsend $HOME/.local/bin/
```

Verify the installation:

**Note**: the `fullsend` binary is not signed, on macOS you need to run
`xattr -d com.apple.quarantine fullsend`

```bash
fullsend --version
```

## Install OpenShell

[OpenShell](https://github.com/NVIDIA/OpenShell) provides the sandbox runtime. There are multiple ways
to install it, here we use one similar to how we download it on Fullsend. Use the same version
printed on your Fullsend workflow for better reproducibility. Install the CLI and download
the gateway binary:

```bash
# Install the CLI (requires uv — https://docs.astral.sh/uv/)
uv tool install openshell==0.0.38

openshell --version
```

Download the gateway binary from the [OpenShell releases](https://github.com/NVIDIA/OpenShell/releases/tag/v0.0.38) page. Pick the archive matching your platform and extract it:

```bash
# Example for macOS (Apple Silicon)
curl -fsSL https://github.com/NVIDIA/OpenShell/releases/download/v0.0.38/openshell-gateway-aarch64-apple-darwin.tar.gz \
  -o /tmp/openshell-gateway.tar.gz
tar xzf /tmp/openshell-gateway.tar.gz -C $HOME/.local/bin/
```

## Get Google Cloud Platform credentials

Fullsend uses GCP's VertexAI to run inference, so you need a GCP project. After authenticating on `gcloud` run:

```bash
gcloud iam service-accounts create fullsend-local \
  --display-name="Fullsend local agent runner" \
  --project={project-id}

gcloud projects add-iam-policy-binding {project-id} \
  --member="serviceAccount:fullsend-local@{project-id}.iam.gserviceaccount.com" \
  --role="roles/aiplatform.user"

gcloud iam service-accounts keys create fullsend-local-credentials.json \
  --iam-account=fullsend-local@{project-id}.iam.gserviceaccount.com
chmod 600 fullsend-local-credentials.json
```

This creates a service account and a local file to authenticate with that account. If you lack
permissions give yourself or ask your GCP administrator for permissions or a key for local development.

Create an environment file somewhere secure, current directory or `$HOME` may be a good option. In our
example it is `./fullsend-gcp.env`:

```bash
# fullsend-gcp.env
ANTHROPIC_VERTEX_PROJECT_ID={project-id}
GOOGLE_CLOUD_PROJECT={project-id}
CLOUD_ML_REGION=global
GOOGLE_APPLICATION_CREDENTIALS=fullsend-local-credentials.json
```

## Get a GitHub token

Create a [fine grained token](https://github.com/settings/personal-access-tokens) at GitHub. The
permissions depend on the agent to execute, but generally with Write access to Issues, Contents and
Pull Requests you cover most of them. If this is not enough, explore the codebase or ask
in our community channels.

## Clone repositories

First clone your target repository locally:

```bash
git clone git@github:{org}/{target_repository} /tmp/target-repo
```

Next clone the repository where the agent lives, in this guide case you need to
clone Fullsend's repository. To learn more about custom agents visit
[Customizing Agents](customizing-agents.md).

```bash
git clone --depth 1 https://github.com/fullsend-ai/fullsend.git /tmp/fullsend-ai_fullsend/
```

## Start the OpenShell gateway

OpenShell requires a gateway to work, start one on a different terminal using Podman:

```bash
# On arm64 hosts (Apple Silicon, Graviton), uncomment these lines:
# export FULLSEND_SANDBOX_IMAGE=ghcr.io/fullsend-ai/fullsend-sandbox:dev
# podman machine start

export OPENSHELL_SSH_HANDSHAKE_SECRET="local-$(openssl rand -hex 16)"
# v0.0.38 requires an explicit supervisor image (version-tagged images start at 0.0.41)
export OPENSHELL_SUPERVISOR_IMAGE="ghcr.io/nvidia/openshell/supervisor:dfd47683e7da4f1a4a8fa5d77f92d3696e6a41f9"

openshell-gateway \
  --bind-address 0.0.0.0 \
  --health-port 8081 \
  --drivers podman \
  --disable-tls \
  --db-url "sqlite:/tmp/gateway.db?mode=rwc"
```

Wait for the health check to pass, then register the gateway:

```bash
# Health endpoint is on port 8081, API on port 8080
for i in $(seq 1 15); do
  curl -sf http://127.0.0.1:8081/healthz >/dev/null 2>&1 && break
  sleep 2
done

# Register and select the local gateway
openshell gateway add http://127.0.0.1:8080 --local --name local
openshell gateway select local
```

## Run default agents

Depending on the agent you want to run you need a different set of environment variables.
Check the variables they need in their environment files, referenced in their harness files.

**Tip**: use `--no-post-script` in the `fullsend run` calls to avoid side-effects.

**Note**: to run custom agents set `--fullsend-dir` to the directory where your
custom agent definitions exist.

### Triage agent

Add to an env file:

```bash
# fullsend-triage.env
GH_TOKEN={github-pat}
GITHUB_ISSUE_URL=https://github.com/{org}/{repo}/issues/{issue_num}
```

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-ai_fullsend/internal/scaffold/fullsend-repo/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env
```

### Review agent

Add to an env file:

```bash
# fullsend-review.env
REVIEW_TOKEN={github-pat}
GITHUB_PR_URL="https://github.com/{org}/{repo}/pull/{pr_number}"
PR_NUMBER="{pr_number}"
REPO_FULL_NAME="{org}/{repo}"
```

```bash
fullsend run review \
  --fullsend-dir /tmp/fullsend-ai_fullsend/internal/scaffold/fullsend-repo/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-review.env
```

### Code agent

Add to an env file:

```bash
# fullsend-code.env
GH_TOKEN={github-pat}
PUSH_TOKEN={github-pat}
PUSH_TOKEN_SOURCE=github-app
GITHUB_ISSUE_URL=https://github.com/{org}/{repo}/issues/{issue_num}
REPO_FULL_NAME={org}/{repo}
ISSUE_NUMBER={issue_num}
TARGET_BRANCH=main
REPO_DIR=/tmp/repo-dir
GITHUB_WORKSPACE=/tmp/
```

```bash
fullsend run code \
  --fullsend-dir /tmp/fullsend-ai_fullsend/internal/scaffold/fullsend-repo/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-code.env
```

### Status notification flags

When running agents locally you can optionally enable status comments on the
target issue/PR. These flags mirror what the CI workflows pass automatically:

| Flag | Description |
|------|-------------|
| `--run-url` | URL of the CI/CD run shown in the status comment |
| `--status-repo` | Repository (`owner/repo`) to post status comments on |
| `--status-number` | Issue or PR number for status comments |
| `--status-token` | Token for posting comments (defaults to `GH_TOKEN`) |

Example:

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-ai_fullsend/internal/scaffold/fullsend-repo/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env \
  --status-repo myorg/myrepo \
  --status-number 42 \
  --run-url "https://github.com/myorg/myrepo/actions/runs/12345"
```

Status comment behavior is configured via `status_notifications` in
`config.yaml`. See the [installation guide](../getting-started/installation.md#status-notifications).

## Simulating Fullsend's real customization layers

Fullsend automatically aggregates different layers of information before running `fullsend run`.
In case you want to test how customizations impact default agents, or you custom agents, follow the
next steps.

Start by cloning `fullsend-ai/fullsend` and copying the scaffold over to a dedicated directory:

```bash
mkdir /tmp/agents

git clone --depth 1 https://github.com/fullsend-ai/fullsend.git /tmp/fullsend-ai_fullsend/
cp -r /tmp/fullsend-ai_fullsend/internal/scaffold/fullsend-repo/. /tmp/agents/
```

Then apply your organization customizations, if any:

```bash
git clone --depth 1 https://github.com/{org}/.fullsend.git /tmp/org-fullsend/
cp -r /tmp/org-fullsend/customized/. /tmp/agents/
```

And finally apply your own target repository customizations, if any:

```bash
git clone https://github.com/{org}/{target-repo} /tmp/target-repo
cp -r /tmp/target-repo/.fullsend/customized/. /tmp/agents/
```

When you execute `fullsend run`, pass `--fullsend-dir` as `/tmp/agents/`.

## Platform notes

### macOS

- **Podman machine**: ensure the Podman machine is running (`podman machine start`) before invoking fullsend. The CLI does not start it automatically.
- **Podman host-gateway**: if sandbox creation fails with `unable to replace "host-gateway"`, set `host_containers_internal_ip = "192.168.127.254"` under `[containers]` in `~/.config/containers/containers.conf` and restart the Podman machine.
- **Architecture mismatch**: if your sandbox image uses a different CPU architecture than the host (e.g. amd64 image on an arm64 Mac via QEMU emulation), set `FULLSEND_SANDBOX_ARCH=amd64` so the CLI downloads the correct binary. This is not needed in the typical setup where the Podman VM matches the host arch.

### Linux

- **Rootless Podman**: Podman runs rootless by default. Ensure your user has subuids/subgids configured (`grep $USER /etc/subuid`). If not, run `sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $USER && podman system migrate`.
- **SELinux**: on Fedora/RHEL, bind-mounted volumes may need the `:z` suffix for standalone `podman run`. OpenShell handles this automatically.

## Troubleshooting

**Sandbox creation fails immediately**
- Check that `podman machine start` has been run (macOS only)
- Verify OpenShell is installed: `openshell --version`
- Verify the gateway is running: `openshell gateway list`

**`Gateway not running` or `no openshell gateway running`**
- Start the gateway as described in step 5
- Verify it's healthy: `curl -sf http://127.0.0.1:8081/healthz`
- Check that it's registered: `openshell gateway list`

**`Syntax error: "(" unexpected` inside sandbox**
- The macOS Mach-O binary was injected instead of a Linux ELF. Update to fullsend 0.4.0+ which auto-resolves the correct binary, or provide one explicitly with `--fullsend-binary`

**Agent fails with missing environment variable**
- Check your env file contains all variables listed in the agent's harness YAML (`harness/{agent}.yaml` in the `.fullsend` config directory)

**arm64 sandbox image pull fails**
- The default `:latest` tag is amd64-only. Add `FULLSEND_SANDBOX_IMAGE=ghcr.io/fullsend-ai/fullsend-sandbox:dev` to your env file

**`L7 policy validation failed: unknown protocol 'tcp'`**
- OpenShell 0.0.38 uses `protocol: rest` (not `tcp`) and `access: read-write`/`read-only` (not `allow`). Update your policy YAML files to use the new schema. See the built-in policies in `policies/` for examples.

**`unable to replace "host-gateway"` on macOS**
- Set `host_containers_internal_ip = "192.168.127.254"` under `[containers]` in `~/.config/containers/containers.conf` and restart the Podman machine
