# Running agents locally

This guide walks through running agents with fullsend on your machine. It
sets the base to help you run any agent, default or custom. Both macOS and
Linux are supported with Podman as the container runtime.

> For building fullsend from source, see the "Building from source" section in CONTRIBUTING.md.

## Prerequisites

| Requirement | macOS | Linux |
|-------------|-------|-------|
| Container runtime | Podman Desktop with a running machine | Podman |
| [OpenShell](https://github.com/NVIDIA/OpenShell) | 0.0.63 | 0.0.63 |
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
printed on your Fullsend workflow for better reproducibility.

```bash
export OPENSHELL_VERSION=0.0.63
curl -LsSf https://raw.githubusercontent.com/NVIDIA/OpenShell/v${OPENSHELL_VERSION}/install.sh | OPENSHELL_VERSION=v${OPENSHELL_VERSION} sh
openshell --version
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

## Run default agents

Depending on the agent you want to run you need a different set of environment variables.
Check the variables they need in their environment files, referenced in their harness files.

**Tip**: use `--no-post-script` in the `fullsend run` calls to avoid side-effects. You
can also use `--keep-sandbox` to debug failures (but remember to remove them).

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
CODE_ALLOWED_TARGET_BRANCHES=main
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

### Remote resource flags

When your harness references URL-based skills with transitive dependencies
(see [ADR-0038](../../ADRs/0038-universal-harness-access.md)), you can tune
resolution limits:

| Flag | Default | Description |
|------|---------|-------------|
| `--forge` | (auto-detect) | Forge platform to use (`github`, `gitlab`). Auto-detected from CI env vars (`GITHUB_ACTIONS`, `GITLAB_CI`) when omitted |
| `--max-depth` | 10 | Maximum dependency depth for transitive resolution (0 disables) |
| `--max-resources` | 50 | Maximum total remote resources fetched per harness |
| `--offline` | false | Reject network fetches; only use cached remote resources |

#### Lock files

If a `lock.yaml` file exists in the fullsend directory, `fullsend run` uses it
to skip re-resolution when the harness has not changed since the lock was
generated. Generate or update a lock file with:

```bash
fullsend lock code --fullsend-dir /path/to/.fullsend
```

To lock all harnesses in the directory at once:

```bash
fullsend lock --all --fullsend-dir /path/to/.fullsend
```

When `--forge` is specified, only that platform variant is locked. When omitted,
all forge variants defined in the harness are resolved and the union of their
dependencies is locked.

When the lock entry is current (harness SHA256 matches), dependencies are
resolved from the local cache without network access. If the harness has changed
or a cached artifact is missing, `fullsend run` falls back to normal network
resolution and prints a warning suggesting you re-run `fullsend lock`.

Use `--update` to force re-resolution even if the lock entry appears current.

### Status notification flags

When running agents locally you can optionally enable status comments on the
target issue/PR. These flags mirror what the CI workflows pass automatically:

| Flag | Description |
|------|-------------|
| `--run-url` | URL of the CI/CD run shown in the status comment |
| `--status-repo` | Repository (`owner/repo`) to post status comments on |
| `--status-number` | Issue or PR number for status comments |
| `--mint-url` | Mint service URL for on-demand status comment tokens (default: `$FULLSEND_MINT_URL`) |

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
`config.yaml`. See the [installation guide](../../reference/installation.md#status-notifications).

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
- **Gateway connectivity**: The sandbox does not move to Ready state and its logs say that it can't connect
to the server (gateway). It is likely that you need to bind the gateway to `0.0.0.0`. Add
`OPENSHELL_BIND_ADDRESS` on `$HOME/.config/openshell/gateway.env` and restart the
`openshell-gateway` service.
- **SELinux**: on Fedora/RHEL, bind-mounted volumes may need the `:z` suffix for standalone `podman run`. OpenShell handles this automatically.

## Troubleshooting

**Sandbox creation fails immediately**
- Check that `podman machine start` has been run (macOS only)
- Verify OpenShell is installed: `openshell --version`
- Verify the gateway is running: `openshell gateway list`

**`Gateway not running` or `no openshell gateway running`**
- Check the `openshell-gateway` service.
- Verify it's healthy: `curl -sf https://127.0.0.1:8081/healthz`
- Check that it's registered: `openshell gateway list`

**`Syntax error: "(" unexpected` inside sandbox**
- The macOS Mach-O binary was injected instead of a Linux ELF. Update to fullsend 0.4.0+ which auto-resolves the correct binary, or provide one explicitly with `--fullsend-binary`

**Agent fails with missing environment variable**
- Check your env file contains all variables listed in the agent's harness YAML (`harness/{agent}.yaml` in the `.fullsend` config directory)

**arm64 sandbox image pull fails**
- The default `:latest` tag is amd64-only. Add `FULLSEND_SANDBOX_IMAGE=ghcr.io/fullsend-ai/fullsend-sandbox:dev` to your env file


**`unable to replace "host-gateway"` on macOS**
- Set `host_containers_internal_ip = "192.168.127.254"` under `[containers]` in `~/.config/containers/containers.conf` and restart the Podman machine
