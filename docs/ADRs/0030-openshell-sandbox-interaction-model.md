---
title: "30. OpenShell sandbox interaction model"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - sandbox
  - openshell
  - configuration
---

# 30. OpenShell sandbox interaction model

Date: 2026-05-06

## Status

Accepted

## Context

The `fullsend run` command provisions and interacts with
[OpenShell](https://github.com/NVIDIA/OpenShell) sandboxes to execute agents.
OpenShell is a container-based sandbox runtime providing a long-lived gateway
(sandbox lifecycle management, credential injection, policy enforcement),
SSH/SCP access via HTTP CONNECT tunnels through the gateway, provider-based
credential delivery, YAML-based security policies, and container images as
sandbox bases. How the gateway works: `openshell gateway start` spawns a
persistent process that manages multiple sandboxes concurrently, maintains
state in a local database, and can resume sandboxes across restarts. The
gateway is reusable — it does not need to be restarted between sandbox
invocations.

OpenShell exposes two interfaces: a gRPC API and a CLI that wraps it. The CLI
does not surface all API capabilities. Notably, the gRPC API supports passing
environment variables to sandboxes via `SandboxSpec.environment` and
`ExecSandboxRequest.environment`, but the CLI's `sandbox create` command has no
`--env` flag. This constraint shapes how fullsend delivers configuration.
Credential delivery is already decided in
[ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md);
harness structure in
[ADR 0024](0024-harness-definitions.md).

## Options

### Environment configuration

**A. gRPC API env var passthrough.** Use `SandboxSpec.environment` directly.
Requires fullsend to maintain a gRPC client and proto dependencies.

**B. Files via SCP.** Write `.env` files on the host, copy into the sandbox,
`source` before the agent runs. Host-side `${VAR}` expansion resolves values
before delivery.

### File and binary delivery

**A. SCP during bootstrap.** Copy files from host after sandbox creation.
Per-file destination control and content expansion. Used for dynamic content:
agent definitions, skills, host files, security hooks.

**B. Pre-built container image (`--from`).** Bake tools, runtimes, and
dependencies into the image. Static content only — changes require a new image
build. Custom images must include a `sandbox` user (uid/gid 1000660000), a
writable workdir, and `iproute2` for network namespace setup.

**C. OpenShell `--upload` flag.** Upload files at sandbox creation time. Single
path argument — less control over destinations and timing than SCP.

### Command execution

**A. SSH via tunneled connection.** Obtain SSH config from `openshell sandbox
ssh-config`, use standard `ssh`/`scp`/`rsync`. Supports stdout streaming.

**B. OpenShell native commands.** `openshell sandbox exec` for command execution
(streaming stdout/stderr via gRPC), `openshell sandbox upload`/`download` for
file transfer. No SSH binary or config needed — all communication goes through
the gateway's gRPC API.

### Credential delivery

**A. OpenShell providers with bare-key form.** Register providers on the gateway
via `openshell provider create --name <n> --type <t> --credential <KEY>`.
Credentials are injected into the child process environment (bare-key form) so
secret values never appear on the command line. Providers are attached to the
sandbox via `--provider <name>` on `sandbox create`. The gateway swaps opaque
placeholder tokens for real credentials at the HTTP proxy layer — credentials
never enter the sandbox.

**B. Host files via SCP.** Copy credential files (e.g. GCP service account JSON)
into the sandbox during bootstrap. Real credentials exist on the sandbox
filesystem. Required for auth flows incompatible with the provider placeholder
model (multi-step OAuth2, in-sandbox cryptographic ops).

**C. Scoped tokens as env vars.** Generate short-lived tokens and pass them into
the sandbox directly. Credentials are present in the sandbox environment —
exfiltrable by a compromised agent for the token's full TTL.

**D. Host-side REST server.** A server on the host holds credentials and exposes
scoped endpoints. The sandbox calls it via HTTP. No credentials in the sandbox,
but requires per-service proxy code.

### Gateway lifecycle

**A. Start once, reuse.** Check with `openshell gateway info`; start only if
absent. Provider state persists across sandbox invocations.

**B. Per-run gateway.** Start and stop for each agent run. Clean state but adds
cold-start latency and prevents provider reuse.

## Decision

**Environment: files via SCP (Option B).** Fullsend writes a `.env` file with
infrastructure paths (`PATH`, `CLAUDE_CONFIG_DIR`, `FULLSEND_OUTPUT_DIR`) and a
loader that sources `.env.d/*.env`. Application configuration is delivered via
`host_files` with host-side `${VAR}` expansion
([ADR 0024](0024-harness-definitions.md)). Host-side `runner_env` variables
are available only to pre/post scripts and never enter the sandbox.

**Credentials: providers reconciled on the gateway.** Credential delivery follows
the four-tier credential delivery model in
[ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md). For
credential delivery tiers that use OpenShell providers, the runner reconciles them before sandbox
creation: it loads provider definitions from the harness's `providers/`
directory and calls `openshell provider create --name <n> --type <t>
--credential <KEY>` for each one. Credentials use the bare-key form — secret
values are injected into the child process environment rather than appearing on
the command line (visible in `ps`), and OpenShell reads them from there.
Providers are then attached to the sandbox via `--provider <name>` flags on
`openshell sandbox create`. The gateway swaps opaque placeholder tokens for
real credentials at the HTTP proxy layer, so credentials never enter the
sandbox. For auth flows incompatible with the provider placeholder model
(e.g. GCP Vertex AI file-based auth), host files deliver credential files
directly (credential delivery tier 4).

**Files and binaries: SCP + images (Options A + B).** Agent definitions, skills,
host files, and security hooks are SCP'd during bootstrap. Tool binaries and
runtimes are baked into the container image. OpenShell's `--upload` (Option C)
is not used — fullsend needs per-file destination control and content expansion.

**Commands: SSH (Option A).** `SSHStreamReader` enables real-time parsing of the
agent's `stream-json` output. All SSH traffic tunnels through the gateway's
HTTP CONNECT endpoint — no direct TCP listener on the sandbox. Rsync over SSH
extracts the modified target repo with `--no-links` (prevent symlink-based
sandbox escape) and `--exclude .git/hooks/` (prevent injected executables).

**Gateway: start once, reuse (Option A).** `EnsureGateway()` (since renamed to `CheckGateway()`) is idempotent. The
gateway persists across sandbox invocations within the same runner job.

**Sandbox lifecycle** follows a fixed sequence: create (`openshell sandbox create
--name <n> --keep --no-auto-providers --no-tty --from <image> --policy <policy>
--provider <p> -- true`) → poll for readiness → bootstrap (SCP files, SSH to
create directories) → execute agent (SSH with streaming) → extract results (SCP
for transcripts and output, rsync for repo) → delete
(`openshell sandbox delete`). The `--keep` flag prevents self-deletion after the
entry command (`true`) exits; fullsend explicitly deletes after extraction.

## Consequences

- Fullsend depends only on the OpenShell CLI, `ssh`, `scp`, and `rsync` — no
  gRPC client or proto compilation required. This may change: shelling out to
  `ssh`/`scp`/`rsync` requires defensive workarounds for path traversal,
  symlink following, and timeout handling.
  [#261](https://github.com/fullsend-ai/fullsend/issues/261) tracks replacing
  these with Go-native SSH/SFTP libraries or OpenShell's native commands, which
  would eliminate these issues structurally.
- Environment configuration is file-based: changes require updating harness
  `host_files`, not code changes. If OpenShell adds `--env` to its CLI,
  fullsend could adopt it for infrastructure variables while keeping files for
  expanded configuration.
- Pre-built images are the tool provisioning path — adding a tool means
  rebuilding the image, not modifying bootstrap.
- SSH tunneling through the gateway makes all sandbox I/O auditable at the
  gateway level.
- The rsync security filters (`--no-links`, `--exclude .git/hooks/`) guard
  against two specific sandbox escape vectors; new extraction paths must apply
  equivalent protections.
