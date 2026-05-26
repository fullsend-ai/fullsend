# Running agents locally

This guide walks through running fullsend agents on your machine using released binaries. No Go toolchain or source checkout is needed. Both macOS and Linux are supported with Podman as the container runtime.

> For building fullsend from source or contributing to the CLI, see [Local development](../dev/local-dev.md).

## Prerequisites

| Requirement | macOS | Linux |
|-------------|-------|-------|
| Container runtime | Podman Desktop with a running machine | Podman |
| [OpenShell](https://github.com/NVIDIA/OpenShell) | 0.0.38 | 0.0.38 |
| GCP project | [Agent Platform API](https://console.cloud.google.com/apis/library/aiplatform.googleapis.com) enabled with [Claude models](https://console.cloud.google.com/vertex-ai/model-garden) enabled | Same |
| GCP credentials | Service account key (see [GCP credentials](#gcp-credentials) below) | Same |
| GitHub PAT | Classic PAT with `repo` scope (see [GitHub tokens](#github-tokens) below) | Same |

> **No Go toolchain required.** On macOS, the CLI automatically downloads a Linux binary for the sandbox. See [Cross-platform binary resolution](#cross-platform-binary-resolution) below.

## 1. Download the fullsend CLI

Download the latest release from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases). Pick the archive matching your platform:

| Platform | Archive |
|----------|---------|
| macOS (Apple Silicon) | `fullsend_{version}_darwin_arm64.tar.gz` |
| macOS (Intel) | `fullsend_{version}_darwin_amd64.tar.gz` |
| Linux (x86_64) | `fullsend_{version}_linux_amd64.tar.gz` |
| Linux (arm64) | `fullsend_{version}_linux_arm64.tar.gz` |

Extract and move to a directory in your PATH:

```bash
tar xzf fullsend_0.4.0_darwin_arm64.tar.gz
mv fullsend_0.4.0_darwin_arm64/fullsend $HOME/.local/bin/
```

Verify the installation:

```bash
fullsend --version
```

## 2. Install OpenShell

[OpenShell](https://github.com/NVIDIA/OpenShell) provides the sandbox runtime. Install the CLI and download the gateway binary:

```bash
# Install the CLI (requires uv — https://docs.astral.sh/uv/)
uv tool install openshell==0.0.38

# Verify
openshell --version
```

Download the gateway binary from the [OpenShell releases](https://github.com/NVIDIA/OpenShell/releases/tag/v0.0.38) page. Pick the archive matching your platform and extract it:

```bash
# Example for macOS (Apple Silicon)
curl -fsSL https://github.com/NVIDIA/OpenShell/releases/download/v0.0.38/openshell-gateway-aarch64-apple-darwin.tar.gz \
  -o /tmp/openshell-gateway.tar.gz
tar xzf /tmp/openshell-gateway.tar.gz -C $HOME/.local/bin/
```

## 3. Clone the .fullsend config directory

Each organization has a `.fullsend` config repo containing harness definitions, agent prompts, policies, scripts, and skills. Clone it to a local path:

```bash
gh repo clone <org>/.fullsend /tmp/fullsend-dot
```

## 4. Create an env file

Env files contain secrets and must never be committed. Keep your env file outside the repo (e.g. `/tmp/fullsend.env`).

### GCP credentials

Fullsend agents use Claude models via [Agent Platform (Vertex AI)](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/partner-models/claude/use-claude). Your GCP project must have:

1. The [Agent Platform API](https://console.cloud.google.com/apis/library/aiplatform.googleapis.com) enabled
2. Claude models enabled in [Model Garden](https://console.cloud.google.com/vertex-ai/model-garden) — search for "Claude" and enable the Anthropic models you need (all agents default to Claude Opus)

> **Cost:** Claude model calls on Agent Platform (Vertex AI) incur per-token charges billed to the GCP project. If you need a new project, request one through your IT team. If using an existing project, contact your GCP project admin to have a service account and key created for you — you may not have the IAM permissions to do this yourself.

In CI, fullsend uses [Workload Identity Federation (WIF)](https://cloud.google.com/iam/docs/workload-identity-federation) — no service account keys are stored. Locally, use a service account key instead.

**Create a service account and key** (requires `roles/iam.serviceAccountAdmin` on the project):

```bash
# Create a service account
gcloud iam service-accounts create fullsend-local \
  --display-name="Fullsend local agent runner" \
  --project=<project-id>

# Grant Vertex AI User role (call Claude models via Agent Platform)
gcloud projects add-iam-policy-binding <project-id> \
  --member="serviceAccount:fullsend-local@<project-id>.iam.gserviceaccount.com" \
  --role="roles/aiplatform.user"

# Create and download the key file
gcloud iam service-accounts keys create sa-key.json \
  --iam-account=fullsend-local@<project-id>.iam.gserviceaccount.com
chmod 600 sa-key.json
```

See [Creating service account keys](https://cloud.google.com/iam/docs/keys-create-delete) for details. Note that Google recommends WIF over long-lived keys for production use — service account keys are appropriate for local development.

**Add to your env file:**

```bash
ANTHROPIC_VERTEX_PROJECT_ID=<project-id>
GOOGLE_CLOUD_PROJECT=<project-id>
CLOUD_ML_REGION=global
GOOGLE_APPLICATION_CREDENTIALS=/path/to/sa-key.json
```

> WIF's OIDC token refresh is handled automatically in CI. With a service account key locally, no refresh is needed — the key is valid for the duration of the run.

### GitHub tokens

In CI, fullsend mints separate GitHub App installation tokens per role (read-only for sandboxes, write-scoped for post-scripts). Locally, use a [classic personal access token](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens#creating-a-personal-access-token-classic) — a single PAT can serve all three token variables:

```bash
GH_TOKEN=<github-pat>
PUSH_TOKEN=<github-pat>
REVIEW_TOKEN=<github-pat>
```

All three can be the same PAT. The separation exists for CI's security architecture (read-only tokens inside sandboxes, write tokens only on the runner for post-scripts), but locally a single token works.

**Required scope:** `repo` — grants read/write access to code, pull requests, and issues in repositories the token owner can access. Create one at [github.com/settings/tokens](https://github.com/settings/tokens/new).

[Fine-grained PATs](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens#creating-a-fine-grained-personal-access-token) are not supported. They are scoped to individual repositories and cannot cover both the target repo and the `.fullsend` config repo in a single token. Fullsend needs access to both during a run.

### Per-agent variables

Each agent requires additional variables via its harness `runner_env` and sandbox env files. Add the ones needed for the agent you want to run:

| Variable | Agent(s) | Description |
|----------|----------|-------------|
| `GITHUB_ISSUE_URL` | triage, code, prioritize | Full URL of the GitHub issue |
| `REPO_FULL_NAME` | code, review, fix, retro | `owner/repo` of the target repository |
| `ISSUE_NUMBER` | code | Issue number the code agent should implement |
| `TARGET_BRANCH` | code, fix | Branch to base work on (e.g. `main`) |
| `GITHUB_WORKSPACE` | code, fix | Parent directory of the target repo checkout |
| `PR_NUMBER` | review, fix | Pull request number to review or fix |
| `GITHUB_PR_URL` | review | Full URL of the pull request to review |
| `REVIEW_BODY_FILE` | fix | Path to a file containing the review body to fix |
| `PUSH_TOKEN_SOURCE` | fix | Token source type (use `pat` for local runs) |
| `FIX_ITERATION` | fix | Current fix iteration (use `1` for local runs) |
| `TRIGGER_SOURCE` | fix | What triggered the fix (use `manual` for local runs) |
| `HUMAN_INSTRUCTION` | fix | Free-text instruction (can be empty) |
| `PRE_AGENT_HEAD` | fix | Git HEAD before agent ran (can be empty locally) |
| `ORG` | prioritize | GitHub organization name |
| `PROJECT_NUMBER` | prioritize | GitHub Projects (v2) project number |
| `ORIGINATING_URL` | retro | URL of the PR or issue to run a retrospective on |

See the harness definitions in `harness/*.yaml` within your `.fullsend` config directory for the complete list per agent.

### arm64 image override

On arm64 hosts (Apple Silicon, Graviton), add:

```bash
FULLSEND_SANDBOX_IMAGE=ghcr.io/fullsend-ai/fullsend-sandbox:dev
```

The default `:latest` tag is amd64-only. The `:dev` tag is a multi-arch image that includes arm64 support. On amd64 hosts this line is not needed.

## 5. Start the OpenShell gateway

The fullsend CLI requires a running OpenShell gateway — it will not start one automatically. Start the gateway with the Podman driver:

```bash
export OPENSHELL_SSH_HANDSHAKE_SECRET="local-$(openssl rand -hex 16)"

# v0.0.38 requires an explicit supervisor image (version-tagged images start at 0.0.41)
export OPENSHELL_SUPERVISOR_IMAGE="ghcr.io/nvidia/openshell/supervisor:dfd47683e7da4f1a4a8fa5d77f92d3696e6a41f9"

openshell-gateway \
  --bind-address 127.0.0.1 \
  --health-port 8081 \
  --drivers podman \
  --disable-tls \
  --db-url "sqlite:/tmp/gateway.db?mode=rwc" &
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

> On macOS, make sure the Podman machine is running (`podman machine start`) before starting the gateway.

## 6. Run an agent

```bash
fullsend run <agent> \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env
```

The `--env-file` flag loads environment variables before the harness is parsed. It is repeatable — later files override earlier ones.

The examples below show the per-agent variables to add to your env file (in addition to the [GCP credentials](#gcp-credentials) and [GitHub tokens](#github-tokens) already configured above).

### Triage an issue

Add to your env file:

```bash
GITHUB_ISSUE_URL=https://github.com/owner/repo/issues/42
```

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env
```

### Review a pull request

Add to your env file:

```bash
REPO_FULL_NAME=owner/repo
PR_NUMBER=123
GITHUB_PR_URL=https://github.com/owner/repo/pull/123
```

```bash
fullsend run review \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env
```

### Implement an issue

Add to your env file:

```bash
REPO_FULL_NAME=owner/repo
ISSUE_NUMBER=42
GITHUB_ISSUE_URL=https://github.com/owner/repo/issues/42
TARGET_BRANCH=main
GITHUB_WORKSPACE=/path/to/parent-of-target-repo
```

`GITHUB_WORKSPACE` is required by the code and fix harnesses — it is the directory containing the `target-repo/` checkout (same as `$GITHUB_WORKSPACE` in GitHub Actions runners).

```bash
fullsend run code \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env
```

### Fix review feedback on a PR

Add to your env file:

```bash
REPO_FULL_NAME=owner/repo
PR_NUMBER=123
TARGET_BRANCH=main
REVIEW_BODY_FILE=/path/to/review-body.txt
GITHUB_WORKSPACE=/path/to/parent-of-target-repo
PUSH_TOKEN_SOURCE=pat
FIX_ITERATION=1
TRIGGER_SOURCE=manual
HUMAN_INSTRUCTION=
PRE_AGENT_HEAD=
```

`HUMAN_INSTRUCTION` and `PRE_AGENT_HEAD` can be empty for local testing.

```bash
fullsend run fix \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env
```

### Prioritize an issue

Add to your env file:

```bash
GITHUB_ISSUE_URL=https://github.com/owner/repo/issues/42
ORG=owner
PROJECT_NUMBER=1
```

```bash
fullsend run prioritize \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env
```

### Run a retrospective

Add to your env file:

```bash
REPO_FULL_NAME=owner/repo
ORIGINATING_URL=https://github.com/owner/repo/pull/123
```

```bash
fullsend run retro \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env
```

## Running customized agents

In CI, the reusable workflows automatically overlay customized files from `customized/` (org-level) and `.fullsend/customized/` (per-repo) onto the base config before running the agent. The `fullsend run` CLI does not perform this overlay — it reads from `--fullsend-dir` as-is.

To run customized agents locally, merge the overlays into a working copy of the config directory before invoking `fullsend run`:

```bash
# Start with a fresh copy of the org config
cp -r /tmp/fullsend-dot /tmp/fullsend-merged

# Apply org-level customizations (from the .fullsend config repo)
for dir in agents skills schemas harness plugins policies scripts env; do
  if [ -d "/tmp/fullsend-merged/customized/${dir}" ]; then
    cp -r "/tmp/fullsend-merged/customized/${dir}/." "/tmp/fullsend-merged/${dir}/"
  fi
done

# Apply per-repo customizations (from the target repo)
TARGET_REPO=/path/to/repo
for dir in agents skills schemas harness plugins policies scripts env; do
  if [ -d "${TARGET_REPO}/.fullsend/customized/${dir}" ]; then
    cp -r "${TARGET_REPO}/.fullsend/customized/${dir}/." "/tmp/fullsend-merged/${dir}/"
  fi
done

# Run with the merged config
fullsend run <agent> \
  --fullsend-dir /tmp/fullsend-merged \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env
```

This replicates the three-tier resolution order: upstream defaults < org customizations < per-repo customizations. See [Customizing agents](customizing-agents.md) for details on the layered content model.

> **Custom agent names** require a matching harness YAML. If you add a custom agent definition (e.g. `customized/agents/my-agent.md`), you also need a `customized/harness/my-agent.yaml` that references it — otherwise `fullsend run my-agent` will fail with "harness not found".

## Testing without side effects

To run agent inference without the post-script (which posts PR comments, pushes branches, or creates PRs), use `--no-post-script`:

```bash
fullsend run review \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env \
  --no-post-script
```

The agent runs normally inside the sandbox, but the post-script is skipped. This means `PUSH_TOKEN` and `REVIEW_TOKEN` are not needed in your env file — only the core variables (`GH_TOKEN`, GCP credentials) are required.

## Cross-platform binary resolution

The pre-agent security scan runs `fullsend scan context` inside the Linux sandbox. This requires a Linux binary, even when your host is macOS.

On **Linux**, the CLI copies its own executable into the sandbox — no extra steps needed.

On **macOS**, the CLI automatically obtains a Linux binary using the following priority:

| Priority | Strategy | When it applies |
|----------|----------|-----------------|
| 1 | `--fullsend-binary <path>` | Always used if provided — skips auto-resolution |
| 2 | Download from GitHub Release | CLI version matches a release (e.g. `0.4.0`) |
| 3 | Cross-compile from source | Go toolchain available, run from module root |
| 4 | Download latest release | Fallback when cross-compilation unavailable |

When using a released binary (this guide's workflow), **strategy 2 applies automatically** — the CLI downloads the matching Linux release. No Go toolchain or manual steps needed.

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
- Check your env file contains all variables listed in the agent's harness YAML (`harness/<agent>.yaml` in the `.fullsend` config directory)

**arm64 sandbox image pull fails**
- The default `:latest` tag is amd64-only. Add `FULLSEND_SANDBOX_IMAGE=ghcr.io/fullsend-ai/fullsend-sandbox:dev` to your env file

**`L7 policy validation failed: unknown protocol 'tcp'`**
- OpenShell 0.0.38 uses `protocol: rest` (not `tcp`) and `access: read-write`/`read-only` (not `allow`). Update your policy YAML files to use the new schema. See the built-in policies in `policies/` for examples.

**`unable to replace "host-gateway"` on macOS**
- Set `host_containers_internal_ip = "192.168.127.254"` under `[containers]` in `~/.config/containers/containers.conf` and restart the Podman machine
