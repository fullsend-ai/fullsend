---
title: "137. An inference gateway is a credential route of its own"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - security
  - sandbox
  - credentials
---

# 137. An inference gateway is a credential route of its own

Date: 2026-10-09

## Status

Accepted

## Context

fullsend reaches every model provider directly. For `openai` there are two
routes ([ADR 0092](0092-openai-wif-credential-delivery.md)). The first is a
WIF exchange, which needs admin access to the OpenAI organization. The second
is a static `OPENAI_API_KEY`, which is a long-lived provider key kept in forge
secret storage. Neither route can put an LLM gateway in front of the provider.
Such a gateway is an OpenAI- and Anthropic-compatible front door, such as
agentgateway, Praxis, LiteLLM or APISIX. It checks the job's CI OIDC token against the
forge's JWKS and holds the provider key server-side.

This decision builds on four earlier ones and answers part of a fifth:

- [ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md):
  credential delivery tiers.
- [ADR 0033](0033-per-repo-installation-mode.md): pull-request events read
  config from the base branch.
- [ADR 0073](0073-named-mint-privilege-levels.md): the OIDC request variables
  stay runner-only (`oidcDenyKeys`).
- [ADR 0092](0092-openai-wif-credential-delivery.md): run-scoped provider,
  endpoint-bound placeholder, refresh path.
- [ADR 0069](0069-ready-made-configuration-presets.md): leaves the inference
  authorization model for shared presets open. Here a preset can carry the
  gateway's `url` and `audience`, and the gateway's `repository` check does
  the per-repository enrollment.

A gateway is not a base-URL knob on an existing route. It moves the trust
boundary, so it gets a record of its own.

The route was validated live on 2026-10-09 against two gateways (see
[#7480](https://github.com/fullsend-ai/fullsend/issues/7480)). The client was
pi with [pi-inference-gateway](https://github.com/fullsend-ai/pi-inference-gateway)
v0.1.1, authenticating with a GitHub Actions OIDC token. Each gateway was
configured with remote JWKS, a fixed audience and an exact match on
`repository`:

| Gateway | Models served | Result |
|---|---|---|
| [agentgateway](https://github.com/agentgateway/agentgateway) v1.6.0 | GPT, Claude, Gemini | every API answered |
| [Praxis](https://github.com/praxis-proxy/ai) 0.6.0 | GPT (Responses and Chat Completions) | every API answered |

On both, the negative cases returned 401/403 with no credential echoed:
wrong audience, wrong issuer, an expired token, another repository's token,
and `x-api-key`-only auth. Prompt-cache reads came through both gateways.

## Decision

**An inference gateway is a credential route of its own.** The gateway trusts
the forge's OIDC issuer directly. The runner hands it the job's OIDC token as
the bearer credential. The gateway holds the upstream provider key, so the
forge stores no reusable provider key.

### Route shape

- **No token exchange.** The OIDC assertion is the credential, and its `exp`
  is the credential's expiry.
- **The token is fetched with an explicit audience.** The runner seeds it into
  a run-scoped OpenShell provider as an endpoint-bound placeholder. This is
  ADR 0025 tier 2, the same pattern as ADR 0092.
- **The token is re-seeded before `exp`.** GitHub OIDC tokens are short-lived:
  `exp − iat` is 300 s in GitHub's documented example and on a real token
  checked on 2026-10-09. GitHub does not promise that value, so the runner
  schedules the refresh from each token's own `exp` and `iat` and never
  hard-codes 300 s. The runner fetches a fresh assertion, updates the
  provider, and re-seeds the in-sandbox placeholder through the ADR 0092
  refresh path. The placeholder is pinned per credential generation, so after
  each refresh the runner waits for the new generation and then re-seeds a
  token file that the extension re-reads on every request
  (`INFERENCE_GATEWAY_TOKEN_FILE`, which wins over
  `INFERENCE_GATEWAY_API_KEY`).
- **The refresh margin already adapts to the token lifetime.** ADR 0092's
  `openAIRefreshDelay` caps the refresh lead at half the remaining lifetime
  (`min(margin, remaining/2)`), so for a 300 s token the lead is 150 s, about
  half of `exp − iat`. The gateway route reuses it. What must fit inside that
  lead is the placeholder settle wait (90 s) plus any retries. With a 300 s
  token, any run longer than five minutes depends on this re-seed.
- **The real token stays on the host side of the OpenShell proxy.** This is
  outbound credential isolation: the sandbox sees only the placeholder, and
  the proxy substitutes it only on requests to the gateway host and its model
  API paths: `POST /v1/responses`, `POST /v1/messages` and
  `POST /v1/chat/completions`. It does not cover the gateway's responses,
  which is why the gateway must never reflect caller credentials (see
  Gateway-side requirements).

### Config shape

The route is configured by one `inference.gateway` block in the committed
`.fullsend/config.yaml`:

```yaml
inference:
  gateway:
    url: https://gateway.example.com
    audience: https://gateway.example.com
    models:
      gpt-6-luna: { api: openai-responses }
      claude-haiku-4-5: { api: anthropic-messages }
      example-org/open-model: { api: openai-completions, contextWindow: 131072 }
```

- **Fields.** `url` is https only; loopback is allowed only under test.
  `audience` is the OIDC audience the runner requests, and the token is valid
  only at the gateway. There is no `providers` field: for pi, the `gateway/`
  model prefix opts a model into this route. A field for other runtimes comes
  with their own decisions.
- **The model list takes exactly one of two forms.** Setting both is an error;
  setting neither is a partial block.
  - `models`, inline: a map of model id to settings. `api` is one of
    `openai-responses`, `anthropic-messages` or `openai-completions`, with
    optional `compat`, `contextWindow` and `maxTokens`.
  - `models_file`: a repository path, for example
    `.fullsend/inference-gateway.json`, to a file in the extension's own
    config format. It lets a repository use every per-model key the extension
    supports, plus `include` and `exclude`, without fullsend mirroring that
    schema. It is read from the same ref as `config.yaml`, so pull-request
    events read it from the base branch. It must hold exactly one entry,
    `providers.gateway`, and the runner accepts only `models`, `include`,
    `exclude` and `defaultApi` in it. The runner refuses a file that sets
    `baseUrl`, `baseUrlEnv`, any credential key, `headers`, `authHeader`,
    `modelsPath`, `discovery` or `fallbackModels`: the runner owns those, and
    discovery is off under `PI_OFFLINE`. The runner validates the file and
    renders it into its own guarded `inference-gateway.json`; the agent never
    reads the committed file directly.
- **All or none.** After the layers merge, a block missing `url`, `audience`
  or a model list is an error.
- **Pull-request events read the block from the base branch** (ADR 0033), so a
  pull request cannot redirect its own run.
- **There is no runner-variable form.** The `inference.openai` block has
  `FULLSEND_OPENAI_*` overrides; the gateway block deliberately has no
  equivalent. The committed file is the only source, so it cannot drift from
  a repository variable.
- **The layers merge field by field.** For example, a preset can carry `url`
  and `audience` while each repository opts in its own models.

### Precedence

The order is **gateway, then WIF, then static key, then error**. For pi, the
`gateway/` model prefix selects the gateway route, and `openai/` models keep
the ADR 0092 resolution.

- **A configured gateway never falls back.** If it is unreachable or refuses
  the token, the run fails. Falling back to a static key would mean that key
  could never be deleted.
- **A `gateway/` model needs a complete block.** With no block, or a partial
  one, the run is an error. It does not fall back to the `openai` provider.
- **A block that does not apply to the run is ignored.** For example, a local
  run has no OIDC endpoint. This matches how an inapplicable `inference.openai`
  WIF block is handled today.

### pi reaches the gateway through a separate `gateway` provider

pi calls the gateway through the `gateway` provider id, served by the
pi-inference-gateway extension. It does not redirect pi's built-in `openai`
provider.

| | Redirect built-in `openai` | Separate `gateway` provider |
|---|---|---|
| APIs covered | Responses (GPT) only | Responses, Messages and Chat Completions, chosen per model |
| Model families | GPT | GPT, Claude, Gemini, open-weight |
| Existing guards (`piOpenAIConfigGuard`) | must change | unchanged |
| Validated live | no | yes (2026-10-09 result on #7480) |

This choice has two consequences:

- **The runner renders the model list.** The sandbox runs `PI_OFFLINE=1`, so
  the extension never fetches `/v1/models`. The runner renders
  `inference-gateway.json` from the config block, either from the inline
  `models` map or from the validated `models_file`. The extension also reads
  an `inference-gateway.local.json` overlay that can replace `baseUrl` and
  `headers`, and the pi config directory is agent-writable between
  iterations. So the rendered file is runner-owned and digest-checked twice,
  once before `.env` is sourced and once after, as pi's manifest guard does.
  The guard refuses any `inference-gateway.local.json` and any
  `inference-gateway.json` that is not the runner's.
- **The extension loads only for `gateway/` models.** pi runs with
  `--no-extensions`, so the runner adds the pi-inference-gateway extension
  when the model prefix is `gateway`. Sub-agent model resolution also learns
  the `gateway` provider and takes its model ids from the block.
- **The `anthropic-messages` auth header is `authorization`.** agentgateway
  reads `Authorization: Bearer` by default, and the header is configurable
  with `location`. The runner sets this in the rendered file, so the token never travels in pi's native `x-api-key`
  header.

### Request flow

```mermaid
sequenceDiagram
    autonumber
    participant R as Runner (host)
    participant O as Forge OIDC endpoint
    participant S as OpenShell provider + proxy
    participant P as pi in sandbox
    participant G as Inference gateway
    participant U as Upstream provider
    R->>O: request token (aud = gateway audience)
    O-->>R: OIDC JWT (short-lived, own exp and iat)
    R->>S: seed run-scoped provider with JWT
    S-->>P: endpoint-bound placeholder only
    P->>S: POST /v1/... with placeholder
    S->>G: same request, placeholder replaced by JWT (gateway host only)
    G->>G: verify via JWKS, check aud and repository claim
    G->>U: request with gateway's own provider key
    U-->>G: response
    G-->>P: response (via proxy)
    R->>O: fresh token before exp
    R->>S: update provider, re-seed placeholder
```

### Gateway-side requirements

The route depends on the gateway enforcing these rules. Operators are
responsible for them, and each was verified live on agentgateway and Praxis:

- remote JWKS for the forge's OIDC issuer
- a fixed audience equal to `inference.gateway.audience`
- **strict authentication:** a request with no token, or an invalid one, is
  refused
- an exact-match claim on `repository` (for example
  `example-org/example-repo`), so an unenrolled repository is refused and one
  repository's token is refused for another. The check applies on every path,
  including the model list, so no bodyless route bypasses it
- caller credential headers (`authorization`, `x-api-key`) are stripped on
  every path and toward every upstream, including bodyless requests such as
  `GET /v1/models`

The two gateways default in opposite directions, so none of these rules can
be left to a default:

| | agentgateway | Praxis |
|---|---|---|
| Authentication | optional unless set to strict | strict |
| Caller `Authorization` upstream | dropped | forwarded unless stripped |
| Other caller headers (`x-api-key`) | forwarded unless removed | forwarded unless stripped |
| Per-model rules on bodyless requests | applied (the model list is filtered per caller) | skipped, so only a global policy holds |

The gateway also meets these requirements, which the live verification did not
cover:

- caller credentials are never reflected in responses, in the body or in a
  header, because a reflected token would be a replayable credential for the
  rest of its lifetime
- the rules fail closed: no policy admits no caller, and malformed
  configuration, missing claims or an unverifiable signature are rejected

Deploying and operating the gateway are out of scope.

### Threat-model delta versus ADR 0092

- **The credential is short-lived and accepted only by the gateway.** It sits
  behind a placeholder, as the ADR 0092 access token does. It lives only as
  long as the forge's OIDC token (300 s in GitHub's documented example), and
  only the gateway's audience accepts it.
- **There is a new service on the request path.** The gateway sees every
  prompt and completion, and it holds the provider key. A compromised gateway
  is a concentrated risk to every repository that uses it.
- **The forge stores nothing reusable.** Once the gateway route is live,
  `FULLSEND_OPENAI_API_KEY` can be deleted.
- **The agent cannot set the base URL.** The runner owns the
  `INFERENCE_GATEWAY_*` variables and refuses to launch without its own base
  URL. Neither the agent-writable `.env` nor plugin env can override them:
  after both are applied, the runner unsets the whole `INFERENCE_GATEWAY_*`
  family and re-exports only its own values (`BASE_URL`, `TOKEN_FILE` and, if
  needed, `PROVIDER_ID`). This is a new ordering, because plugin env is
  exported last today and only a deny-list protects it. Plugin env may not
  use the `INFERENCE_GATEWAY_` prefix.
- **The egress rules are scoped to the gateway host.** The gateway gets its
  own egress profile and provider, rendered per configured host with a
  per-host id, so two gateways on one shared OpenShell gateway do not collide
  and the direct OpenAI routes are untouched during rollout. The egress
  preflight that refuses uninspected credential endpoints runs for the
  gateway host as well.

### Deferred

These are deferred and named:

- Claude Code and Codex on the gateway route
- GitLab ID tokens, which arrive as an `id_tokens:` job variable rather than
  a request URL
- operator guides for gateways beyond agentgateway and Praxis (LiteLLM,
  APISIX); any gateway that meets the requirements above can serve the route

## Consequences

- A repository can run GPT, Claude, Gemini and open-weight models on pi
  through a gateway, without WIF admin access or a stored provider key.
- Deleting `FULLSEND_OPENAI_API_KEY` is safe once a repository's runs use the
  gateway, because a configured gateway never falls back to it.
- A gateway outage fails runs for every repository behind it, by design.
- Every gateway model must be listed, inline or in `models_file`, because pi
  cannot discover models offline.
- Rotating the placeholder inside one running iteration before the token's
  `exp` is still to be proven live, in the implementation tracked in
  [#8280](https://github.com/fullsend-ai/fullsend/issues/8280).

## Related

- ADR 0025: credential delivery tiers
- ADR 0033: base-branch config reads
- ADR 0069: shared presets; inference authorization left open
- ADR 0073: `oidcDenyKeys`
- ADR 0092: OpenAI WIF and static-key routes; the refresh path this route
  reuses
- [#8262](https://github.com/fullsend-ai/fullsend/issues/8262): pi-inference-gateway in the sandbox image
