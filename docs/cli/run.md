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
| `--runtime` | Override the agent runtime from `config.yaml` for this run (`claude`, `pi`, `dummy`); also `FULLSEND_RUNTIME` |
| `--model` | Override the harness/agent model for this run (alias, model id, or `provider/id` on pi); also `FULLSEND_MODEL` |
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

The runtime for a run is resolved once, in this order: `--runtime` flag, `FULLSEND_RUNTIME`, `runtime:` on the agent's `agents:` entry in `config.yaml` / `.fullsend/config.yaml`, the repo-wide `runtime:` there, then the built-in `claude`. The same order applies to the model (`--model`, `FULLSEND_MODEL`, `model:` on the agent's `agents:` entry, harness `model:`, agent frontmatter; `FULLSEND_PI_MODEL` is a lower-precedence alias on pi) and to effort (`--effort`, `FULLSEND_EFFORT`, `effort:` on the agent's `agents:` entry, harness `effort:`). `<agent>` is the name given to `fullsend run` (`triage`, `code`, …); see [Runtimes — per-agent settings](../runtimes.md#per-agent-runtime-model-and-effort). `FULLSEND_FALLBACK_MODELS=a,b` becomes Claude Code's `--fallback-model`; pi ignores it with a warning.

The plan block prints `Runtime: <name> (from <source>)` and, when an override applied, `Model: <value> (from <source>)`; stderr carries `runtime: selected "<name>" from <source>` (and `model: requested "<value>" from <source>`) for scripts. A value from the config file is labelled with the file path, suffixed ` agents.<name>` when the agent's entry decided. An invalid override (unknown runtime, unknown effort level, an `agents:` entry that names no agent) fails before the sandbox is created.

```bash
# try a repo's triage on pi with Gemini Flash, without touching its config
fullsend run triage --fullsend-dir . --target-repo ../repo \
  --runtime pi --model google-vertex/gemini-2.5-flash --effort medium
```

## Output artifacts

Each run produces artifacts in the output directory:

| File | Description |
|------|-------------|
| `metrics.json` | Behavioral metrics: tokens, cost, model, runtime, iterations |
| `transcripts/` | Agent conversation transcripts |
| `claude-debug.log` or `pi-debug.log` | Debug log (when `--debug` is set) |

### metrics.json fields

| Field | Description |
|-------|-------------|
| `runtime` | Runtime that executed the run (e.g. `claude`, `pi`) |
| `model` | Model the provider reported using |
| `requested_runtime` | Runtime selected for the run (config file, or a `--runtime`/`FULLSEND_RUNTIME` override) |
| `requested_model` | Model the harness/agent requested |
| `override_source` | Where `requested_model` came from (`--model flag`, `FULLSEND_MODEL`, `FULLSEND_PI_MODEL`, `<config path> agents.<name>`, `harness`, `default`) |
| `runtime_source` | Where `requested_runtime` came from (`--runtime flag`, `FULLSEND_RUNTIME`, the config file path — suffixed ` agents.<name>` when the agent's entry decided — or `default (config not found)`) |
| `total_cost_usd` | Total inference cost |
| `num_turns` | Number of conversation turns |
| `iterations` | Number of retry iterations |

## Related

- [Running Agents Locally](../guides/user/running-agents-locally.md) for a step-by-step walkthrough
- [Runtimes](../runtimes.md) for runtime selection and capabilities
- [CLI internals](../guides/dev/cli-internals.md) for the full command tree
