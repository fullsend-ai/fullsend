# Claude Code

[Claude Code](https://claude.com/claude-code) is fullsend's default runtime. Every role is supported,
and nothing needs configuring to use it — this page is the operational detail once you are on it.

```bash
fullsend run triage --model opus --effort high
```

Choosing between runtimes is in [Agent runtimes](../runtimes.md). Selection, precedence and the
config keys live there too.

## Models

Pass an alias or a model id; Claude Code resolves aliases natively.

| Alias | Resolves to |
|---|---|
| `opus`, `sonnet`, `haiku`, `fable` | the current Anthropic model of that tier, as the pinned Claude Code version defines it |

By default all inference goes to Anthropic models on Vertex AI, on the fleet's WIF credentials.
Vertex enables models **per project**, so the tier's current model is not always one your project
can serve; a run that asks for a model the project cannot serve fails at the first model call. When
a specific generation matters, name the id. The one other supported route is the
[inference gateway route](#inference-gateway-route), selected by a `gateway/<model>` model.

**Per-repo alias overrides.** Point an alias at a different model for this repo with
`models.aliases` in `.fullsend/config.yaml` (`sonnet: claude-sonnet-5`); the run then passes that id
to `--model` and `--fallback-model`. Syntax, rules and what the plan block shows are on the
[pi page](pi.md#per-repo-alias-overrides) — it is the same block. It covers the run's own model only:
sub-agent `model:` frontmatter is resolved by Claude Code itself, so a sub-agent that needs a
specific generation names the id.

**Fallback chains.** `FULLSEND_FALLBACK_MODELS=a,b` becomes `--fallback-model a,b`, tried in order
when the primary model is overloaded or retired. pi uses the same list differently: the runner
retries an alias request on the next model when Vertex answers that it does not serve the first, for
the top-level run only; see [pi](pi.md#at-a-glance).

## At a glance

| | |
|---|---|
| Roles | All, including `review` and `retro` — they need sub-agents |
| Credentials | Vertex: WIF `external_account` + a refreshed OIDC token. Gateway route: a run-scoped placeholder for the forge OIDC token or the gateway API key. What the launch clears is listed under [Environment the launch clears](#environment-the-launch-clears) |
| Unattended | `--dangerously-skip-permissions`; hooks wired from the harness, never from agent-writable files |
| Artifacts | `output.jsonl`, transcripts, `metrics.json` with `runtime: claude`, and `claude-debug.log` with `--debug` |
| Effort | `--effort low..max` |

## Inference gateway route

The runner can send Claude Code's requests through a self-hosted, Anthropic-compatible inference
gateway ([ADR 0137](../ADRs/0137-inference-gateway-credential-route.md)) instead of Vertex. The
gateway holds the upstream key, and the sandbox only ever holds a placeholder.

**Selecting it.** Give the run a `gateway/<model>` model (harness `model:`, `--model`, an `agents:`
entry, a `models.aliases` target, or the agent's frontmatter) and configure an `inference.gateway`
block ([config reference](../reference/config-reference.md)). Claude Code is given `<model>`: the
prefix only selects the route, so `gateway/claude-haiku-5-5` calls `claude-haiku-5-5` and
`gateway/vendor/org/model` calls `vendor/org/model`. Claude Code needs no model list, so the block's
`models` and `models_file` are not read.

- With no block, a `gateway/` model is an error.
- In the `oidc` mode the block applies only on a run with a forge OIDC endpoint, so a local run with
  a `gateway/` model is an error. Use `auth: api-key` for a local run.
- Other Claude models in a run with a block keep the Vertex route. The Vertex credential check and
  the Vertex host-file requirement are skipped only for gateway runs.

**What the runner owns.** For gateway runs, after the agent-writable `.env` (and the harness
`env.sandbox` and host-file env files it sources), the launch clears the variables listed
[below](#environment-the-launch-clears) and exports:

- `ANTHROPIC_BASE_URL`: the block's `url` origin;
- `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, without which Claude Code also dials
  `api.anthropic.com`;
- the credential, per mode (below).

`.env` cannot override any of these. A variable that `.env` makes `readonly` stops the launch rather
than reaching Claude Code. The placeholder is read from the sandbox before `.env` runs.

**Credential per `auth` mode.** Gateways differ in which header they accept. For example, one tested
gateway refuses `Authorization: Bearer` on `/v1/messages` for static keys. Check that your gateway
accepts the header for your mode:

| Mode | How Claude Code gets it | Header sent |
|---|---|---|
| `oidc` (default) | an `apiKeyHelper` (`command -p cat` of a runner-seeded token file) in the `--settings` file, with `CLAUDE_CODE_API_KEY_HELPER_TTL_MS=10000` | `Authorization: Bearer` |
| `api-key` | `ANTHROPIC_API_KEY` set to the placeholder for `FULLSEND_INFERENCE_GATEWAY_API_KEY` | `x-api-key` |

In the `oidc` mode the token is the job's forge OIDC token, which lives about 300 s. Each refresh
pins a new placeholder generation, so Claude Code cannot keep one in `ANTHROPIC_API_KEY`. Instead,
the runner re-seeds the token file after every refresh, as it does for pi. Claude Code (verified on
2.1.295) re-runs the helper once its TTL has passed, using the cached value for one more request
while it does, and drops the cached value on a 401. The runner hands each new placeholder over at
least the refresh work plus a safety margin before the old token expires. A 10 s TTL keeps that
inside the margin, so a run longer than one token lifetime keeps working. If a hand-off does not
land, the route fails closed at the expiry of the token Claude Code holds.

**Egress.** The runner imports a per-host profile for the gateway, rendered for Claude Code and the
auth mode. It allows `POST /v1/messages` and `POST /v1/messages/count_tokens`, from the binaries
`**/claude`, `**/claude.exe` (the sandbox image's binary) and `**/node`. Its credential metadata
names the mode's header. OpenShell resolves the placeholder only on requests to that host and path.

**Limits.**

- Sub-agents inherit the base URL and the credential. A sub-agent's `model:` alias (`sonnet`,
  `haiku`) is resolved by Claude Code to its own Anthropic id and sent to the gateway as that id.
  Use `inherit`, or an id your gateway serves.
- Model discovery and the interactive model picker are not used.
- `fullsend inference gateway status` reports this route and its header.

**Do not point Claude Code at an endpoint by hand.** Setting `ANTHROPIC_BASE_URL` through harness
`env.sandbox` with a provider that supplies `ANTHROPIC_API_KEY` reaches Claude Code on non-gateway
runs (see [below](#environment-the-launch-clears)), but it is discouraged:

- It needs a long-lived API key stored somewhere. The Vertex route and the gateway's `oidc` mode
  store no reusable credential.
- fullsend does not test it, so behaviour built around Anthropic models on Vertex may be wrong:
  cost reporting, model alias resolution, sub-agent model tiers and fallback chains.

Use the gateway route above. For Claude models, the default Vertex route is the other choice.

## Environment the launch clears

The launch sources the sandbox `.env` and then clears variables before it starts Claude Code.
`.env` carries the harness `env.sandbox` values, the host-file env files it sources, and anything
the agent wrote there between iterations. The rest of the environment, including provider
credentials, comes from the sandbox.

| Run | Cleared after `.env` | Sources it covers |
|---|---|---|
| Every run | `LD_PRELOAD`, `LD_LIBRARY_PATH`, `LD_AUDIT`, `PYTHONPATH`, `PYTHONHOME`, `PYTHONSTARTUP`, `NODE_OPTIONS`, `NODE_PATH`, `BUN_OPTIONS`: code-loading variables, not routing ones | all |
| Gateway runs | `ANTHROPIC_BASE_URL`, `ANTHROPIC_UNIX_SOCKET`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_CUSTOM_HEADERS`, `ANTHROPIC_IDENTITY_TOKEN`, `ANTHROPIC_IDENTITY_TOKEN_FILE`, `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR`, `CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR`, `CLAUDE_CODE_API_KEY_HELPER_TTL_MS`, and the provider selectors `CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_FOUNDRY`, `CLAUDE_CODE_USE_MANTLE`, `CLAUDE_CODE_USE_ANTHROPIC_AWS`, `CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD`, `CLAUDE_CODE_USE_GATEWAY`; then the runner exports its own values | all: `env.sandbox`, host-file env files, the agent's `.env` writes, and providers |

On non-gateway runs no `ANTHROPIC_*` variable is cleared. A value the harness sets is honoured.
Model-choice variables (`ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_*_MODEL`) are never cleared. The
`env` block of the repository's own `.claude/settings.json` is read by Claude Code itself, after
launch. On every route, the egress profile and the endpoint-bound placeholder are what keep the
credential on its host.

## Behaviour differences worth knowing

These are the places Claude Code differs from pi — useful when comparing a run across runtimes.

- **The agent definition *replaces* the system prompt.** `--agent` makes the agent `.md` body the
  system prompt outright. pi appends it to its own default instead, so an agent that relies on
  Claude Code's exact framing can read differently there.
- **Native sub-agents** via the `Agent` tool. Their tokens and cost are included in the
  `metrics.json` totals and broken down per model in `per_model_usage`, read from the result's
  `modelUsage` (see [metrics.json fields](../cli/run.md#per-model-usage)).
- **A `CLAUDE.md` bridge is injected** when the repo has `AGENTS.md` but no `CLAUDE.md`, because
  Claude Code auto-loads only the former. pi reads `AGENTS.md` natively and needs no bridge.
- **`tools:` is enforced unreliably** (≥ 2.1.119); pi enforces its `--tools` allowlist strictly. In
  both cases the sandbox, not the tool list, is the boundary
  ([ADR 0027](../ADRs/0027-allowed-and-disallowed-tools-for-agents.md)).
- **Failed tool calls cannot be rewritten.** Claude Code fires `PostToolUse` only on success; a
  failed call goes to `PostToolUseFailure`, which accepts no output rewrite. Secrets or control
  characters in a failed command's output are detected and logged, and the agent is warned, but they
  reach the transcript unmasked. pi sanitizes those too.
- **The repo's own `.claude/settings.json` still auto-loads** from the working directory. fullsend's
  hook wiring is passed explicitly with `--settings` so it loads regardless, but repo-supplied hooks
  are a separate exposure to be aware of.

## Troubleshooting

**The model is not what you asked for.** Check `metrics.json`: `requested_model` is what was handed
to the runtime after overrides and `override_source` says where it came from — ending in
`remapped by <config path> models.aliases` when a per-repo alias override applied — so a silent
override is visible after the fact.

**A tool call was blocked.** The security hooks log to `/sandbox/workspace/.security/findings.jsonl`
inside the sandbox. A blocked tool reports its reason in the transcript; an allowlist mismatch names
the offending tool and the expected vocabulary.

**A harness plugin did not load.** When Claude Code reports a plugin in the `plugin_errors` of its
startup `system`/`init` event, the run output prints one `Plugin <name> failed to load …` warning per
entry, with the error category and message. It also shows the plugin directory when Claude Code
names one (2.1.283 and later, for `--plugin-dir` entries). The run continues: the warning is the
signal to check the plugin's path and `plugin.json`.

**Output looks truncated or condensed.** The PostToolUse chain condenses verification-command output
only on positive evidence of success, and attaches a note saying it did. Anything carrying a failure
marker passes through untouched.

## See also

- [Agent runtimes](../runtimes.md) — choosing and selecting a runtime
- [Pi](pi.md) — the second runtime, for Grok and Gemini
- [Running agents locally](../guides/user/running-agents-locally.md) — local runs
