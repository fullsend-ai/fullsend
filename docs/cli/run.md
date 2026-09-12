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
| `iterations` | Number of agent iterations run; an iteration killed at the budget is not retried (see [Budget and deadline](#budget-and-deadline)) |
| `per_model_usage` | Per-model-spec breakdown, present only when a runtime reports one (today: `pi` with the `Agent` tool enabled). See below |

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

## Budget and deadline

Each agent iteration gets the harness's `timeout_minutes` (30 when it sets none). When the budget
is spent the runner ends the iteration and sweeps the processes the agent left running in the
sandbox, best effort. Before every iteration it tells the agent when that will happen, through two
environment variables set on every runtime (claude, pi, codex):

| Variable | Value |
|---|---|
| `FULLSEND_TIMEOUT_MINUTES` | The budget: the harness's `timeout_minutes`, or `30` when it sets none |
| `FULLSEND_ITERATION_DEADLINE` | Unix time (seconds) at which the running iteration is killed |

**Example.** A probe that shows both, and what a kill looks like. The agent prints the variables
with its own clock, then sleeps past the budget:

```markdown
# Budget probe (timeout)

Do exactly these two steps with the Bash tool, nothing else. Do not write any output file.

1. Run: `env | grep -E '^FULLSEND_(TIMEOUT_MINUTES|ITERATION_DEADLINE)=' | sort; date +%s`
2. Run: `sleep 600`

Say nothing before step 1. After step 2 finishes, reply with the single word DONE.
```

Its harness is a copy of the review harness with these fields changed. Keep `role` as it is:
the mint issues the agent's token by `role`, which must be one it serves, and never reads `slug`
(an install-time hint for `fullsend github setup`). The probe ran locally without a mint.

```yaml
agent: agents/probe-timeout.md
role: review            # unchanged
slug: probe-timeout
validation_loop:
  script: scripts/validate-output-schema.sh
  schema: schemas/review-result.schema.json
  max_iterations: 2
timeout_minutes: 1
```

```console
$ fullsend run probe-timeout --fullsend-dir "$PROBE" --target-repo "$TARGET" \
    --env-file "$PROBE_ENV" --forge github --no-post-script
```

**What you see.** The agent's first step printed:

```console
FULLSEND_ITERATION_DEADLINE=1788632648
FULLSEND_TIMEOUT_MINUTES=1
1788632601
```

The last line is the agent's `date +%s`: the deadline is 47 seconds ahead of it. The runner's
heartbeat counts down to the same instant. At the budget the runner ends the exec (it records exit
code `-1`), sweeps the processes the agent left running in the sandbox (best effort), extracts what
the agent wrote, and the loop stops after the first iteration:

```console
  ⏳ Agent running (30s elapsed, 30s remaining)
  ⏳ Agent running (1m0s elapsed, 0s remaining)
  ! Agent exited with code -1
    Agent exceeded its budget; terminating its processes in the sandbox
  Terminated 4 stray process(es) left running by the timed-out iteration (200ms)
  ...
  ✗ Validation failed: FAIL: output/agent-result.json not found
  ! Agent timed out (used 1m0s of 1m0s budget) — not retrying
  ...
    Agent runs: 1
    Validation: failed
  ...
Error: agent timed out after 1m0s without completing (timeout: 1m0s)
```

For comparison:

- **Before this rule**, the same harness ran `Iteration 2 of 2`, was killed again, and ended with
  `Agent runs: 2` and `Error: validation failed after 2 iteration(s)`.
- **On `--runtime pi`** the run ends the same way, including the sweep. The pi agent handed the
  probe's commands to a sub-agent, and that child printed both variables with a deadline 45 seconds
  ahead of its clock: children inherit the values.
- **On `--runtime codex`** (`--model openai/gpt-5.6-luna`) the same: both variables printed with the
  deadline 50 seconds ahead, `Terminated 5 stray process(es) left running by the timed-out iteration`,
  one iteration, the same error.
- **A validation failure still retries.** A control agent that exits at once without a usable result
  ran both iterations under `timeout_minutes: 5`; the deadline it printed moved from `1788630695`
  in the first iteration to `1788630719` in the second, five minutes after each start.
- **On the issue or pull request**, the completion status comment shows the run's error text as
  its failure detail, so the timeout is visible without a post-script. The post-script itself is
  skipped on a failed run, as for any other failure.

**Rules.**

- A retry (`validation_loop.max_iterations`) is for output the agent finished but validation
  rejected. An iteration the runner killed is not retried, because the next one would replay the
  same run with the same budget. The run ends with
  `agent timed out after <elapsed> without completing (timeout: <budget>)`.
- "Killed" means: the iteration failed — non-zero exit, or exit 0 with an error reported in its
  transcript — after 90 % or more of the budget. This is the same test the no-loop path has used
  since #5075. An agent that fails deliberately in the last tenth of its budget is reported as
  timed out too, and a late API error does not get a retry.
- A valid result wins. The killed iteration's output is still validated; if the agent wrote a
  result that passes before the kill, the run succeeds.
- An agent that exits early with invalid output is a validation failure and keeps its retry. When
  every iteration fails that way the run ends with `validation failed after N iteration(s)`.
- The kill lands at the deadline or a few seconds after it. Treat the deadline as the hard stop and
  write your result before it.
- The budget ends the agent's exec, not its processes: OpenShell has no per-exec kill by design (an
  exec's processes are not expected to exit with the caller,
  [NVIDIA/OpenShell#3159](https://github.com/NVIDIA/OpenShell/issues/3159)), so the
  runner sweeps the processes the agent left running before it extracts the output, under
  `--keep-sandbox` too. The sweep is best effort, like the one between iterations: a sweep that
  fails prints `Warning: could not terminate stray sandbox processes` and the run continues.
- Child processes inherit both variables. In a pi sub-agent read the deadline, not the budget: the
  deadline is the same wall-clock limit the parent has, while `FULLSEND_TIMEOUT_MINUTES` is the
  parent iteration's whole budget, not the time the child has left.
- Both names are reserved: an `env.sandbox` entry with either name is dropped. Pre-, post- and
  validation scripts run on the host and do not see them.

**Pacing from a skill.** Read the deadline, not the budget, and keep the arithmetic in two steps —
the sandbox's command scanner blocks a `$( )` substitution nested inside `$(( ))`:

```bash
now=$(date +%s)
remaining=$(( FULLSEND_ITERATION_DEADLINE - now ))
echo "remaining=$remaining"
```

Run under a three-minute budget by a probe agent, about twenty seconds after its start:

```console
remaining=161
```

Skills in the [agents repository](https://github.com/fullsend-ai/agents) that mirror the budget
through a harness `TIMEOUT_SECONDS` variable and their own start time can read
`FULLSEND_ITERATION_DEADLINE` instead; the mirror becomes redundant once a runner with these
variables is deployed.

**If it goes wrong.**

| You see | Meaning | Do |
|---|---|---|
| `agent timed out after 20m0s without completing (timeout: 20m0s)` | The agent was killed at the budget and no iteration produced a valid result. | Raise `timeout_minutes`, or make the agent write its result before `FULLSEND_ITERATION_DEADLINE`. |
| `validation failed after 2 iteration(s)` with `Agent exited with code 0` above it | The agent finished in time; its output failed validation on every iteration. | Read the `Validation failed:` lines and fix the output, not the budget. |
| `Could not export the iteration deadline: exit 1: ...` | The runner could not write the variables into the sandbox. The iteration runs without them; the runner still kills it at the budget. | Usually a non-writable sandbox workspace; a sandbox that died mid-run looks the same. Check the sandbox logs and the image. |
| `clearing stale iteration deadline (iteration N): ...` | The write failed and the previous iteration's file could not be removed either. The run stops rather than let the agent read a stale deadline. | Same as above. |
| `FULLSEND_ITERATION_DEADLINE` unset inside the agent | The agent's shell was started without sourcing `/sandbox/workspace/.env`. | Runtimes that fullsend ships always source it; a custom command must do the same. |

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
