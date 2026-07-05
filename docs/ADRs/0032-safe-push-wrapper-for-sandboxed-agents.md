---
title: "32. safe-push wrapper binary for sandboxed agents"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - sandbox
  - security
  - push
  - providers
  - credentials
---

# 32. safe-push wrapper binary for sandboxed agents

Date: 2026-05-07

## Status

Accepted (extends [ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md))

## Context

[ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md) introduced a four-tier credential delivery model and described custom wrapper binaries as a credential delivery tier 2 mechanism for operation-level control — citing `safe-push` as the canonical example of a binary that wraps `git push` and rejects force pushes. The [openshell-policy-bypass experiment](https://github.com/fullsend-ai/experiments/pull/5) validated that the three-layer defense (L7 binary matching + wrapper logic + Landlock read-only path) holds against an agent with 20 turns of unrestricted bypass attempts. This ADR specifies the design of `safe-push` and its integration with the harness and sandbox infrastructure.

### The push robustness problem

The current code agent relies on a non-agentic post-script (`post-code.sh`) to push code after the sandbox is destroyed. This is the credential delivery tier 1 (prefetch + post-process) model: the agent never touches push credentials, and the post-script handles branch validation, secret scanning, pre-commit hooks, and the actual `git push`. This model is robust for security but has two limitations:

1. **The agent has no control over the push flow.** The post-script is a fixed script — the agent cannot choose between force-push and regular push, retry on conflict, or adapt to diverged branches. Making the script more complex to handle edge cases increases fragility.

2. **The post-script is the only option.** In low-risk environments (private repos, internal tooling), the overhead of post-script content validation may be unnecessary. Allowing the agent to push directly from inside the sandbox — with policy enforcement — would simplify the flow for these cases.

### Why git's own configuration is insufficient

Git provides configuration mechanisms that could theoretically enforce push policy: system/global `.gitconfig`, `core.hooksPath` for pre-push hooks, and `receive.denyNonFastForwards`. Even with Landlock protecting these config files as read-only, the agent can bypass them through multiple vectors:

- **`git -c key=value`** overrides any config file via command-line flag
- **`GIT_CONFIG_GLOBAL=/dev/null`** and **`GIT_CONFIG_SYSTEM=/dev/null`** redirect config to different files via environment variables the agent controls
- **`HOME=/tmp/fake`** causes git to look for `.gitconfig` in a different directory
- **`git push --no-verify`** skips pre-push hooks entirely
- **Local `.git/config`** in the workspace (writable) overrides system and global config

Git configuration is designed to be overridable by the user. Inside the sandbox, the agent is the user. A mandatory enforcement layer requires controlling the network path, not the git configuration.

### Three categories of git operation enforcement

Analysis of git operations reveals three distinct enforcement categories, which determine what mechanism can provide mandatory policy control:

**Category 1: Coarse network access.** Whether a binary can reach a host or endpoint at all. OpenShell L7 filtering enforces this directly via HTTP method + path + binary matching. Examples: allow/deny fetch entirely, allow/deny clone to specific hosts, block `git` from reaching GitHub while allowing `safe-push`.

**Category 2: Fine-grained push semantics.** Behavioral distinctions within the same network operation where the difference is in the request body or client-side flags, not the URL or HTTP method. All `git push` variants hit the same endpoint (`git-receive-pack`) via the same HTTP method (POST). L7 cannot distinguish them. This is where `safe-push` provides mandatory enforcement:

| Push variant | What differs | L7 can distinguish? |
|-------------|-------------|-------------------|
| Regular push | Refspec in body | No |
| Force push (`--force`, `--force-with-lease`) | Client flag, pack negotiation | No |
| Force push via refspec (`+refs/heads/main`) | `+` prefix in refspec, in body | No |
| Push to specific branch (main vs feature/*) | Branch name in refspec, in body | No |
| Remote branch deletion (`--delete`) | Zero-OID refspec in body | No |
| Tag push (`--tags`) | Tag refspecs in body | No |

**Category 3: Local operations.** Operations with no network component — `merge`, `rebase`, `reset`, `checkout`, `clean`, `branch -D`. No enforcement mechanism can make these mandatory because the agent can call `/usr/bin/git` directly (no network call for L7 to intercept), write scripts that perform equivalent operations, or directly manipulate `.git/` objects and refs on the writable filesystem. Policy for local operations belongs in prompt instructions and post-script/validation-loop checks, not in binary enforcement.

`safe-push` is scoped to **category 2 only** — fine-grained push policy enforcement. It does not attempt to gate local operations, and it does not duplicate L7's coarse network enforcement.

### Policy delivery and tamper resistance

The policy file that governs `safe-push` behavior must be tamper-proof — the agent must not be able to modify it. This constrains the delivery mechanism:

- **Writable paths are agent-controlled.** Any file delivered to a writable path (e.g., via `host_files` to `/tmp/workspace/`) can be modified by the agent before `safe-push` reads it.
- **Environment variables are agent-controlled.** An env var selecting a policy profile (e.g., `SAFE_PUSH_POLICY=strict`) can be overridden by the agent via `export`.
- **Landlock-protected read-only paths can only be populated at image build time.** OpenShell applies the Landlock policy at sandbox creation. The `host_files` mechanism copies files via SCP into the running sandbox *after* the policy is active — so files cannot be delivered to read-only paths. There is no pre-Landlock bootstrap phase in OpenShell.

Therefore, the only tamper-proof delivery path for the policy file is the container image itself: the file is placed on a Landlock-protected read-only path during `docker build`, before the sandbox ever starts.

## Decision

Introduce `safe-push`, a Go binary that acts as a mandatory policy gate for all `git push` operations from inside the sandbox. `safe-push` is a credential delivery tier 2 mechanism ([ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md)) that coexists with credential delivery tier 1 post-script push — the harness configuration determines which model an agent uses.

Credential delivery tier 2 is a scoped relaxation of the constraint established in the [security threat model](../problems/security-threat-model.md), which states that "agents cannot take forge actions directly — credentialed operations (push, label, comment) are applied by deterministic post-scripts outside the sandbox." Under credential delivery tier 2, agents *can* push directly, but only through `safe-push` with policy enforcement and only in environments where the risk profile justifies it (private repos, internal tooling). The threat model constraint remains the default — credential delivery tier 1 post-script push is the recommended path for public and high-risk repos.

### Binary design

`safe-push` is a Go binary baked into the sandbox container image at `/usr/local/bin/safe-push`, protected by Landlock as read-only and executable.

The agent invokes `safe-push` with the same arguments it would pass to `git push`:

```
safe-push origin feature/my-branch
safe-push --force origin feature/my-branch   # denied by default policy
```

`safe-push` performs three steps:

1. **Parse** the push arguments — remote, refspecs, flags (`--force`, `--force-with-lease`, `--delete`, `--tags`, `+` prefix in refspecs).
2. **Evaluate** against the policy file at `/etc/fullsend/safe-push-policy.yaml` (read-only, Landlock-protected).
3. **Allow:** Spawn a child process via `os/exec` that runs `/usr/bin/git push` with the original arguments, wait for it to exit, and propagate the exit code. Spawning a child rather than replacing the process image preserves the `safe-push` parent in the process tree, which is required for L7 process-tree tracking — OpenShell walks `/proc/pid/` ancestry to verify that the `git` process making the network call is descended from `safe-push`.
   **Deny:** Print a clear error to stderr describing what was blocked and why (e.g., `safe-push: denied: force-push is not allowed by policy`), write a structured JSON audit entry to `/tmp/workspace/.safe-push-audit.jsonl`, and exit non-zero.

### L7 enforcement

OpenShell L7 policy ensures that `safe-push` is the only path to push:

- **`safe-push` (`/usr/local/bin/safe-push`):** L7 process-tree tracking allows `git` processes descended from `safe-push` to reach `git-receive-pack` on `github.com`.
- **All other binaries:** L7 denies all processes that are NOT descended from `safe-push` from reaching `git-receive-pack`. This includes direct `git push`, custom scripts using `curl` to speak the git smart HTTP protocol, compiled binaries, and custom git remote helpers (`git-remote-<transport>` scripts the agent could write to `/tmp/`). The L7 deny rule must match the endpoint, not the binary — any process reaching `git-receive-pack` without `safe-push` ancestry is blocked.
- **SSH transport:** L7 policy must block outbound SSH (port 22 and `ssh.github.com:443`) to prevent the agent from bypassing HTTPS-based enforcement by configuring a remote with `git@github.com:...` syntax. The entire enforcement model assumes HTTPS transport — SSH is an opaque encrypted tunnel where L7 cannot inspect or gate operations.
- **Other git network operations** (`fetch`, `pull`, `clone`, `ls-remote`): Gated by L7 directly — allow or deny by binary + host + path as appropriate for the agent's credential delivery tier. `safe-push` is not involved.

### Credential separation

`safe-push` does not handle authentication. The credential flow is:

1. Agent calls `safe-push origin feature/my-branch`
2. `safe-push` checks policy — allowed
3. `safe-push` spawns a child process running `/usr/bin/git push origin feature/my-branch`
4. `git` makes HTTPS request to `github.com/repo.git/git-receive-pack`
5. OpenShell gateway intercepts, provider injects real token into auth header
6. Request reaches GitHub with valid credentials

Credentials never enter the sandbox. `safe-push` never sees them. OpenShell providers handle credential injection at the HTTP layer as described in [ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md).

### Default policy

One policy is baked into the container image for the initial implementation:

```yaml
# /etc/fullsend/safe-push-policy.yaml
version: 1

push:
  force: deny
  branches:
    deny: ["main", "master", "release/*"]
  delete_remote: deny
  tags: deny
```

This default reflects the safety checks already present in `post-code.sh` (branch validation, no push to main/master) and `post-fix.sh` (no force-push), enforced before the network call rather than after.

### Audit logging

Denied operations are logged to `/tmp/workspace/.safe-push-audit.jsonl`:

```json
{"timestamp":"2026-05-07T14:32:01Z","args":["--force","origin","main"],"decision":"denied","reason":"force-push not allowed","policy_version":1}
```

The audit log is on a writable path — the agent could tamper with it. It is extracted by the runner after sandbox teardown for observability. It is a best-effort record for operational visibility, not a security boundary.

### Coexistence with credential delivery tier 1 post-script push

`safe-push` (credential delivery tier 2) coexists with post-script push (credential delivery tier 1). The harness configuration determines which model an agent uses:

- **Credential delivery tier 1 (post-script push):** Preferred for public repos and high-risk environments. The post-script runs secret scanning (gitleaks), pre-commit hooks, and other content validation before code reaches the remote. Push happens outside the sandbox with the `PUSH_TOKEN`. The agent has no push capability inside the sandbox.
- **Credential delivery tier 2 (safe-push):** Suitable for private repos and low-risk environments where content validation overhead is unnecessary. The agent pushes directly from inside the sandbox via `safe-push`, with policy enforcement and credential injection via providers.

The credential delivery tier is implicit in the harness's image + policy combination. A credential delivery tier 1 harness uses an image without safe-push network access (or without safe-push entirely) and includes a `post_script` that handles push. A credential delivery tier 2 harness uses an image with safe-push and an L7 policy that routes push traffic through it.

`safe-push` covers push policy even for agents where credential delivery tier 1 post-script push is preferred, because the same image may be used in both high-risk (credential delivery tier 1) and low-risk (credential delivery tier 2) environments. The L7 policy — not the image — determines whether the agent can actually reach the remote.

### Per-agent policy customization (future)

The initial implementation ships one default policy baked into the image. When multiple distinct policy profiles are needed, two paths are available:

**Option A: Multiple named images.** Build policy variants into separate container images (e.g., `fullsend-code:strict`, `fullsend-code:permissive`). The harness `image` field selects which profile. Scales to a handful of profiles without new infrastructure. Cost: the image build matrix grows linearly with the number of profiles.

**Option B: Ephemeral image layering.** `fullsend run` pulls the base image, builds a single-layer ephemeral image on top with the per-agent policy file placed at the read-only path, loads it into containerd, and creates the sandbox from that image. Scales to arbitrary per-agent customization without a pre-built image per profile. Costs: per-invocation container build latency, containerd image lifecycle management (garbage collection of ephemeral images), and a runtime dependency on container build tooling (`ctr` or `docker` CLI). OpenShell's `--from` flag expects registry or containerd image references, not local filesystem paths — local images must be loaded via `ctr import`.

Both options preserve tamper resistance because the policy file ends up on a Landlock-protected read-only path regardless of how it got there. The choice between them depends on how many distinct policy profiles are needed and whether per-invocation build latency is acceptable.

A third option — a pre-Landlock file delivery phase in OpenShell that allows `host_files` to target read-only paths — would make per-agent policy customization trivial without image builds. This capability does not exist today and would require an OpenShell feature request.

Re-evaluate when the second distinct policy profile is needed.

### Harness integration

No changes to the harness YAML schema ([ADR 0024](0024-harness-definitions.md)) are required. The existing `image` and `policy` fields are sufficient:

```yaml
# harness/code.yaml (credential delivery tier 2 with safe-push)
description: Code agent with direct push capability for low-risk repos.
agent: agents/code.md
model: opus
image: ghcr.io/fullsend-ai/fullsend-code:latest  # includes safe-push + policy
policy: policies/code-write-credential-tier2.yaml   # includes L7 binary filtering for safe-push

pre_script: scripts/pre-code.sh
# No post_script push — agent pushes via safe-push inside sandbox

timeout_minutes: 120
```

## Consequences

- Agents in low-risk environments can push directly from inside the sandbox with mandatory policy enforcement, removing the post-script as the only push path.
- The post-script remains the preferred push mechanism for public and high-risk repos where content validation (secret scanning, pre-commit hooks) must run before code reaches the remote. `safe-push` does not replace these content checks.
- `safe-push` is scoped to push operations only. It provides mandatory enforcement for fine-grained push semantics (force-push, branch targeting, remote deletion, tag pushing) that L7 path + method matching cannot distinguish. It does not attempt to gate local git operations (merge, rebase, reset, checkout), which cannot be mandatorily enforced because the agent can call `git` directly for operations with no network component.
- L7 policy authoring gains a new pattern: binary process-tree matching to allow `git` processes descended from `safe-push` while blocking direct `git push`. This pattern must be documented and tested for each sandbox image that includes `safe-push`.
- The single baked-in policy is a simplification. When per-agent customization is needed, the options (multiple named images or ephemeral image layering) have been analyzed and documented, with the trade-offs understood. Runtime configuration (env vars, writable config files) is not viable because the agent controls all writable state inside the sandbox.
- The `safe-push` binary, its policy file, and the real `git` binary must all reside on Landlock-protected read-only paths. If any of these can be modified by the agent, the enforcement is bypassed.
- Audit logging for denied operations is best-effort (writable path, agent could tamper). The primary security boundary is the deny itself (the push never reaches the network), not the audit record.
- Credential separation is maintained: `safe-push` never sees or handles credentials. OpenShell providers inject credentials at the HTTP layer after `safe-push` has already approved the operation and spawned the real `git` process.
- Credential delivery tier 2 is a scoped relaxation of the security threat model's constraint that "agents cannot take forge actions directly." The threat model constraint remains the default for public and high-risk repos (credential delivery tier 1). Credential delivery tier 2 must be an explicit opt-in via harness configuration, not an automatic upgrade.
