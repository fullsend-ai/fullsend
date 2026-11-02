# Harness Field Reference

Complete reference for all fields available in a fullsend harness YAML file. For a guide-oriented introduction to harnesses, see [Bring Your Own Agent](../guides/user/bring-your-own-agent.md).

```yaml
# ── Required ──────────────────────────────────────────────────
agent: agents/my-agent.md           # Path to agent definition
role: triage                        # A role the mint serves (built-in on the hosted mint); not the agent's name. Format: lowercase letter first, then a-z, 0-9, _, -; no double hyphens
policy: policies/base.yaml          # Sandbox policy; required

# ── Identity & metadata ──────────────────────────────────────
slug: my-org-my-role                # Install-time App discovery (convention: <org>-<role>); not read by the mint
description: One-line summary       # Human-readable description
doc: docs/agents/my-agent.md        # Source-repo-only; not resolved at runtime
trigger: "event.entity.kind == 'work_item'"  # Optional CEL expression over NormalizedEvent (see CEL Triggers Reference)

# ── Composition ───────────────────────────────────────────────
base: harness/common-base.yaml      # Inherit from another harness (local or URL)

# ── Sandbox ───────────────────────────────────────────────────
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
model: opus                         # LLM model override
effort: high                        # Reasoning effort (low, medium, high, xhigh, max); claude runtime only
readonly_repo: false                # Mount repo as read-only in sandbox
providers:                           # Network access via provider profiles
  - vertex-ai                       # Bare builtin name: resolves to fullsend's embedded definition
  - github                          # Same; or a providers/<name>.yaml path for a local/custom definition

# ── Skills & plugins ──────────────────────────────────────────
skills:
  - skills/my-skill                  # Local path or URL with #sha256=...
  - skills/pr-review:                # Map form: per-file overrides (staged per run)
      sub-agents/security.md: overrides/security.md   # Replace (or add) from a local file
      sub-agents/extra.md: https://example.com/extra.md#sha256=abc...  # Or a pinned URL
      sub-agents/user-experience.md: null             # Remove from this run's copy
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
agent_input: inputs/my-agent/        # Local directory of files passed as agent input

# ── Mint privilege per run-stage (ADR 0073) ───────────────────
privilege_levels:
  pre_script: write                 # host-side deterministic automation
  runtime: read                     # LLM sandbox / validation_loop inherit this
  post_script: write
  # default: write                  # used for unlisted stages; omitted field = write everywhere

# ── Validation ────────────────────────────────────────────────
validation_loop:
  script: scripts/validate-output-schema.sh
  preflight_check: 'python3 -c "import jsonschema"'  # Literal sh -c command (NOT a script path); runs before sandbox creation in the allowlisted preflight environment
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
  sandbox:                           # Available inside sandbox; reserved names are dropped
    MY_SETTING: "value"
runner_env:                          # ⚠ Deprecated: use env.runner instead
  MY_VAR: "${MY_VAR}"

# ── Timeouts ──────────────────────────────────────────────────
timeout_minutes: 20                  # Per-iteration budget (default 30); exported to
                                     # the sandbox as FULLSEND_TIMEOUT_MINUTES
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
  providers: [providers/myorg-github.yaml]  # Concatenated with top-level
  openshell:
    profiles: [profiles/myorg-github.yaml]  # Concatenated with top-level
  host_files:                         # Overlay-specific host files
    - src: env/github.env
      dest: /sandbox/workspace/.env.d/forge.env
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

**`privilege_levels`** — Maps run-stages to named mint privilege levels so the LLM sandbox can receive a narrower token than host-side scripts. Keys: `pre_script`, `runtime`, `post_script`, and `default` (covers unlisted stages). `validation_loop` is not a key — it inherits `runtime` because it shares that stage's security context. Level names are lowercase identifiers (`read` and `write` on every role; custom roles may define more). When the field is omitted, every stage gets `write`, matching pre-ADR-0073 behavior. The runner mints the `runtime` token **before** provider credential expansion and `host_files` with `expand: true`, remints around `pre_script` when that stage differs, and remints for `post_script` after sandbox teardown. A remint failure is fatal (aborts the run) whenever the configured stage level differs from the currently active token's level; it is non-fatal — falling back to the existing token — only when the levels match (a same-level expiry refresh). **Top level only** — a `privilege_levels` key placed under `overlays:`/`forge:` is silently ignored; only `base:` composition (see the merge table below) can override it. See [ADR 0073](../ADRs/0073-named-mint-privilege-levels.md).

**`slug`** — Install-time hint used by `fullsend github setup` to find or name the GitHub App. The `<org>-<role>` convention keeps slugs unique when multiple orgs share a mint. The **mint does not read `slug`** when issuing a token — identity and permissions come from `role`, so changing `slug` alone changes neither. For a custom GitHub App identity, see [Custom Agent Identity](../guides/user/custom-agent-identity.md).

**`doc`** — Path to a human-readable document describing the agent's purpose and design. Resolved in the source repo only; the runtime ignores it. Useful for documentation indexes and discoverability.

**`validation_loop.feedback_mode`** — Controls how validation script output reaches the agent for its next iteration. `none` (default): no feedback; `append`: the previous iteration's validation failure is appended to the agent prompt on retry. See [Configuring agent behavior](../guides/user/customizing-agents.md) for examples.

**`validation_loop.max_iterations`** — The maximum number of agent runs in one invocation (default 1). A second run happens only when the agent finished and its output failed validation; an iteration the runner killed at `timeout_minutes` is not retried. See [`fullsend run` § Budget and deadline](../cli/run.md#budget-and-deadline) and [ADR 0105](../ADRs/0105-timed-out-iteration-ends-the-run.md).

**`validation_loop.preflight_check`** — A host-dependency probe run before sandbox creation as a literal `sh -c` command ([ADR 0128](../ADRs/0128-preflight-check-literal-command.md)). It uses the host process's working directory and is not resolved through the resource-fetch pipeline. Prefer a self-contained probe such as `python3 -c "import jsonschema"`; a relative script command works only if its file is present in that host working directory. The probe runs in a minimal allowlisted environment: the host `PATH`, `HOME`, and `TMPDIR` (when set), the runner-forced `FULLSEND_DIR` and `PYTHONDONTWRITEBYTECODE`, and non-credential variables declared under `env.runner`/`runner_env` — nothing else is inherited from the process environment. Credential names (`GH_TOKEN`, `GITHUB_TOKEN`, `GITLAB_TOKEN`, the minted role tokens `PUSH_TOKEN` and `REVIEW_TOKEN`, `FULLSEND_FORGE_TOKEN`, `FULLSEND_FETCH_TOKEN`, `FULLSEND_GITLAB_*` role tokens, `OPENAI_API_KEY`, `GH_WORKFLOW_TOKEN`, and the OIDC/trigger/runner-secret names) are excluded even when declared under `env.runner`. Plain `${VAR}` references are validated and expanded before the command runs: a plain reference to a name outside the allowlisted environment fails the run before any shell executes, naming the variable (never its value) and pointing to the allowlist and the `env.runner` declaration channel. Non-plain brace forms such as `${VAR:-default}` are not validated and silently expand to empty rather than failing the run. On failure or timeout, diagnostics carry only redacted command text and output. When the command contains no `${...}` reference, bare `$VAR` references are left to the shell, where every non-allowlisted name is unset; once a brace reference is present, bare references are also resolved against the preflight environment before the shell runs, and a bare reference to a name outside it expands to empty, matching the shell's unset semantics.

**`env.runner`** — Variables passed to host-side scripts (`pre_script`, `post_script`,
and `validation_loop.script`), layered over the `fullsend run`
process environment. (`validation_loop.preflight_check` also reads `env.runner`
declarations, but atop its own allowlisted environment — see above, not the
process environment.) A few named workflow secrets are **not** inherited from that environment:
`JIRA_TOKEN`, `JIRA_USER_EMAIL`, and the OTLP collector headers `OTEL_EXPORTER_OTLP_HEADERS` /
`OTEL_EXPORTER_OTLP_*_HEADERS` (for example `OTEL_EXPORTER_OTLP_TRACES_HEADERS`). A script
receives one only when the harness declares it, e.g. `JIRA_TOKEN: "${JIRA_TOKEN}"` under
`env.runner`. `fullsend run` itself still reads them for its own Jira client and trace export.
Earlier releases passed them to every script unconditionally; a harness whose scripts read
`JIRA_TOKEN` or `JIRA_USER_EMAIL` without declaring them must add them to `env.runner`. Scripts
always run with `PYTHONDONTWRITEBYTECODE=1`, overriding any `env.runner` value, so bytecode never
breaks the hash-verified fetch cache.

`env.runner` values never reach the sandbox. Each value is expanded with `${NAME}` after `base:` composition and overlays are resolved. Names come from the runner's environment and, on GitHub, from the `FULLSEND_GITHUB_RUNNER_SECRETS` object ([ADR 0136](../ADRs/0136-runner-secrets-through-one-stored-secret.md)). A name in that object wins over a runner variable of the same name. In CI the composite action stages the object in a mode 0600 file and `fullsend run` deletes it after reading, so the object is never in the environment of `fullsend run` or of any process it starts. Keys the harness does not reference reach no script. A referenced value reaches the script as an ordinary environment variable, and scripts run as the job user. With the object unset, expansion behaves as before.

These rules make a run fail before `pre_script` starts:

- **Refused names.** A key in the object may not be a runner-owned name. That
  covers `GH_WORKFLOW_TOKEN`, the OIDC request variables, provider-only keys
  such as `OPENAI_API_KEY`, the `FULLSEND_*`, `GITHUB_*`, `ACTIONS_*`,
  `RUNNER_*`, `CI_*`, `LD_*`, `DYLD_*`, `OTEL_*`, `GOOGLE_*` and `CLOUDSDK_*`
  families, the minted role tokens (`GH_TOKEN`, `PUSH_TOKEN`,
  `PUSH_TOKEN_SOURCE`, `REVIEW_TOKEN`, `GITLAB_TOKEN`), `PATH`, the
  runner-infrastructure names (`HOME`, `SHELL`, `BASH_ENV`, `ENV`,
  `TRACEPARENT`), and every name the reusable workflow sets on the `fullsend
  run` step, such as the named `JIRA_TOKEN`, `JIRA_USER_EMAIL` and
  `JIRA_BASE_URL` on `harness-run`. An `env.runner` key with one of those names
  may not receive a value from the object either. Credential-class names beyond
  the runner-infrastructure names, `PATH`, and the `LD_*`/`DYLD_*` families —
  proxy, TLS trust, Git, interpreter and shell-behavior variables such as
  `HTTPS_PROXY`, `SSL_CERT_FILE`, `GIT_SSH_COMMAND`, `NODE_OPTIONS` and
  `HOSTALIASES` — are **not** refused here: runner secrets reach host-side
  scripts (`pre_script`, `post_script`, `validation_loop`) only as `env.runner`
  values, and the sandbox gate independently blocks them from `env.sandbox`
  exports; see [Migrating proxy and certificate
  settings](#migrating-proxy-and-certificate-settings)
  for where the proxy and TLS-trust settings go instead. Names are matched
  case-insensitively, so `path` or `github_token` is refused like `PATH` or
  `GITHUB_TOKEN`.
- **Host side only.** The object's names may be referenced only from `env.runner` values. A reference from `env.sandbox`, `runner_env`, a provider's `credentials` or `config`, a `host_files` `src`, the content of a `host_files` entry with `expand: true`, or a `validation_loop` field is a validation error.
- **Overlays.** An overlay that references one of the object's names may be guarded only by `runtime.forge` and `config`. An overlay that reads `event` or any other `runtime` field is refused, even when it does not match. This rule is interim until [ADR 0112](../ADRs/0112-overlays-may-set-any-harness-field.md)'s guarded-field check is implemented; until then an event-guarded overlay can still swap `pre_script`, `post_script` or `validation_loop`, and so choose which script receives a referenced value.
- **Shape.** The value must be a JSON object whose values are strings, with no duplicate keys and no `null` values. Each value must be at least as long as the redactor's minimum (currently 8 characters), so it can be redacted. Errors name the offending key, never its value.

`fullsend run` masks each value, and each line of a multi-line value, in the GitHub Actions log. It also registers the value, its lines and its JSON-escaped form for redaction from pre-script failure detail and validation feedback. Both match exact strings: a value a script transforms (base64, URL-encoded, a substring) is not hidden, and a line shorter than that minimum is masked but not redacted.

These rules cover names in the GitHub object only. GitLab is unchanged: each secret is its own masked CI/CD variable, `${NAME}` reads it directly, and no name is refused there (keeping fullsend's own credentials out of GitLab host-side scripts is tracked in [#8146](https://github.com/fullsend-ai/fullsend/issues/8146)). For setup, see [Bring Your Own Agent § Pass secrets to host-side scripts](../guides/user/bring-your-own-agent.md#pass-secrets-to-host-side-scripts-github).

**`env.sandbox`** — Variables exported into the sandbox environment. Reserved
names are dropped instead of exported, matched case-insensitively: the
runner-infrastructure names (`PATH`, `HOME`, `SHELL`, `BASH_ENV`, `ENV`,
`LD_PRELOAD`, `LD_LIBRARY_PATH`, `TRACEPARENT` and the `FULLSEND_*` names the
runner reserves), the credentials it mints or receives and the runner-only
inputs that select them, and the credential deny-list (proxy, TLS-trust, Git,
interpreter, loader and shell-behavior names such as `HTTPS_PROXY`,
`SSL_CERT_FILE`, `GIT_SSH_COMMAND`, `NODE_OPTIONS` and `IFS`). `fullsend run`
warns once when the harness loads, naming every dropped key, and once per key on
stderr as it writes the sandbox environment; `fullsend lock` prints the
load-time warning when it composes a harness. The full list is in [Reserved
`env.sandbox` names](#reserved-envsandbox-names), where blocked proxy and
certificate settings go instead is in [Migrating proxy and certificate
settings](#migrating-proxy-and-certificate-settings), and the warnings are
described in [`fullsend run` § Reserved `env.sandbox`
keys](../cli/run.md#reserved-envsandbox-keys).

**`agent_input`** — A local directory, not a file. When a URL `base:` harness declares it, the inherited value is cleared rather than fetched; supply the directory in the child harness if needed. See [Harness field semantic types](../contributing/harness-fields.md#semantic-types-adr-0127).

**`host_files[].dest`** — An absolute path under `/sandbox/` or `/tmp/`. Paths the runner writes or puts on `PATH` ahead of the image are refused when the harness loads: `/sandbox/workspace/.env`, the runner and runtime binaries in `/sandbox/workspace/bin` (`claude`, `codex`, `pi`, `opencode`, `fullsend`, `fullsend-check-output`), and anything under `/sandbox/claude-config`, `/sandbox/codex-config`, `/sandbox/pi-config`, `/sandbox/workspace/.fullsend`, `/sandbox/workspace/.security`, `/sandbox/.venv` or `/sandbox/go/bin`. A `dest` containing a `..` path component is refused as well. The same rule applies to `host_files` under `overlays:`, `forge:` and inherited from `base:`. On Claude and pi runs `/sandbox/workspace/bin` is last on the agent's `PATH`, so a file there adds a command but does not replace one the image provides. Codex and OpenCode runs restore the `PATH` from before `.env`, so `/sandbox/workspace/bin` and `.env.d` additions are not on the codex or OpenCode agent's `PATH`. See [Adding executables](../guides/user/customizing-agents.md#adding-executables).

**`.env.d` host files** — A `host_files` entry with a `dest` in
`/sandbox/workspace/.env.d/` ending in `.env` is read before the runtime starts.
Claude, codex, pi and OpenCode launches, and the runner's own in-sandbox probes
(the pre-agent context scan, the GitHub preflight and the runtime bootstrap
reads), read these files, like `.env` itself, as data: only `[export
]NAME=value` assignments, `export NAME` and comments are taken (values may be
quoted and may use `$NAME`, `${NAME}` and a leading `~`), and any other line is
ignored with a warning that names the file and line number. Shell forms that
these launches ignore include `${NAME:-default}` and every other `${...}` form
beyond `${NAME}`, `export A=1 B=2` (put one assignment on each line), `FOO=x;
export FOO` (write `export FOO=x`), and a value containing a NUL byte. An
`.env.d` directory or an included file that exists but cannot be read fails the
launch. `.env` itself stays valid shell, and a shell that sources it (such as an
agent running `. /sandbox/workspace/.env`) runs these files too, so still treat
them like `pre_script`. `host_files` can also place configuration that tools
load, such as git config or shell rc files; that is trusted like `pre_script`
too. After the `.env.d` files are read, on Claude and pi runs the runner puts
its own `PATH` back in front; OpenCode runs restore the `PATH` from before
`.env`. `LD_PRELOAD`, `LD_LIBRARY_PATH`, `LD_AUDIT`, every `PYTHON*` variable,
`BASH_ENV`, `ENV` and `IFS` are never taken from `.env` or `.env.d` on Claude,
codex, pi or OpenCode runs. Claude runs also unset `LD_PRELOAD`,
`LD_LIBRARY_PATH`, `LD_AUDIT`, `PYTHONPATH`, `PYTHONHOME`, `PYTHONSTARTUP`,
`NODE_OPTIONS`, `NODE_PATH` and `BUN_OPTIONS`, plus the Claude Code variables
`CLAUDE_CODE_SAFE_MODE`, `CLAUDE_CODE_SIMPLE`, `CLAUDE_CODE_SHELL_PREFIX` and
`CLAUDE_CODE_SUBPROCESS_ENV_SCRUB`; codex runs unset the same loader names
except `BUN_OPTIONS`. Setting those there has no effect on the agent of those
runtimes.


**`policy`** — Path or pinned URL of the sandbox policy. Required at `fullsend run`; fullsend ships no default. A child that omits `policy:` still passes if it inherits one from `base:`. A local file that unmarshals to no fields (comments only) is rejected with the path named. Commit a copy of the fleet policy from [fullsend-ai/agents](https://github.com/fullsend-ai/agents), or set `policy:` to its URL with a `#sha256=` hash under `allowed_remote_resources`; `fullsend agent new` writes both the file and the field for a new agent.

**`timeout_minutes`** — Wall-clock budget for one agent iteration, default 30. The runner ends the iteration and sweeps the processes the agent left running in the sandbox (best effort) when it is spent, and a killed iteration ends the run with `agent timed out after <elapsed> without completing (timeout: <budget>)` unless its output validates anyway. Before every iteration the runner writes the budget as `FULLSEND_TIMEOUT_MINUTES`, the kill time as `FULLSEND_ITERATION_DEADLINE` (Unix seconds), and the current agent span as `TRACEPARENT` into the agent's environment — see [`fullsend run` § Budget and deadline](../cli/run.md#budget-and-deadline). Those names are reserved: an `env.sandbox` entry with any of them is dropped.

**`security.fail_mode`** — Determines what happens when a pre-run security scan finds issues or fails to complete. `closed` (default): the run aborts on scan failure or critical findings. `open`: the run continues with a warning. Omitting the `security` block is equivalent to `fail_mode: closed`.

**`allow_runtime_fetch`** — When `true`, the agent can fetch remote resources (skills, plugins, profiles) at runtime rather than only at harness resolution time. Fetched URLs must still be covered by `allowed_remote_resources`.

**`skills`** — Skill directories to upload. Each entry is a source string (a
local path or a forge tree URL pinned with `#sha256=`), or a single-key map
of that source to per-file overrides. An override key is a file path
relative to the skill root in clean form: no leading `/`, no `..` or `.`
segment, no `//` or trailing `/`, no null bytes, and never a directory. Its value is `null` (remove the file), a local path
resolved from the `.fullsend` directory, or an `https://` URL with
`#sha256=` covered by `allowed_remote_resources`; a string replaces the file
when it exists and adds it otherwise. For each entry with overrides, the
runner copies the resolved skill to
`<output-dir>/<sandbox-name>/skills/<index>/<skill-name>` right after
harness resolution, then applies the overrides to the copy; the source is
never written. A symlink inside an overridden skill, or on the path to an
override file, fails the run. Every run, with or without overrides, writes a
persona manifest of the personas found in the staged skills (by the rules the
`pi` and `codex` runtimes use to discover them) to `<output-dir>/<sandbox-name>/personas.json` and
`/sandbox/workspace/.fullsend/personas.json`, and exports its path as
`FULLSEND_PERSONAS_FILE` to the pre-script, post-script, validation script
and sandbox. Under `base:` composition, a map-form entry whose name matches a
base skill but whose path has no `SKILL.md` in the `.fullsend` directory
keeps the base skill's source and applies only its overrides, so a child can
override files in an inherited skill without copying it. When you list a
skill directly and another skill also pulls it in through its
`dependencies:`, only one copy is staged, and the direct entry's overrides
apply to that copy whatever the list order. Two entries for the same skill
may set the same file to the same value; setting it to different values is
an error. See
[Configuring with Skills § Overriding single files in a skill](../guides/user/customizing-with-skills.md#overriding-single-files-in-a-skill).

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
- **Reserved names** — not `fullsend-hooks`, `fullsend-agent`, `fullsend-edit-repair`, `anthropic-vertex` or `xai-vertex`, which the runner owns. An entry also must not register a tool named `edit`: pi rejects two extensions that register the same tool name, and the runner's own `fullsend-edit-repair` extension already registers `edit` whenever the agent has the edit tool.
- **`pi.args`** — flags the extension registered with `pi.registerFlag`, each `--flag` or `--flag=value` (pi has no single-dash options), never one of pi's own option names, with no value starting with `-` or `@`. One bare word may follow a `--flag` written without `=`; any other bare word is prompt text pi would prepend to the agent's prompt.
- **`env` keys** match `^[A-Z_][A-Z0-9_]*$` and may not name the interpreter environment (`PATH`, `HOME`, `TMPDIR`, `ENV`, `BASH_ENV`, `SHELL`, `IFS`, `CDPATH`, `PROMPT_COMMAND`, `LD_*`, `DYLD_*`, `PYTHON*`, `NODE_*`, `SSL_*`, `JITI_*`, `GIT_*`, `JAVA_TOOL_OPTIONS`, `RUBYOPT`, `PERL5OPT`), a credential- or proxy-shaped name (`*_API_KEY`, `*_TOKEN`, `*_SECRET*`, `*_PROXY`), a trust-store or resolver name (`HOSTALIASES`, `OPENSSL_CONF`, `SSLKEYLOGFILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GOPROXY`, `GOFLAGS`), or a runner/provider family (`PI_*`, `FULLSEND_*`, `TIRITH_*`, `GOOGLE_*`, `GCLOUD_*`, `CLOUDSDK_*`, `ANTHROPIC_*`, `XAI_*`, `OPENAI_*`, `AZURE_*`, `AWS_*`, `CLOUD_ML_REGION`).

`plugins` is a top-level field only: a `plugins:` key under `forge:` or `overlays:` is silently ignored. Walkthrough for the pi side: [Pi § Plugins (pi extensions)](../runtimes/pi.md#plugins-pi-extensions). Rationale and run-time mechanics: [Runtime Implementation § Pi extensions](../contributing/runtime-implementation.md#pi-extensions-adr-0094).

**`max_runtime_fetches`** — Caps the number of runtime fetches per run. Only meaningful when `allow_runtime_fetch` is `true`.

**`api_servers`** — Planned host-side HTTP servers outside the sandbox, exposed to it via port forwarding; server startup is not yet implemented. The intended design would keep API credentials on the trusted runner rather than inside the sandbox.

## Reserved `env.sandbox` names

`env.sandbox` cannot modify runner infrastructure or security settings through
the reserved names below. The runner drops these names before writing the
sandbox environment, case-insensitively:

- **Runner infrastructure and process-loading safeguards:** `PATH`, `HOME`,
  `SHELL`, `BASH_ENV`, `ENV`, `LD_PRELOAD`, `LD_LIBRARY_PATH`, `TRACEPARENT`,
  and every `FULLSEND_*` variable the runner itself exports into the sandbox
  (its output and fetch plumbing, the repository path, role and slug, the
  iteration budget and deadline, the persona manifest path
  `FULLSEND_PERSONAS_FILE`, and its own `PATH` entry). The set grows
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
[Runtime Implementation](../contributing/runtime-implementation.md).
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
dynamic-loader, interpreter and shell-startup variables that the `.env.d` host
files entry in [Field details](#field-details) lists. `.env` itself is still
valid shell that an agent may source, so keep treating these files like
`pre_script`. Either way, `.env.d` is not a general migration path for blocked
proxy or certificate settings; that entry has the per-runtime rules.

## Migrating proxy and certificate settings

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
  `host_files` `.env.d` still delivers differs by runtime; the `.env.d` host
  files entry in [Field details](#field-details) lists what each runtime drops
  or unsets. Whatever it delivers, it does so only because that file is
  trusted like `pre_script`, so it is not a sanctioned way to restore them.

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
| `skills` | Merged with deduplication by skill identity (declared `SKILL.md` name when on disk, otherwise basename). Child overrides a matching base entry; sibling nested dirs that share a leaf name are kept; two entries with one identity in a single list are both kept and fail at bootstrap; when a child entry replaces a base entry, their per-file override maps merge (child keys win); a map-form child entry whose path has no `SKILL.md` keeps the base entry's source (and, with no other match, the one pinned-URL base entry of its name; two or more such entries are an error that asks for the full base URL as the map key) |
| `providers`, `openshell.profiles` | Concatenated (base + child); also applies per matched overlay |
| `plugins`, `api_servers` | Concatenated (base + child); each entry keeps its own `env`/`pi` |
| `host_files` | Concatenated; child overrides by `dest` |
| `env`, `runner_env` (deprecated) | Merged; child keys win |
| `privilege_levels` | Merged; child keys win. Omitted entirely defaults every stage to `write`. Top-level only — this merge applies only to `base:` composition; an `overlays:`/`forge:` entry is silently ignored |
| `validation_loop` | Field-level merge; child/overlay non-zero values win, omitted fields inherit |
| `security` | Child replaces entirely |
| `allowed_remote_resources`, `allow_runtime_fetch`, `max_runtime_fetches` | NOT inherited (child must declare its own); however, the config-level `allowed_remote_resources` from repository-local configuration acts as a fallback for URL resolution |

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

**`validation_loop.preflight_check` is a command, not a script resource** — The runner validates `${VAR}` references against the preflight allowlist environment (host `PATH`/`HOME`/`TMPDIR`, runner-forced `FULLSEND_DIR`/`PYTHONDONTWRITEBYTECODE`, and `env.runner` declarations), expands them, and passes the result to `sh -c` on the host; it does not fetch or stage a file named by the command. A reference outside that environment — including any credential name, whether inherited from the process environment or declared under `env.runner` — fails the run before the command executes. Do not interpolate untrusted values, even within shell quotes: on failure or timeout the diagnostics embed the command and output with declared secret values redacted.

## See also

- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — end-to-end guide for building and registering agents
- [Configuring agent behavior](../guides/user/customizing-agents.md) — harness configurations and `base:` composition
- [CEL Triggers Reference](../guides/user/cel-triggers-reference.md) — dispatch flow and trigger patterns
