---
title: "92. OpenAI WIF credential delivery — runner-side exchange, run-scoped provider"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - security
  - sandbox
  - runtime
---

# 92. OpenAI WIF credential delivery — runner-side exchange, run-scoped provider

Date: 2026-08-27

## Status

Accepted

## Context

The pi runtime (#6464) serves Claude, Grok and Gemini on Vertex, but no
OpenAI model (#6532). OpenAI now offers Workload Identity Federation: a
GitHub Actions job exchanges its OIDC JWT at
`POST https://auth.openai.com/oauth/token` for an opaque Bearer access
token that lives at most one hour — and no longer than the GitHub
assertion it was exchanged from, which is minutes.

The token is a plain header credential, which means fullsend can run GPT
models with **no OpenAI credential inside the sandbox at all** — ADR 0025
credential-delivery **tier 2**, one tier above today's Vertex path (which
puts a real GitHub OIDC token and an `external_account` file on the
sandbox filesystem).

### Alternatives considered

1. **`inference.local`** — at OpenShell 0.0.115 (the pinned version) this is a cluster-global
   route: one provider *and one model* per gateway, applied to every
   sandbox. fullsend runs on shared gateways (the GitLab runner VM's
   persistent gateway, local dev, parallel eval cases), so concurrent
   runs with different models or service accounts would race. Rejected.

2. **Dynamic `token_grant`** — the supervisor mints a SPIFFE JWT-SVID as an RFC 7523
   client assertion; OpenAI's exchange needs a GitHub-issued JWT plus
   `identity_provider_id`/`service_account_id` in a JSON body. Rejected.

3. **In-sandbox exchange** — `ACTIONS_ID_TOKEN_REQUEST_URL/_TOKEN` are
   runner-only by design (`oidcDenyKeys`, `internal/cli/run.go`, #5832 /
   ADR 0073), and pi's built-in `openai` provider has no WIF concept.
   Rejected.

## Decision

Runner-side exchange with a run-scoped OpenShell provider:

1. The runner exchanges the GitHub OIDC JWT for an OpenAI access token
   before sandbox creation (`internal/inference/openaiwif/`).
2. A run-scoped provider (`openai-<suffix>`) carries the token as a
   bare-key credential, never expanded through `os.ExpandEnv` (the
   opaque token may contain `$`). The provider has
   `--credential-expires-at` set (the token's own expiry, or a bounded
   lifetime for a static `OPENAI_API_KEY` from the runner environment)
   and is deleted in deferred cleanup regardless of `--keep-sandbox`;
   a provider whose expiry cannot be set is deleted immediately.
3. The `fullsend-openai` OpenShell profile scopes egress to
   `POST /v1/responses` on `api.openai.com` for `**/node` binaries.
4. The pi runtime gate writes the placeholder the sandbox environment
   carries for `OPENAI_API_KEY` into pi's `auth.json` (an `openai`
   `api_key` entry) before the agent-writable `.env` is sourced, and
   pi is started **without** `--api-key`. pi's `AuthStorage` re-reads
   that file whenever its revision changes and resolves the key per
   request (`packages/coding-agent/src/core/auth-storage.ts`,
   `model-registry.ts` at 0.84.3), so after each refresh the runner
   re-runs the same seed through `sandbox exec` — an exec'd shell's
   environment holds the new placeholder, the runner never learns the
   opaque revision — and the running iteration's next request carries
   the new credential. This is the only hand-off that works on
   OpenShell 0.0.115, where a revision-scoped placeholder stays pinned
   to the value of the generation it was issued for and the unrevisioned
   alias is refused for an endpoint-bound credential (see
   Consequences). A config-dir integrity guard (exit 98 when
   `models.json` exists, or `auth.json` is anything but pi's own `{}`
   or exactly the seeded placeholder entry) closes the redirect vector
   even with hooks disabled; it runs before `.env` is sourced and again
   after it behind `unset -f test command grep tr`, with the runner-owned
   config paths re-exported in between, so neither a sourced shell
   function nor a rewritten `PI_CODING_AGENT_DIR` can defeat it.
5. Before the agent starts, the run reads the sandbox's effective policy
   and refuses to continue when any rule admits `api.openai.com:443`
   without L7 inspection (no `protocol`, or `tls: skip`): since
   OpenShell 0.0.110 the proxy will not inject a provider credential
   over such a route, and the base sandbox image's default policy ships
   exactly that shape in its `codex` rule. The fleet's harnesses carry
   `policy: policies/base.yaml`, which has no network rules of its own,
   so the provider's inspected entry is the only route; a custom harness
   must do the same.
6. The token value is redacted by exact match in run output and provider
   errors; `::add-mask::` is emitted on GitHub Actions;
   `FULLSEND_OPENAI_*` are in `oidcDenyKeys`.

### Accepted residual

Static placeholders are endpoint-bound at OpenShell 0.0.115: the proxy
substitutes the placeholder only on a request to the host, port and path
of the profile endpoint that issued it (`providers-v2.mdx` "Static
Credential Endpoint Binding"), so the cross-provider risk ADR 0025
documents — a compromised agent having the gateway resolve the
placeholder on another allowed endpoint — no longer applies to this
credential. What remains:

- `auth.json` is agent-writable during an iteration (pi's config
  directory is not a permission boundary), and pi re-reads it per
  request. An agent can therefore change the placeholder pi sends — to
  nothing that resolves anywhere else — or plant a real key of its own;
  both are self-sabotage, not a leak of the run's credential. The
  integrity guard runs at iteration start, before and after `.env`, not
  continuously.
- An iteration starting in the same seconds as a refresh seeds
  `auth.json` from its own exec environment; the refresher verifies the
  file after its re-seed and seeds once more if the iteration's write
  landed last (one poll interval of exposure at most).
- The token lifetime is bounded by the GitHub assertion (minutes) and by
  `expires_in` (at most 1 h); the provider is run-scoped, carries that
  expiry and is deleted at run end; the mapping grants
  `api.model.request` only.

## Consequences

- GPT models on pi are usable via `openai/<model-id>` with no OpenAI
  credential inside the sandbox.
- The runner refreshes the credential for the life of the run: a WIF
  token is re-exchanged from a fresh GitHub assertion before
  `expires_in` (at a margin capped to half the token's lifetime, so a
  minutes-long token refreshes every couple of minutes) and updated
  into the run-scoped provider in one call with its new expiry, then
  waits (up to 90 s; ~20 s measured) for the sandbox to hand new
  processes the new placeholder and re-seeds pi's `auth.json` with it
  (a settle timeout fails the attempt rather than re-seeding the old
  placeholder); a static key's provider expiry is pushed out on the
  same schedule and re-seeded the same way, because that update is a
  new generation as well. When the bounded retries fail, the running pi
  keeps the generation it holds, which stops resolving when that token
  expires — the run fails visibly rather than silently outliving its
  credential.
- **Verified against OpenShell 0.0.115 on a live gateway (2026-08-27),
  re-verify on every bump.** (a) The placeholder in the sandbox
  environment is a placeholder in the `openshell:resolve:env` namespace
  with an opaque revision (`v<opaque>_OPENAI_API_KEY`) and resolves; (b)
  the canonical, unrevisioned form (`OPENAI_API_KEY` in that namespace)
  returns `500 credential_unavailable` for this endpoint-bound
  credential (`crates/openshell-core/src/secrets.rs`
  `resolve_placeholder`); (c) after `provider update`, a placeholder
  issued for the previous generation keeps resolving to the *previous*
  value (OpenShell retains up to eight generations,
  `MAX_RETAINED_CREDENTIAL_GENERATIONS`; only once a generation ages out
  does its placeholder fall back to the current credential — eleven
  quick updates were observed to leave the old value resolvable, the
  supervisor's ~10 s poll coalescing them into fewer generations), while
  the sandbox hands new processes a new placeholder within ~20 s (one
  poll); (d) expiring the credential in place makes every generation
  fail closed, and an expiry-only update is a new generation too, whose
  predecessor keeps the expiry it was built with. (c) and (d) are why
  the file hand-off exists; it also contradicts the 0.0.115
  `providers-v2` documentation ("the proxy resolves existing
  placeholders against current credentials … so rotation … take effect
  without restarting the process"), which describes gateway-managed
  refresh handles, not `static`/`external` credentials. `TestPiOpenAIAuthSeed`,
  `TestReseedOpenAIAuth_*` and `TestRefreshOpenAIProvider_ReseedsOnlyOnce…`
  pin the runner side; pi's side — `AuthStorage` re-reading `auth.json`
  when its file revision changes and `prepareRequest` resolving auth per
  request — is verified in the pi 0.84.3 source
  (`packages/coding-agent/src/core/auth-storage.ts`,
  `packages/ai/src/auth/resolve.ts`, `model-runtime.ts`) and exercised
  across iteration starts locally, but a rotation *inside* one running
  iteration has not been observed live yet: the first WIF run is that
  check. If it fails, the failure is visible (`401` after the old token's
  expiry, with the refresher's log lines to go on) and the fallback is
  to shorten iterations below the token lifetime.
- OpenShell 0.0.110+ resets any model request whose body contains the
  contiguous placeholder prefix (`openshell:resolve:env` followed by a
  colon) — it is treated as credential-bearing traffic that cannot be
  rewritten. An agent therefore cannot read a file, diff or PR body that
  spells the prefix out; this repository builds it from two parts in
  source and tests and never writes it contiguously in docs. Verified in
  the v0.0.115 source: the guard is a plain substring test on the prefix
  (`crates/openshell-core/src/secrets.rs:44-55`
  `contains_raw_reserved_marker`, streamed by
  `crates/openshell-supervisor-network/src/l7/rest.rs:1169-1182`), it
  never parses what follows, and it applies to every inspected REST
  endpoint as soon as the sandbox holds *any* static provider credential
  (`l7/mod.rs:317-318`, `l7/relay.rs:842`) — so a credential-less
  endpoint such as Vertex is guarded too once `github-ro` is attached.
  Intended fail-closed design (OpenShell PR #2162: a live placeholder must
  never leave the proxy unresolved); the bare-prefix false positive is
  the open upstream issue NVIDIA/OpenShell#2904.
  `request_body_credential_rewrite: true` is not an opt-out (an
  unresolvable marker still fails, `rest.rs:1698`); the only one is
  `allow_uninspected_credentials: true` on the model endpoint, which
  forwards such a body raw while the bearer header is still injected and
  the method/path rules still apply (verified: a body carrying the
  prefix, and even the real placeholder string, gets 200; `GET
  /v1/models` stays 403). A literal placeholder — a reserved token, not
  a secret — would then reach the model API. **Adopted** for the two
  default model profiles fullsend ships (`profiles/fullsend-openai.yaml`
  and `profiles/fullsend-vertex-ai.yaml`); the fleet's own agents resolve
  their Vertex profile from fullsend-ai/agents (`harness/*.yaml` →
  `profiles/fullsend-vertex-ai.yaml`), which needs the same flag to
  protect fullsend's review/code agents, and the upstream fix is
  NVIDIA/OpenShell#2904.
- Static credentials are endpoint-bound at 0.0.115: the proxy
  substitutes a placeholder only at the host, port and path of the
  profile endpoint that issued it (`providers-v2.mdx` "Static Credential
  Endpoint Binding"). The residual this ADR inherited from ADR 0025 — a
  compromised agent resolving the placeholder against another allowed
  host — is closed for this credential.
- A harness without `policy:` fails the egress preflight on the base
  image (its default policy allows `api.openai.com:443` as a raw tunnel
  for `/usr/bin/node`); the fleet's `policies/base.yaml` is the
  documented answer for custom and local harnesses.
- The three identifiers live in the committed `inference.openai` block
  of `.fullsend/config.yaml`, written by `fullsend github setup
  --openai-*` like the Vertex project and provider; they are not secrets
  (a token is issued only to a caller whose GitHub OIDC claims match the
  mapping, and pull-request events read the config from the base
  branch, ADR 0033). Within `config.yaml` the three resolve through the
  layers field by field, so a base preset can carry the organization's
  audience and identity provider and each repository its service
  account. The reusable workflows also pass the `FULLSEND_OPENAI_*`
  repository variables into the runner environment for installations
  that prefer not to commit them; when any is set they replace the
  resolved block, never merge with it. On a machine without a GitHub
  OIDC endpoint a set `OPENAI_API_KEY` wins over the block, so a
  developer's local run of a repository that committed it still works.
- Live end-to-end verification is gated on external access and run by
  a maintainer after merge.

## Related

- ADR 0025 — provider-based credential delivery (tier definitions)
- ADR 0073 — `oidcDenyKeys` (#5832)
- #6532 — GPT / Azure OpenAI / Bedrock providers for pi
- #6464 — pi runtime tracker
- #1952 — Anthropic WIF sibling design
- ADR 0095 — mapping scope: repository-only by default (#6782)
- ADR 0099 — **amends this ADR's in-sandbox half**: the seeding step described here for pi's
  `auth.json` is one implementation of a runtime-neutral seeder interface, and the
  `fullsend-openai` egress profile admits `**/codex` as well as `**/node` (#6920)
