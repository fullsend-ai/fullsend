---
title: "99. Codex credential delivery — custom model provider with a runner-seeded token file"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - runtime
  - security
  - sandbox
---

# 99. Codex credential delivery — custom model provider with a runner-seeded token file

Date: 2026-09-02

## Status

Accepted

## Context

Adding [openai/codex](https://github.com/openai/codex) as a third agent runtime
([ADR 0091](0091-per-agent-runtime-model-effort.md)) has to answer one question before anything
else: how the agent reaches OpenAI without a credential entering the sandbox. The threat model
treats keeping credentials out of the sandbox as the main limit on an injection's blast radius
([security-threat-model.md](../problems/security-threat-model.md)), and the sandbox — not the
runtime — is the containment boundary
([agent-infrastructure.md](../problems/agent-infrastructure.md)).

[ADR 0092](0092-openai-wif-credential-delivery.md) already settled the runner half: exchange the
job's OIDC token for a short-lived OpenAI token, put it in a run-scoped OpenShell provider, and let
the sandbox see only a gateway placeholder. Its **in-sandbox** half is written for pi specifically —
pi's `auth.json`, and a `fullsend-openai` egress profile admitting `**/node`. Codex is a native
binary that reads its credential differently, so that half needs generalising; this ADR records how,
and amends 0092 accordingly.

The constraint that decides the design: those tokens live minutes, and OpenShell pins each
placeholder to the credential generation it was issued for, so a running agent must be able to pick
up a *new* placeholder mid-run. Codex's built-in `openai` provider reads `OPENAI_API_KEY` once at
startup and built-in provider ids cannot be overridden, so it cannot.

## Options

**The built-in `openai` provider driven by the environment.** Simplest, and wrong for the reason
above: the process reads the variable once and keeps using a placeholder that stops resolving at the
first refresh.

**A host-side API server the sandbox calls instead of OpenAI.** Would work, but it is tier 3 in
[ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md)'s ordering, which prefers a
provider whenever one is viable — and here one is.

## Decision

Codex is configured with a **custom model provider** (`fullsend-openai`, `wire_api = "responses"`)
whose `auth.command` is an absolute path to a runner-written script that prints the current
placeholder from a runner-owned token file under `CODEX_HOME`. Codex re-runs that command every
`refresh_interval_ms`, and the runner re-seeds the file after every credential refresh through the
runtime-neutral `OpenAICredentialSeeder` interface — the same follow-the-refresh shape pi gets from
re-reading `auth.json`, expressed in codex's own mechanism. A value that is not a gateway
placeholder fails the run rather than being forwarded.

This **amends ADR 0092**, whose in-sandbox half is pi-specific: the `fullsend-openai` egress profile
admits `**/codex` as well as `**/node`, and "seed pi's `auth.json`" is one implementation of a
runtime-neutral seeder interface rather than the mechanism itself.

The endpoint and the auth command are additionally pinned as `-c` SessionFlag overrides, which
outrank every config layer, and the runner-owned `config.toml` is guarded by a whole-file digest —
because a single `projects."<repo>".trust_level = "trusted"` line there would make codex load the
target repo's own `.codex/` layer, and codex offers no `-c` override for project trust.

## Consequences

- Codex has no Vertex path, so it gets **no default behaviour-CI coverage** until an OpenAI
  organization is mapped to the pool repositories; until then the evidence is unit tests, recorded
  fixtures and local smoke runs, one of which exercised the full rotate → re-seed → next-turn path.
- With a custom provider codex also issues `GET /v1/models` at startup, which the `fullsend-openai`
  profile denies; it is logged, non-fatal and does not delay the first turn, but "only
  `POST /v1/responses` is attempted" is not true of codex the way it is of pi.
- Codex reports no cost, so `total_cost_usd` stays 0 for codex runs; token usage is reported.
- The config guard compares against **runner-held digests** — digests the runner records outside the sandbox at Bootstrap and injects into the launch command at Run. The
  agent-writable manifest was rejected as the anchor for them, since an agent that rewrote
  `config.toml` could rewrite a digest recorded there to match; so `Run` requires `Bootstrap` to
  have run in the same process and fails closed otherwise.
- The pin must be re-checked on every `CODEX_VERSION` bump against `auth.command` semantics and the
  provider config keys; the list is in
  [runtime-implementation.md](../contributing/runtime-implementation.md#codex-runtime-internals-6920).

Sandbox tool hooks on codex are a separate decision — see [ADR 0100](0100-codex-sandbox-hooks.md).
