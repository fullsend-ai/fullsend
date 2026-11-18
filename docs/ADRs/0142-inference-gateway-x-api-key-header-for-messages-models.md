---
title: "142. The inference gateway route can send its credential as x-api-key for Messages-dialect models"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - security
  - sandbox
  - credentials
---

# 142. The inference gateway route can send its credential as x-api-key for Messages-dialect models

Date: 2026-10-15

Amends: [ADR 0137](0137-inference-gateway-credential-route.md), [ADR 0138](0138-inference-gateway-api-key-credential-mode.md)

## Status

Accepted

## Context

[ADR 0137](0137-inference-gateway-credential-route.md) sets the
`anthropic-messages` auth header on the inference gateway route to
`authorization`, because agentgateway reads `Authorization: Bearer` by default.
[ADR 0138](0138-inference-gateway-api-key-credential-mode.md) sends its gateway
key the same way, so a gateway reads one header whatever the runtime or mode
([Claude Code § Inference gateway route](../runtimes/claude.md#inference-gateway-route)).

Some gateways read the caller's key on the Messages API from `x-api-key`, the
header Anthropic documents for API keys on that API.
[#8370](https://github.com/fullsend-ai/fullsend/issues/8370) reports one that
answers `401 missing API key` to a key sent as Bearer on `/v1/messages`, while
it accepts Bearer on `/v1/chat/completions` and `/v1/responses`. Its
gateway-side fix has no timeline, so Claude models behind it cannot be reached
through the gateway route on pi or on Claude Code. Other clients already send
`x-api-key` to Messages-dialect models: pi-inference-gateway does by default
(the runner overrides it to `authorization`), and so does OpenCode's Anthropic
provider package.

### Why this is a lasting option, not an interim workaround

When an API or runtime provides separate places for an API key and for a
token, a credential put in the other place often still works, but only because
every layer in between happens to tolerate it. fullsend's path to a model has
several layers that change independently: the runner, the OpenShell provider
and profile, the runtime and its extensions, the gateway, and the inference
provider behind it. More may be added. Different teams maintain them on their
own schedules, and none of them has to keep tolerating a credential in the
slot meant for something else. Putting each credential where the interface
documents it keeps the path working as the layers change. Anthropic documents
`x-api-key` for API keys on the Messages API, so a gateway built to that
documentation may read only that header. The gateway in #8370 is one example
of that class, not a one-off quirk.

## Decision

**The `inference.gateway` block gets an opt-in setting that sends the gateway
credential as `x-api-key` for Messages-dialect requests.** This amends the
ADR 0137 rule that the `anthropic-messages` header is always `authorization`,
and the ADR 0138 statement that the `api-key` mode sends its key the same way.

- **The setting is `messages_auth_header`,** a scalar in `inference.gateway`
  with the values `authorization` and `x-api-key`. Its name says it does not
  affect the other dialects or codex. It merges field by field like the rest of
  the block, has no runner-variable override, and
  `fullsend inference gateway status` reports it.
- **The default stays `authorization`,** and it is used when the field is
  absent. Existing gateways, including the agentgateway setup in the
  [operator guide](../guides/infrastructure/inference-gateway-operator.md),
  are unaffected.
- **It applies to `api-key` mode only.** An `oidc` block that sets
  `messages_auth_header: x-api-key` is a config error. See
  [Why `api-key` mode only](#why-api-key-mode-only).
- **Only the Messages dialect moves.** `openai-responses` and
  `openai-completions` models, and codex, keep `Authorization: Bearer`.
- **It applies to every runtime that speaks the Messages dialect on the
  route.** On pi that is `anthropic-messages` models: the runner renders
  `x-api-key` as the auth header in the runner-owned `inference-gateway.json`.
  A `models_file` still may not set `authHeader`. On Claude Code the runner
  hands the placeholder to Claude Code as the variable it sends in `x-api-key`
  (`ANTHROPIC_API_KEY`) in place of `ANTHROPIC_AUTH_TOKEN`. The runner still
  pins it in the inline `--settings` document and still clears every other
  credential variable after `.env`.
- **One credential header per request, when the setting is on.** A
  Messages-dialect request then carries the credential in `x-api-key` and the
  runner sets no credential in `Authorization`. When the setting is off, behavior
  is unchanged: the credential goes in `Authorization: Bearer`, and Claude Code
  in `oidc` mode keeps sending its `apiKeyHelper` value as both
  `Authorization: Bearer` and `x-api-key`, as it does today. This ADR does not
  change that default launch.
- **The block is still read from the base branch** on pull-request events
  ([ADR 0033](0033-per-repo-installation-mode.md)), so a pull request cannot
  change the header for its own run.

### How the OpenShell provider profile binds the credential

The credential stays behind the run-scoped provider's endpoint-bound
placeholder. OpenShell substitutes it only on requests to the gateway host and
the profile's model API paths, as ADR 0137 describes. The host and path
scoping is what keeps the real credential on the gateway host, and it does not
change. What changes is the profile's credential metadata. When the setting is
on, the Messages binding names `header_name: x-api-key` with OpenShell's
plain-header style, not `auth_style: bearer`, so the value is sent without a
`Bearer ` prefix. The profile's endpoints, method and path rules, and binaries
are unchanged.

The per-host profile id already hashes the host and path prefix. It must also
cover the header binding, so that two profiles for the same host, one per
header, on one shared OpenShell gateway never share an id or overwrite each
other. The pi and codex profile
serves several dialects at once, because one pi run can mix
`anthropic-messages` and `openai-responses` models on the same gateway. It
must bind `x-api-key` on the Messages paths and `authorization` on the others.
The implementation checks on the pinned OpenShell version how to do that: one
profile that resolves the placeholder in either header on its bound paths, or
a separate Messages-paths profile. It must not send the credential in both
headers on any path.

### Header custody for an x-api-key gateway

ADR 0137 already requires a gateway to strip both caller credential headers,
`authorization` and `x-api-key`, on every path and toward every upstream. The
operator guide applies that to agentgateway with
`requestHeaders.remove: [x-api-key]`. agentgateway takes the caller's
credential from `Authorization`, but passes every other caller header to the
upstream, where `x-api-key` is the header an Anthropic upstream reads for the
provider key.

On a gateway that reads the caller's credential from `x-api-key`, the same
header is first the caller's credential, then the upstream's key. Its operator
must:

- authenticate the caller from `x-api-key`, then remove that header before the
  request goes upstream;
- set the upstream's `x-api-key` from the gateway's own provider key by
  replacing the header, never by appending to it, so the caller's value cannot
  reach the upstream as a second header or the first of two;
- still remove any caller `Authorization` header;
- repeat the operator guide's custody check, with a canary value in the
  caller's `x-api-key`, and confirm the upstream sees only the gateway's own
  key.

The operator guide gets a section for this when the setting is implemented.

### Rejected alternative: detect the header by trying one and falling back on a 401

The runner, or the runtime, would send the credential in one header and retry
in the other after a 401. That sends the credential in a header the gateway
may not strip, so a gateway that reads the other header can forward the
credential to its upstream. This is the leak the stripping rules exist to
prevent. It would also double the requests on every auth failure, and it
would hide a misconfigured gateway behind a retry. The header is configured,
never guessed.

### Why `api-key` mode only

`x-api-key` is the documented place for an API key on the Messages API, and an
OIDC JWT is a bearer token, whose documented place is `Authorization`. The
gateway in #8370 issues keys. Limiting the setting to `api-key` mode is also
the smallest surface to build, test and explain. Alternatives considered:

- **Both modes.** A gateway that validates forge OIDC tokens but reads them
  from `x-api-key` on its Messages route could use the route too. Claude
  Code's `oidc` mode already copies its `apiKeyHelper` value into `x-api-key`
  as well as `Authorization`, so "one credential header per request" would
  need a different launch for that combination. It also carries a short-lived
  token in the slot documented for a static key.
- **Both modes, with `oidc` behind a warning,** as `api-key` mode itself is in
  ADR 0138. This is more flexible, but it keeps the testing cost of the
  both-modes option.

### Why a Messages-scoped scalar

The name `messages_auth_header` says the setting does not affect the other
dialects or codex. Alternatives considered:

- **A general scalar,** for example `auth_header: x-api-key`. It is shorter,
  but a reader may expect it to move every dialect, including codex.
- **A map keyed by dialect,** for example
  `auth_headers: { anthropic-messages: x-api-key }`. It extends to other
  dialects without a rename, but today only one key and one non-default value
  are valid.
- **A per-model `authHeader` in `models`.** It is the finest grain, but Claude
  Code reads no model list, and ADR 0137 has the runner own `authHeader`, so
  it would fit only pi.

## Consequences

- A repository behind a gateway that reads `x-api-key` on its Messages route
  can reach Claude models through the gateway route on pi and on Claude Code.
- Repositories that do not set the option see no change, and agentgateway
  deployments that follow the operator guide need no new configuration.
- A gateway reads one header per dialect, set by the repository's block,
  instead of one header for every runtime, mode and dialect.
- Operators of an `x-api-key` gateway take on the custody rules above, so the
  caller's credential is replaced, not forwarded, on the upstream hop.
- Binding two headers in the shared pi and codex profile depends on OpenShell
  behaviour the implementation must confirm first.

## Related

- [ADR 0033](0033-per-repo-installation-mode.md): base-branch config reads
- [ADR 0137](0137-inference-gateway-credential-route.md): the gateway route
  and its `authorization` header for `anthropic-messages`
- [ADR 0138](0138-inference-gateway-api-key-credential-mode.md): the
  `api-key` credential mode
- [#8370](https://github.com/fullsend-ai/fullsend/issues/8370):
  implementation
- [#8471](https://github.com/fullsend-ai/fullsend/issues/8471): this record
