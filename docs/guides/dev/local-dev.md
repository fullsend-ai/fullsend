# Local development

This guide walks through running fullsend agents locally on macOS and Linux. PR [#612](https://github.com/fullsend-ai/fullsend/pull/612) added multi-arch (amd64 + arm64) sandbox image support, so both architectures are covered.

## Prerequisites

| Requirement | macOS | Linux |
|-------------|-------|-------|
| Container runtime | Podman Desktop with a running machine | Podman |
| OpenShell | 0.0.54 (Podman support) | 0.0.54 |
| GCP credentials | Service account key (`Vertex AI User` role) | Same |
| GitHub PAT | `repo` scope for the target org | Same |
| Go toolchain | Optional — only needed when building the CLI from source | Same |

> **arm64 note**: Apple Silicon Macs and arm64 Linux hosts must use the `:dev` multi-arch sandbox images. The default `:latest` tag is amd64-only until multi-arch merges to main. See step 3 below.

## 1. Build the fullsend CLI

```bash
make go-build
```

The binary is written to `./bin/fullsend`.

## 2. Set up the .fullsend directory

Clone the `.fullsend` config repo for your org:

```bash
gh repo clone <org>/.fullsend /tmp/fullsend-dot
```

## 3. Create an env file

Env files contain secrets and must never be committed. The repo `.gitignore` already excludes `*.env` and `.env.*`. Keep your env file outside the repo (e.g. `/tmp/fullsend.env`) or use a name that matches these patterns.

Create a file with the required environment variables:

```bash
ANTHROPIC_VERTEX_PROJECT_ID=<gcp-project-with-vertex-ai>
CLOUD_ML_REGION=global
GOOGLE_APPLICATION_CREDENTIALS=/path/to/sa-key.json
GH_TOKEN=<github-pat>
```

Each agent requires additional variables via its harness `runner_env`. Add the ones needed for the agent you are running:

| Variable | Agent(s) | Description |
|----------|----------|-------------|
| `GITHUB_ISSUE_URL` | triage | Full URL of the GitHub issue to triage |
| `REPO_FULL_NAME` | triage, code, review, fix | `owner/repo` of the target repository |
| `ISSUE_NUMBER` | code | Issue number the code agent should implement |
| `TARGET_BRANCH` | code, fix | Branch to base work on (e.g. `main`) |
| `PUSH_TOKEN` | code, fix | GitHub token with push access for the post-script |
| `PR_NUMBER` | review, fix | Pull request number to review or fix |
| `GITHUB_PR_URL` | review | Full URL of the pull request to review |
| `REVIEW_TOKEN` | review | GitHub token for posting review comments |

See the harness definitions in `harness/*.yaml` within your `.fullsend` config directory for the complete list per agent.

### arm64 image override

On arm64 hosts (Apple Silicon, Graviton, etc.), add the sandbox image override to your env file:

```bash
FULLSEND_SANDBOX_IMAGE=ghcr.io/fullsend-ai/fullsend-sandbox:dev
```

This switches from the default `:latest` tag (amd64-only) to the `:dev` multi-arch image that includes arm64 support. On amd64 hosts this line is not needed — `:latest` works directly.

## 4. Run an agent

```bash
./bin/fullsend run triage \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env
```

The `--env-file` flag loads environment variables before the harness is parsed, so variables are available for harness YAML expansion. The flag is repeatable — later files override earlier ones.

## Security scanning and cross-platform binary

The pre-agent security scan runs `fullsend scan context` inside the Linux sandbox to check for prompt injection before the agent starts. This requires a Linux fullsend binary in the sandbox.

On macOS, the host binary is a Mach-O executable that cannot run inside the Linux sandbox. The CLI detects the OS mismatch and automatically obtains a Linux binary — no manual steps needed. The architecture (arm64/amd64) typically matches between the host and the Podman VM, so only the OS differs.

### How the Linux binary is resolved

| Priority | Strategy | When it applies |
|----------|----------|-----------------|
| 1 | `--fullsend-binary <path>` | Always used if provided — skips all auto-resolution |
| 2 | Download from GitHub Release | CLI version matches a release (e.g. `0.4.0`) |
| 3 | Cross-compile from source | Go toolchain available and run from the module root |
| 4 | Download latest release | Fallback when cross-compilation fails |

On Linux hosts, the CLI copies its own executable directly — no download or compilation needed.

### Providing an explicit binary

To skip auto-resolution (e.g. testing a dev build), cross-compile a Linux binary and pass it via `--fullsend-binary`:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/fullsend-linux ./cmd/fullsend/
./bin/fullsend run review \
  --fullsend-dir /tmp/fullsend-dot \
  --target-repo /path/to/repo \
  --env-file /tmp/fullsend.env \
  --fullsend-binary /tmp/fullsend-linux
```

The CLI validates that the provided file is a Linux ELF binary before copying it into the sandbox. Passing a macOS Mach-O binary produces a clear error instead of a cryptic failure inside the sandbox.

> **Note:** if your sandbox image uses a different CPU architecture than the host (e.g. amd64 image on an arm64 Mac via QEMU emulation), set `FULLSEND_SANDBOX_ARCH=amd64` so the CLI downloads or cross-compiles for the correct architecture. This is not needed in the typical setup where the Podman VM matches the host arch.

## Known issues

### macOS (Apple Silicon)

- **Podman entrypoint**: the container entrypoint `/bin/bash` may fail with "cannot execute binary file". OpenShell handles this internally, but standalone `podman run` needs `--entrypoint ""` with `/usr/bin/env sh -c`.
- **Podman machine**: ensure the Podman machine is running (`podman machine start`) before invoking fullsend. The CLI does not start it automatically.
- **Podman host-gateway**: if sandbox creation fails with `unable to replace "host-gateway"`, set `host_containers_internal_ip = "192.168.127.254"` under `[containers]` in `~/.config/containers/containers.conf` and restart the Podman machine.

### Linux

- **Docker socket permissions**: if using Docker, your user must be in the `docker` group or you need to run with `sudo`. Podman runs rootless by default.
- **SELinux**: on Fedora/RHEL, bind-mounted volumes may need the `:z` suffix for standalone `podman run`. OpenShell handles this automatically.

### Both platforms

- **Image tags**: harness YAMLs ship with `:latest` which is amd64-only until multi-arch merges to main. Use `FULLSEND_SANDBOX_IMAGE` to override to `:dev` for local testing on arm64.
