# Codex

[Codex](https://github.com/openai/codex) is fullsend's third agent runtime. It runs **OpenAI models
only**, through the same sandbox, egress policy and secretless credential path pi uses for GPT — the
runner holds the credential, the sandbox never sees it. Turn it on for one repo, for one agent, or
as a `repos.yaml` default.

```bash
fullsend run triage --runtime codex --model openai/gpt-5.6-luna
```

Selecting it, and how it compares to the other runtimes, is in [Agent runtimes](../runtimes.md).
This page is what changes once you are on it.

## Models

**Codex takes an OpenAI model id** — `openai/<id>` or the bare id — and nothing else. It serves the
OpenAI Responses API, so there is no Claude, Gemini or Grok on codex.

**The Claude aliases do not apply.** `opus`, `sonnet`, `haiku` and `fable` name Anthropic models, so
codex refuses them rather than picking a GPT model on your behalf:

```
codex takes OpenAI model ids only, and the Claude model aliases do not apply to it: "opus" is one
of them. To run this agent on codex, set FULLSEND_CODEX_MODEL=openai/<id> for the repo, or
model: openai/<id> on the agent's agents: entry or the harness
```

A model carrying another provider's prefix, and a run with no model named at all, fail the same way
and name the same two fixes. Nothing is remapped: the per-repo `models.aliases` overrides are not
consulted on codex either, so a Claude alias can never resolve to a GPT model behind your back.

That matters because the fleet harnesses ship `model: opus`. You do not need to edit them — there
are two places to name a model for a repo on codex.

**A default for every agent in the repo,** set on the runner:

```bash
FULLSEND_CODEX_MODEL=openai/gpt-5.6-luna
```

It is read only when codex is the runtime actually selected, and it sits below `--model` and
`FULLSEND_MODEL` in the [usual precedence](../runtimes.md#selecting-a-runtime-and-model) — so it is a
default, not an override. When it decides the model, `metrics.json` records
`override_source: FULLSEND_CODEX_MODEL` and the plan block names it, the same way
`FULLSEND_PI_MODEL` does on pi.

**A model for one agent,** on its `agents:` entry in `.fullsend/config.yaml`, which also outranks the
harness:

```yaml
agents:
  - name: triage
    runtime: codex
    model: openai/gpt-5.6-luna
```

Effort maps onto codex's own reasoning levels:

| `--effort` | Codex `model_reasoning_effort` |
|---|---|
| `low` | `low` |
| `medium` | `medium` |
| `high` | `high` |
| `xhigh` | `xhigh` |
| `max` | `max` |

Codex has no equivalent of a fallback model chain: `FULLSEND_FALLBACK_MODELS` is ignored with a
warning, as it is on pi.

## At a glance

| | |
|---|---|
| Credentials | A runner-exchanged OpenAI WIF token in CI, or your `OPENAI_API_KEY` on the runner locally — never in the sandbox. Codex reads a placeholder from a runner-owned token file and re-reads it when the credential is refreshed ([ADR 0092](../ADRs/0092-openai-wif-credential-delivery.md)) |
| Unattended | Approvals off; codex's own sandbox off, because OpenShell is the boundary. A missing credential exits before the agent starts |
| Artifacts | `output.jsonl` (the `codex exec --json` stream), `transcripts/<agent>-<rollout>.jsonl`, `metrics.json` with `runtime: codex`, plus `codex-debug.log` with `--debug`. Only uncompressed rollouts are extracted — codex compresses older sessions, so a `.jsonl.zst` is never the run's own transcript. The agent's final message is in the stream; `--output-last-message` also drops it in the runner-owned config directory inside the sandbox, which is a convenience when inspecting a kept sandbox rather than a downloaded artifact |
| Extra knobs | `FULLSEND_CODEX_MODEL` (the runner-side model default for codex runs; see [Models](#models)) |
| Not supported | Sub-agents, `plugins:`, fallback chains, non-OpenAI providers |

Cost is **not** in `metrics.json` on codex: the `codex exec --json` stream carries no cost field, so
the value stays `0`. Token counts are recorded normally.

## Running it locally

Complete [Running agents locally](../guides/user/running-agents-locally.md) first — the CLI,
OpenShell, credentials and the fleet clone are the same. Add `OPENAI_API_KEY` to an env file as that
guide's [OpenAI section](../guides/user/running-agents-locally.md#get-an-openai-key-gpt-on-pi-or-codex)
describes, then add `--runtime codex` to any example on it:

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-openai.env \
  --env-file fullsend-triage.env \
  --runtime codex \
  --model openai/gpt-5.6-luna
```

The plan block confirms the selection — overridden values carry their source, harness defaults print
bare — and `metrics.json` records the same (`runtime`, `runtime_source`, `requested_model`,
`override_source`):

```
    Model: openai/gpt-5.6-luna
    Effort: high
    Runtime: codex (from --runtime flag)
...
runtime: selected "codex" from --runtime flag
...
→ Agent: gpt-5.6-luna (v0.152.1)
  ✓ Agent exited with code 0
```

The version on the `Agent:` line is the codex CLI the sandbox image carries, captured by
Bootstrap's `codex --version` preflight — not the model's.

To keep an agent on codex (or off it) without passing flags every time, set `runtime:`/`model:` on
its `agents:` entry in `config.yaml` — see [per-agent
settings](../runtimes.md#per-agent-runtime-model-and-effort).

What a local codex run needs, beyond the guide:

- **fullsend from the release that carries `CODEX_VERSION`** — the first one cut after the codex
  runtime lands. The release download and the container image both work as-is.
- **A sandbox image that includes codex** — `ghcr.io/fullsend-ai/fullsend-sandbox` built with
  `CODEX_VERSION` (0.152.1 today). A stale image fails Bootstrap's preflight before the agent
  starts, with ``codex preflight: `codex --version` exited 127``; `podman pull
  ghcr.io/fullsend-ai/fullsend-sandbox:latest` fixes it.
- **A harness that declares the provider and a policy** — `providers: [openai]` and
  `policy: policies/base.yaml`. The fleet's agents already carry both; a custom harness needs the
  policy because the sandbox image's default policy leaves an uninspected route to `api.openai.com`,
  which the gateway refuses to carry the credential over.
- **Debugging** — `--debug='*'` (the `=` is required); sandbox-side failures land in
  `codex-debug.log` inside the run directory, next to the transcripts, not in the runner's output.

- **Platforms** — the runtime was brought up and smoke-tested on macOS Apple Silicon (podman
  machine, Homebrew `openshell`, arm64 image); the local guide's platform notes otherwise apply
  unchanged.

## Behaviour differences worth knowing

- **No permission prompts.** Codex, like pi, expects to run inside a container: the sandbox and its
  egress policy are what contain the agent, not a tool-approval dialog
  ([ADR 0027](../ADRs/0027-allowed-and-disallowed-tools-for-agents.md)).
- **Reads `AGENTS.md` natively** — no `CLAUDE.md` bridge is injected.
- **The repository's own `.codex/` is never loaded.** Codex reads a project config layer only for a
  directory it has been told to trust, and fullsend does not trust the cloned repo — so a target
  repo cannot change how the agent runs.
- **Two tools, not a menu.** Codex works through a shell and `apply_patch`, so a harness `tools:`
  list has no native allowlist to map onto. A `Bash(...)` allowlist is **recorded but not enforced**
  on codex, and the run says so:

  ```
  Agent Bash allowlist (gh, curl, jq) is recorded but not enforced on codex
  (see docs/contributing/runtime-implementation.md)
  ```

  Entries with no codex equivalent are dropped, each with its own note — `WebFetch`, for instance,
  because codex reaches the web through the shell.
- **A blocked tool call reads as *declined*, not failed.** A PreToolUse block reaches the model as
  `Command blocked by PreToolUse hook: <reason>`, so the agent understands it was refused rather
  than that the command broke — in a smoke run it summarised the block as "blocked by a safety hook"
  and moved on.
- **The security hooks block a tool result or warn about it; they never edit it.** Codex does not
  let a hook replace the output of a built-in tool, so where another runtime would hand the model a
  redacted result, codex either withholds it entirely or passes it through with a note saying what
  it contained. Your run artifacts are redacted either way — `output.jsonl`, the transcripts and
  `codex-debug.log` are all scrubbed before they are written.
- **Skills** work as they do on Claude Code: the harness's skills, plus your repository's own
  `.agents/skills`, both scanned for injected content before the agent sees them. Codex's bundled
  skills (`skill-installer`, `imagegen` and friends) are switched off, so an agent sees only yours.
- **No cost in metrics** — see [At a glance](#at-a-glance).

## Not yet exercised

**Start on a disposable repo,** with `triage` or `prioritize` before `code` or `fix`. Codex has been
run end to end by hand — the fleet's own `triage` and `review` harnesses, on `openai/gpt-5.6-luna`,
on macOS — but not yet through a full fleet lifecycle, and not yet on the CI credential path: the
Workload Identity route needs an OpenAI organization mapped to the repositories, which does not
exist yet, so local runs use `OPENAI_API_KEY` on the runner. Until that mapping exists codex also
has no default behaviour-test coverage; its scenario is gated. What was run, and on which versions,
is recorded in [codex runtime
internals](../contributing/runtime-implementation.md#codex-runtime-internals-6920).

**Keep `review` and `retro` on Claude Code.** Codex has a `spawn_agent` tool, but fullsend does not
build a persona roster for it yet, so those two agents run in a single context instead of with their
reviewer personas. Nothing prevents a repo-wide `runtime: codex` from applying to them — they will
run — so pin them with `runtime: claude` on their `agents:` entries if you want the roster:

```yaml
agents:
  - name: review
    runtime: claude
```

## Troubleshooting

**``codex preflight: `codex --version` exited 127``, or `fullsend: codex not found on PATH`
(exit 127) once the run starts.** The sandbox image predates the `CODEX_VERSION` pin, so there is no
codex to run. Pull a current `ghcr.io/fullsend-ai/fullsend-sandbox` image.

**Denied requests in the sandbox egress log at startup.** Expected noise, not failures. Codex
probes on the way up and the policy refuses what the run does not need:

| Denied | Why it appears |
|---|---|
| `GET /v1/models` on `api.openai.com` | Codex refreshes its model catalog on a custom provider. The `fullsend-openai` profile allows only `POST /v1/responses`, so the probe is denied at L7 — once, plus an immediate retry. The first allowed `POST` follows about 100 ms later. |
| `chatgpt.com:443` | A sign-in/account probe the agent run has no use for; denied at L4. |
| `api.github.com:443` | Denied at L4 from codex itself — the agent reaches GitHub through the `gh` CLI and its own provider, not from the model client. |

None of these stop the run. What *would* is a denial on `POST /v1/responses`, which means the
profile or the policy is wrong.

**``provider auth command `...` ...``** — codex's own wording, one of `exited with status N`,
`failed to start`, `timed out after N ms`, `produced an empty token`, or `wrote non-UTF-8 data to
stdout`. Codex could not read the credential: the runner-owned token file is missing, or it does not
hold a gateway placeholder. Check that the harness declares `providers: [openai]` and that
`OPENAI_API_KEY` reached the **runner**, not the sandbox.

**The run stops before the agent starts, naming `api.openai.com`.** The effective sandbox policy
admits `api.openai.com:443` without protocol inspection, so the gateway refuses to carry the
credential over it. Add `policy: policies/base.yaml` to the harness.

**`codex takes OpenAI model ids only ...`.** The resolved model is a Claude alias (`opus` and
friends), carries another provider's prefix, or is missing entirely. The message names both fixes:
`FULLSEND_CODEX_MODEL=openai/<id>` for the repo, or `model: openai/<id>` on the agent's `agents:`
entry or the harness. See [Models](#models).

**A guard refused the run.** The runner-written files under `CODEX_HOME` are checked before every
launch, and a mismatch stops the run rather than continuing unprotected. Each message names what
failed:

```
fullsend: codex config, hook adapter or auth script missing or modified; refusing to run
```
```
fullsend: the sandbox hook scripts are not the set fullsend installed (a file was changed,
replaced with another allowed file, or something was added); refusing to run
```
```
fullsend: codex config.toml or hooks.json is not the file fullsend wrote; refusing to run (a
rewritten config can trust the target repo, which loads its .codex/ layer and its hooks)
```

The first two exit 97 and mean the hook wiring cannot be trusted; the third exits 98 and is the
credential-and-trust guard. All three are fail-closed by design. Between iterations the agent runs
as the same user as those files, so "did the agent write there?" is the question to ask first.

**`--debug "..."` fails with `accepts 1 arg(s)`.** `--debug` takes an optional value: write
`--debug='*'` (with `=`).

**The model answers, then the run fails anyway.** A model your account cannot serve on the Responses
API surfaces as several `error` reconnect events and a `turn.failed` carrying the 404 — not as a
startup error. `codex exec` can still exit 0 there, so fullsend takes the verdict from the stream
rather than the exit code, and the run fails as it should.

**`Run-scoped provider ... expired in place instead of deleted`.** The run finished and deleted its
sandbox, but OpenShell still reported the provider attached to it, so fullsend expired the
credential rather than leaving a live one behind and printed the `openshell provider delete` command
to finish the job. The credential is dead either way; run that command to tidy up. Under
`--keep-sandbox` the same expire-in-place is deliberate, because the sandbox you kept still
references the provider.

**The agent fails with nothing in the terminal.** Codex has no debug flag of its own, so
`--debug='*'` makes the runner export `RUST_LOG` and capture codex's stderr to `codex-debug.log` in
the run directory, next to the transcripts. Kept sandboxes must be removed manually
(`openshell sandbox delete <name>`).

**The run used Claude instead of codex.** The runtime falls back to `claude` when neither the
config's `runtime:` (repo-wide or on the agent's `agents:` entry) nor `--runtime`/`FULLSEND_RUNTIME`
selects codex; the plan block's `Runtime:` line and stderr's `runtime: selected ...` show which one
ran and why.

## See also

- [Agent runtimes](../runtimes.md) — choosing and selecting a runtime
- [Running agents locally](../guides/user/running-agents-locally.md) — the local-run flow that [Running it locally](#running-it-locally) builds on
- [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md) — the CI credential path
- [codex runtime internals](../contributing/runtime-implementation.md#codex-runtime-internals-6920) — config layers, the hook adapter contract, and what to re-check on a `CODEX_VERSION` bump
- [ADR 0099](../ADRs/0099-codex-agent-runtime.md) — why codex uses a custom model provider, a runner-seeded token file and a translating hook adapter
