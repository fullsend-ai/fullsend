# Running agents locally

This guide walks through running agents with fullsend on your machine. It
sets the base to help you run any agent, default or custom. Both macOS and
Linux are supported with Podman as the container runtime.

> For building fullsend from source, see the "Building from source" section in CONTRIBUTING.md.

## Prerequisites

| Requirement | macOS | Linux |
|-------------|-------|-------|
| Container runtime | Podman Desktop with a running machine | Podman |
| [OpenShell](https://github.com/NVIDIA/OpenShell) | [Pinned per release](https://github.com/fullsend-ai/fullsend/blob/main/.github/scripts/openshell-version.sh) | Same |
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
to install it, here we use one similar to how we download it on Fullsend. Use the version
fullsend is pinned to — the source of truth is
[`.github/scripts/openshell-version.sh`](https://github.com/fullsend-ai/fullsend/blob/main/.github/scripts/openshell-version.sh)
in the fullsend repo at your release tag (also printed on Fullsend workflow runs).

```bash
export OPENSHELL_VERSION=0.0.83  # check the pin file for the current version
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
  --project={project-id} \
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

**Tip**: if you plan to run the CLI from the
[container image](#run-from-a-container)
instead of the native binary, keep the key file and env file in your
working directory — the container mounts it as `/work` and resolves
`GOOGLE_APPLICATION_CREDENTIALS` relative to it.

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

Next clone the repository where the agent definitions live. The canonical
source is `fullsend-ai/agents`. To learn more about custom agents visit
[Configuring Agents](customizing-agents.md).

```bash
git clone --depth 1 https://github.com/fullsend-ai/agents.git /tmp/fullsend-agents/
```

## Run default agents

Depending on the agent you want to run you need a different set of environment variables.
Check the variables they need in their environment files, referenced in their harness files.

**Tip**: use `--no-post-script` in the `fullsend run` calls to avoid side-effects. You
can also use `--keep-sandbox` to debug failures (but remember to remove them).

**Tip**: `fullsend run` uses multiple tools on your system. Instead of
installing them all, you can use a container image fullsend publishes —
see [Run from a container](#run-from-a-container) below.

**Note**: to run custom agents set `--agent-dir` to the directory where your
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
  --agent-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env
```

### Review agent

Add to an env file:

```bash
# fullsend-review.env
# In CI, REVIEW_TOKEN is auto-minted by the binary when --mint-url is provided.
# For local runs, supply a GitHub PAT manually:
REVIEW_TOKEN={github-pat}
GITHUB_PR_URL="https://github.com/{org}/{repo}/pull/{pr_number}"
PR_NUMBER="{pr_number}"
REPO_FULL_NAME="{org}/{repo}"
```

```bash
fullsend run review \
  --agent-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-review.env
```

### Code agent

Add to an env file:

```bash
# fullsend-code.env
# In CI, GH_TOKEN and PUSH_TOKEN are auto-minted by the binary when --mint-url is provided.
# For local runs, supply GitHub PATs manually:
GH_TOKEN={github-pat}
PUSH_TOKEN={github-pat}
PUSH_TOKEN_SOURCE=pat
GITHUB_ISSUE_URL=https://github.com/{org}/{repo}/issues/{issue_num}
REPO_FULL_NAME={org}/{repo}
ISSUE_NUMBER={issue_num}
CODE_ALLOWED_TARGET_BRANCHES=main
REPO_DIR=/tmp/repo-dir
GITHUB_WORKSPACE=/tmp/
```

```bash
fullsend run code \
  --agent-dir /tmp/fullsend-agents/ \
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
fullsend lock code --agent-dir /path/to/.fullsend
```

To lock all harnesses in the directory at once:

```bash
fullsend lock --all --agent-dir /path/to/.fullsend
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
  --agent-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env \
  --status-repo myorg/myrepo \
  --status-number 42 \
  --run-url "https://github.com/myorg/myrepo/actions/runs/12345"
```

Status comment behavior is configured via `status_notifications` in
`config.yaml`. See the [operations guide](../getting-started/operations.md#status-notifications).

## Run from a container

Instead of downloading the fullsend binary and installing its host-side
dependencies, you can run the CLI from the released runner image:

```bash
podman pull ghcr.io/fullsend-ai/fullsend-runner:latest
```

You still need on the host: Podman, OpenShell (the gateway and sandboxes
stay on the host; only the CLI moves into the container), GCP credentials,
and a GitHub token.

Mount your OpenShell client config and the same paths you would pass to a
native `fullsend run`. `--network=host` lets the containerized CLI reach
the gateway:

```bash
podman run --rm -it --network=host \
  -v "$HOME/.config/openshell:/root/.config/openshell" \
  -v /tmp/fullsend:/tmp/fullsend \
  -v /tmp/fullsend-agents:/tmp/fullsend-agents \
  -v /tmp/target-repo:/tmp/target-repo \
  -v "$PWD:/work" \
  ghcr.io/fullsend-ai/fullsend-runner:latest \
  run triage \
    --agent-dir /tmp/fullsend-agents/ \
    --target-repo /tmp/target-repo/ \
    --env-file fullsend-gcp.env \
    --env-file fullsend-triage.env
```

The image's working directory is `/work`, so relative paths in `--env-file`
resolve against the mounted current directory. Run artifacts are written to
`/tmp/fullsend/` — mount it (or pass `--output-dir` pointing at a mounted
path) to keep them on the host.

**macOS**: use `/private/tmp/...` for the `/tmp` mounts above (and
`$(pwd -P)` instead of `$PWD`) — see "Container image mounts" in the
[macOS platform notes](#macos). No other change is needed: fullsend
detects when it's running inside a container whose loopback doesn't
reach the gateway — the case on a macOS Podman machine, where
`--network=host` shares the VM's network namespace, not the Mac's — and
transparently points the OpenShell client at `host.containers.internal`
instead. See "Container image" in the [macOS platform notes](#macos) if
you need a manual override.

When using `--keep-sandbox` the CLI within the container is not able to
gather podman logs, because the `podman` binary is not installed within.
Run `podman logs <sandbox-container>` manually on your machine.

On SELinux-enforcing hosts (Fedora/RHEL), bind mounts may need the `:z`
suffix. Prefer adding `:z` only to the `/tmp` and `$PWD` mounts —
relabeling `~/.config/openshell` touches files the host gateway also reads.

## Simulating Fullsend's real customization layers

Fullsend automatically aggregates different layers of information before running `fullsend run`.
In case you want to test how customizations impact default agents, or you custom agents, follow the
next steps.

Start by cloning `fullsend-ai/agents` to a dedicated directory:

```bash
git clone --depth 1 https://github.com/fullsend-ai/agents.git /tmp/agents/
```

Then apply your organization customizations, if any:

> **Note:** The `customized/` overlay mechanism is deprecated per
> [ADR-0064](../../ADRs/0064-deprecate-customized-directory-overlay.md).
> Orgs that have migrated to config-driven agents should skip these
> `cp -r customized/` steps and use the registered harness paths directly.

```bash
git clone --depth 1 https://github.com/{org}/.fullsend.git /tmp/org-fullsend/
cp -r /tmp/org-fullsend/customized/. /tmp/agents/
```

And finally apply your own target repository customizations, if any:

```bash
git clone https://github.com/{org}/{target-repo} /tmp/target-repo
cp -r /tmp/target-repo/.fullsend/customized/. /tmp/agents/
```

When you execute `fullsend run`, pass `--agent-dir` as `/tmp/agents/`.

## Platform notes

### macOS

- **Podman machine**: ensure the Podman machine is running (`podman machine start`) before invoking fullsend. The CLI does not start it automatically.
- **Podman host-gateway**: if sandbox creation fails with `unable to replace "host-gateway"`, set `host_containers_internal_ip = "192.168.127.254"` under `[containers]` in `~/.config/containers/containers.conf` and restart the Podman machine.
- **Architecture mismatch**: if your sandbox image uses a different CPU architecture than the host (e.g. amd64 image on an arm64 Mac via QEMU emulation), set `FULLSEND_SANDBOX_ARCH=amd64` so the CLI downloads the correct binary. This is not needed in the typical setup where the Podman VM matches the host arch.
- **Container image**: `--network=host` shares the Podman VM's network namespace, not the Mac's, so a gateway configured at `127.0.0.1` is unreachable from inside the container. Fullsend detects this automatically and redirects the containerized CLI to whichever of `host.containers.internal` (Podman) or `host.docker.internal` (Docker) is actually reachable (fullsend-ai/fullsend#5261) — no manual steps needed. This depends on one of those names resolving inside the container; if neither does, see the **Podman host-gateway** note above. To override the detection yourself, set `OPENSHELL_GATEWAY_ENDPOINT` (e.g. `https://host.containers.internal:17670`) before running the container — an explicit value here is never overwritten. Always use `https://`: check `openshell gateway list`'s `AUTH` column, and if it says `mtls`, OpenShell will present your client certificate to whatever host this points at, so only point it at a gateway you trust.
- **Container image mounts**: bind-mounting `/tmp/...` paths fails with `statfs: no such file or directory` on macOS — Podman Desktop's VM shares `/Users`, `/private`, and `/var/folders` via virtiofs, but not the literal `/tmp` path, and Podman does not resolve the `/tmp` → `/private/tmp` symlink before mounting. Use `/private/tmp/...` (and `$(pwd -P)` instead of `$PWD`). The [container example](#run-from-a-container) above already accounts for this.

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
