# OpenCode

[OpenCode](https://github.com/anomalyco/opencode) is fullsend's third agent runtime, opt-in per repo. Like pi, it runs through the same sandbox, credentials and egress policy, and reads
`AGENTS.md` natively.

```bash
fullsend run triage --runtime opencode --model google-vertex-anthropic/claude-opus-4-6@default
```

Selecting it, and how it compares to Claude Code and pi, is in [Agent runtimes](../runtimes.md). This
page is what changes once you are on it.

> **Planned: write-path support.** OpenCode is enabled for read-only agents (`triage`,
> `prioritize`). Write-capable agents (`code`, `fix`) are gated on the security-hook adapter, tracked
> in [unbound-force#515](https://github.com/unbound-force/unbound-force/issues/515): OpenCode has no
> native PreToolUse/PostToolUse hooks, so until the runner-owned, sha256-gated plugin adapter lands,
> no sandbox tool hooks are installed. Harnesses using the default `security.enabled: true` will
> exit 97 (hook adapter missing); set `security.enabled: false` on the harness entry until #515
> lands. This is not a role-aware gate: disabling security also removes the exit-97 guard, including
> for write-capable agents. Select OpenCode only for read-only agents and pilot on a disposable repo.
>
> **What `security.enabled: false` turns off.** The flag gates more than the exit-97 hook guard.
> Setting it to `false` also suppresses:
>
> 1. the pre-upload runtime content scan (agent def, skills, plugins),
> 2. the host-side context file scan (unicode, SSRF patterns on repo context files),
> 3. the pre-agent sandbox `scan context` pass,
> 4. the post-agent output scan and secret redaction (`scanOutputFiles`).
>
> OpenCode transcripts tee raw JSON to the host and download a second copy; with this workaround,
> neither copy receives secret redaction and untrusted repo context files are not scanned.
> Use only on disposable repos with no real credentials until #515 lands.

## Models and providers

A model on OpenCode is `provider/model`. Aliases and bare ids also work — `opus`/`sonnet`/`haiku`
resolve through fullsend's table, and a bare id gets the provider from `FULLSEND_OPENCODE_PROVIDER`
(default `google-vertex-anthropic`). A `provider/model` spec passes through unchanged.

| Model | Spec |
|---|---|
| Claude | `google-vertex-anthropic/claude-opus-4-6@default` |
| Alias | `opus`, `sonnet`, `haiku` (resolve to the catalog ids) |

Harness `model:` and `agents:` entry `model:` values accept the `provider/model` form directly. The
same Vertex WIF credentials cover the Anthropic-on-Vertex provider the injected config registers.

## Config discovery

OpenCode's config directory is **runner-owned**, off the agent-writable workspace, and pointed at
OpenCode with `OPENCODE_CONFIG_DIR` (`/sandbox/opencode-config`). The target repo cannot pre-seed it
and a workspace reset does not clear it. The injected `OPENCODE_CONFIG_CONTENT` (Vertex provider +
tool-permission policy) merges last in OpenCode's config stack, so it wins over any repo config.

During Bootstrap, before an agent iteration can modify `.env`, the runner records the effective
`OPENCODE_CONFIG_CONTENT` and `GOOGLE_APPLICATION_CREDENTIALS` values outside the sandbox. Run fails
closed if that state is missing, then restores both values after sourcing `.env`. Bootstrap and Run
must therefore execute in the same fullsend process, as they do in the normal CLI lifecycle.

> A non-interactive `opencode run` has no TTY, so OpenCode auto-rejects every permission request that
> config does not pre-resolve. The injected policy therefore **allows** the read-only tools an agent
> needs (read, grep, glob, list, read-only bash). Write-path denial and the compensating hook adapter
> are [unbound-force#515](https://github.com/unbound-force/unbound-force/issues/515).

## At a glance

| | |
|---|---|
| Credentials | Same WIF `external_account` + refreshed OIDC token as Claude Code and pi |
| Unattended | No approval prompts, stdin closed; a non-config-allowed tool request is auto-rejected |
| Artifacts | `output.jsonl`, `transcripts/<agent>-output.jsonl`, `metrics.json` with `runtime: opencode`, plus `opencode-debug.log` (stderr with `--print-logs` structured logs) when `--debug` is set |
| Extra knobs | `FULLSEND_OPENCODE_PROVIDER` (prefix for bare ids) |
| Not supported | Fallback chains, `plugins:` (Claude marketplace layout), sandbox tool hooks (until #515) |

## Running it locally

Complete [Running agents locally](../guides/user/running-agents-locally.md) first — the CLI,
OpenShell, credentials and the fleet clone are the same. An OpenCode run additionally needs the
`OPENCODE_CONFIG_CONTENT` env var (see below) and `security.enabled: false` on the harness until
#515 lands.

The plan block confirms the selection, and `metrics.json` records `runtime`, `runtime_source`,
`requested_model` and `override_source`.

To keep an agent on OpenCode (or off it) without passing flags every time, set `runtime:`/`model:` on
its `agents:` entry in `config.yaml` — see [per-agent settings](../runtimes.md#per-agent-runtime-model-and-effort).

What a local OpenCode run needs, beyond the guide:

- **A sandbox image that includes `opencode`** — Bootstrap preflights `opencode --version` and fails
  fast if the pinned binary is missing or broken, rather than producing an empty transcript.
- **Read-only agents** — pilot `triage`/`prioritize`; `code`/`fix` are gated on #515.
- **`OPENCODE_CONFIG_CONTENT`** — Bootstrap validates this env var and fails closed when it is
  missing or has no `permission` policy. Supply it via `--env-file`. A minimal example:

  ```bash
  # fullsend-opencode.env
  OPENCODE_CONFIG_CONTENT='{"provider":{"google-vertex-anthropic":{"id":"google-vertex-anthropic"}},"permission":{"read":"allow","glob":"allow","grep":"allow","list":"allow","bash":"allow","write":"deny","edit":"deny","skill":"deny","agent":"deny","sourcegraph":"deny","mcp":"deny","task":"deny"}}'
  ```

  Adjust the `permission` record to match the agent's `tools:` frontmatter. The injected config
  merges last in OpenCode's config stack (see [Config discovery](#config-discovery)).
- **`security.enabled: false`** — required on the harness entry until #515 lands. Without it the
  run exits 97 (hook adapter missing). See the warning at the top of this page for the full
  implications — it also suppresses all scan pipelines and secret redaction.
- **Knobs** — `FULLSEND_OPENCODE_PROVIDER` sets the provider for bare model ids (default
  `google-vertex-anthropic`).
- **Debugging** — `--debug='*'` (the `=` is required); sandbox-side failures land in
  `opencode-debug.log` inside the run directory, next to the transcripts.

The local-run command therefore becomes:

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env \
  --env-file fullsend-opencode.env \
  --runtime opencode
```

## Behaviour differences worth knowing

- **Reads `AGENTS.md` only** — unlike Claude Code, which supports multiple instruction files
  (`CLAUDE.md`, per-directory overrides), OpenCode currently reads only `AGENTS.md`. No
  `CLAUDE.md` bridge is injected (like pi). `OPENCODE_DISABLE_PROJECT_CONFIG=true` suppresses
  OpenCode's own project-level config walk; the runner re-injects `AGENTS.md` through
  `config.instructions` in the runner-owned `opencode.json`.
- **The Claude-style agent definition is translated** into OpenCode's `agent/<name>.md` layout with
  JSON frontmatter (`mode: primary`, `permission:` as a `{toolID: "allow"|"deny"}` record). Claude
  tool names are mapped to OpenCode tool ids; names without an OpenCode equivalent are dropped with
  a warning. Per-argument Bash restrictions currently collapse to a bare `bash: "allow"`; bootstrap
  warns when this occurs, and enforcement is grouped with the hook adapter in unbound-force#515.
- **`plugins:` are unsupported** — the Claude marketplace layout is warned and skipped.
- **Effort maps to `--variant`** — the harness `effort` value selects OpenCode's model reasoning
  variant.
- **No sandbox tool hooks yet** — the plugin-adapter path is reserved at
  `OPENCODE_CONFIG_DIR/plugins/fullsend-hooks.ts` with a fail-closed sha256 integrity guard (exit
  97), but the adapter itself lands in #515.

## Transcripts

OpenCode's `--format json` stream is the transcript. During a run it is streamed to the host and
tee'd into a sandbox file, which `ExtractTranscripts` downloads to
`transcripts/<agent>-output.jsonl`. A stream that reports an error (or ends zero-turn) overrides a
`0` exit code, the same false-success guard pi uses. The full-fidelity transcript redesign is
deferred to [unbound-force#513](https://github.com/unbound-force/unbound-force/issues/513); this is
the interim tee approach.

## See also

- [Agent runtimes](../runtimes.md) — choosing and selecting a runtime
- [Running agents locally](../guides/user/running-agents-locally.md) — the local-run flow that [Running it locally](#running-it-locally) builds on
- [Runtime implementation](../contributing/runtime-implementation.md) — the runtime contract and security matrix
