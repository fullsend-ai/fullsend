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

## Run a minimal agent on the pi runtime

The pi runtime (`runtime: pi`) lets you run agents using
[pi](https://github.com/earendil-works/pi) instead of Claude Code inside the
sandbox. You can run a minimal pi agent locally without cloning the
`fullsend-ai/agents` fleet repo — `fullsend run --fullsend-dir` resolves a
config-registered agent to a local harness directory.

> For background on the pi runtime, its security posture, and known
> constraints, see [Agent runtimes — Pi-specific known
> constraints](../../runtimes.md#running-pi).

### Prerequisites (pi-specific)

In addition to the general [prerequisites](#prerequisites) above, you need:

| Requirement | Details |
|-------------|---------|
| Sandbox image with pi | `ghcr.io/fullsend-ai/fullsend-sandbox:latest` (must include `PI_VERSION`). Pull the latest to avoid stale cached images — see [Troubleshooting](#troubleshooting-pi-runtime) |
| GCP credentials | A service account key or `gcloud` ADC (`application_default_credentials.json`). The existing [GCP credentials](#get-google-cloud-platform-credentials) section applies — the pi Vertex provider reads the same variables |

### Directory layout

A working `--fullsend-dir` for the pi runtime needs the harness, a
`config.yaml` that both registers the agent and selects the runtime, and
supporting files for sandbox credentials and network policy. A bare
`config.yaml` + `harness/` is **not sufficient** — the agent starts but
fails when the sandbox has no credentials or Vertex egress:

```
pi-hello/
├── config.yaml                 # registers the agent and selects runtime: pi
├── harness/
│   └── pi-smoke.yaml           # agent harness: image, model, host_files, policy
├── agents/
│   └── pi-smoke.md             # agent definition (frontmatter + task prompt)
├── policies/
│   └── base.yaml               # OpenShell sandbox policy (Vertex egress)
├── profiles/
│   └── fullsend-vertex-ai.yaml # OpenShell egress allowlist for Vertex
├── providers/
│   └── vertex-ai.yaml          # OpenShell inference provider
└── env/
    └── gcp-vertex.env           # sandbox-side GCP env vars (expand: true)
```

You can copy `policies/`, `profiles/`, `providers/` and `env/` from a
`fullsend-ai/agents` clone, or write them yourself — all four are short,
and their contents are given below so this example stays fleet-free.

#### `config.yaml`

The config must both register the harness **and** set `defaults.runtime: pi`.
The two are checked in different places and fail differently:

- **No `agents:` entry** — placing `harness/pi-smoke.yaml` on disk is not
  enough. `resolveAgentSource` looks the agent up in the config and, finding
  nothing, fails with `resolving agent "pi-smoke": no config and agents-repo
  fallback unavailable`.
- **No `defaults.runtime: pi`** — the run *succeeds* and silently uses the
  default `claude` runtime (`backendFromConfigFile` → `ResolveFromConfig`).
  The give-away is the `runtime: selected "claude"` line; pi is never
  started.

```yaml
version: "1"
agents:
  - source: harness/pi-smoke.yaml
defaults:
  runtime: pi
```

#### `harness/pi-smoke.yaml`

The harness must include `host_files` to deliver GCP credentials into the
sandbox, and reference OpenShell profiles/providers for Vertex egress.
The `--env-file` flag sets the **runner** environment only — sandbox
environment comes from the harness via `env.sandbox` and `host_files`
([ADR 0055](../../ADRs/0055-unified-env-var-delivery.md)):

```yaml
agent: agents/pi-smoke.md
policy: policies/base.yaml
openshell:
  profiles:
    - profiles/fullsend-vertex-ai.yaml
providers:
  - providers/vertex-ai.yaml

role: triage
slug: fullsend-ai-pi-smoke
model: haiku
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest

host_files:
  - src: env/gcp-vertex.env
    dest: /sandbox/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/.gcp-credentials.json
```

#### `agents/pi-smoke.md`

A minimal agent definition with a deterministic task:

```markdown
---
name: pi-smoke
description: Minimal smoke-test agent for the pi runtime.
tools: Bash(ls), Write
model: haiku
---

You are a smoke-test agent. Do exactly this, then stop: run `ls .` with the
bash tool, then use the write tool to create
`/sandbox/workspace/output/agent-result.json` containing exactly:

{"action": "sufficient", "reasoning": "Smoke run: pi executed a tool call and wrote this file.", "comment": "pi runtime smoke test - no action needed."}

Do not read or modify anything else.
```

The payload matches the `triage` result schema (`action`, `reasoning`,
`comment`) because the harness declares `role: triage`. This example passes
`--no-post-script`, so nothing validates it — but writing a valid result
keeps the example composable if you drop that flag or reuse the harness for
a real agent.

#### `env/gcp-vertex.env`

Sandbox-side GCP environment — these variables reach pi inside the
sandbox:

```bash
export ANTHROPIC_VERTEX_PROJECT_ID={project-id}
export GOOGLE_CLOUD_PROJECT={project-id}
export CLOUD_ML_REGION=global
export GOOGLE_APPLICATION_CREDENTIALS=/tmp/.gcp-credentials.json
```

Two details this file depends on:

- **`export` is required.** The sandbox sources `.env.d/*.env` with plain `.`
  and no `set -a` (`internal/cli/run.go`), so a bare `KEY=value` becomes a
  shell variable that pi — a child process — never sees. The symptom is the
  Vertex extension disabling itself, or a credentials error, with the file
  plainly present in the sandbox.
- **The filename must end in `.env`** — the sourcing loop globs `*.env`, so
  `gcp-vertex.conf` would be copied and silently ignored.
- `GOOGLE_APPLICATION_CREDENTIALS` here is the **sandbox** path, matching the
  `host_files` `dest` above — not the path on your machine. The `host_files`
  entry uses `${GOOGLE_APPLICATION_CREDENTIALS}` from your *runner* shell to
  find the key locally.

#### `policies/base.yaml`

The sandbox policy. Note the `read_only`/`read_write` prefixes — anything the
agent must read has to sit under one of them, which is why the pi Vertex
extension lives under `/usr/local/share` and not `/opt` (fullsend#6504):

```yaml
---
version: 1
filesystem_policy:
  include_workdir: true
  read_only: [/usr, /lib, /proc, /dev/urandom, /app, /etc, /var/log]
  read_write: [/sandbox, /tmp, /dev/null]
landlock:
  compatibility: best_effort
process:
  run_as_user: sandbox
  run_as_group: sandbox
```

#### `profiles/fullsend-vertex-ai.yaml`

The egress allowlist. Without it the sandbox blocks the inference call and pi
reports a model-not-found error rather than a network error. `**/node` is the
entry that matters for pi; `**/claude` serves the Claude Code runtime:

```yaml
---
id: fullsend-vertex-ai
display_name: Fullsend Vertex AI
description: Google Cloud APIs for Vertex AI inference
category: inference
endpoints:
  - host: "*.googleapis.com"
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
binaries:
  - "**/claude"
  - "**/node"
```

#### `providers/vertex-ai.yaml`

Binds that profile to the sandbox as an OpenShell provider:

```yaml
---
name: vertex-ai
type: fullsend-vertex-ai
credentials:
  _NOOP_VERTEX_AI: ""
```

### Running the agent

```bash
fullsend run pi-smoke \
  --fullsend-dir ./pi-hello \
  --target-repo /tmp/target-repo \
  --env-file fullsend-gcp.env \
  --no-post-script \
  --output-dir /tmp/fullsend-out
```

On a successful run, you see output like:

```
runtime: selected "pi" from ./pi-hello/config.yaml
→ Agent: claude-haiku-4-5 (v0.84.2)
→ Result: stop
    Turns: 2
    Tokens: in=5169 out=372 reasoning=140 cache_create=0 cache_read=0
  ✓ Agent exited with code 0 (5.5s)
```

The `runtime: selected "pi"` line confirms the pi backend was used.

Add `--keep-sandbox` when a run fails and you want to inspect the sandbox
afterwards — but delete it when you are done (`openshell sandbox delete
<name>`), since kept sandboxes are not cleaned up for you.

### Run artifacts

After a successful run, the output directory contains:

```
/tmp/fullsend-out/<sandbox-name>/
├── logs/
│   ├── openshell-sandbox.log       # OCSF events (network, policy decisions)
│   └── openshell-gateway.log
├── iteration-1/
│   ├── output/
│   │   └── agent-result.json       # whatever the agent wrote to output/
│   ├── output.jsonl                # the agent's raw event stream
│   └── transcripts/
│       └── <agent>-<timestamp>_<id>.jsonl  # pi session transcript
├── metrics.json                    # includes "runtime": "pi"
├── run-telemetry.jsonl
└── security/                       # findings.jsonl appears only when a
                                    # security hook actually reports something
```

Everything lives under a per-run directory named after the sandbox
(`fs-<slug>-<id>`), so `--output-dir` accumulates one subdirectory per run
rather than being overwritten. A clean run leaves `security/` empty — that is
the expected result, not a missing artifact.

Key artifacts to verify:

- **`metrics.json`** — check `"runtime": "pi"` to confirm the pi backend
  was used
- **Session transcript** — the `.jsonl` file under `transcripts/` contains
  pi's session events; look for `toolCall` / `toolResult` entries
- **`pi-debug.log`** — appears when `--debug='*'` is passed (note the `=`
  syntax — see [Troubleshooting](#troubleshooting-pi-runtime))

Use the `analyze-transcript` skill to inspect the session:

```bash
python3 skills/analyze-transcript/analyze-transcript.py summary \
  /tmp/fullsend-out/<sandbox-name>/iteration-1/transcripts/<session>.jsonl
```

```
Agent:      pi-smoke
Model:      claude-haiku-4-5
Messages:   7 (4 user, 3 assistant)
Tokens:     5485 in / 531 out / 0 cache-read / 0 cache-create

Tool calls:
  bash                           2
  write                          1

Stop reasons: toolUse=2, stop=1
```

`tools` and `conversation` are the other two subcommands worth knowing —
`tools` for a per-call table, `conversation` for the readable flow.

### Pi runtime knobs

| Variable | Description |
|----------|-------------|
| `FULLSEND_MODEL` (or `fullsend run --model`) | Override the model for the run on any runtime; `FULLSEND_PI_MODEL` is kept as a pi-only alias |
| `FULLSEND_RUNTIME` (or `--runtime`) | Override the runtime selected by `config.yaml` |
| `FULLSEND_EFFORT` (or `--effort`) | Override the harness effort level |
| `FULLSEND_PI_PROVIDER` | Override the inference provider (runner env) |
| `FULLSEND_PI_BASH_ALLOWLIST` | Set to `enforce` to make the Bash first-token allowlist block instead of warn |

### Security hooks

Security hooks are enabled by default on pi. The run refuses to start
(exit 97) without the hook adapter — this is intentional (fail-closed).
The runner checks the adapter's SHA-256 before sourcing the agent-writable
`.env`, so a tampered adapter is rejected.

A planted `.pi/extensions/evil.js` in the target repo is **not** loaded
when `--no-approve` is set (the default in fullsend runs). Pi's
`defaultProjectTrust: never` setting in the sandbox config prevents
repo-owned extensions, skills, and settings from loading.

### Troubleshooting pi runtime

**`pi preflight: pi --version exited 127: sh: 1: pi: not found`**
- The sandbox image is stale and predates the pi layers. Pull the latest:
  ```bash
  podman pull ghcr.io/fullsend-ai/fullsend-sandbox:latest
  ```

**`[pi-anthropic-vertex] disabled: set GOOGLE_CLOUD_PROJECT ...`**
- The harness is missing `host_files` and/or the OpenShell egress profile.
  Sandbox environment comes from the harness, not from `--env-file`. See
  the [harness layout](#directory-layout) above.

**`--debug "..."` fails with `accepts 1 arg(s), received 2`**
- `--debug` is an optional-value flag. Use `--debug='*'` (with `=`), not
  `--debug "*"`.

**Agent fails silently — check `pi-debug.log`**
- When a custom harness is missing `host_files` or the OpenShell profile,
  the failure appears in `pi-debug.log` inside the run directory, not in
  the runner's terminal output.

### Platform notes (pi)

**Linux (Fedora, rootless Podman):** verified end-to-end. The general
[Linux platform notes](#linux) apply.

**macOS (Apple Silicon):** sandbox creation, the pi bootstrap and preflight,
loading the Vertex extension, and model-id translation are verified on
`darwin/arm64` (macOS 26.5.2, podman machine, `openshell` from Homebrew).
The general [macOS platform notes](#macos) apply. In particular:
- Use `/private/tmp/...` for bind mounts (not `/tmp/...`)
- If the sandbox image architecture differs from the host, set
  `FULLSEND_SANDBOX_ARCH` and provide a Linux binary with
  `--fullsend-binary`

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

> Running the **pi runtime**? Its platform-specific notes live with the rest
> of the pi walkthrough: [Platform notes
> (pi)](#platform-notes-pi).

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
