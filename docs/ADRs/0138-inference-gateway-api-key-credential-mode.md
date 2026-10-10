---
title: "138. The inference gateway route has an api-key credential mode"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - security
  - sandbox
  - credentials
---

# 138. The inference gateway route has an api-key credential mode

Date: 2026-10-10

## Status

Accepted

## Context

[ADR 0137](0137-inference-gateway-credential-route.md) made an inference
gateway a credential route of its own. Its credential is the job's forge OIDC
token, fetched with the gateway's audience and re-seeded before each `exp`.
That needs a gateway that can validate forge OIDC tokens against the forge's
JWKS. It also needs a run with a forge OIDC endpoint, so ADR 0137's route
never applies to a local run.

Some gateways cannot trust forge OIDC. Examples are a hosted gateway with only
key-based auth, or a gateway whose operator does not let a forge issuer in.
For those, the only credential the gateway accepts is an API key it issues.
ADR 0137 does not cover that credential, and its alternative is worse. A
repository falls back to the direct `openai` static key (ADR 0092), which is a
provider key with no gateway in front of it.

## Decision

The `inference.gateway` block gets an `auth` field with two values. There is
no implicit precedence and no fallback between them: the block names one.

- **`oidc` (the default):** the route as ADR 0137 describes it. `audience` is
  required, and the block applies only on a run with a forge OIDC endpoint.
- **`api-key`:** the runner reads the gateway API key from
  `FULLSEND_INFERENCE_GATEWAY_API_KEY`, which is a forge secret in CI or a
  variable in the local environment. It is never read from config. `audience`
  is not required.

`api-key` is a lasting, supported mode, not an interim or deprecated one. It
is not the primary or safest option, though, so use it with caution: the setup
help text, the run log and `fullsend inference gateway status` warn that it
relies on a long-lived secret, and recommend `oidc` when the gateway supports
it.

In the `api-key` mode:

- The key travels the same way as the OIDC token. It goes into the same
  run-scoped OpenShell provider, as the same endpoint-bound placeholder, under
  the same per-host egress profile. It is registered for redaction and masked
  in the job log. The same guards apply: the runner owns the
  `INFERENCE_GATEWAY_*` variables, the config is digest-guarded, and plugin
  env may not use the prefix.
- **There is no re-seed loop.** The key is not rotated. On OpenShell even an
  expiry update mints a new placeholder generation, which the running agent
  would then need to be re-seeded with. So the provider instance gets one
  expiry bound when it is created and is never extended. The bound is sized
  above any run (24 hours), so that a runner that dies before its deferred
  delete leaves no instance serving the key for ever.
- **The block applies on local runs too.** The runner owns the route whenever
  an `api-key` block is configured, with or without an OIDC endpoint. A local
  run that wants the harness-plugin setup from the local guide leaves the
  block out, or uses `oidc`.
- **A missing credential fails the run.** Without
  `FULLSEND_INFERENCE_GATEWAY_API_KEY`, the run fails before the agent
  starts. It never falls back to `oidc`, the `openai` provider, WIF or a
  static OpenAI key.

`fullsend github setup` gets `--inference-gateway-auth`. Clearing the block
(empty `--inference-gateway-url` and `--inference-gateway-audience`) removes
`auth` with the rest of the block. `--inference-gateway-auth` alone never
creates a block without a `url`.

## Consequences

- A repository behind a gateway that cannot trust forge OIDC still gets the
  gateway's custody of the provider key, per-repository authorisation and the
  runner-owned route, instead of a direct provider key.
- The forge stores a reusable secret again: a gateway key, not a provider
  key. The gateway can scope it, for example to some models or some
  repositories, and revoke it, but a leaked key works until it is revoked.
  This is the main threat-model difference from ADR 0137.
- The mode switch is per block, so an org preset can carry `url` and
  `audience` while a repository opts into `api-key`, or the other way round.
- Wiring `FULLSEND_INFERENCE_GATEWAY_API_KEY` through the reusable workflows
  is a separate, maintainer-owned change.

## Related

- [ADR 0092](0092-openai-wif-credential-delivery.md): the OpenAI static-key
  route, which has the same long-lived-secret trade-off without a gateway
- [ADR 0137](0137-inference-gateway-credential-route.md): the gateway route
  and its `oidc` credential
- [#8280](https://github.com/fullsend-ai/fullsend/issues/8280):
  implementation
