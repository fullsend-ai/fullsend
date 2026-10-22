# OpenCode

[OpenCode](https://github.com/anomalyco/opencode) is fullsend's fourth agent runtime, opt-in per repo
or per agent. It runs in the same sandbox, with the same credentials and egress policy as Claude
Code and pi. Choosing between runtimes is in [Agent runtimes](../runtimes.md).

> **Planned:** sandbox tool hooks and write-capable agents
> ([#7824](https://github.com/fullsend-ai/fullsend/issues/7824)). Until they land, OpenCode is
> experimental:
>
> - Use it only for read-only agents (`triage`, `prioritize`) on a disposable repo. fullsend does
>   not enforce this, so do not select it for `code` or `fix`.
> - The harness must set `security.enabled: false`. That also turns off the content scans of the
>   agent, skills and repo context, and the secret redaction of outputs and transcripts.

## Run it locally

Complete [Running agents locally](../guides/user/running-agents-locally.md) first. The CLI,
OpenShell, credentials and the agents clone are the same. The fleet sandbox image includes the
pinned OpenCode (1.19.1).

**1. Write the tool policy.** OpenCode reads its global config, including the tool-permission
policy, from `OPENCODE_CONFIG_CONTENT`. A run fails before it starts when the variable is empty or
has no `permission` block. Put it in an env file:

```bash
# fullsend-opencode.env
OPENCODE_CONFIG_CONTENT='{"provider":{"google-vertex-anthropic":{"id":"google-vertex-anthropic"}},"permission":{"*":"deny","read":"allow","glob":"allow","grep":"allow","list":"allow","skill":"allow","bash":{"*":"allow","rm *":"deny"},"write":"deny","edit":"deny","webfetch":"deny","task":"deny"}}'
```

Allow every tool the agent needs. OpenCode runs without a TTY here, so it rejects any tool call
the policy does not allow. The agent's own `tools:` list can narrow this policy but never widen it.

**2. Set up the harness.** In the agent's harness (`harness/triage.yaml`), turn security off and
add the variable to the existing `env.sandbox` block:

```yaml
security:
  enabled: false
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
Agent Bash allowlist (gh, curl, jq) is recorded but not enforced on opencode — ...
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
| Roles | Read-only agents (`triage`, `prioritize`) |
| Credentials | Same Vertex WIF credentials as Claude Code and pi |
| Instructions | Reads `AGENTS.md`; no `CLAUDE.md` bridge |
| Effort | `--effort` maps to OpenCode's `--variant` |
| Artifacts | `output.jsonl`, `transcripts/<agent>-output.jsonl`, `metrics.json`, and `opencode-debug.log` with `--debug='*'` |
| Not supported | Fallback chains, `plugins:`, sandbox tool hooks (#7824) |

## Behaviour differences worth knowing

- **Bash allowlists are not enforced.** `Bash(gh:*)` in the agent's `tools:` becomes plain `bash`,
  with a warning. To restrict commands, give `bash` a pattern map in the policy
  (`"bash": {"*": "deny", "gh *": "allow"}`). An agent that declares Bash gets that same map.
- **The agent is reset before every iteration.** The runner removes agent, tool and config overrides
  the previous iteration could have written, re-uploads the agent, and checks that OpenCode loads it.
  `skills/` in the config directory stays agent-writable until #7824.
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

## Troubleshooting

| You see | Cause and fix |
|---|---|
| `OPENCODE_CONFIG_CONTENT is empty` | The env file or the harness `env.sandbox` entry is missing (steps 1 and 2) |
| `OPENCODE_CONFIG_CONTENT ... invalid JSON` or `... permission policy` | The JSON is invalid, or its `permission` block is missing or invalid (step 1) |
| `hooks adapter not yet available` | The harness still has `security.enabled: true` (step 2) |
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
