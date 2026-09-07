# OpenCode

[OpenCode](https://github.com/anomalyco/opencode) is fullsend's third agent runtime, opt-in per org
or repo. Like pi, it runs through the same sandbox, credentials and egress policy, and reads
`AGENTS.md` natively.

```bash
fullsend run triage --runtime opencode --model anthropic-vertex/claude-opus-4-6
```

Selecting it, and how it compares to Claude Code and pi, is in [Agent runtimes](../runtimes.md). This
page is what changes once you are on it.

> **Experimental — read-only agents only.** OpenCode is enabled for read-only agents (`triage`,
> `prioritize`). Write-capable agents (`code`, `fix`) are gated on the security-hook adapter, tracked
> in [unbound-force#515](https://github.com/unbound-force/unbound-force/issues/515): OpenCode has no
> native PreToolUse/PostToolUse hooks, so until the runner-owned, sha256-gated plugin adapter lands,
> no sandbox tool hooks are installed. Pilot on a disposable repo before relying on it.

## Models and providers

A model on OpenCode is `provider/model`. Aliases and bare ids also work — `opus`/`sonnet`/`haiku`
resolve through fullsend's table, and a bare id gets the provider from `FULLSEND_OPENCODE_PROVIDER`
(default `anthropic-vertex`). A `provider/model` spec passes through unchanged.

| Model | Spec |
|---|---|
| Claude | `anthropic-vertex/claude-opus-4-6` |
| Alias | `opus`, `sonnet`, `haiku` (resolve to the catalog ids) |

Harness `model:` and `agents:` entry `model:` values accept the `provider/model` form directly. The
same Vertex WIF credentials cover the Anthropic-on-Vertex provider the injected config registers.

## Config discovery

OpenCode's config directory is **runner-owned**, off the agent-writable workspace, and pointed at
OpenCode with `OPENCODE_CONFIG_DIR` (`/sandbox/opencode-config`). The target repo cannot pre-seed it
and a workspace reset does not clear it. The injected `OPENCODE_CONFIG_CONTENT` (Vertex provider +
tool-permission policy) merges last in OpenCode's config stack, so it wins over any repo config.

> A non-interactive `opencode run` has no TTY, so OpenCode auto-rejects every permission request that
> config does not pre-resolve. The injected policy therefore **allows** the read-only tools an agent
> needs (read, grep, glob, list, read-only bash). Write-path denial and the compensating hook adapter
> are [unbound-force#515](https://github.com/unbound-force/unbound-force/issues/515).

## At a glance

| | |
|---|---|
| Credentials | Same WIF `external_account` + refreshed OIDC token as Claude Code and pi |
| Unattended | No approval prompts, stdin closed; a non-config-allowed tool request is auto-rejected |
| Artifacts | `output.jsonl`, `transcripts/<agent>-output.jsonl`, `metrics.json` with `runtime: opencode`, plus `opencode-debug.log` with `--debug` |
| Extra knobs | `FULLSEND_OPENCODE_PROVIDER` (prefix for bare ids) |
| Not supported | Fallback chains, `plugins:` (Claude marketplace layout), sandbox tool hooks (until #515) |

## Running it locally

Complete [Running agents locally](../guides/user/running-agents-locally.md) first — the CLI,
OpenShell, credentials and the fleet clone are the same. Every example there runs on OpenCode by
adding `--runtime opencode` to the same command:

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-triage.env \
  --runtime opencode
```

The plan block confirms the selection, and `metrics.json` records `runtime`, `runtime_source`,
`requested_model` and `override_source`.

To keep an agent on OpenCode (or off it) without passing flags every time, set `runtime:`/`model:` on
its `agents:` entry in `config.yaml` — see [per-agent settings](../runtimes.md#per-agent-runtime-model-and-effort).

What a local OpenCode run needs, beyond the guide:

- **A sandbox image that includes `opencode`** — Bootstrap preflights `opencode --version` and fails
  fast if the pinned binary is missing or broken, rather than producing an empty transcript.
- **Read-only agents** — pilot `triage`/`prioritize`; `code`/`fix` are gated on #515.
- **Knobs** — `FULLSEND_OPENCODE_PROVIDER` sets the provider for bare model ids (default
  `anthropic-vertex`).
- **Debugging** — `--debug='*'` (the `=` is required); sandbox-side failures land in
  `opencode-debug.log` inside the run directory, next to the transcripts.

## Behaviour differences worth knowing

- **Reads `AGENTS.md` natively** — no `CLAUDE.md` bridge is injected (like pi).
- **The Claude-style agent definition is translated** into OpenCode's `agent/<name>.md` layout with
  JSON frontmatter (`mode: primary`, `tools:` as a `{toolID: bool}` record). Claude tool names are
  mapped to OpenCode tool ids; names without an OpenCode equivalent are dropped with a warning.
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
