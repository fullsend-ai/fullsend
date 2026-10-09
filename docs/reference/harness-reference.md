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
  - vertex-ai                       # Bare builtin name: resolves to fullsend's embedded definition
  - github                          # Same; or a providers/<name>.yaml path for a local/custom definition

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
workflow:                            # Workflow-definition repository (ADR 0130); claude or pi runtime
  source: https://github.com/example-org/sample-pipeline/tree/<commit-sha>#sha256=<tree-hash>  # Or a path in this repository ("." is its root)
  name: run-all                      # Claude plugin only: workflows/run-all.js must exist and declare it
  args: "issue ${ISSUE_NUMBER}"      # Claude plugin only, optional; one string the script receives unsplit
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
  preflight_check: 'python3 -c "import jsonschema"'  # Literal sh -c command (NOT a script path); runs before sandbox creation
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

**`privilege_levels`** — Maps run-stages to named mint privilege levels so the LLM sandbox can receive a narrower token than host-side scripts. Keys: `pre_script`, `runtime`, `post_script`, and `default` (covers unlisted stages). `validation_loop` is not a key — it inherits `runtime` because it shares that stage's security context. Level names are lowercase identifiers (`read` and `write` on every role; custom roles may define more). When the field is omitted, every stage gets `write`, matching pre-ADR-0073 behavior. The runner mints the `runtime` token **before** provider credential expansion and `host_files` with `expand: true`, remints around `pre_script` when that stage differs, and remints for `post_script` after sandbox teardown. A remint failure is fatal (aborts the run) whenever the configured stage level differs from the currently active token's level; it is non-fatal — falling back to the existing token — only when the levels match (a same-level expiry refresh). **Top level only** — it is not a `ForgeConfig` field, so a `privilege_levels` key placed under `overlays:`/`forge:` is silently ignored; only `base:` composition (see the merge table below) can override it. See [ADR 0073](../ADRs/0073-named-mint-privilege-levels.md).

**`slug`** — Install-time hint used by `fullsend github setup` to find or name the GitHub App. The `<org>-<role>` convention keeps slugs unique when multiple orgs share a mint. The **mint does not read `slug`** when issuing a token — identity and permissions come from `role`, so changing `slug` alone changes neither. For a custom GitHub App identity, see [Custom Agent Identity](../guides/user/custom-agent-identity.md).

**`doc`** — Path to a human-readable document describing the agent's purpose and design. Resolved in the source repo only; the runtime ignores it. Useful for documentation indexes and discoverability.

**`validation_loop.feedback_mode`** — Controls how validation script output reaches the agent for its next iteration. `none` (default): no feedback; `append`: the previous iteration's validation failure is appended to the agent prompt on retry. See [Configuring agent behavior](../guides/user/customizing-agents.md) for examples.

**`validation_loop.max_iterations`** — The maximum number of agent runs in one invocation (default 1). A second run happens only when the agent finished and its output failed validation; an iteration the runner killed at `timeout_minutes` is not retried. See [`fullsend run` § Budget and deadline](../cli/run.md#budget-and-deadline) and [ADR 0105](../ADRs/0105-timed-out-iteration-ends-the-run.md).

**`validation_loop.preflight_check`** — A host-dependency probe run before sandbox creation as a literal `sh -c` command ([ADR 0128](../ADRs/0128-preflight-check-literal-command.md)). It uses the host process's working directory and is not resolved through the resource-fetch pipeline. Prefer a self-contained probe such as `python3 -c "import jsonschema"`; a relative script command works only if its file is present in that host working directory.

**`env.runner`** — Variables passed to host-side scripts (`pre_script`, `post_script`, `validation_loop.script` and `validation_loop.preflight_check`), layered over the `fullsend run` process environment. A few named workflow secrets are **not** inherited from that environment: `JIRA_TOKEN`, `JIRA_USER_EMAIL`, and the OTLP collector headers `OTEL_EXPORTER_OTLP_HEADERS` / `OTEL_EXPORTER_OTLP_*_HEADERS` (for example `OTEL_EXPORTER_OTLP_TRACES_HEADERS`). A script receives one only when the harness declares it, e.g. `JIRA_TOKEN: "${JIRA_TOKEN}"` under `env.runner`. `fullsend run` itself still reads them for its own Jira client and trace export. Earlier releases passed them to every script unconditionally; a harness whose scripts read `JIRA_TOKEN` or `JIRA_USER_EMAIL` without declaring them must add them to `env.runner`.

**`agent_input`** — A local directory, not a file. When a URL `base:` harness declares it, the inherited value is cleared rather than fetched; supply the directory in the child harness if needed. See [Harness field semantic types](../contributing/harness-fields.md#semantic-types-adr-0127).

**`timeout_minutes`** — Wall-clock budget for one agent iteration, default 30. The runner ends the iteration and sweeps the processes the agent left running in the sandbox (best effort) when it is spent, and a killed iteration ends the run with `agent timed out after <elapsed> without completing (timeout: <budget>)` unless its output validates anyway. Before every iteration the runner writes the budget as `FULLSEND_TIMEOUT_MINUTES`, the kill time as `FULLSEND_ITERATION_DEADLINE` (Unix seconds), and the current agent span as `TRACEPARENT` into the agent's environment — see [`fullsend run` § Budget and deadline](../cli/run.md#budget-and-deadline). Those names are reserved: an `env.sandbox` entry with any of them is dropped.

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
- **Reserved names** — not `fullsend-hooks`, `fullsend-agent`, `fullsend-edit-repair`, `anthropic-vertex` or `xai-vertex`, which the runner owns. An entry also must not register a tool named `edit`: pi rejects two extensions that register the same tool name, and the runner's own `fullsend-edit-repair` extension already registers `edit` whenever the agent has the edit tool.
- **`pi.args`** — flags the extension registered with `pi.registerFlag`, each `--flag` or `--flag=value` (pi has no single-dash options), never one of pi's own option names, with no value starting with `-` or `@`. One bare word may follow a `--flag` written without `=`; any other bare word is prompt text pi would prepend to the agent's prompt.
- **`env` keys** match `^[A-Z_][A-Z0-9_]*$` and may not name the interpreter environment (`PATH`, `HOME`, `TMPDIR`, `ENV`, `BASH_ENV`, `SHELL`, `IFS`, `CDPATH`, `PROMPT_COMMAND`, `LD_*`, `DYLD_*`, `PYTHON*`, `NODE_*`, `SSL_*`, `JITI_*`, `GIT_*`, `JAVA_TOOL_OPTIONS`, `RUBYOPT`, `PERL5OPT`), a credential- or proxy-shaped name (`*_API_KEY`, `*_TOKEN`, `*_SECRET*`, `*_PROXY`), a trust-store or resolver name (`HOSTALIASES`, `OPENSSL_CONF`, `SSLKEYLOGFILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GOPROXY`, `GOFLAGS`), or a runner/provider family (`PI_*`, `FULLSEND_*`, `TIRITH_*`, `GOOGLE_*`, `GCLOUD_*`, `CLOUDSDK_*`, `ANTHROPIC_*`, `XAI_*`, `OPENAI_*`, `AZURE_*`, `AWS_*`, `CLOUD_ML_REGION`).

`plugins` is a top-level field only: it is not part of `ForgeConfig`, so a `plugins:` key under `forge:` or `overlays:` is silently ignored. Walkthrough for the pi side: [Pi § Plugins (pi extensions)](../runtimes/pi.md#plugins-pi-extensions). Rationale and run-time mechanics: [Runtime Implementation § Pi extensions](../contributing/runtime-implementation.md#pi-extensions-adr-0094).

**`workflow`** — Pins a workflow-definition repository: a Claude Code plugin that ships a workflow script, or a pi extension whose session hook drives the sequence ([ADR 0130](../ADRs/0130-workflow-definition-repos-are-harness-resources.md)). A harness has at most one.

- **`source`** (required) — where the definition is. Two forms:
  - **A tree URL**: `https://github.com/<owner>/<repo>/tree/<commit sha>[/<path>]#sha256=<tree hash>`, at the repository root or a sub-directory. The ref must be the full 40-character commit sha (a branch or tag is refused) and the `#sha256=` tree hash is required. Only github.com is supported today. The URL must be covered by `allowed_remote_resources` in `config.yaml`, and `fullsend lock` records it with field `workflow`.
  - **A path** in the repository that holds the harness, `.` being its root: for a harness under `.fullsend/`, the repository that holds `.fullsend/`. The path must be relative, use `/`, and contain no `..`, `.git` or `.fullsend-cache` segment. Only files git tracks under it are read (commit or `git add` a file for it to be delivered), fullsend's configuration directory is never part of it (a path that is that directory or lies inside it is refused), and it has no pin and no lock entry.
- **`name`** — for a Claude plugin, required: the workflow's `meta.name`, which is the name Claude Code gives the workflow. fullsend finds the script by file name, so it checks that `workflows/<name>.js` exists and that its `meta.name` is `<name>`: keep the file name and `meta.name` identical. A name with no script fails resolution with the list of workflows the definition ships. fullsend allows only letters, digits, `_` and `-` here, which is stricter than Claude Code. For a pi extension, `name` is refused.
- **`args`** (optional) — for a Claude plugin, one string, and `args` needs `name`. For a pi extension, `args` is refused. `${VAR}` references expand from the runner environment, as in `env:`, so a harness can name the work item (`issue ${ISSUE_NUMBER}`). Claude Code hands the text after the workflow command to the script as a single string, unsplit, in `args`; there is no argument splitting or quoting. Every referenced variable must be set in the runner environment (set to the empty string is allowed, as for `runner_env`); an unset one fails the run with `workflow.args references ${<NAME>}, which is not set in the runner environment; set it, or remove it from args`. No NUL, carriage return or newline, before or after expansion: a variable that holds one fails the run with `workflow.args references ${<NAME>}, whose value holds NUL, carriage return or newline characters, so args would no longer be one line; set it to a single-line value`, which names the variable but prints neither the args nor the value. Args reach the model prompt, the run plan and `metrics.json`, so they must never carry a credential. A `${VAR}` whose name is credential-shaped (ending in `_TOKEN`, `_API_KEY`, `_PRIVATE_KEY`, `_ACCESS_KEY`, `_SECRET_KEY`, `_PROXY`, `_PASSWORD` or `_CREDENTIALS`, containing `_SECRET`, or starting with `OTEL_`; a bare `_KEY` such as `ISSUE_KEY` is allowed) is refused when the harness loads, so `fullsend lock` reports it; at run time so is a runner-only credential that no harness may expand. Both print `workflow.args references ${<NAME>}, which names a credential (<rule>); pass work-item identifiers such as ${ISSUE_NUMBER} instead`. The args text is scanned for credential-looking content whether or not it names a variable: literal text that looks like a credential is refused when the harness loads, by `fullsend lock` and `fullsend run`, with `workflow.args looks like it holds a credential (<rules>); args reach the model prompt, the run plan and metrics.json, so remove it and pass work-item identifiers such as ${ISSUE_NUMBER} only`, and after expansion the result is scanned again, failing the run with `workflow.args expands to a value that looks like a credential (<rules>); pass work-item identifiers only`. `<rules>` names the matched secret patterns; the matched text is not printed. Traces record the workflow without its args.

**Relative sources and `base:`.** A relative `source` in a harness composed through a URL `base:` is resolved in the base harness's repository at the base's commit: `source: pipelines/sample` in a base fetched from `https://raw.githubusercontent.com/example-org/sample-pipeline/<commit-sha>/.fullsend/harness/base.yaml` becomes `https://github.com/example-org/sample-pipeline/tree/<commit-sha>/pipelines/sample`. The path is taken from the repository root, not from the base's `.fullsend/` directory. As for a base plugin, it needs no `#sha256=` fragment: the base's pin covers the path, the definition is fetched at that commit, and `fullsend lock` records its tree hash. The base URL must be pinned at a full commit sha, and `https://raw.githubusercontent.com/<owner>/<repo>/<commit-sha>/<path>/` must be covered by `allowed_remote_resources`, the prefix a base plugin is checked against. Several harnesses of one repository share a pin this way: each is a thin harness with a `base:` that holds the `workflow:`. A relative `source` in a local `base:` file resolves in the git checkout that holds that base file, which may be another checkout than the child's.

**Harnesses added by URL.** A harness registered by URL (`fullsend agent add <url>`, or a `config.yaml` `agents:` entry whose source is a URL) resolves relative paths in your repository, not in the harness's, so a relative `workflow.source` there is refused:

```text
workflow.source "pipelines/sample" is relative, but this harness was added by URL, so relative paths resolve in your repository, not the harness's; pin source as a tree URL with #sha256=, or install the harness through a one-line base: harness
```

**The tree's kind decides what runs it.** After the fetch, fullsend classifies the definition the way it classifies a `plugins:` entry, and the kind must match the agent's runtime; a mismatch fails at plan time naming both:

| Definition | Runtime | Delivered as |
|------------|---------|--------------|
| Claude Code plugin | `claude` | A plugin directory passed with `--plugin-dir`; `name` and `args` apply |
| pi extension | `pi` | An extension loaded with `-e`, like a `plugins:` entry; `name` and `args` are refused |
| either | `dummy`, `dummy-playback` | Accepted, for behaviour tests |
| either | `codex` and other runtimes | Refused at plan time, before the definition is fetched |

The definition is uploaded under the fixed sandbox directory name `workflow-definition`, so in a harness that declares `workflow:` (its own or composed from `base:`) a `plugins:` entry with that directory name (compared without case) is refused; a harness without `workflow:` may use the name. A Claude plugin's namespace is the `name` in `.claude-plugin/plugin.json`, or `workflow-definition` when that file is absent; a `plugins:` entry with the same Claude Code plugin name is refused. Like every plugin, the definition is injection-scanned before upload when the harness has security enabled (the default; `security.enabled: false` turns the scan off).

A Claude plugin's script must begin with its meta object, as Claude Code requires for listing it: `export const meta = { ... }` is the first statement (comments may precede it) and a plain object literal, with `name` as a single- or double-quoted string. fullsend also needs `;` right after the closing `}`:

```js
export const meta = { name: 'run-all', description: 'Run every phase' };
```

fullsend reads this object as text, without running the script, and accepts only strings, numbers, `true`, `false`, `null`, and arrays and objects of these as values. Only spaces or tabs may come between the closing `}` and the `;`; fullsend reads nothing after the `;`. Save the script with LF or CRLF line endings: a lone carriage return, U+2028 or U+2029 before the `;` fails resolution. A meta that is not first, is not a plain object literal (a spread, a computed key, a variable, a template literal or a regular expression as a value), lacks the `;` after its closing `}`, declares `name` more than once or with an escape, or whose `name` differs from the file name fails resolution.

The fetch materializes symlinks whose targets are inside the fetched tree: the target's content is stored under the link's path and counts toward the fetch limits. A symlink that leaves the tree, dangles or loops is refused with its path in the error. The run plan prints the source, its tree hash and, for a Claude plugin, the command the runner starts:

```text
Workflow: example-org/sample-pipeline@0123456789ab/pipelines/sample (sha256:<hash>) → /<namespace>:<name> <args>
Workflow: pipelines/sample (sha256:<hash>) delivered as pi extension
```

A remote source is shortened to `<owner>/<repo>@<first 12 characters of the commit>[/<path>]`, a path source is printed as written, `<hash>` is the first 12 characters of the tree hash, and ` <args>` (expanded) is left out when `args` is unset.

**Claude plugin: the runner starts the workflow.** Instead of the default agent prompt the runner passes `/<namespace>:<name> <args>` to `claude`, still with `--agent`, so the agent file sets the main loop's tools and instructions. If the agent file has a `tools:` list, it must name `Workflow`; otherwise the run fails at plan time with `workflow: agent "<name>" lists tools: without Workflow, so it cannot start /<namespace>:<name>; add Workflow to its tools:`. It must not list `Workflow` in `disallowedTools:` either (`workflow: agent "<name>" lists Workflow in disallowedTools:, so it cannot start /<namespace>:<name>; remove Workflow from its disallowedTools:`). An agent without `tools:` gets every tool not in its `disallowedTools:`. An agent file whose frontmatter cannot be parsed fails the run at plan time. If the harness enables the tool allowlist hook (`security.sandbox_hooks.tool_allowlist_pretool`), `FULLSEND_TOOL_ALLOWLIST` must name `Workflow` as well. When the harness sets it in `env.sandbox`, the run fails at plan time otherwise, with `workflow: security.sandbox_hooks.tool_allowlist_pretool is enabled and env.sandbox FULLSEND_TOOL_ALLOWLIST does not name Workflow, so the hook would block /<namespace>:<name>; add Workflow to FULLSEND_TOOL_ALLOWLIST`. A list supplied only through a `host_files` env file is not read at plan time, and the hook blocks the first `Workflow` call instead.

A `validation_loop` retry starts the same command again. A Claude Code workflow resumes only within the session that started it, so the workflow must skip finished work from its own durable state (files in the repository or the workspace). With `feedback_mode: append`, the validation output is **not** passed to the workflow: the command stays as written, and the run prints once `The workflow restarts from its own state; validation feedback is not passed to it`.

**pi extension: its own hook starts it.** The runner starts nothing: the run uses the default prompt (and, on a `validation_loop` retry with `feedback_mode: append`, the feedback prompt), and the extension's session hook drives the sequence. No tools check applies. How a script-led pi parent reports completion is a follow-up.

`metrics.json` records the definition in its [`workflow` object](../cli/run.md#metricsjson-fields) on every runtime (`kind` is `claude-plugin` or `pi-extension`, and `command` is set for a Claude plugin only), so a `dummy` run, which runs no model, still shows the command.

`workflow` is a top-level field only: it is not part of `ForgeConfig`, so a `workflow:` key under `forge:` or `overlays:` is silently ignored, as for `plugins`. An older fullsend binary ignores the field too and runs the harness with the default prompt, so a harness that uses it must state the fullsend release it needs.

**`max_runtime_fetches`** — Caps the number of runtime fetches per run. Only meaningful when `allow_runtime_fetch` is `true`.

**`api_servers`** — Planned host-side HTTP servers outside the sandbox, exposed to it via port forwarding; server startup is not yet implemented. The intended design would keep API credentials on the trusted runner rather than inside the sandbox.

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
| `privilege_levels` | Merged; child keys win. Omitted entirely defaults every stage to `write`. Top-level only — not a `ForgeConfig` field, so this merge applies only to `base:` composition; an `overlays:`/`forge:` entry is silently ignored |
| `validation_loop` | Field-level merge; child/overlay non-zero values win, omitted fields inherit |
| `security`, `workflow` | Child replaces entirely |
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

**`validation_loop.preflight_check` is a command, not a script resource** — The runner expands `${VAR}` references from its permitted host environment, then passes the result to `sh -c` on the host; it does not fetch or stage a file named by the command. Do not interpolate untrusted values or credentials, even within shell quotes: on failure or timeout the expanded command currently appears in diagnostics.

## See also

- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — end-to-end guide for building and registering agents
- [Configuring agent behavior](../guides/user/customizing-agents.md) — harness configurations and `base:` composition
- [CEL Triggers Reference](../guides/user/cel-triggers-reference.md) — dispatch flow and trigger patterns
