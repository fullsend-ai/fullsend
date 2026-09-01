# Harness Field Reference

Complete reference for all fields available in a fullsend harness YAML file. For a guide-oriented introduction to harnesses, see [Bring Your Own Agent](../guides/user/bring-your-own-agent.md).

```yaml
# ── Required ──────────────────────────────────────────────────
agent: agents/my-agent.md           # Path to agent definition
role: triage                        # A role the mint serves (built-in on the hosted mint); not the agent's name. Format: lowercase letter first, then a-z, 0-9, _, -; no double hyphens

# ── Identity & metadata ──────────────────────────────────────
slug: my-org-my-role                # Install-time App discovery (convention: <org>-<role>); not read by the mint
description: One-line summary       # Human-readable description
doc: docs/agents/my-agent.md        # Source-repo-only; not resolved at runtime
trigger: "event.entity.kind == 'work_item'"  # Optional CEL expression over NormalizedEvent (see CEL Triggers Reference)

# ── Composition ───────────────────────────────────────────────
base: harness/common-base.yaml      # Inherit from another harness (local or URL)

# ── Sandbox ───────────────────────────────────────────────────
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
policy: policies/base.yaml          # Sandbox policy (filesystem, landlock, process)
model: opus                         # LLM model override
effort: high                        # Reasoning effort (low, medium, high, xhigh, max); claude runtime only
readonly_repo: false                # Mount repo as read-only in sandbox
providers:                           # Network access via provider profiles
  - vertex-ai                       # References providers/vertex-ai.yaml
  - github                          # References providers/github.yaml

# ── Skills & plugins ──────────────────────────────────────────
skills:
  - skills/my-skill                  # Local path or URL with #sha256=...
plugins:                             # Directories a runtime loads (ADR 0094)
  - plugins/gopls-lsp                # Claude plugin (plugin.json); Claude Code loads it
  - extensions/go-diagnostics        # pi extension (index.* or package.json entry point)
  - path: extensions/pi-fff          # Object form only when env or runtime options are needed
    env:
      FFF_MULTIGREP: "1"             # Exported before the runtime starts (code-loaded entries)
    pi:
      args: ["--fff-mode", "override"] # Flags the extension registers with pi.registerFlag
openshell:                           # OpenShell sandbox profiles
  profiles:
    - https://example.com/profile.yaml#sha256=abc...

# ── Scripts (local paths only) ────────────────────────────────
pre_script: scripts/pre-my-agent.sh
post_script: scripts/post-my-agent.sh
agent_input: inputs/my-input.md     # File passed as initial input to the agent

# ── Validation ────────────────────────────────────────────────
validation_loop:
  script: scripts/validate-output-schema.sh
  max_iterations: 2
  feedback_mode: append              # "none" (default) or "append" — append the
                                     # previous iteration's validation failure to
                                     # the agent prompt on retry

# ── Host files ────────────────────────────────────────────────
host_files:
  - src: env/my-agent.env            # Runner path (supports ${VAR})
    dest: /sandbox/workspace/.env.d/my-agent.env
    expand: true                     # Resolve ${VAR} in contents
  - src: ${SOME_CREDENTIAL}
    dest: /tmp/.cred.json
    optional: true                   # Skip if missing

# ── Environment ───────────────────────────────────────────────
env:
  runner:                            # Available to pre/post scripts
    MY_VAR: "${MY_VAR}"
  sandbox:                           # Available inside sandbox
    MY_SETTING: "value"
runner_env:                          # ⚠ Deprecated: use env.runner instead
  MY_VAR: "${MY_VAR}"

# ── Timeouts ──────────────────────────────────────────────────
timeout_minutes: 20
sandbox_timeout_seconds: 300         # 30-600

# ── Remote resources ──────────────────────────────────────────
allowed_remote_resources:
  - https://github.com/my-org/agent-library/
allow_runtime_fetch: true
max_runtime_fetches: 10

# ── API servers ───────────────────────────────────────────────
api_servers:                         # Host-side REST proxies exposed to sandbox
  - name: my-api
    script: scripts/api-server.sh    # Local script that runs the server
    port: 8080                       # Port the sandbox connects to
    env:                             # Env vars for the server process
      API_KEY: "${API_KEY}"

# ── Conditional overrides (CEL-guarded, merge-all-matching) ──
overlays:
- when: 'event.source.system == "jira" && runtime.forge == "github"'
  pre_script: scripts/pre-jira-on-gh.sh
  skills: [skills/jira-read]          # Merged with top-level
  env:
    runner:
      GH_TOKEN: "${GH_TOKEN}"
      JIRA_TOKEN: "${JIRA_TOKEN}"
- when: 'runtime.forge == "github"'
  pre_script: scripts/pre-gh.sh
  post_script: scripts/post-gh.sh
  skills: [skills/github-specific]    # Merged with top-level
  providers: [providers/github.yaml]  # Concatenated with top-level
  openshell:
    profiles: [profiles/github.yaml]  # Concatenated with top-level
  host_files:                         # Overlay-specific host files
    - src: env/github.env
      dest: /run/secrets/forge.env
  env:
    runner:
      GH_TOKEN: "${GH_TOKEN}"
- when: 'event.source.system == "jira"'
  pre_script: scripts/pre-jira.sh

# ── Security ──────────────────────────────────────────────────
security:
  fail_mode: closed                  # "closed" (default) or "open"
```

> **Naming convention:** Prefix settings that tune one agent's behavior with
> that agent's role in caps, e.g. `REVIEW_SEVERITY_THRESHOLD` — this avoids
> collisions when multiple agents share a sandbox or env file.
>
> A setting meant to apply the same way across every agent (like
> `roles` or `create_issues.allow_targets`) belongs in `config.yaml`
> instead, not as an env var.

## Field details

Most fields are self-explanatory from the inline comments above. This section expands on fields where additional context helps.

**`role`** — The agent's identity within fullsend. Dispatch uses the role to match config-registered agents to built-in defaults (same-name config agents take precedence). The role also determines which GitHub App credentials **and permissions** the mint service issues. It must be a role the mint serves: on the hosted mint that is the fixed built-in set (`triage`, `coder`, `review`, `retro`, `prioritize`, `fullsend`); custom roles require your own mint. An unserved role returns `403`. See [Custom Agent Identity](../guides/user/custom-agent-identity.md).

**`slug`** — Install-time hint used by `fullsend github setup` to find or name the GitHub App. The `<org>-<role>` convention keeps slugs unique when multiple orgs share a mint. The **mint does not read `slug`** when issuing a token — identity and permissions come from `role`, so changing `slug` alone changes neither. For a custom GitHub App identity, see [Custom Agent Identity](../guides/user/custom-agent-identity.md).

**`doc`** — Path to a human-readable document describing the agent's purpose and design. Resolved in the source repo only; the runtime ignores it. Useful for documentation indexes and discoverability.

**`validation_loop.feedback_mode`** — Controls how validation script output reaches the agent for its next iteration. `none` (default): no feedback; `append`: the previous iteration's validation failure is appended to the agent prompt on retry. See [Configuring agent behavior](../guides/user/customizing-agents.md) for examples.

**`security.fail_mode`** — Determines what happens when a pre-run security scan finds issues or fails to complete. `closed` (default): the run aborts on scan failure or critical findings. `open`: the run continues with a warning. Omitting the `security` block is equivalent to `fail_mode: closed`.

**`allow_runtime_fetch`** — When `true`, the agent can fetch remote resources (skills, plugins, profiles) at runtime rather than only at harness resolution time. Fetched URLs must still be covered by `allowed_remote_resources`.

**`plugins`** — Directories a runtime loads. Which runtime loads an entry follows from the directory, not from the key: a directory with `plugin.json` at its root or `.claude-plugin/plugin.json` is a Claude plugin and Claude Code loads it; anything else must be a directory pi's `-e` loader resolves an entry point in, and pi loads it as an extension ([ADR 0094](../ADRs/0094-pi-extensions-are-harness-resources.md)). Each runtime names and skips the entries in the other format, so one list works whichever runtime the org configures.

Sourcing is the `skills:` rule: a path in the harness repository, or a forge tree URL pinned with `#sha256=`. `npm:`/`git:`/`ssh:` sources are rejected — pi would fetch them from the network at startup, which the sandbox cannot do.

Each entry is a path string, or `{path, env, pi}`. `env` (exported before the runtime starts) and the `pi:` block apply only to an entry a runtime loads as code; on a Claude plugin they are a validation error, not a silent drop.

Validation rejects an entry that breaks any of these rules:

- **Format** — the directory is a Claude plugin (`plugin.json` at its root or `.claude-plugin/plugin.json`, checked first) or one pi would load. A directory that is neither is rejected: Claude Code would ignore it and pi would exit 1 or load nothing.
- **Names** — `a-z`, `A-Z`, `0-9`, `_`, `-`; no duplicate paths, and no duplicate basenames across entries (the second upload would replace the first in the sandbox).
- **Sources** — `npm:`/`git:`/`ssh:` sources and `..` segments are rejected; a URL entry must carry `#sha256=` and point at a forge `/tree/` directory.
- **Tree contents** — regular files and directories only (no symlinks or special files), with names free of newlines, carriage returns and backslashes; the injection scan reads every text file, and a symlink would carry its target into the sandbox unscanned.

A pi-format entry must also satisfy pi's own loader rule:

- **Entry point** — `index.js`/`index.ts`/`index.mjs`/`index.cjs`, or a `package.json` `main` pointing at an existing file, or a `package.json` `"pi": {"extensions": [...]}` list.
- **A `pi` object wins outright** — pi then loads only what `pi.extensions` names, never `index.*` or `main`, so `{"pi": {}}` or an unresolvable `pi.extensions` loads *nothing*, silently, with pi exiting 0.
- **No package layout** — an `extensions/`, `prompts/`, `skills/` or `themes/` entry (a plain file of that name counts) makes pi read the directory as a package and ignore `index.js`; use `pi.extensions` instead.
- **Containment** — a `pi.extensions` or `main` entry that is absolute or climbs out with `..` is rejected, in a nested `package.json` as well as the top one; pi resolves both with no containment check.
- **Glob entries** (`*`, `?`) are matched against the tree, so a pattern selecting nothing is rejected; `**` and brace patterns are accepted unevaluated, `[...]` is a literal file name to pi, and a leading `!` is a *disable* pattern — a `pi.extensions` made only of `!` entries is rejected.
- **`package.json`** — a UTF-8 byte-order mark is stripped before parsing, as pi strips it.
- **Reserved names** — not `fullsend-hooks`, `anthropic-vertex` or `xai-vertex`, which the runner owns.
- **`pi.args`** — flags the extension registered with `pi.registerFlag`, each `--flag` or `--flag=value` (pi has no single-dash options), never one of pi's own option names, with no value starting with `-` or `@`. One bare word may follow a `--flag` written without `=`; any other bare word is prompt text pi would prepend to the agent's prompt.
- **`env` keys** match `^[A-Z_][A-Z0-9_]*$` and may not name the interpreter environment (`PATH`, `HOME`, `TMPDIR`, `ENV`, `BASH_ENV`, `SHELL`, `IFS`, `CDPATH`, `PROMPT_COMMAND`, `LD_*`, `DYLD_*`, `PYTHON*`, `NODE_*`, `SSL_*`, `JITI_*`, `GIT_*`, `JAVA_TOOL_OPTIONS`, `RUBYOPT`, `PERL5OPT`), a credential- or proxy-shaped name (`*_API_KEY`, `*_TOKEN`, `*_SECRET*`, `*_PROXY`), a trust-store or resolver name (`HOSTALIASES`, `OPENSSL_CONF`, `SSLKEYLOGFILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GOPROXY`, `GOFLAGS`), or a runner/provider family (`PI_*`, `FULLSEND_*`, `TIRITH_*`, `GOOGLE_*`, `GCLOUD_*`, `CLOUDSDK_*`, `ANTHROPIC_*`, `XAI_*`, `OPENAI_*`, `AZURE_*`, `AWS_*`, `CLOUD_ML_REGION`).

`plugins` is a top-level field only: it is not part of `ForgeConfig`, so a `plugins:` key under `forge:` or `overlays:` is silently ignored. Walkthrough for the pi side: [Pi § Plugins (pi extensions)](../runtimes/pi.md#plugins-pi-extensions). Rationale and run-time mechanics: [Runtime Implementation § Pi extensions](../contributing/runtime-implementation.md#pi-extensions-adr-0094).

**`max_runtime_fetches`** — Caps the number of runtime fetches per run. Only meaningful when `allow_runtime_fetch` is `true`.

**`api_servers`** — Host-side HTTP servers that run outside the sandbox and are exposed to it via port forwarding. Use these to give an agent access to APIs that require credentials the sandbox should not hold -- the server script runs on the trusted runner with full env access, while the sandbox connects to `localhost:<port>`.

## Deprecated fields

> **Deprecated:** `forge` is deprecated. Use `overlays` with CEL `when`
> expressions instead (see [ADR 0088](../ADRs/0088-cel-guarded-overlays.md)).
> The `forge` field still works but emits a deprecation warning at lint time.
> Migration: each forge key becomes an overlay entry -- e.g. `forge: github:`
> becomes `overlays: - when: 'runtime.forge == "github"'`. Note the conditioning
> axis: `runtime.forge` reflects the effective forge platform (from `--forge`
> flag, `config.forge`, or CI env vars), while `event.source.system` identifies
> the event origin. These diverge for cross-system events (e.g. a JIRA issue
> triggering work on GitHub). `forge` and `overlays` cannot coexist in the
> same harness.

> **Deprecated:** `runner_env` is deprecated. Use `env.runner`
> instead. The `runner_env` field still works but emits a deprecation warning
> at runtime. Migration: move `runner_env:` entries under `env: runner:` and
> delete the `runner_env:` block.

## Field merge rules (for `base` and `overlays`)

Overlays use merge-all-matching: every overlay whose `when` evaluates to true
is applied in declaration order, with later matches taking precedence over
earlier ones for scalar fields. Cross-concern scenarios (e.g. JIRA-specific
scripts *and* GitHub-specific runner env) can use separate overlay entries.
More-specific entries go last so they override broader defaults.

| Field type | Behavior |
|-----------|----------|
| Scalars (`model`, `pre_script`, `policy`, `image`, etc.) | Child wins if non-empty |
| `skills` | Merged with deduplication by basename (child overrides base) |
| `providers`, `openshell.profiles` | Concatenated (base + child); also applies per matched overlay |
| `plugins`, `api_servers` | Concatenated (base + child); each entry keeps its own `env`/`pi` |
| `host_files` | Concatenated; child overrides by `dest` |
| `env`, `runner_env` (deprecated) | Merged; child keys win |
| `validation_loop`, `security` | Child replaces entirely |
| `allowed_remote_resources`, `allow_runtime_fetch`, `max_runtime_fetches` | NOT inherited (child must declare its own) |

## Referencing resources: local vs. remote

**Local paths** resolve relative to the harness file's base directory:
```yaml
agent: agents/triage.md              # → {base}/agents/triage.md
```

**Remote URLs** require a `#sha256=...` integrity hash:
```yaml
agent: https://raw.githubusercontent.com/org/repo/<sha>/agents/lint.md#sha256=abc...
```

**Scripts are local-only** — `pre_script`, `post_script`, and `validation_loop.script` must be local paths (they run on the trusted runner). Exception: scripts declared in a `base` harness fetched via URL are allowed.

## See also

- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — end-to-end guide for building and registering agents
- [Configuring agent behavior](../guides/user/customizing-agents.md) — harness configurations and `base:` composition
- [CEL Triggers Reference](../guides/user/cel-triggers-reference.md) — dispatch flow and trigger patterns
