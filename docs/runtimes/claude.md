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
- Other Claude models in a run with a block keep the Vertex route. The Vertex credential check is
  skipped only for gateway runs. A harness `host_files` mount of `${GOOGLE_APPLICATION_CREDENTIALS}`
  that is not `optional` is still required, so mark it optional, or remove it, when you move a
  Vertex harness to the gateway.

**What the runner owns.** For gateway runs, after the agent-writable `.env` (and the harness
`env.sandbox` and host-file env files it sources), the launch clears the variables listed
[below](#environment-the-launch-clears) and exports:

- `ANTHROPIC_BASE_URL`: the block's `url` origin;
- `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, without which Claude Code also dials
  `api.anthropic.com`;
- the credential, per mode (below).

`.env` cannot override any of these. A variable that `.env` makes `readonly` stops the launch rather
than reaching Claude Code. The placeholder is read from the sandbox before `.env` runs.

The repository's own `.claude/settings.json` and `.claude/settings.local.json` can carry an `env`
block, which Claude Code applies after launch over the launch environment. The runner therefore
repeats the route in the `--settings` file it passes, which ranks above both. That file sets
`ANTHROPIC_BASE_URL`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` and, in the `oidc` mode, the helper
and its TTL. It also sets every other variable in the [cleared list](#environment-the-launch-clears)
to `""`, which Claude Code treats as unset. In the `api-key` mode it sets `apiKeyHelper` to `""`,
so a repository helper adds no `x-api-key` value. One exception: in the `api-key` mode
`ANTHROPIC_AUTH_TOKEN` is not pinned, because its value, the placeholder, is known only inside the
sandbox. A repository `env` value for it replaces the Bearer credential. The agent holds no other
credential for the gateway, so this can break the run but not redirect it.

**Credential per `auth` mode.** In both modes the credential is sent as `Authorization: Bearer`,
the header [ADR 0137](../ADRs/0137-inference-gateway-credential-route.md) sets for the route and the
one pi sends, so a gateway reads one header whatever the runtime or mode:

| Mode | How Claude Code gets it | Header sent |
|---|---|---|
| `oidc` (default) | an `apiKeyHelper` (`command -p cat` of a runner-seeded token file) in the `--settings` file, with `CLAUDE_CODE_API_KEY_HELPER_TTL_MS=10000` | `Authorization: Bearer`, plus a copy in `x-api-key` (below) |
| `api-key` | `ANTHROPIC_AUTH_TOKEN` set to the placeholder for `FULLSEND_INFERENCE_GATEWAY_API_KEY` | `Authorization: Bearer` |

`ANTHROPIC_API_KEY`, which Claude Code would send as `x-api-key`, is cleared and never set, so a
`.env` value cannot add a second credential header. A gateway that accepts static keys only in
`x-api-key` is not supported on the route.

In the `oidc` mode Claude Code also sends the helper's value as `x-api-key`: it puts an
`apiKeyHelper` value in both headers. The gateway authenticates the `Authorization` header and must
strip both headers before it calls upstream, as
[ADR 0137](../ADRs/0137-inference-gateway-credential-route.md) requires.

In the `oidc` mode the token is the job's forge OIDC token, which lives about 300 s. Each refresh
pins a new placeholder generation, so Claude Code cannot keep one in an environment variable.
Instead, the runner re-seeds the token file after every refresh, as it does for pi, and hands the new
placeholder over at least the refresh work plus a safety margin before the old token expires
(typically about two minutes). Claude Code (checked on 2.1.296) re-runs the helper when a request
is answered 401 or 403. Otherwise it re-runs it in the background once
`CLAUDE_CODE_API_KEY_HELPER_TTL_MS` has passed, and sends its cached value meanwhile, however old.

**Limit: long gaps between model requests in the `oidc` mode.** A model request after a gap that
spans a hand-off and outlasts the old token still carries the old placeholder. Such a gap is
typically one tool call of more than about 2 minutes. OpenShell refuses to resolve an expired
placeholder and answers `500` (`credential_unavailable`), not 401, so Claude Code retries with the
same value until it gives up, and the run ends with a credential error. This fails closed: no wrong
or expired credential reaches the gateway. For agents with long tool calls, use the `api-key` mode,
whose credential does not rotate. The same runs on pi are not affected: pi reads the token file on
every request. A way to recover from the stale value is tracked in
[#8316](https://github.com/fullsend-ai/fullsend/issues/8316).

**Egress.** The runner imports a per-host profile for the gateway, rendered for Claude Code. It
allows `POST /v1/messages` and `POST /v1/messages/count_tokens`, from the binaries `**/claude` and
`**/claude.exe` (the sandbox image's native binary). OpenShell also admits a process whose ancestor
matches ([egress binary identity](../contributing/runtime-implementation.md#pinned-runtime-binaries-in-the-sandbox-image)),
so the tools Claude Code starts share this access. Its credential metadata names
`Authorization: Bearer`. OpenShell
resolves the placeholder only on requests to that host and path.

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
| Gateway runs | `ANTHROPIC_BASE_URL`, `ANTHROPIC_UNIX_SOCKET`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_CUSTOM_HEADERS`, `ANTHROPIC_IDENTITY_TOKEN`, `ANTHROPIC_IDENTITY_TOKEN_FILE`, `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR`, `CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR`, `CLAUDE_CODE_API_KEY_HELPER_TTL_MS`, `ANTHROPIC_PROFILE`, `ANTHROPIC_FEDERATION_RULE_ID`, `CLAUDE_CODE_GATEWAY_TOKEN_FILE_DESCRIPTOR`, `CLAUDE_CODE_SESSION_ACCESS_TOKEN`, `CLAUDE_CODE_HOST_AUTH_ENV_VAR`, `CLAUDE_CODE_ENABLE_PROXY_AUTH_HELPER`, `AGENT_PROXY_AUTH_TOKEN`, and the provider selectors `CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_FOUNDRY`, `CLAUDE_CODE_USE_MANTLE`, `CLAUDE_CODE_USE_ANTHROPIC_AWS`, `CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD`, `CLAUDE_CODE_USE_GATEWAY`; then the runner exports its own values | all: `env.sandbox`, host-file env files, the agent's `.env` writes, and providers |

On non-gateway runs no `ANTHROPIC_*` variable is cleared. A value the harness sets is honoured.
Model-choice variables (`ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_*_MODEL`) are never cleared or
pinned. On gateway runs the cleared variables are also pinned in the `--settings` file, against a repository
`.claude/settings.json` `env` block (see [What the runner owns](#inference-gateway-route)). On
non-gateway runs that block is read by Claude Code itself, after launch. On every route, the egress profile and the endpoint-bound placeholder are what keep the
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
