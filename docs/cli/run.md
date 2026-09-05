---
sidebar_label: fullsend run
---

# fullsend run

Execute an agent locally in a sandbox. `fullsend run` resolves the agent harness, provisions a sandbox container, and runs the agent to completion.

## Usage

```bash
fullsend run <agent-name> [flags]
```

## Flags

| Flag | Description |
|------|-------------|
| `--fullsend-dir` | Path to the `.fullsend` configuration directory |
| `--runtime` | Override the agent runtime from `config.yaml` for this run (`claude`, `pi`, `codex`, `dummy` or `dummy-playback`); also `FULLSEND_RUNTIME` |
| `--model` | Override the harness/agent model for this run (alias, model id, or `provider/id` on pi and codex — codex takes OpenAI ids only); also `FULLSEND_MODEL` |
| `--effort` | Override the harness effort level for this run (`low`…`max`); also `FULLSEND_EFFORT` |
| `--output-dir` | Base directory for run output (default: `/tmp/fullsend`) |
| `--target-repo` | Path to the target repository |
| `--fullsend-binary` | Path to a Linux fullsend binary to copy into the sandbox |
| `--env-file` | Load environment variables from a dotenv file (repeatable) |
| `--no-post-script` | Skip post-script execution |
| `--keep-sandbox` | Skip sandbox deletion after the run |
| `--debug [filter]` | Enable agent runtime debug logging with optional category filter (e.g. `"api,hooks"`) |
| `--forge` | Forge platform to use (e.g. `"github"`, `"gitlab"`); auto-detected from CI env vars when omitted |
| `--offline` | Reject network fetches; only use cached remote resources |
| `--max-depth` | Maximum dependency depth for transitive resolution (0 disables) |

## Plan block

At startup, `fullsend run` prints a plan block summarizing the resolved configuration:

```
Agent:     code
Role:      code
Model:     sonnet
Effort:    high
Runtime:   claude (from /path/to/.fullsend/config.yaml)
Image:     fullsend-sandbox:latest
```

The **Runtime** line shows which runtime was selected and the config source it was read from. When no `config.yaml` exists, the source reads `default (config not found)`.

## Runtime selection

The runtime for a run is resolved once, in this order: `--runtime` flag, `FULLSEND_RUNTIME`, `runtime:` on the agent's `agents:` entry in `config.yaml` / `.fullsend/config.yaml`, the repo-wide `runtime:` there, then the built-in `claude`. The same order applies to the model (`--model`, `FULLSEND_MODEL`, `model:` on the agent's `agents:` entry, harness `model:`, agent frontmatter; `FULLSEND_PI_MODEL` on pi and `FULLSEND_CODEX_MODEL` on codex are lower-precedence aliases, each read only when that runtime is the one selected) and to effort (`--effort`, `FULLSEND_EFFORT`, `effort:` on the agent's `agents:` entry, harness `effort:`). `<agent>` is the name given to `fullsend run` (`triage`, `code`, …); see [Runtimes — per-agent settings](../runtimes.md#per-agent-runtime-model-and-effort). `FULLSEND_FALLBACK_MODELS=a,b` becomes Claude Code's `--fallback-model`; pi and codex ignore it with a warning.

The plan block prints `Runtime: <name> (from <source>)` and, when an override applied, `Model: <value> (from <source>)`; stderr carries `runtime: selected "<name>" from <source>` (and `model: requested "<value>" from <source>`) for scripts. A value from the config file is labelled with the file path, suffixed ` agents.<name>` when the agent's entry decided. When `models.aliases` in `.fullsend/config.yaml` remaps the alias, the line keeps the alias and its source and adds the remap: `Model: sonnet (from <source>) → claude-sonnet-5 (from <config path> models.aliases)`, with `model: alias "sonnet" remapped to "claude-sonnet-5" from <config path> models.aliases` on stderr. Aliased entries in the `Fallback models` line show as `alias → id` (`sonnet → claude-sonnet-5, claude-opus-4-6 (from FULLSEND_FALLBACK_MODELS)`); literal ids print as written, and pi ignores the chain with a warning. An invalid override — unknown runtime, unknown effort level, an `agents:` entry that names no agent, or a `models.aliases` key or value the block does not accept — fails before the sandbox is created.

```bash
# try a repo's triage on pi with Gemini Flash, without touching its config
fullsend run triage --fullsend-dir . --target-repo ../repo \
  --runtime pi --model google-vertex/gemini-2.5-flash --effort medium
```

On pi, when the agent's skills ship sub-agent personas, Bootstrap prints the resolved
per-persona table just after this block — one line per persona with the model it resolved to
and which entry decided, plus the blanket `default` used by children that name no persona:

```console
subagents: challenger → xai-vertex/xai/grok-4.6 (from subagents.challenger)
subagents: correctness → anthropic-vertex/claude-opus-4-6 (from frontmatter)
subagents: docs-currency → google-vertex/gemini-3.8-flash (from subagents.docs-currency)
```

A malformed `subagents` key or model reference is rejected by config validation, before the
sandbox is created, like the other invalid overrides above. A key that names no discovered
persona, or a model this run cannot serve, is caught slightly later — at Bootstrap, once the
harness's skills have been read — so the sandbox exists but the agent has not started. See
[pi § Per-persona model configuration](../runtimes/pi.md#per-persona-model-configuration).

## Stall watchdog

The global run timeout is wall-clock, so a wedged agent looks exactly like a thinking one until it expires — and the run is billed for the difference. The watchdog watches the runtime output stream instead: every well-formed line the runtime writes counts as liveness, including lines that map to no agent event (pi's `tool_execution_update` while a tool streams output, Claude Code's `user` tool-result messages, codex's `item.started`/`item.updated`), so an actively streaming tool is never mistaken for a stall. It covers claude, pi and codex — every runtime that streams — and the scripted `dummy` runtimes ignore it. After half of `FULLSEND_STALL_TIMEOUT` of stream silence it warns once (`::warning::no agent events for 7m30s` in CI), and after the full duration it kills the run: first the agent inside the sandbox, through the same TERM-then-KILL sweep that clears stray processes between iterations, then the local `openshell sandbox exec` client. Cancelling the exec is all the global timeout does and it signals nothing inside the sandbox, so the sweep is what actually stops a wedged agent from writing the workspace and spending tokens — including under `--keep-sandbox`, where nothing else would. The run then fails with `agent stalled` and records `"stalled": true` in `metrics.json`.

`FULLSEND_STALL_TIMEOUT` takes a Go duration and defaults to `15m`; `0` disables the watchdog. The default sits above Claude Code's bash ceiling — `BASH_MAX_TIMEOUT_MS` defaults to 600000ms (10 minutes) and the model routinely requests the full ceiling for test suites — so a legitimately quiet long command is not killed as stalled; a repo that raises `BASH_MAX_TIMEOUT_MS` should raise the stall timeout with it. The value must clear the harness `timeout_minutes` by more than the watchdog's polling interval (a twentieth of the stall timeout, capped at 30s): any closer and the global timeout wins the race, so the watchdog is not armed and the run logs that stall protection is inactive. Harnesses with a short `timeout_minutes` — 10 minutes or less — therefore get no stall protection at the default; lower `FULLSEND_STALL_TIMEOUT` for those or accept that the global timeout is the only backstop. A value that is not a duration is reported on stderr and ignored, and the default applies.

## Output artifacts

Each run produces artifacts in the output directory:

| File | Description |
|------|-------------|
| `metrics.json` | Behavioral metrics: tokens, cost, model, runtime, iterations |
| `transcripts/` | Agent conversation transcripts |
| `claude-debug.log`, `pi-debug.log` or `codex-debug.log` | Debug log (when `--debug` is set) |

### metrics.json fields

| Field | Description |
|-------|-------------|
| `runtime` | Runtime that executed the run (e.g. `claude`, `pi`, `codex`) |
| `model` | Model the provider reported using |
| `requested_runtime` | Runtime selected for the run (config file, or a `--runtime`/`FULLSEND_RUNTIME` override) |
| `requested_model` | Model the harness/agent requested |
| `override_source` | Where `requested_model` came from (`--model flag`, `FULLSEND_MODEL`, `FULLSEND_PI_MODEL`, `FULLSEND_CODEX_MODEL`, `<config path> agents.<name>`, `harness`, `default`), suffixed `, remapped by <config path> models.aliases` when a per-repo alias override applied to it |
| `runtime_source` | Where `requested_runtime` came from (`--runtime flag`, `FULLSEND_RUNTIME`, the config file path — suffixed ` agents.<name>` when the agent's entry decided — or `default (config not found)`) |
| `total_cost_usd` | Total inference cost in USD, as reported by the runtime (raw floating-point aggregate across all iterations; no fullsend-side pricing-table fallback). See [Cost data contract](../guides/infrastructure/distributed-tracing.md#cost-data-contract) |
| `num_turns` | Number of conversation turns |
| `iterations` | Number of retry iterations |
| `per_model_usage` | Per-model-spec breakdown, present only when a runtime reports one (today: `pi` with the `Agent` tool enabled). See below |
| `stalled` | Present (`true`) only when the run was killed by the stall watchdog |

#### Per-model usage

A map from pi model spec (`anthropic-vertex/claude-opus-4-6`) to
`{requests, input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens, cost_usd}`.
It exists because a pi sub-agent is a separate `pi` process whose tokens never appear in the
parent's stream, so without it `total_cost_usd` would grow with no way to attribute it.

- **What folds.** Tokens and cost, from both the parent and every child, summed across retry
  iterations. Each iteration contributes one `requests` for the parent plus one per sub-agent call,
  so `requests` counts inference *episodes*, not HTTP requests.
- **The invariant.** The breakdown sums to the run totals for the five fields an entry has:
  `sum(cost_usd) == total_cost_usd`, and likewise for `input_tokens`, `output_tokens`,
  `cache_creation_input_tokens` and `cache_read_input_tokens`. `reasoning_tokens` is a run-level
  total with no per-model counterpart, so it is outside the invariant. The parent's entry is
  recorded on every iteration of an `Agent`-enabled run, including ones that dispatched no
  sub-agent, which is what keeps the invariant true across a retry.
- **What stays parent-only.** `num_turns` and `tool_calls` are read from the parent's stream and are
  not broken down or added to per model — a child's turns and tool calls are recorded in its own
  session transcript (`transcripts/<agent>-sub<seq>-*.jsonl`) instead.
- A model spec of `unknown` is a usage record that carries no model spec at all; its cost is
  bucketed there rather than dropped. A dispatch rejected *before* a model was resolved writes
  no record, so it never reaches the breakdown.

## OpenAI credentials on pi and codex

A `fullsend-openai` provider (`providers: [openai]` on the harness, `openai/<id>` models on pi or codex)
gets its credential from the runner, never from the harness or the sandbox:

| Runner environment | Effect |
|---|---|
| `FULLSEND_OPENAI_AUDIENCE`, `FULLSEND_OPENAI_IDENTITY_PROVIDER_ID`, `FULLSEND_OPENAI_SERVICE_ACCOUNT_ID` | Workload Identity Federation (GitHub Actions only): the run exchanges the job's OIDC token for a short-lived OpenAI token, refreshes it before expiry, and refuses a token whose mapping grants more than model access. All three must be set together; when unset, the `inference.openai` block of `config.yaml` (written by `fullsend github setup --openai-*`) supplies them — except on a machine without a GitHub OIDC endpoint where `OPENAI_API_KEY` is set, which then wins. |
| `OPENAI_API_KEY` | Static key for local runs (used only when the three above are unset). In harness YAML, `env.sandbox` and provider definitions `${OPENAI_API_KEY}` expands to the empty string (like the other runner-only variables), and it is never passed to pre/post scripts; the sandbox sees only the gateway placeholder. |

In CI the run prepares `.fullsend/providers/` from the upstream defaults, so a file there with a
scaffold-shipped name (`openai.yaml`, `github-ro.yaml`, `vertex-ai.yaml`, …) is replaced by the
upstream copy; give repository-specific providers their own file name. A harness that declares the
bare name `openai` with no `providers/openai.yaml` on disk gets the definition built into fullsend;
other bare names still need a file.

Both paths create a provider named after the run and remove it when the run ends. Setup and
troubleshooting: [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md).

## Related

- [Running Agents Locally](../guides/user/running-agents-locally.md) for a step-by-step walkthrough
- [Runtimes](../runtimes.md) for runtime selection and capabilities
- [CLI internals](../guides/dev/cli-internals.md) for the full command tree
