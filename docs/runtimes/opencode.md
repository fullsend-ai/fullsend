# OpenCode

[OpenCode](https://github.com/anomalyco/opencode) is fullsend's fourth agent runtime, opt-in per repo
or per agent. It runs in the same sandbox, with the same credentials and egress policy as Claude
Code and pi. Choosing between runtimes is in [Agent runtimes](../runtimes.md).

OpenCode 1.19.1 supports the shared sandbox security hooks through a native plugin, including
write-capable agents. **Security must remain enabled for every role**; `fullsend run` rejects
`security.enabled: false`. OpenCode v2 uses different tool and plugin APIs and is outside this
implementation's scope.

Bootstrap uploads the Go-embedded adapter and selected shared Python scripts to
`/sandbox/opencode-config`, following pi's deployment pattern. The image supplies
the pinned OpenCode binary and SDK dependencies. Use a sandbox image that includes
OpenCode 1.19.1 and the matching SDK. Hook changes travel with the
runner and do not require baking those scripts into the image.

## Run it locally

Complete [Running agents locally](../guides/user/running-agents-locally.md) first. The CLI,
OpenShell, credentials and the agents clone are the same. Use a fleet sandbox image that includes
the pinned OpenCode (1.19.1).

**1. Write the tool policy.** OpenCode reads its global config, including the tool-permission
policy, from `OPENCODE_CONFIG_CONTENT`. A run fails before it starts when the variable is empty or
has no `permission` block. Put it in an env file:

```bash
# fullsend-opencode.env
OPENCODE_CONFIG_CONTENT='{"provider":{"google-vertex-anthropic":{"id":"google-vertex-anthropic"}},"permission":{"*":"deny","read":"allow","glob":"allow","grep":"allow","list":"allow","skill":"allow","bash":{"*":"allow","rm *":"deny"},"write":"deny","edit":"deny","webfetch":"deny","task":"deny"}}'
```

Allow every tool the agent needs. OpenCode runs without a TTY here, so it rejects any tool call
the policy does not allow. The agent's own `tools:` list can narrow this policy but never widen it.

**2. Set up the harness.** In the agent's harness (`harness/triage.yaml`), keep security enabled and
add the variable to the existing `env.sandbox` block:

```yaml
security:
  enabled: true
env:
  sandbox:
    # ...existing entries...
    OPENCODE_CONFIG_CONTENT: ${OPENCODE_CONFIG_CONTENT}
```

`fullsend agent new --runtime opencode` adds the `env.sandbox` entry for you.

**3. Run the agent:**

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env \
  --env-file fullsend-opencode.env \
  --runtime opencode \
  --model google-vertex-anthropic/claude-sonnet-4-6@default
```

Output from a triage run (trimmed):

```text
    Model: google-vertex-anthropic/claude-sonnet-4-6@default (from --model flag)
    Runtime: opencode (from --runtime flag)
runtime: selected "opencode" from --runtime flag
  • Bootstrapping sandbox
Skill "github": uploaded to sandbox
Skill "issue-labels": uploaded to sandbox
→ Iteration 1 of 2
→ Agent: claude-sonnet-4-6@default
  ⚙ bash: gh issue view 1 --repo <owner>/<repo> --json number,title,body,...
  ⚙ bash: gh label list --repo <owner>/<repo> --json name,description --limit 100
  ✓ Validation passed: Validating: output/agent-result.json against .../triage-result.schema.json
    Agent exit code: 0
    Validation: passed
```

`metrics.json` records `runtime: opencode`. To keep an agent on OpenCode without the flag, set
`runtime:` on its `agents:` entry in `config.yaml` (see
[per-agent settings](../runtimes.md#per-agent-runtime-model-and-effort)).

## Models

A model is `provider/model`. The aliases resolve to ids in OpenCode's built-in model catalog:

| You give | OpenCode gets |
|---|---|
| `opus` (default) | `google-vertex-anthropic/claude-opus-5-5@default` |
| `sonnet` | `google-vertex-anthropic/claude-sonnet-5-5@default` |
| `haiku` | `google-vertex-anthropic/claude-haiku-5-5@default` |
| `fable` | `google-vertex-anthropic/claude-fable-5-1@default` |
| A bare id | the id, prefixed with `FULLSEND_OPENCODE_PROVIDER` (default `google-vertex-anthropic`); on `google-vertex-anthropic`, `@default` is appended when the id has no `@` suffix |
| `provider/model` | unchanged |

The harness or `--model` wins over the agent's `model:` frontmatter. `models.aliases` in
`.fullsend/config.yaml` can retarget an alias. A bare id there gets `@default`: `sonnet: claude-sonnet-5`
becomes `google-vertex-anthropic/claude-sonnet-5@default`. That matches the catalog for 4.6 and later
models only. Older models are listed under a dated id, so give it in full
(`sonnet: claude-sonnet-4-5@20250929`); an id with an `@` suffix is left as is. An explicit
`google-vertex-anthropic/...` spec passes through unchanged, so it must carry the catalog suffix too.

**There is no fallback chain.** Vertex enables models per project. On a project that does not serve
5.5, Claude Code falls back to 4.6, but OpenCode fails. Name a model your project serves, as in the
example above.

## At a glance

| | |
|---|---|
| Roles | Read-only and write-capable agents; security enabled for all roles |
| Credentials | Same Vertex WIF credentials as Claude Code and pi |
| Instructions | Reads `AGENTS.md`; no `CLAUDE.md` bridge |
| Effort | `--effort` maps to OpenCode's `--variant` |
| Artifacts | `output.jsonl`, `transcripts/<agent>-output.jsonl`, `metrics.json`, and `opencode-debug.log` with `--debug='*'` |
| Sandbox hooks | Shared PreToolUse/PostToolUse scripts via the native plugin, including nested Task sessions |
| Not supported | Fallback chains, harness `plugins:`, OpenCode v2 |

## Behaviour differences worth knowing

- **Bash allowlists are enforced.** `Bash(gh,jq)` permits only those program names in each
  simple command. Native patterns deny other commands where compatible with the trusted policy;
  the plugin independently enforces the allowlist and rejects ambiguous shell syntax, environment
  assignments, command substitutions and shell wrappers. Trusted pattern maps remain a ceiling.
  This restricts executable names, not every capability of an allowed program.
  Unlike pi's advisory default, OpenCode enforces the declared list without an
  opt-in switch. Agent instructions must use the commands they declare; see
  [agents#1821](https://github.com/fullsend-ai/agents/issues/1821) for the scribe
  compatibility follow-up.
- **Directory listing stays distinct from file reading.** OpenCode uses `read` for both.
  An agent declaring only `LS` can list directories, including directory symlinks, but cannot
  read regular files or file symlinks through that tool.
- **The agent is reset before every iteration.** The runner removes agent, tool and config overrides
  from writable config locations, re-uploads the agent definition, and checks that OpenCode loads it.
  Adapter/script digests are checked before agent verification and before and after `.env` is sourced.
  Each intercepted tool call rechecks the script directory. Native write/edit/patch tools cannot
  write outside the workspace or into `.env`, `.security` or `runner-state`, including via symlinks.
- **Repo config is ignored.** OpenCode does not load the target repo's `.opencode/` config or skills.
- **`.env` is read as data.** As on the other runtimes, the launch parses `.env` with a pinned
  `python3` and runs none of it as shell, so the image needs `python3` and a regular-file `readlink`
  in `PATH` directories the sandbox user neither owns nor can write. Without them the launch fails
  with `python3 not found in a PATH directory the agent cannot write` (exit 127). The fullsend
  sandbox image ships both.
- **`opencode` comes from a directory the agent cannot write.** The launch pins
  the first `opencode` in a `PATH` directory the sandbox user neither owns nor
  can write, judged by the file it resolves to and every directory above that
  file. An `opencode` found only through an agent-writable directory, or a
  symlink into one, fails the launch with
  `opencode not found in a PATH directory the agent cannot write` (exit 127).
  If `opencode` is a `#!/usr/bin/env node` script rather than a binary, `node`
  must sit in such a directory too, and the launch starts the script with it.
  In the fullsend sandbox image `opencode` is a binary.

The adapter is `plugins/fullsend-hooks.ts`, with selected scripts under `hooks/`,
inside `/sandbox/opencode-config`. Hooks use the bootstrap-captured interpreter,
isolated Python mode and a fixed system PATH. Default plugins are disabled and
nonempty native `plugin` configuration is rejected; use the harness provider/environment
credential route. Shell-startup injection variables are cleared for tool subprocesses.
The launch also clears `OPENCODE_PURE`, which would disable the hook plugin.

**Deployment decision:** this adapter uses pi's existing bootstrap-and-hash-check model.
The config tree remains writable by the sandbox user. Checks detect modifications but cannot
prevent a deliberate change between verification and loading. Image-owned or separately
privileged storage is separate hardening work, not a guarantee provided by this adapter.
The OpenShell filesystem and egress policies remain the primary boundary for Bash and other
writable state; native path checks and command scanning supplement those controls.

Harness skills under `/sandbox/opencode-config/skills/` remain writable and
persist across iterations, as they do in pi. They are uploaded at bootstrap but
are not restored or digest-checked before each run. The adapter's integrity
checks cover its plugin and selected hook scripts, not skill contents.

Native exceptions skip the after hook, so `experimental.chat.messages.transform` scans failed-tool
history before model requests, including Task sessions. Credentials are redacted; canary hits,
scanner failures or invalid replies stop the next request. Raw session/transcript storage is
not covered by this model-boundary check (see #7320). Denials identify the blocking
script and include a bounded, credential-redacted reason when the verified shared sanitizer is
available. Reasons containing the canary, missing sanitizer files, and sanitization failures
produce only the script name; raw hook stderr is never reflected.
The image includes the pinned plugin SDK. Bootstrap restores global SDK bookkeeping from
that installation after clearing user config, so startup needs no registry download.

## Troubleshooting

| You see | Cause and fix |
|---|---|
| `OPENCODE_CONFIG_CONTENT is empty` | The env file or the harness `env.sandbox` entry is missing (steps 1 and 2) |
| `OPENCODE_CONFIG_CONTENT ... invalid JSON` or `... permission policy` | The JSON is invalid, or its `permission` block is missing or invalid (step 1) |
| `OpenCode requires security.enabled: true` | Remove the old security-disabled workaround or set it to `true` |
| `opencode hook adapter requires version 1.19.1` | Use the pinned sandbox image; v2 needs a separate adapter migration |
| Hook adapter/scripts missing or modified (exit 97) | A security file changed after bootstrap. Recreate the sandbox; do not disable security |
| `OpenCode plugin SDK missing` | Use an image with the pinned plugin SDK installed under `/sandbox/opencode-config` |
| `ProviderModelNotFoundError` | The id is not in OpenCode's built-in catalog. Use an id from the table above |
| Vertex `404 NOT_FOUND` for the model | Your project does not serve it. Name one it serves |
| `Falling back to default agent` | OpenCode could not load the agent, and the iteration fails. Rerun with `--debug='*'` and read `opencode-debug.log` |
| `python3 not found in a PATH directory the agent cannot write` | The image has no `python3` outside directories the sandbox user owns or can write. Use the fullsend sandbox image or add one |
| `no readlink that is a regular file in a PATH directory the agent cannot write`, then `opencode not found in a PATH directory the agent cannot write` | The image has no regular-file `readlink` outside those directories, so no binary can be pinned. Use the fullsend sandbox image or add coreutils `readlink` |
| `opencode not found in a PATH directory the agent cannot write` | The image's `opencode` is missing, or is found only through a directory the sandbox user owns or can write. Install it outside such directories |
| Tool calls fail with a permission error | The policy does not allow that tool (step 1) |

## See also

- [Agent runtimes](../runtimes.md): choosing and selecting a runtime
- [Running agents locally](../guides/user/running-agents-locally.md): the local-run setup this page builds on
- [Runtime implementation](../contributing/runtime-implementation.md): how the runtime is secured, per feature
