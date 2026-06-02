# ADR 0043 — Host-Side API Server Design — Spec

Tracking issue: [#880](https://github.com/fullsend-ai/fullsend/issues/880)
Parent issue: [#879](https://github.com/fullsend-ai/fullsend/issues/879)
Experiment: `experiments/host-side-api-server` ([fullsend-ai/experiments#28](https://github.com/fullsend-ai/experiments/pull/28))

## Purpose

Define the complete design for host-side API servers that run outside the
OpenShell sandbox and are callable by the agent via HTTP. ADR 0024 introduced
the `api_servers` harness field as PLANNED, and ADRs 0017/0025 established the
host-side REST server as Tier 3 of the credential delivery model. This ADR
fills the remaining design gaps before implementation (#881).

## Decisions to record

### 1. Server process contract and lifecycle

**Context.** The experiment validated a uniform process contract across two
servers in different languages (Go, Python). The runner needs a
language-agnostic interface to manage arbitrary API servers.

**Decision.** Every host-side API server managed by the runner must:

- Accept `--port <port>` and `--token <bearer-token>` CLI flags
- Accept `--bind-address <addr>` (default `127.0.0.1`, see §8 for why the
  runner overrides to `0.0.0.0` today)
- Serve `GET /healthz` returning `{"status": "ok"}` when ready (unauthenticated)
- Serve `GET /tools.json` for agent discovery (see §2)
- Validate `Authorization: Bearer <token>` on all non-health, non-discovery
  endpoints
- Shut down cleanly on `SIGTERM` (5s grace period, then `SIGKILL`)

**Runner lifecycle:**

1. Start declared API servers after pre-script, before sandbox creation
2. Poll `GET /healthz` until 200 (timeout: 15s, 500ms interval)
3. Configure sandbox network policy and provider credentials
4. Create sandbox and start agent
5. On exit (success or failure): send `SIGTERM` to servers, wait grace period,
   `SIGKILL` if needed — after sandbox destruction (step 11 of ADR 0024)

**Crash behavior.** If an API server crashes mid-run, the run fails. No restart
logic. API servers are critical infrastructure — a crashed server means the
agent has lost access to capabilities it depends on, and continuing would
produce incomplete or incorrect results.

**Harness schema.** Uses the existing `api_servers` field from ADR 0024,
keeping the `script` field name:

```yaml
api_servers:
  - name: builder
    script: api-servers/builder/bin/builder-server
    port: 9090
    providers:                       # NEW — see §3
      - builder-build
      - builder-push
    env:
      REGISTRY_TOKEN: ${REGISTRY_TOKEN}
```

### 2. API discoverability for the agent

**Context.** The experiment compared three approaches for making the API known
to the agent. Each was tested under full-access and restricted L7 policies.

**Options:**

| | `/tools.json` | `/openapi.json` | Baked instructions |
|---|---|---|---|
| Token efficiency (full access) | 92k (best) | 107k | 100k |
| Token efficiency (restricted) | 205k (fails) | 534k (fails) | 84k (succeeds) |
| Resilience to blocked discovery | Fails — agent guesses paths | Fails — agent guesses paths | Succeeds — knows paths from skill |
| Maintainability | Single source of truth in server | Single source of truth in server | Skill can drift from API |
| Agent parsing | Structured JSON, minimal ambiguity | Verbose nested structure, more context tokens | Prose, more interpretation needed |

**Decision.** Require `GET /tools.json` as the standard discovery endpoint.
Each entry contains `name`, `description`, `endpoint`, `method`, and
`input_schema`:

```json
[
  {
    "name": "build_container",
    "description": "Build a container image using podman or docker",
    "endpoint": "/build",
    "method": "POST",
    "input_schema": {
      "type": "object",
      "required": ["tag"],
      "properties": {
        "tag": {"type": "string", "description": "Image tag"},
        "dockerfile": {"type": "string", "default": "Dockerfile"}
      }
    }
  }
]
```

**Rationale.**

- `/tools.json` is the most token-efficient under full access (92k vs 100k for
  baked, 107k for OpenAPI).
- It returns structured data purpose-built for agent consumption — agents parse
  JSON directly rather than interpreting Markdown prose or navigating verbose
  OpenAPI nesting.
- The schema is a single source of truth in the server. When the API changes,
  the agent automatically discovers the new schema.
- OpenAPI is designed for code generators and documentation tools — its nested
  structure adds context tokens without proportional benefit for LLM agents.
- Baked instructions are the most resilient to restricted policies (84k, only
  method that succeeds), but the discovery endpoint should not be blocked in
  normal operation — it is part of the required process contract.

### 3. Network policy via composable provider profiles

**Context.** The experiment manually authored L7 policies and hit bugs from
mismatches between server capabilities and policy rules. OpenShell now supports
provider-backed policy composition
([NVIDIA/OpenShell#947](https://github.com/NVIDIA/OpenShell/issues/947),
[NVIDIA/OpenShell#1037](https://github.com/NVIDIA/OpenShell/pull/1037)) where
attaching a provider to a sandbox auto-injects L7 rules as Layer 2 policy
entries. Fullsend issue
[#776](https://github.com/fullsend-ai/fullsend/issues/776) tracks adopting
this for harness policies.

**How OpenShell policy composition works.** OpenShell has a 3-layer policy
stack:

- **Layer 1 (Base):** Filesystem, process, landlock — static sandbox config.
- **Layer 2 (Provider):** Auto-generated from attached providers. Each attached
  provider contributes network policy rules under a reserved `_provider_*` key.
- **Layer 3 (User):** Explicit user-authored rules via `openshell policy set`.

A **provider** bundles three things: credentials (with injection style),
endpoints (L7 rules), and binaries (which executables can use those endpoints).
A **provider profile** is the template that defines a provider type — the YAML
schema declaring the endpoints, binaries, and auth configuration. When a
provider is attached to a sandbox, OpenShell auto-injects its endpoint rules
into the effective policy.

Composition is **additive**: the proxy permits a request if it matches ANY rule
across layers (union of allows). Deny rules win globally — if any rule denies
a request, it's blocked regardless of allows in other rules.

**Options:**

**Option A: Composable provider profiles per capability (recommended).** Each
API server ships atomic provider profiles — one per logical group of endpoints.
Each profile bundles the credential injection (`auth` block), the L7 endpoint
rules, and the binary restrictions as a single `(credential, endpoint, binary)`
unit. Harnesses list which profiles to attach. Different agent roles compose
different capability sets for the same server.

```yaml
# Provider profiles (defined once per API server, registered on gateway):
#
# builder-build profile:
#   endpoints: POST /build, GET /healthz, GET /tools.json
#   binaries: **/curl
#   auth: bearer token injection
#
# builder-push profile:
#   endpoints: POST /push
#   binaries: **/curl
#   auth: bearer token injection
#
# builder-read profile:
#   endpoints: GET /images
#   binaries: **/curl
#   auth: bearer token injection

# Code agent harness — full access:
api_servers:
  - name: builder
    script: api-servers/builder/bin/builder-server
    port: 9090
    providers:
      - builder-build
      - builder-push
      - builder-read

# Review agent harness — read-only:
api_servers:
  - name: builder
    script: api-servers/builder/bin/builder-server
    port: 9090
    providers:
      - builder-read
```

Pros: reusable across harnesses, follows least privilege naturally (compose
only what the agent needs), aligns with OpenShell's provider model (credential
+ endpoint + binary as a unit), each profile is defined once and composed
freely. Cons: requires creating and registering custom provider profiles for
each API server capability, depends on OpenShell >= v0.0.37 and fullsend #776.

**Option B: Runner-generated monolithic policy.** The runner generates a single
L7 policy file from an `allowed_paths` list in the harness `api_servers` entry.
No dependency on OpenShell provider profiles.

```yaml
api_servers:
  - name: builder
    script: api-servers/builder/bin/builder-server
    port: 9090
    allowed_paths:
      - method: POST
        path: /build
      - method: GET
        path: /images
```

Pros: works with any OpenShell version, no external dependencies, simpler to
implement. Cons: duplicates policy logic across harnesses, error-prone
(experiment hit bugs from mismatches between server API surface and manually
authored policy), no reuse of capability definitions, credential injection must
be handled separately.

**Decision.** Option A — composable provider profiles per capability.

**Requirements:**

- OpenShell >= v0.0.37 (profile-backed policy composition,
  [NVIDIA/OpenShell#1037](https://github.com/NVIDIA/OpenShell/pull/1037))
- The `use_providers_v2` gateway setting may be required (see
  [#776](https://github.com/fullsend-ai/fullsend/issues/776))
- Prerequisite: [fullsend-ai/fullsend#776](https://github.com/fullsend-ai/fullsend/issues/776)
  (adopt provider-backed policy composition)

**Composition semantics.** Composition is additive-only: provider rules and
user rules live in separate keys, and the proxy permits a request if it matches
any rule. There is no cross-rule deny mechanism that would let a user policy
narrow what a provider profile grants (though provider deny rules do block
globally). Different access levels for the same server are achieved by
composing different profile sets, not by adding deny overrides.

### 4. Per-run authentication

**Context.** The agent inside the sandbox needs to authenticate to API servers.
The real credential must never enter the sandbox.

**Options:**

**Option A: UUID bearer token via provider placeholders (recommended).** The
runner generates a random UUID token per run. The token is registered as an
OpenShell provider credential with an `auth: bearer` declaration. The L7 proxy
resolves the placeholder to the real token in outgoing `Authorization` headers
— the real token never enters the sandbox.

```yaml
# Provider definition
name: api-server
type: generic
credentials:
  API_TOKEN: ${API_TOKEN}
```

Pros: simple, proven by experiment, no key management, credential never enters
sandbox, credential scoping ensures the token is only injected for requests
matching the provider's endpoints and binaries. Cons: no claims or expiry —
the token is valid for the entire run and grants whatever endpoints the L7
policy allows.

**Option B: Short-lived JWTs with claims.** The runner generates a JWT signed
with a per-run key pair. Claims include run ID, repo, and allowed operations.
Servers validate the signature and claims. The JWT can be short-lived (e.g., 1
hour) with refresh.

Pros: per-operation authorization, expiry, audit trail via claims. Cons: adds
signing key management, JWT library dependency in every server, more complex
token lifecycle, and the L7 policy already restricts which endpoints are
reachable — JWT claims would be a second layer of the same restriction.

**Decision.** Option A — UUID bearer token via provider placeholders. The L7
policy already enforces which endpoints each agent can reach (§3), making
per-operation JWT claims redundant for the initial design. JWT-based auth is a
future enhancement for when per-operation claims become necessary (e.g.,
multi-tenant servers, cross-run audit).

**Security requirement.** Token comparison must be timing-safe
(`crypto/subtle.ConstantTimeCompare` in Go, `hmac.compare_digest` in Python).
The experiment code flagged this as a TODO.

### 5. Credential delivery to the server process

**Context.** API servers hold credentials on behalf of the agent (registry
tokens, GitHub tokens, API keys). These must reach the server without passing
through the sandbox.

**Decision.** Credentials are delivered via environment variables expanded from
the host environment at server startup. The `env` field in `api_servers` (ADR
0024) supports `${HOST_VAR}` syntax:

```yaml
api_servers:
  - name: builder
    script: api-servers/builder/bin/builder-server
    port: 9090
    env:
      REGISTRY_TOKEN: ${REGISTRY_TOKEN}
      GCP_KEY_PATH: ${GOOGLE_APPLICATION_CREDENTIALS}
```

The per-run bearer token is passed via `--token` CLI flag (not through `env`,
since it's part of the process contract).

No secrets mounts or vault integration in the initial design. The runner
expands `${VAR}` references against its own environment and passes the resolved
values to the server process. Sensitive values must not appear in logs or error
messages — servers must scrub credentials from error output (the experiment's
provisioner implements `_scrub_credentials` for this).

### 6. File transfer between server and sandbox

**Context.** API servers that build artifacts or provision repos need to
exchange files with the sandbox. File transfer must happen during request
handling — the agent calls the API, the server produces or consumes files, and
the result must be in the sandbox before the response returns. The runner is
not in this loop.

**Options:**

**Option A: `openshell sandbox upload/download` from the server (recommended).**
The server shells out to the OpenShell CLI to transfer files during request
handling. The agent passes its sandbox name per-request (discovered via
`hostname | sed 's/^sandbox-//'`), and the server uses it with `openshell
sandbox download <name> <sandbox-path> <local-path>` and `openshell sandbox
upload <name> <local-path> <sandbox-path>`.

Pros: works today, validated by experiment, handles real-time exchange
naturally (transfer happens during request handling), no runner mediation
needed. Cons: couples server to OpenShell CLI — servers need `openshell` on
`PATH` and can't be tested without it.

**Option B: Shared host mount.** The runner creates a staging directory on the
host and mounts it into the sandbox via `openshell sandbox create --mount
<host-path>:<sandbox-path>`. Both the server and the agent see the same
directory — no explicit transfer needed.

Pros: no transfer commands, transparent POSIX access, no OpenShell CLI
dependency in the server. Cons: depends on OpenShell mount support (available
on K3s via
[NVIDIA/OpenShell#500](https://github.com/NVIDIA/OpenShell/issues/500), pending
for VM driver via
[NVIDIA/OpenShell#1509](https://github.com/NVIDIA/OpenShell/issues/1509)),
bidirectional mounts may introduce TOCTOU risks if both sides write
concurrently.

**Option C: HTTP multipart via the API.** The agent uploads files to the server
and downloads results through the server's own REST endpoints using multipart
form data over the L7 proxy. The server stores files on the host side.

Pros: fully portable, standard HTTP, server is self-contained, no OpenShell
dependency. Cons: large files go through the L7 proxy (bandwidth/latency
overhead), requires multipart handling in every server, the proxy must allow
the content-type and body size.

**Decision.** Option A — `openshell sandbox upload/download` from the server.
These servers are purpose-built for the OpenShell environment, so the CLI
coupling is acceptable. The experiment validated this end-to-end with both
the builder (download context, build, upload tarball) and the provisioner
(clone, scan, upload repo).

Option B (shared mount) is noted as the preferred future direction when
OpenShell mount support is universally available — it eliminates transfer
commands entirely and makes file exchange transparent.

### 7. Provider vs. API server decision framework

**Context.** Issue #196 evaluated providers as a replacement for REST servers.
Providers (Tier 2) became the preferred path, but API servers (Tier 3) remain
necessary for cases providers cannot handle.

**Decision.** Use providers (Tier 2) by default. Use API servers (Tier 3) when
any of the following apply:

| Condition | Why providers can't handle it |
|---|---|
| **Long-running operations** (> 60s) | MCP client timeouts (~30-60s) make provider-based tools unsuitable ([claude-code#7575](https://github.com/anthropics/claude-code/issues/7575)) |
| **Sandbox capability gaps** | Operations the sandbox deliberately blocks (e.g., container builds — seccomp blocks `CLONE_NEWUSER`, `AF_NETLINK`, `setns`; agent has zero Linux capabilities; [NVIDIA/OpenShell#113](https://github.com/NVIDIA/OpenShell/issues/113)) |
| **Credentials in request bodies** | Provider placeholder model only intercepts `Authorization` headers; credentials embedded in JSON bodies or query parameters require server-side injection |
| **Response transformation** | Scanning, filtering, or transforming responses before they reach the agent (e.g., repo provisioner's scan-before-copy) |
| **Multi-step atomic operations** | Operations that combine multiple steps as a single unit (clone + scan + copy) where partial completion would be worse than failure |

### 8. Bind address and network exposure

**Context.** The experiment found that API servers must bind to `0.0.0.0`
because the L7 proxy connects from inside the container network namespace —
servers bound to `127.0.0.1` are unreachable. On rootless Podman, the container
bridge gateway IP (e.g., `10.88.0.1`) lives inside the container namespace and
cannot be bound from the host (`EADDRNOTAVAIL`).

**Decision.** Servers default to `--bind-address 127.0.0.1` (secure by
default). The runner explicitly passes `--bind-address 0.0.0.0` when starting
servers for sandboxed agents. This is a security trade-off: on shared hosts,
other processes can probe the server ports. Bearer token authentication
mitigates this but doesn't eliminate the attack surface.

**Future.** [NVIDIA/OpenShell#1633](https://github.com/NVIDIA/OpenShell/issues/1633)
proposes generalizing the `inference.local` supervisor proxy pattern for
arbitrary host services. If implemented, the supervisor would proxy connections
from inside the sandbox to `127.0.0.1` on the host, eliminating the need for
`0.0.0.0` binding entirely. Servers would never be network-exposed. The runner
should detect OpenShell support for this and stop passing
`--bind-address 0.0.0.0` when available.

**Network policy.** All URLs delivered into the sandbox must use
`host.openshell.internal`, never raw IPs. The L7 proxy matches requests by
hostname, and SSRF protection blocks private IP addresses. The `allowed_ips`
field in network policies handles SSRF allowlisting separately using the host
IP rendered into policy templates at runtime.

### 9. Security hardening requirements

Based on experiment findings and code review of the PoC servers:

| Requirement | Rationale |
|---|---|
| **Timing-safe token comparison** | Naive string comparison leaks token length via timing side-channel. Use `crypto/subtle.ConstantTimeCompare` (Go) or `hmac.compare_digest` (Python). |
| **Request body size limits** | Prevent DoS via oversized payloads. Recommend 1 MB default (`http.MaxBytesReader` in Go, `Content-Length` check in Python). |
| **Rate limiting on unauthenticated endpoints** | `/healthz`, `/tools.json` are reachable without a bearer token. Rate limit to prevent abuse, especially when bound to `0.0.0.0`. |
| **Credential scrubbing in error messages** | Error responses must not leak credentials embedded in URLs or environment variables. Servers must scrub before returning errors to the agent. |
| **Bounded in-memory state** | Servers that track operation state (e.g., provisioner's job map) must bound the state or expire old entries. Unbounded growth is acceptable only for short-lived experiment runs. |

### 10. Relationship to existing ADRs

| ADR | Relationship |
|---|---|
| **0016** (Unidirectional control flow) | Preserved — API servers are provisioned top-down from the harness. Agents cannot request servers to be started. The runner manages the full lifecycle. |
| **0017** (Credential isolation) | Implemented — this ADR specifies the concrete process contract for the host-side REST server model. Per-run bearer tokens via provider placeholders fulfill the "credentials never enter the sandbox" requirement. |
| **0024** (Harness definitions) | Extended — this ADR specifies runtime behavior for the `api_servers` field, adds `providers` sub-field for composable policy profiles, and inserts API server lifecycle into the execution sequence (after pre-script, before sandbox creation). |
| **0025** (Provider-based credential delivery) | Tier 3 (host-side REST server) is now fully specified. The decision framework in §7 defines when to use Tier 3 vs. Tier 2 (providers). |
