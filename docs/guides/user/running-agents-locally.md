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
  --fullsend-dir /tmp/fullsend-agents/ \
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
  --fullsend-dir /tmp/fullsend-agents/ \
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
  --fullsend-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-code.env
```

### Choosing the runtime

<a id="run-a-minimal-agent-on-the-pi-runtime"></a><a id="troubleshooting-pi-runtime"></a><a id="platform-notes-pi"></a>

Every example above runs on **Claude Code**, the default runtime. Fullsend
also has an opt-in **pi** runtime, and any example on this page runs on it
by adding one flag to the same command:

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env \
  --runtime pi
```

Everything else about runtimes lives in one place: [Agent
runtimes](../../runtimes.md) for selecting and overriding the runtime,
model and effort — per run, or per agent in `config.yaml` — and
[Pi › Running it locally](../../runtimes/pi.md#running-it-locally) for
what a local pi run needs, its models and its troubleshooting.

### Remote resource flags

When your harness references URL-based skills with transitive dependencies,
you can tune resolution limits:

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
| `--forge` | Forge platform (`github` or `gitlab`); auto-detected from CI env vars when omitted |

Example:

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env \
  --status-repo myorg/myrepo \
  --status-number 42 \
  --run-url "https://github.com/myorg/myrepo/actions/runs/12345"
```

For GitLab repositories, use `--forge gitlab` instead of `--mint-url`. The agent reads `GITLAB_TOKEN` from the environment and does not require the mint service. See the [operations guide](../getting-started/operations.md#gitlab-ci) for required environment variables.

Status comment behavior is configured via `status_notifications` in
`config.yaml`. See [Status Notifications](customizing-agents.md#status-notifications).

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
    --fullsend-dir /tmp/fullsend-agents/ \
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

If your organization uses config-driven agents registered in `config.yaml`,
pass your `.fullsend` config repo as `--fullsend-dir`:

```bash
git clone --depth 1 https://github.com/{org}/.fullsend.git /tmp/org-fullsend/
```

When you execute `fullsend run`, pass `--fullsend-dir` as `/tmp/org-fullsend/`.
See [Bring Your Own Agent](bring-your-own-agent.md) for the config-driven
approach.

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

## Debugging network policies locally

When customizing network policies, running agents locally lets you inspect
sandbox artifacts directly instead of pushing changes and waiting for CI. This
section describes what a local `fullsend run` produces and how to use the
output to iterate on network policy allowlists.

### Run directory structure

Every `fullsend run` creates a run directory. By default this is under
`/tmp/fullsend/`; override it with `--output-dir`:

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env \
  --output-dir /tmp/my-debug-output
```

The run directory is printed at the end of every run under **Run directory**.
Its layout:

```
<run-dir>/
  logs/
    openshell-sandbox.log    # OCSF events: network, process, policy decisions
    openshell-gateway.log    # Gateway-side events
  iteration-1/
    output/                  # Agent output files (agent-result.json, etc.)
    transcripts/             # Agent conversation transcripts (.jsonl)
  metrics.json               # Behavioral metrics (tokens, cost, duration)
  security/
    findings.jsonl           # Security scan findings
```

### Key artifacts for network policy debugging

**`openshell-sandbox.log`** is the primary artifact. It contains
[OCSF](https://schema.ocsf.io/) events for every network connection, HTTP
request, process launch, and policy evaluation that occurred inside the
sandbox. Each event records whether the connection was allowed or denied
and which policy rule applied.

Look for `DENIED` entries to find connections your policy rejected:

```bash
grep -i DENIED <run-dir>/logs/openshell-sandbox.log
```

Each denied entry includes the destination host, port, and the binary that
attempted the connection — enough to decide whether to add the endpoint to
your policy.

**`openshell-gateway.log`** contains gateway-side events. Useful when a
connection fails before reaching the sandbox (e.g., provider misconfiguration
or gateway routing issues).

### Debugging workflow

1. **Start with a restrictive policy.** Create or modify a policy YAML that
   blocks the endpoint you want to test. See
   [Building custom agents — Sandbox policy](building-custom-agents.md#step-3-define-the-sandbox-policy)
   for the policy format.

2. **Run the agent locally with `--keep-sandbox`:**

   ```bash
   fullsend run <agent> \
     --fullsend-dir /tmp/fullsend-agents/ \
     --target-repo /tmp/target-repo/ \
     --env-file fullsend-gcp.env \
     --env-file fullsend-<agent>.env \
     --keep-sandbox \
     --no-post-script
   ```

   `--keep-sandbox` preserves the sandbox container after the run finishes,
   letting you exec into it for further inspection. `--no-post-script`
   prevents side effects (issue comments, PR creation) while debugging.

3. **Inspect the sandbox log for denied connections:**

   ```bash
   grep -i DENIED <run-dir>/logs/openshell-sandbox.log
   ```

   If the [analyze-transcript](https://github.com/fullsend-ai/fullsend/tree/main/skills/analyze-transcript)
   skill is available, you can use the network analysis commands for a
   structured view:

   ```bash
   python3 skills/analyze-transcript/analyze-transcript.py network \
     <run-dir>/logs/openshell-sandbox.log

   python3 skills/analyze-transcript/analyze-transcript.py netsearch "DENIED" \
     <run-dir>/logs/openshell-sandbox.log
   ```

4. **Update your policy** to allow the blocked endpoints, then re-run. Each
   cycle takes only the time to start a sandbox and run the agent — no push
   or CI wait.

5. **When done, clean up the kept sandbox:**

   ```bash
   openshell sandbox delete <sandbox-name>
   ```

   The sandbox name is printed during the run and also shown in the
   `--keep-sandbox` warning message at the end.

### Entering a kept sandbox

When you pass `--keep-sandbox`, the CLI prints a command to exec into the
sandbox:

```bash
openshell sandbox exec --tty --name <sandbox-name> -- bash
```

Inside the sandbox you can inspect the workspace, re-run commands the agent
tried, or verify that network access works as expected for a specific host:

```bash
curl -sf https://api.example.com/healthz
```

### Tips

- **Network summary:** `openshell-sandbox.log` can be large. Use
  `grep -c DENIED` to get a count before reading individual entries.
- **Iterate fast:** after updating your policy YAML, you do not need to
  reinstall or reconfigure OpenShell — just re-run `fullsend run` with the
  updated policy file.
- **Compare runs:** diff two sandbox logs to confirm that a policy change
  resolved the denials:
  ```bash
  diff <(grep DENIED run-1/logs/openshell-sandbox.log) \
       <(grep DENIED run-2/logs/openshell-sandbox.log)
  ```

## See also

- [Agent runtimes](../../runtimes.md) — choosing a runtime and overriding runtime, model and effort per run or per agent
- [Pi › Running it locally](../../runtimes/pi.md#running-it-locally) — what a local pi run needs, its models and troubleshooting
- [fullsend run](../../cli/run.md) — the full flag reference
- [Configuring agent behavior](customizing-agents.md) — harness configuration and `base:` composition
