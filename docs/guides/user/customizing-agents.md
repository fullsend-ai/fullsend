# Configuring Agent Behavior

This guide explains how to configure existing fullsend agents for your
organization and repositories. It covers harness-based configuration with
`base:` composition, environment variables, status notifications, and
disabling agents.

For a quick overview of all customization options, see
[Customizing Agents](customizing-overview.md). For the complete harness field
reference, see [Harness Field Reference](../../reference/harness-reference.md).
To create a new agent rather than configure an existing one, start with
[`fullsend agent new`](../../cli/agent.md#agent-new).

## What you can configure

You can customize any existing agent without building one from scratch.
Common configuration goals:

| Goal | How |
|------|-----|
| Change model, timeout, or image | Override scalar fields via `base:` composition |
| Add org-specific skills | Add entries to the `skills:` list |
| Add environment variables | Add entries under `env.runner` or `env.sandbox` |
| Install GitHub Packages npm dependencies | Add a repo-level GitHub Packages provider (see [Private registries and GitHub Packages](#private-registries-and-github-packages)) |
| Extend the sandbox with packages | Build a custom image and set `image:` |
| Add executables to the sandbox | Use `host_files` to copy scripts to `/sandbox/workspace/bin` |
| Disable a built-in agent | Set `enabled: false` in `config.yaml` |
| Control status comments | Configure `status_notifications` in `config.yaml` |
| Teach agents your conventions | Use [AGENTS.md](customizing-with-agents-md.md) (no harness change needed) |
| Give an agent domain knowledge | Use [skills](customizing-with-skills.md) (no harness change needed) |
| Limit sandbox token privilege | Set `privilege_levels.runtime: read` so write tokens stay on pre/post scripts |

## Configuration with `base:` composition

The primary mechanism for customizing agents. Register agents in
`config.yaml` with a `base:` URL pointing to the upstream harness, and
override only the fields that differ.

### Example: add a skill to the code agent

Create a thin harness that inherits from the upstream code agent and adds your skill:

**`harness/code.yaml`:**
```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

skills:
  - skills/my-custom-linting        # Merged with base skills (child overrides by skill identity)

timeout_minutes: 45                 # Override timeout (scalar: child wins)
```

**`skills/my-custom-linting/SKILL.md`:**
```markdown
---
name: my-custom-linting
description: Org-specific linting rules and conventions.
---

# My Custom Linting

[Your skill content...]
```

Register the agent in `config.yaml`:

```yaml
agents:
  - name: code
    source: harness/code.yaml
```

Because config-registered agents take precedence over built-in agents on name collision, your `code` agent replaces the default — with all of the base agent's scripts, policies, host_files, and plugins still inherited.

To re-pin the `base:` URL to a new upstream commit (and recompute the integrity hash), run `fullsend agent update code`. That writes the new SHA into the local harness file and leaves `config.yaml` unchanged.

Test it locally first (add `--forge github` or `--forge gitlab` only if this
repo's `.fullsend/config.yaml` does not already set `forge:`):
```bash
fullsend run code --target-repo ./my-repo --env-file .env.local
```

See [Running agents locally](running-agents-locally.md) for prerequisites and troubleshooting.

### Example: swap the model for review

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/review.yaml#sha256=abc...

model: sonnet
```

### Example: add org-specific environment variables

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

env:
  runner:
    JIRA_TOKEN: "${JIRA_TOKEN}"     # Merged with base env; child keys win
  sandbox:
    JIRA_PROJECT: "MYPROJ"
```

### Example: swap the validation schema

Setting a single `validation_loop` field inherits the rest from the base
(`script`, `max_iterations`, `feedback_mode`, `preflight_check`):

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

validation_loop:
  schema: schemas/my-custom-schema.json
```

Setting `validation_loop.preflight_check` swaps the dependency probe. It runs in a minimal, credential-safe environment, and an out-of-scope `${VAR}` reference fails the run before the probe executes. See the [harness reference](../../reference/harness-reference.md) for the full contract.

### What you can override

Any harness field can be overridden. See the [field merge rules](../../reference/harness-reference.md#field-merge-rules-for-base-and-overlays) for how each field type combines with the base:

- **Change model, timeout, image, scripts** — scalars replace the base value.
- **Add skills** — your entries are merged with the base's by skill identity (declared `SKILL.md` name when on disk, otherwise directory basename); a matching child skill overrides the base entry. Nested forge skills that share a leaf directory name are kept alongside each other. **Add plugins or host_files** — your entries are concatenated with the base's, base first.
- **Add or override env vars** — maps are merged; your keys win on collision.
- **Override validation_loop fields** — child non-zero values win; omitted fields inherit from the base (for example, set only `schema` to swap the schema without copying `script`). **Replace security config** — child replaces the entire block.

Base chains support up to 5 levels. Circular references are detected and rejected. Resolution order: base chain, child overrides, overlay resolution.

> **Note:** `allowed_remote_resources`, `allow_runtime_fetch`, and `max_runtime_fetches` are NOT inherited from base harnesses — the child must declare its own. This prevents a base harness from injecting arbitrary URL prefixes or enabling runtime fetching in the child.

> **Config-level fallback:** Separately from base-harness inheritance, the config-level `allowed_remote_resources` from the repository's `.fullsend/config.yaml` (layered over `config.base.yaml`) acts as a fallback for all URL resolution. URLs trusted at the config level are accepted even when the child harness omits the field. This is a distinct trust layer from base composition — the config-level list is set by the repository's administrators, not by base harness authors.

### Remote providers and profiles

Providers and openshell profiles can be referenced from remote URLs, enabling fully portable harnesses that bundle everything an agent needs.

**`providers`** accepts local provider names, local file paths, and HTTPS URLs with integrity hashes:

```yaml
providers:
  - vertex                       # Local name: loaded from providers/vertex.yaml
  - providers/custom.yaml        # Local path: resolved relative to harness
  - "https://github.com/org/repo/tree/main/providers/claude.yaml#sha256=abc..."  # Remote
```

**`openshell.profiles`** accepts local paths and HTTPS URLs:

```yaml
openshell:
  profiles:
    - profiles/claude-code.yaml    # Local path (resolved relative to harness)
    - "https://github.com/org/profiles/tree/main/claude-code.yaml#sha256=abc..."
```

When using `base:` composition, the base harness can declare its own providers and profiles. Child harnesses inherit and can extend them:

- **Profiles:** base + child lists are concatenated; deduplicated by profile `id` (child wins)
- **Providers:** base + child lists are concatenated; local names shadow URL-resolved names of the same `name`

Only profiles listed in `openshell.profiles` are imported. A YAML file that merely exists under the `profiles/` directory is **not** imported unless the harness names it. To use a local profile as a per-repo override, list it explicitly under its own `id` (e.g., `profiles/myorg-vertex-ai.yaml`); the child-wins dedup rule applies by `id`. The `fullsend-<name>` ids of the built-in profiles are reserved: list a copy under one and `fullsend run` warns and uses it for now, and a later release rejects it.

Remote URLs must include a `#sha256=...` integrity hash and match an `allowed_remote_resources` prefix in the same config. The integrity hash is checked on every resolution to ensure the content hasn't been tampered with since it was pinned.

### Private registries and GitHub Packages

Use this when a code or fix agent must install a **public** npm package on
GitHub Packages that **another organization** owns. The minted App token
cannot read it: GitHub scopes installation tokens to the packages of the org
the App is installed in, and `npm.pkg.github.com` answers 403 across orgs.
Same-org packages already work with `${GH_TOKEN}`, and the shipped code and
fix workflows grant `packages: read`. Nothing in this section ships by
default.

**What fullsend provides.** On GitHub Actions the runner keeps the job's own
workflow token as `GH_WORKFLOW_TOKEN`, a *provider-only* credential:

- Only provider credential expansion can read it. `${GH_WORKFLOW_TOKEN}` is
  refused in `runner_env`, `env.runner`, `env.sandbox`, `host_files` and
  `validation_loop.schema`, and pre/post/validation scripts never see it.
- `gh` and the post-script keep using the minted token (`GH_TOKEN`,
  `GITHUB_TOKEN`, `PUSH_TOKEN`).
- Outside GitHub Actions the variable is left alone; a local PAT is never
  copied into it. Set it yourself only for a local run against GitHub Packages.
- Any provider definition in `.fullsend` may reference it, the same way one
  can already reference `${GH_TOKEN}` or `${PUSH_TOKEN}`. `.fullsend` is read
  from the trusted ref, so a pull request cannot point a provider at it; if
  your repository ships other providers, review their `${}` uses with that in
  mind.

**Setup.** Add four files to your repository (or config repo) and overlay them
onto `code` and `fix`.

1. **`.fullsend/providers/github-packages.yaml`**

   ```yaml
   ---
   name: github-packages
   type: fullsend-github-packages
   credentials:
     GITHUB_TOKEN: "${GH_WORKFLOW_TOKEN}"
   ```

2. **`.fullsend/profiles/fullsend-github-packages.yaml`**

   ```yaml
   ---
   id: fullsend-github-packages
   display_name: Fullsend GitHub Packages
   description: GitHub Packages npm registry for cross-org public packages
   category: data
   credentials:
     - name: github_token
       description: Actions workflow token with packages:read
       env_vars: [GITHUB_TOKEN]
       required: true
       auth_style: bearer
       header_name: authorization
   endpoints:
     - host: npm.pkg.github.com
       port: 443
       protocol: rest
       access: read-only
       enforcement: enforce
       allow_encoded_slash: true
     - host: pkg-npm.githubusercontent.com
       port: 443
       protocol: rest
       access: read-only
       enforcement: enforce
   binaries:
     - "**/node"
     - "**/npm"
     - "**/pnpm"
     - "**/corepack"
     - "**/yarn"
   ```

3. **`.fullsend/env/npmrc-github-packages`**

   ```
   //npm.pkg.github.com/:_authToken=${GITHUB_TOKEN}
   ```

4. **`.fullsend/harness/code.yaml`** (and the same overlay for `fix.yaml`)

   ```yaml
   base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

   providers:
     - providers/github-packages.yaml
   openshell:
     profiles:
       - profiles/fullsend-github-packages.yaml
   host_files:
     - src: env/npmrc-github-packages
       dest: /sandbox/workspace/.npmrc
       expand: false
   env:
     sandbox:
       NPM_CONFIG_USERCONFIG: /sandbox/workspace/.npmrc
   ```

Register the overlay in `.fullsend/config.yaml` as usual (`name: code` /
`name: fix` with `source: harness/code.yaml` / `harness/fix.yaml`). Pin the
`base:` URL to a SHA as shown in [Configuration with `base:` composition](#configuration-with-base-composition).

**How the credential is scoped.**

- *Host-bound.* OpenShell binds every static credential in a profile to all
  of the profile's endpoints, so the token resolves at `npm.pkg.github.com`
  and at the tarball CDN `pkg-npm.githubusercontent.com` (which serves
  pre-signed URLs and ignores it). Anywhere else the proxy answers
  `credential_endpoint_mismatch`.
- *Separate from the forge credential.* OpenShell matches hosts label by
  label: a plain `github.com` endpoint matches only `github.com`, never
  `npm.pkg.github.com`, unless a profile writes an explicit wildcard label.
  The code and fix GitHub profile uses exact hosts, so `GH_TOKEN` never
  resolves at the registry and this profile's token never resolves at
  `api.github.com` or `github.com`. The two providers also use different
  sandbox keys (`GITHUB_TOKEN` here, `GH_TOKEN` for the forge), so both attach
  to one sandbox without a duplicate-key rejection.
- *Read-only.* Both registry hosts are `access: read-only`.
- *Readable by the agent.* This is the standard exposure of any static
  OpenShell credential
  ([ADR 0025](../../ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md)):
  a placeholder resolves wherever it appears in a request to a bound host,
  and the registry echoes unknown package names in its 404 body. A value read
  back that way is the job's own workflow token until the job ends, and
  through the proxy it can only reach the hosts the role's profiles allow; see
  [ADR 0114](../../ADRs/0114-github-packages-via-host-bound-workflow-token-provider.md).

**Notes.**

- `allow_encoded_slash: true` is required: scoped package names (`@org/pkg`)
  put a `%2F`-encoded slash in the metadata request, and without the field the
  proxy closes the connection (verified on OpenShell 0.0.116, whose policy
  schema documents the field for npm scoped packages).
- The npmrc lives at `/sandbox/workspace/.npmrc` with `NPM_CONFIG_USERCONFIG`
  pointing at it, because `/home` is outside the base filesystem policy's
  `read_write` set (`/sandbox`, `/tmp`, `/dev/null`). Keep `expand: false`:
  the file holds no secret, pnpm expands `${GITHUB_TOKEN}` when it reads the
  file as user config, and inside the sandbox that value is the placeholder.
- pnpm ≥ 10.34.2 no longer expands `${VAR}` in project `.npmrc` files
  ([GHSA-3qhv-2rgh-x77r](https://github.com/advisories/GHSA-3qhv-2rgh-x77r));
  the user-config route above is unaffected.

### Tuning agents with augmentation skills

Before you fork a whole agent or replace a built-in skill, decide what you
are actually changing:

| Goal | Prefer |
|------|--------|
| Domain rules, linting, or constraints that sit *alongside* defaults | A **unique-named** augmentation skill (append via harness `skills:`) |
| Shorter or reformatted human-facing output (comments, summaries) | Augmentation skill with **field ownership** and hard limits — not soft "be concise" |
| New review dimension under an orchestrator (for example `pr-review`) | A **sub-agent** file under that skill's `sub-agents/`, plus whatever registration the current platform requires |
| Replace most of a skill's procedure | Whole-skill override / derived harness — heavier; you stop inheriting upstream edits |

**What to think about when authoring:**

1. **Discover first** — read the target agent's harness, agent definition,
   schema, post-script, and any shipped skills under
   [fullsend-ai/agents](https://github.com/fullsend-ai/agents). Do not guess
   field names or roster lists from memory.
2. **Unique skill names** — a repo skill whose identity (its declared
   `SKILL.md` `name:`, or its directory name when none is declared) matches a
   built-in's is ignored and produces a warning (see
   [skill precedence](customizing-with-skills.md#skill-precedence)).
3. **Specificity wins** — vague augmentations lose to hard default
   instructions. Own exact fields; use word limits and templates.
4. **Sub-agents are not wrapper skills** — if you need a new review dimension,
   ship `sub-agents/<name>.md` (and parent dispatch updates when the
   orchestrator uses a fixed roster). Do not invent a parallel
   `*/SKILL.md` that embeds the same content.
5. **Prefer the lightest shipping path** the current docs support —
   upstream contribution, file-level skill override when available, or
   whole-skill fork only when that is still required.

> **Planned:** File-level overrides inside a pinned skill directory (add or
> replace a single `sub-agents/<name>.md` without vendoring the whole tree)
> are tracked in [#6158](https://github.com/fullsend-ai/fullsend/issues/6158)
> / [#6157](https://github.com/fullsend-ai/fullsend/issues/6157). Until that
> lands, fixed-roster sub-agent changes usually need an upstream PR or a
> whole-skill pin.

**Authoring help:** the contributor skill
[`author-fullsend-augmentations`](../../../skills/author-fullsend-augmentations/SKILL.md)
walks this discovery and conflict analysis. Use it when writing or reviewing
augmentation skills and sub-agents. Details also live in
[Configuring with skills](customizing-with-skills.md#authoring-skills-that-augment-defaults).

## Configuration examples

### Extending the sandbox image

When `host_files` injection is not enough and you need additional packages or
toolchains in the sandbox, build an image that extends the published base and
point your harness `image:` field at it:

```dockerfile
FROM ghcr.io/fullsend-ai/fullsend-sandbox:latest
RUN apt-get update && apt-get install -y --no-install-recommends rustc \
  && rm -rf /var/lib/apt/lists/*
```

Use `ghcr.io/fullsend-ai/fullsend-code:latest` as the parent instead when you
also need the Go toolchain. Pin the parent tag to a digest before CI use.

### Adding executables

On Claude and pi runs the sandbox already has `/sandbox/workspace/bin` on its
`PATH`, after the image's own directories. A file there adds a command; it
does not replace a command the image already provides, such as `git`,
`python3` or `node`. Codex and OpenCode runs restore the `PATH` from before
the workspace `.env` is read, so `/sandbox/workspace/bin` is not on the codex
or OpenCode agent's `PATH`; call the script by its absolute path there. To make a script
available as a command on Claude and pi runs, drop it there:

1. Create your script (e.g. `scripts/my-tool.sh`):

   ```bash
   #!/bin/bash
   echo "Hello from $0"
   ```

2. Make it executable with `chmod +x scripts/my-tool.sh`.
3. Map it into the sandbox via `host_files`:

   ```yaml
   host_files:
     - src: scripts/my-tool.sh
       dest: /sandbox/workspace/bin/my-tool.sh
   ```

4. The agent should be able to run `my-tool.sh` directly.

`host_files` cannot write to paths the runner manages. Loading the harness
fails with `dest "..." is reserved for the runner` when a `dest` is one of:

- `/sandbox/workspace/.env`
- `/sandbox/workspace/bin/` followed by `claude`, `codex`, `pi`, `opencode`,
  `fullsend` or `fullsend-check-output`
- anything under `/sandbox/claude-config`, `/sandbox/codex-config`,
  `/sandbox/pi-config`, `/sandbox/workspace/.fullsend`,
  `/sandbox/workspace/.security`, `/sandbox/.venv` or `/sandbox/go/bin`

`dest` must also be an absolute path under `/sandbox/` or `/tmp/` with no `..`
path component. Other
names in `/sandbox/workspace/bin` and files in `/sandbox/workspace/.env.d/`
work as shown here.

#### Modifying the PATH for external toolchains

When you need a directory outside `/sandbox/workspace/bin` on the `PATH`
(e.g. an external toolchain), use a `.env.d` file:

1. Create an env file (e.g. `env/extra-path.env`):

   ```bash
   PATH=/opt/my-toolchain/bin:$PATH
   ```

2. Map it into the sandbox:

   ```yaml
   host_files:
     - src: env/extra-path.env
       dest: /sandbox/workspace/.env.d/extra-path.env
   ```

3. At startup the sandbox reads every `*.env` file under
   `/sandbox/workspace/.env.d/`, picking up your PATH addition. Claude, codex,
   pi and OpenCode launches read these files as data, not shell: write plain
   `export NAME=value` lines, one assignment per line and without
   `${NAME:-default}` (see
   [`.env.d` host files](../../reference/harness-reference.md)).

On Claude and pi runs, after the `.env.d` files are read, the runner puts
its own `PATH` back in front, so your directory ends up after the runner's
entries: a command found only in `/opt/my-toolchain/bin` is picked up from
there, and a command the image also provides, such as `git` or `go`, is
picked up from the image. To replace one of those, extend the sandbox image
(see [Extending the sandbox image](#extending-the-sandbox-image)). Codex and
OpenCode runs restore the `PATH` from before `.env`, so `.env.d` additions are
not on the codex or OpenCode agent's `PATH`.

Launches and the runner's probes read `.env.d` files as data, and shell
constructs in them (command substitution, `;`-joined commands, `${NAME:-default}`
and other lines that are not plain assignments) are skipped with a warning.
`.env` itself is still valid shell that an agent may source, so keep treating
these files like `pre_script`. `host_files` can also place configuration that tools load, such
as git config or shell rc files; that is trusted like `pre_script` too.

#### Reserved `env.sandbox` names

**Note**: `env.sandbox` cannot modify runner infrastructure or security
settings through the reserved names below. The runner drops these names
before writing the sandbox environment, case-insensitively. The full
reserved set is published in the generated [sandbox environment
contract](../../normative/sandbox-env/v1/README.md):

- **Runner infrastructure and process-loading safeguards:** `PATH`, `HOME`,
  `SHELL`, `BASH_ENV`, `ENV`, `LD_PRELOAD`, `LD_LIBRARY_PATH`, `TRACEPARENT`,
  and every `FULLSEND_*` variable the runner itself exports into the sandbox
  (its output and fetch plumbing, the repository path, role and slug, the
  iteration budget and deadline, and its own `PATH` entry). The set grows
  with the runner; the warnings described below name any such key a harness
  sets.
- **Credentials it mints or receives:** `FULLSEND_GCP_OIDC_AUTH_FILE`,
  `FULLSEND_TRIGGER_TOKEN`, `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN`,
  `FULLSEND_WEBHOOK_SECRET`, `FULLSEND_INFERENCE_GATEWAY_API_KEY`,
  `FULLSEND_OPENAI_API_KEY`, `OPENAI_API_KEY`, `GH_WORKFLOW_TOKEN`, the
  runner-secret bundle and its staging path (`FULLSEND_GITHUB_RUNNER_SECRETS`,
  `FULLSEND_GITHUB_RUNNER_SECRETS_FILE`), and the GitHub Actions OpenID Connect
  (OIDC) request pair `ACTIONS_ID_TOKEN_REQUEST_URL` /
  `ACTIONS_ID_TOKEN_REQUEST_TOKEN`. The runner uses this pair to request a
  token proving the job's identity.
- **Non-secret, runner-only inputs that select those credentials:**
  `FULLSEND_GCP_OIDC_URL`, `FULLSEND_GCP_PROJECT_ID`,
  `FULLSEND_GCP_WIF_PROVIDER`, `FULLSEND_OPENAI_AUDIENCE`,
  `FULLSEND_OPENAI_IDENTITY_PROVIDER_ID`,
  `FULLSEND_OPENAI_SERVICE_ACCOUNT_ID`.
- **Security-sensitive names also refused in URL-sourced provider credential
  definitions:** proxy settings (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`,
  `ALL_PROXY`), Transport Layer Security (TLS) trust settings that select
  certificates for verifying encrypted connections (`SSL_CERT_FILE`,
  `SSL_CERT_DIR`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`), git configuration
  (`GIT_SSH_COMMAND`, `GIT_CONFIG_GLOBAL`, `GIT_EXEC_PATH`, `GIT_ASKPASS`,
  `GIT_TEMPLATE_DIR`), interpreter injection (`NODE_OPTIONS`, `PYTHONPATH`,
  `PYTHONSTARTUP`, `JAVA_TOOL_OPTIONS`, `RUBYOPT`, `PERL5OPT`),
  dynamic-linker hooks (`LD_AUDIT`, `DYLD_INSERT_LIBRARIES`), shell behavior
  (`IFS`, `CDPATH`, `PROMPT_COMMAND`) and the resolver alias file
  (`HOSTALIASES`).

The list is by name, not by namespace: harness-supplied hook inputs such as
`FULLSEND_CANARY_TOKEN` and `FULLSEND_TOOL_ALLOWLIST` are still delivered
through `env.sandbox` — see
[Runtime Implementation](../../contributing/runtime-implementation.md).
Matching is case-insensitive, so `https_proxy` is dropped exactly as
`HTTPS_PROXY` is.

This deny-list covers selected names, not every variable that can affect
network routing, certificate trust, Git or interpreter behavior. An unlisted
name is not necessarily safe.

`fullsend run` warns when the harness loads, naming all reserved keys it will
drop, and warns again on stderr for each key as it writes the sandbox
environment. To check the harness without starting an agent:

1. Run `fullsend lock <agent-name>`. It reports the same lint warning when it
   loads and composes the harness. Locking may fetch dependencies and write the
   lock file. If the entry is already current, it skips composition and does
   not check the keys. `lock` composes without event context, so a key added
   by an overlay guarded on `event` is reported only by `fullsend run`.
2. Run `fullsend lock <agent-name> --update` (or
   `fullsend lock --all --update`) to check the composed harness when the entry
   is current. This forces dependency re-resolution and may change the lock
   file; it is not a diagnostics-only operation.

No key-specific warning on a cache hit means the keys were not checked, not
that they are allowed.

A key must start with an ASCII letter or underscore, followed only by ASCII
letters, digits or underscores. `fullsend run` skips invalid names and warns
on stderr; this separate identifier check is not reported by `fullsend lock`.
Dropping a key removes that one `env.sandbox` export; it does not unset a value
that reaches the sandbox another way, as `host_files` `.env.d` does.

This gate covers `env.sandbox` only. `host_files` `.env.d` files are read
before the runtime starts: every launch and the runner's in-sandbox probes
read them as data and take only assignment lines, and never take the
dynamic-loader, interpreter and shell-startup variables that the
[`.env.d` host files](../../reference/harness-reference.md#field-details)
entry in the harness reference lists. `.env` itself is still valid shell that
an agent may source, so keep treating these files like `pre_script`. Either
way, `.env.d` is not a general migration path for blocked proxy or
certificate settings; the harness reference entry has the per-runtime
rules.

#### Migrating proxy and certificate settings

Coordinate these changes with the operator of your OpenShell gateway:

- **Corporate proxies:** use the operator-managed outbound-network settings
  for the OpenShell version and compute driver you deploy; fullsend records
  the version it pins in
  [`.github/scripts/openshell-version.sh`](https://github.com/fullsend-ai/fullsend/blob/main/.github/scripts/openshell-version.sh)
  at your release tag.
  The [OpenShell gateway configuration reference](https://github.com/NVIDIA/OpenShell/blob/v0.1.4/docs/how-it-works/gateways/configuration.mdx)
  for that version describes proxy chaining for policy-approved TLS
  connections using the HTTP CONNECT method to establish a tunnel. Plain-HTTP
  destination traffic remains direct. These are not fullsend harness fields or
  a general replacement for every `HTTP_PROXY` or `ALL_PROXY` use. Do not
  reintroduce application proxy exports through `.env.d` as a workaround.
  **GitHub Action limitation:** fullsend's own GitHub Action writes
  `gateway.toml` with a fixed template and has no input for upstream proxy or
  CA settings. Self-hosted Actions runners behind a corporate proxy that relied
  on `HTTPS_PROXY` in `env.sandbox` have no configuration step available yet.
  This is tracked as a follow-up for an Action proxy input
  ([#8505](https://github.com/fullsend-ai/fullsend/issues/8505)).
- **Additional certificate authorities (CAs):** preserve the active sandbox's
  OpenShell CA certificate and other required roots in application trust.
  OpenShell's supervisor also verifies upstream TLS separately. Follow the
  selected driver's certificate-provisioning instructions in that reference,
  which documents `proxy_ca_bundle` for a corporate proxy. It requires
  `https_proxy`, and on Kubernetes it is a path in the gateway Pod mounted
  from a ConfigMap, not a host path. Do not assume that replacing a
  generated bundle with a custom-only file, or changing only the agent
  image's trust store, configures both trust paths. Have the gateway operator
  validate the required application and supervisor connections before
  deployment. The linked tag is what the pinned gateway supports; a setting
  documented only at a later OpenShell tag is not available until fullsend
  bumps the pin. If the linked tag does not document `additional_ca_bundle`
  (added in OpenShell 0.1.5, for Podman gateways), a private CA used without
  a corporate proxy has no gateway-level replacement for `SSL_CERT_FILE` or
  `NODE_EXTRA_CA_CERTS` at that version.
- **Git settings:** place git configuration with `host_files` (a git config
  file or shell rc file the tools load) or use the tool's own configuration,
  instead of `GIT_SSH_COMMAND`, `GIT_CONFIG_GLOBAL`, `GIT_ASKPASS` and the
  other blocked `GIT_*` names. `host_files` content is trusted like
  `pre_script`. Git and TLS equivalents the list does not yet cover, such as
  `GIT_SSH`, `GIT_CONFIG_*` and `REQUESTS_CA_BUNDLE`, are tracked in
  [#8503](https://github.com/fullsend-ai/fullsend/issues/8503).
- **Interpreter, loader and shell names:** `NODE_OPTIONS`, `PYTHONPATH`,
  `JAVA_TOOL_OPTIONS`, `LD_AUDIT`, `IFS` and the other blocked names of this
  class have no supported replacement route into the sandbox. They are
  blocked because they change how code is loaded or run. Which of them
  `host_files` `.env.d` still delivers differs by runtime; the
  [`.env.d` host files](../../reference/harness-reference.md#field-details)
  entry lists what each runtime drops or unsets. Whatever it delivers, it
  does so only because that file is trusted like `pre_script`, so it is not
  a sanctioned way to restore them.

### Adding a skill

Create `.fullsend/skills/my-skill/SKILL.md` in the target repository, or `skills/my-skill/SKILL.md` in a shared agents repo:

```markdown
# My Custom Skill

Custom domain knowledge for this organization.

## Examples

...
```

Reference the skill in your harness's `skills:` list. The skill is available to all agents that include it in their harness configuration. See [Configuring with Skills](customizing-with-skills.md) for details on skill authoring and precedence.

## Agent roles

On the hosted mint, agents run as one of a **fixed** set of built-in roles.
Each role is a GitHub App identity with a fixed permission ceiling. An agent's
name is separate from its role — the `code` and `fix` agents both run as the
`coder` role. To pick a role for a custom agent, or to use your own identity or
a custom role, see [Custom Agent Identity](custom-agent-identity.md).

| Role | GitHub App | Purpose |
|------|------------|---------|
| `fullsend` | `{org}-fullsend[bot]` | Dispatch/control |
| `triage` | `{org}-triage[bot]` | Issue triage |
| `coder` | `{org}-coder[bot]` | Code generation |
| `review` | `{org}-review[bot]` | PR review |
| `fix` | (reuses coder app) | Fix failures |
| `retro` | `{org}-retro[bot]` | Retrospectives |
| `prioritize` | `{org}-prioritize[bot]` | Backlog priority |

**Naming conventions:**
- App naming: `{org}-{role}`
- Bot naming: `{org}-{role}[bot]`
- PEM storage: GCP Secret Manager or filesystem (standalone)
- Secret name: `fullsend-{role}-app-pem`

> **Note:** The "fix" role reuses the "coder" app and PEM — no separate GitHub App or secret is created for it.
>
> **Note:** The default deployment uses a shared vendor App (`fullsend-ai-review[bot]`). Code that gates on a review bot's identity must match the org-specific and shared vendor forms, plus `${FULLSEND_APP_SET}-review[bot]` when a custom app set is configured — see [Bot Identities](../../contributing/bot-identities.md) for details.

> **Note:** Mint-only dogfood roles such as `scribe` can be registered with
> `fullsend mint add-role` (and used via remote harness registration) but are
> **not** valid values for the `.fullsend` `roles:` field until scaffold /
> workflow wiring lands. Adding them under `roles:` fails config validation
> rather than silently no-oping.

## Status notifications

Agent workflows post status comments on issues and PRs when they start and complete. Control this with `status_notifications` in `.fullsend/config.yaml`:

```yaml
status_notifications:
  comment:
    start: enabled
    completion: enabled
```

When `status_notifications` is omitted entirely, both start and completion comments default to enabled.

### Completion modes

| Value | Behavior |
|-------|----------|
| `enabled` | Always post a completion comment (default) |
| `on_failure` | Post when the agent fails, is cancelled, or is skipped by a pre-script; the start comment is automatically suppressed to avoid notification noise |
| `disabled` | Never post a completion comment; silently remove the start comment |

`on_failure` is useful when you want to reduce notification noise — successful runs leave no trace, but failures still surface. When `completion` is set to `on_failure`, the start comment is automatically suppressed regardless of the `start` setting, because posting and then deleting a start comment would still trigger a GitHub notification pointing to a deleted comment.

In `enabled` mode (the default), a hard crash or cancellation that happens before the agent could post anything at all is also surfaced after the fact: a post-job cleanup step synthesizes an "Interrupted" comment so the run doesn't silently vanish.

### Reactions

As an alternative (or supplement) to comments, agents can signal status with emoji reactions. Reactions don't generate a GitHub notification, so they're a lower-noise way to show that an agent is working on something and how it turned out.

```yaml
status_notifications:
  reaction:
    start: enabled       # "enabled" | "disabled" (default)
    completion: enabled  # "enabled" | "on_failure" | "disabled" (default)
```

Unlike comments, reactions default to `disabled` — they're an opt-in addition, not a default-on behavior. When `start` is enabled, a 👀 reaction is added when the agent begins.

At completion, the start reaction (if any) is removed, and — depending on `completion` — replaced with an outcome reaction:

| Value | Behavior |
|-------|----------|
| `enabled` | Always add a completion reaction: 👍 on success, 😕 on failure/cancelled/skipped |
| `on_failure` | Add a 😕 reaction only on failure/cancelled/skipped; leave no reaction on success |
| `disabled` | Never add a completion reaction (default) |

👎 is deliberately avoided for failures — it overloads GitHub's native up/down-vote convention, so a routine agent failure could be misread as the bot disliking the issue.

Because reactions carry no notification cost, `on_failure` here simply means "leave no reaction on success," with no start-suppression workaround needed.

**Where the reaction lands:** for runs triggered by an issue or PR event, the reaction is added to the issue/PR itself. For runs triggered by a slash command comment (e.g. `/fs-fix`), the reaction is added to the triggering comment instead, so it's clear which command the reaction is responding to.

**Known limitations:**

- Reactions are currently GitHub-only. Enabling `reaction.*` on a GitLab-backed repo is silently a no-op today ([#5998](https://github.com/fullsend-ai/fullsend/issues/5998)).
- If a run is hard-killed before it can post its completion reaction, the start reaction (👀) can be left behind indefinitely — unlike status comments, there's no out-of-process reconciler for orphaned reactions yet.

## Disabling agents

To disable an agent (including built-in scaffold agents) without removing
its role, add an entry with `enabled: false` in your config:

```yaml
agents:
  - name: retro
    enabled: false
```

This prevents the agent from dispatching and from resolving via
`fullsend run`. The role can stay in `roles` — only the agent
is suppressed. Omitting `enabled` (or setting it to `true`) keeps the
agent active (backward compatible).

When multiple entries share a name, the last writer wins. This allows a
disable-then-enable pattern to replace a default agent with a custom one:

```yaml
agents:
  - name: retro
    enabled: false
  - name: retro
    source: harness/custom-retro.yaml
    enabled: true
```

**Important:** The `name` must match the **agent/harness name**, not the
role name. The built-in agent names are: `code`, `triage`, `review`,
`fix`, `retro`, `prioritize`. Note that the role `coder` maps to the
agent named `code` — writing `name: coder` passes validation but
disables nothing because no agent has that harness name.

## Choosing when review runs

By default, the dispatch workflow decides when the review agent runs: when a
pull request is opened, pushed to or marked ready, when the
`ready-for-review` label is added, and on a `/fs-review` comment. To decide it
yourself, give the `review` entry in `.fullsend/config.yaml` a `trigger`: a
[CEL expression](cel-triggers-reference.md) over the event. The review still
runs in the same `Review` job; the trigger only decides whether that job
starts.

### Example: review only on `/fs-review`

To stop the automatic review on every pull request but keep `/fs-review`, add
this entry to `.fullsend/config.yaml` on your default branch:

```yaml
agents:
  - name: review
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.entity.linked_change_proposal)
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-review"
      && event.actor.kind == "human"
```

`has(event.entity.linked_change_proposal)` keeps the trigger to comments on
pull requests. The `Route` job evaluates the review trigger for the events
the fullsend workflow receives: a pull request opened, pushed to
(`synchronize`), marked ready for review, labeled or unlabeled, and a `/fs-*`
comment on a pull request from a user who is not a bot. It does so only when
the default rules chose review or no stage, and not for a closed pull request
or a `/fs-retro`, `/fs-triage` or `/fs-prioritize` comment (see
[Which trigger decides](#which-trigger-decides)), so a broad trigger can start
reviews the default rules would not. It
never starts a review from a bot's comment.

The trigger is read from your default branch, so a pull request cannot change
its own routing. After the change merges, open a pull request: no review
starts. Comment `/fs-review` on it: one review starts.

### What opts a repository in

Only a `trigger` on the `review` entry in `config.yaml` moves review routing
to CEL:

| Your configuration | Who decides |
|---|---|
| No `review` entry, or one with only `runtime`, `model`, `effort` or `subagents` | The dispatch workflow, as before |
| `trigger` on the `review` entry | The trigger |
| A `review` entry with `source:` whose harness sets or inherits `trigger:` | The dispatch workflow, as before |

A harness `trigger:` field never changes when a built-in agent runs, whether
your own harness writes it or inherits it through `base:`. To route review by
a trigger you keep in a harness, copy the expression into the `review` entry's
`trigger` in `config.yaml`. The kill switch and `enabled: false` on the review
entry keep working as before. The `Route`
job evaluates the trigger with the fullsend CLI built at the dispatch
workflow's commit; if your workflow call pins `fullsend_version`, the `Review`
job runs that version instead.

### Known differences from the default rules

A trigger is checked by the same permission gate as other CEL-dispatched
agents: the review harness's `role: review` needs at least triage access, so
triage-role users can start a review as before. Some cases still differ from
the dispatch workflow's built-in rules:

| Case | Default rules | Trigger |
|---|---|---|
| Pull request opened or pushed by a bot without a registered fullsend role | Reviewed | Not reviewed |
| Pull request pushed by the fix agent | Reviewed | Not reviewed until [#8329](https://github.com/fullsend-ai/fullsend/issues/8329) ships, so the review → fix → re-review loop stops |
| Who is checked when a pull request is opened, pushed to or marked ready | The pull request author | The user who sent the event |
| A maintainer's push to an outside contributor's fork pull request | Not reviewed | Reviewed |

> [!WARNING]
> Opt in only after
> [#8329](https://github.com/fullsend-ai/fullsend/issues/8329) ships, or
> accept that pushes from the fix agent no longer start a re-review. Until
> then the trigger path does not look up a bot's role.

### Checking the decision

When a trigger is set, the `Route` job's "Finalize stage" step logs the
decision for each pull request and pull request comment. To catch an invalid
trigger before it merges, see
[Checking a trigger before you merge](#checking-a-trigger-before-you-merge).

```text
CEL route (review): bash=review cel=skip reason=trigger did not match harness=agents@3c3081741e3c
```

The job summary shows `review_route=cel` when the trigger decided, or
`review_route=bash` when the dispatch workflow did: the kill switch is on or
the review entry is disabled.

| Log text | Meaning | Action |
|---|---|---|
| `cel=skip reason=trigger did not match` | The trigger left this event out | Expected for events your trigger excludes; otherwise fix the trigger |
| `cel=skip reason=actor not authorized` | The actor failed the dispatch permission check | See [Known differences from the default rules](#known-differences-from-the-default-rules) for bots |
| `cel=skip reason=harness unavailable; actor below write` | The review harness could not be fetched or loaded, so only write access passes | Check the route step's warning for the harness error |
| `cel=skip reason=trigger evaluation failed: …` | The trigger compiles but failed on this event, for example it reads a field the event lacks without `has()`; this counts as no match | Guard optional fields with `has()` |
| `CEL route failed; keeping the bash stage` | A CEL route step failed or the configuration is invalid, so the dispatch workflow decided | Open the failed "… CEL route" step for the error, for example an invalid `trigger` or another invalid `agents:` entry |
| `invalid agent config: agents[N] (review): invalid trigger` in any agent job | The `trigger` in `config.yaml` does not compile; every agent run validates the whole `agents:` list, so all agents fail, not only review | Fix the expression in `.fullsend/config.yaml`; see [Checking a trigger before you merge](#checking-a-trigger-before-you-merge) |

## Choosing when retro runs

By default, the dispatch workflow starts the retro agent when a pull request
is closed, merged or not, and on a `/fs-retro` comment on an issue or a pull
request. To decide it yourself, give the `retro` entry in
`.fullsend/config.yaml` a `trigger`. The retro still runs in the same `Retro`
job; the trigger only decides whether that job starts. What opts a repository
in, and how to check the decision, work as for review: see
[What opts a repository in](#what-opts-a-repository-in) and
[Checking the decision](#checking-the-decision), with `retro` in place of
`review` (`CEL route (retro): ...`, `retro_route=cel`). To check a retro
trigger before you merge, see
[Checking a trigger before you merge](#checking-a-trigger-before-you-merge).

### Example: retro only on `/fs-retro`

To stop the retro on every closed pull request but keep `/fs-retro`, add this
entry to `.fullsend/config.yaml` on your default branch:

```yaml
agents:
  - name: retro
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-retro"
      && event.actor.kind == "human"
```

After the change merges, close a pull request: no retro starts. Comment
`/fs-retro` on it: one retro starts.

The `Route` job asks the trigger only about closed pull requests and
`/fs-retro` comments; every other event keeps the dispatch workflow's rules,
and a bot's `/fs-retro` comment never starts a retro.

### Who can start a retro

A trigger decides which events start a retro, not who may start one:

- **Closing a pull request:** anyone who closes or merges it, including its
  read-only author and bots without a registered fullsend role, as with the
  dispatch workflow. Only the retro agent gets this exception (the
  [lifecycle-close exception](../../normative/authorization/v1/README.md#lifecycle-close-pull_request_targetclosed)).
- **`/fs-retro`:** users with write access or above.

## Choosing when triage and prioritize run

The triage and prioritize agents opt in the same way as review: give the
`triage` or `prioritize` entry in `.fullsend/config.yaml` a `trigger`, and
that trigger, not the dispatch workflow's built-in rules, decides whether the
`Triage` or `Prioritize` job starts. The rules in
[What opts a repository in](#what-opts-a-repository-in) apply to both, with
`triage` or `prioritize` in place of `review`. Each agent opts in on its own: a
`triage` trigger does not change when prioritize runs. To check either trigger
before you merge, see
[Checking a trigger before you merge](#checking-a-trigger-before-you-merge).

A trigger only sees the events that reach the `Route` job. The workflow that
`fullsend` installs in your repository forwards issues opened, edited and
labeled, and comments that start with `/fs-` from people, not bots. So a
prioritize trigger only decides `/fs-prioritize`, and on a pull request only
`/fs-triage` asks the triage trigger.

By default the dispatch workflow starts:

- **Triage** when an issue is opened or edited, when the `ready-for-triage`
  label is added to an issue, and on a `/fs-triage` comment on an issue or a
  pull request.
- **Prioritize** on a `/fs-prioritize` comment on an issue or a pull request.
  The scheduled scoring run (a scheduler workflow that dispatches
  `prioritize.yml`) does not go through this routing, so a trigger does not
  change it.

### Example: triage only on `/fs-triage`

To stop triaging every new or edited issue but keep `/fs-triage`, add this
entry to `.fullsend/config.yaml` on your default branch:

```yaml
agents:
  - name: triage
    trigger: >-
      event.entity.kind == "work_item"
      && event.transition.kind == "comment_added"
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-triage"
      && event.actor.kind == "human"
```

After the change merges, open an issue: no triage starts. Comment
`/fs-triage` on it: one triage starts. The `Route` job summary shows
`triage_route=cel`.

Leave out the `ready-for-triage` label at your own cost: when the triage
agent splits an issue, it labels each new sub-issue `ready-for-triage` so that
the sub-issue is triaged. Keep a label arm like this one if you rely on
splits:

```text
(event.entity.kind == "work_item"
  && event.transition.kind == "label_changed"
  && event.transition.label.name == "ready-for-triage"
  && event.transition.label.action == "added")
```

### Which trigger decides

The `Route` job asks one agent's trigger per event:

| Event | Agent asked |
|---|---|
| An issue opened, edited or labeled | `triage` |
| A pull request closed, merged or not | `retro` |
| A pull request opened, pushed to, marked ready or labeled | `review` |
| A `/fs-` comment that the built-in rules route to `review`, `retro`, `triage` or `prioritize` | That agent |
| Any other `/fs-retro`, `/fs-triage` or `/fs-prioritize` comment | `retro`, `triage` or `prioritize` |
| Any other `/fs-` comment on a pull request | `review` |
| Any other `/fs-` comment on an issue | `triage` |

An event the built-in rules route to another stage, such as `/fs-code` or the
`ready-to-code` label, is never handed to a trigger.

### Copying the built-in triage rules into a trigger

To start from the built-in rules and then change them, write them as a
trigger. Keep the `event.actor.kind == "human"` guard on opened and edited:
it stops the triage agent's own sub-issues and edits from being triaged
twice once bots are recognized
([#8329](https://github.com/fullsend-ai/fullsend/issues/8329)).

```yaml
agents:
  - name: triage
    trigger: >-
      (event.entity.kind == "work_item"
        && event.transition.kind in ["opened", "edited"]
        && event.actor.kind == "human")
      || (event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-triage"
        && event.transition.label.action == "added")
      || (event.entity.kind in ["work_item", "change_proposal"]
        && event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-triage"
        && event.actor.kind == "human")
```

It differs from the built-in rules in one case:

| Case | Built-in rules | This trigger |
|---|---|---|
| An issue opened or edited by a bot, for example a sub-issue the triage agent creates | Triaged when the bot holds a collaborator role | Not triaged; the sub-issue is triaged through its `ready-for-triage` label |

### Checking a trigger before you merge

A `trigger` on the `review`, `retro`, `triage` or `prioritize` entry that does
not compile, or does not return a boolean, still merges. After the merge,
every agent run in the repository fails with `invalid agent config:
agents[N] (<name>): invalid trigger`. To catch this before you merge, run
[`fullsend lint`](../../cli/lint.md) from the repository root:

```bash
fullsend lint
```

If the configuration directory is not `.fullsend` in the current directory,
pass its path:

```bash
fullsend lint --fullsend-dir <path>
```

`fullsend lint` loads `.fullsend/config.yaml`, layered over
`.fullsend/config.base.yaml` if you have one, with the same validation agent
runs use. It exits 1 when it finds an error.

`fullsend lint` caches the agents it fetches in `.fullsend/.fullsend-cache/`.
Add `.fullsend/.fullsend-cache/` to your `.gitignore`, or delete the directory
before you commit.

#### A valid trigger

This `.fullsend/config.yaml` holds only the `review` entry from
[Example: review only on `/fs-review`](#example-review-only-on-fs-review):

```yaml
version: "1"
agents:
  - name: review
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.entity.linked_change_proposal)
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-review"
      && event.actor.kind == "human"
```

```text
$ fullsend lint
  • Fetching agent review from fullsend-ai/agents@5144c1d561a0
  ✓ agent review resolved from fullsend-ai/agents@main
  ! review: warning: env.sandbox: env.sandbox coexists with host_files entry /home/user/repo/.fullsend/.fullsend-cache/resources/sha256/e2874d7d47a7a710f89ea6dcb99ae3b8c720ac06fccb7499dfddbe14f12b5ecb/content.env (dest: /sandbox/workspace/.env.d/gcp-vertex.env); env.sandbox values take precedence on key collision
  ! review: warning: forge: forge is deprecated; use overlays with CEL when expressions instead (see ADR 0088)
$ echo $?
0
```

There is no `error:` line, so the trigger is valid. The entry has no `source:`,
so `fullsend lint` fetches the `review` agent from the agents repository and
lints it too. The two `warning:` lines come from that agent, not from your
`config.yaml` (see [Reading the output](#reading-the-output)).

#### A syntax error

This `review` trigger ends with a stray `&&`:

```yaml
version: "1"
agents:
  - name: review
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.entity.linked_change_proposal)
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-review"
      && event.actor.kind == "human" &&
```

```text
$ fullsend lint
  ✗ config: error: config.yaml: agents[0] (review): invalid trigger: ERROR: <input>:1:212: Syntax error: mismatched input '<EOF>' expecting {'[', '{', '(', '.', '-', '!', 'true', 'false', 'null', NUM_FLOAT, NUM_INT, NUM_UINT, STRING, BYTES, IDENTIFIER}
 | event.transition.kind == "comment_added" && has(event.entity.linked_change_proposal) && has(event.transition.comment.command) && event.transition.comment.command == "/fs-review" && event.actor.kind == "human" &&
 | ...................................................................................................................................................................................................................^
Error: lint found 1 error-level diagnostic(s)
$ echo $?
1
```

`agents[0] (review)` names the entry. `<input>:1:212` is line 1, column 212
of the trigger as YAML reads it: the `>-` block joins the trigger into one
line. The caret marks where parsing failed, here the end of the expression.

#### A trigger that does not return a boolean

This `review` trigger is a string, not a condition:

```yaml
version: "1"
agents:
  - name: review
    trigger: '"opened"'
```

```text
$ fullsend lint
  ✗ config: error: config.yaml: agents[0] (review): invalid trigger: trigger expression must evaluate to bool, got string
Error: lint found 1 error-level diagnostic(s)
$ echo $?
1
```

#### Reading the output

| Output | Meaning | Action |
|---|---|---|
| `invalid trigger: ERROR: <input>:L:N: Syntax error: …` | The trigger does not compile; the caret marks line `L`, column `N` of the trigger as YAML reads it | Fix the expression at the caret in `.fullsend/config.yaml` |
| `invalid trigger: trigger expression must evaluate to bool, got string` | The trigger compiles but returns a string, not `true` or `false` | Write a condition, for example `event.transition.kind == "opened"` |
| `! Git token not available; private repo fetches may fail` | No `GH_TOKEN`, `GITHUB_TOKEN` or `gh` login was found | Nothing to fix for the public agents repository; set `GH_TOKEN` to remove the line |
| A `warning:` line prefixed with an agent name, for example `review: warning: forge: forge is deprecated` | The warning comes from the upstream harness that an entry without `source:` resolves from the agents repository | Nothing to fix in this repository; warnings do not fail the run unless you pass `--strict` |

`fullsend lint` does not evaluate triggers against events. A trigger that
compiles but reads a field the event lacks without `has()` evaluates to an
error at run time, which counts as no match (`cel=skip`); see the
`Guard optional fields with has()` row in
[Checking the decision](#checking-the-decision).

#### Running the check in CI

This GitHub Actions workflow runs `fullsend lint` on every pull request that
changes `.fullsend/config.yaml` or `.fullsend/config.base.yaml`. It installs
the CLI with the same action and `agent: __install_only__` input as
[Building custom agents](building-custom-agents.md):

```yaml
name: Lint fullsend config

on:
  pull_request:
    paths: ['.fullsend/config.yaml', '.fullsend/config.base.yaml']

permissions:
  contents: read

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7

      - name: Install fullsend CLI
        uses: fullsend-ai/fullsend@vX.Y.Z
        with:
          agent: __install_only__
          version: vX.Y.Z

      - name: Lint fullsend config
        env:
          GH_TOKEN: ${{ github.token }}
        run: fullsend lint --fullsend-dir .fullsend
```

`fullsend lint` first ships in the release after v0.50.0. Replace both
`vX.Y.Z` values with that release or a later one. The `version` input picks the CLI release
the step installs, and it defaults to `latest`. If the repository vendors the
CLI at `.fullsend/bin/fullsend`, the step uses that binary instead, so it
must also be that release or later.

An entry without `source:` makes `fullsend lint` fetch the agent from the
agents repository, so the job needs network access; `GH_TOKEN` authenticates
that request, and `--offline` cannot
resolve that entry
([#8743](https://github.com/fullsend-ai/fullsend/issues/8743)).

## Agent dispatch authorization

By default, fullsend checks the forge's permission API to decide who can
trigger agents. Users with `write` or above can trigger all agents; users
with `triage` can trigger the built-in observation agents (`/fs-triage`,
`/fs-review`). This applies to **GitHub webhook dispatch** (slash commands,
label-triggered dispatch, and event-triggered dispatch).

On the CEL dispatch path (GitHub `fullsend dispatch` and the Jira poll), the
threshold follows the harness `role:`:

- `role: triage` or `role: review` — triage-level actors can trigger it,
  including OWNERS reviewers on GitHub when
  [OWNERS authorization](owners-file-authorization.md) is enabled.
- Any other `role:` — the actor needs `write`.

GitLab uses its own stage router, which applies the same triage and write
split to slash commands. The GitLab agent job also requires the event actor
to hold Developer access or higher for every stage except retro and
prioritize, so automatic triage of a newly opened issue runs only when its
author is a Developer, Maintainer, or Owner.
See the
[Authorization Contract](../../normative/authorization/v1/README.md) for the
full role hierarchy, exception rules, and implementation notes.

To extend trigger access beyond forge collaborators using Prow-style
OWNERS files, see the
[OWNERS file authorization guide](owners-file-authorization.md).

## See also

- [OWNERS file authorization](owners-file-authorization.md) — using Prow-style OWNERS files to authorize agent dispatch
- [Customizing Agents](customizing-overview.md) — overview of all customization approaches
- [Harness Field Reference](../../reference/harness-reference.md) — complete harness YAML field reference, merge rules, and resource referencing
- [Bring Your Own Agent](bring-your-own-agent.md) — building and registering custom agents from scratch
- [Configuring with AGENTS.md](customizing-with-agents-md.md) — repo-level instructions for all agents
- [Configuring with Skills](customizing-with-skills.md) — extending agents with skills
- [Default, derived, and custom agents](default-vs-custom.md) — when does configuration cross into derived or custom agent territory?
- [Escalation ladder](escalation-ladder.md) — prove-it path before deriving or replacing a core agent
- [Authorization Contract](../../normative/authorization/v1/README.md) — role hierarchy, thresholds, and exceptions
- [Getting Started](../getting-started/) — initial setup
- [Bugfix Workflow](bugfix-workflow.md) — how agents work together
- [Standalone Mint](../infrastructure/standalone-mint.md) — running your own mint with custom agent roles
