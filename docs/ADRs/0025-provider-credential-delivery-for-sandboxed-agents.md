---
title: "0025. Provider-based credential delivery for sandboxed agents"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - credentials
  - sandbox
  - security
  - providers
---

# 0025. Provider-based credential delivery for sandboxed agents

Date: 2026-04-23

## Status

Accepted (extends [ADR 0017](0017-credential-isolation-for-sandboxed-agents.md))

## Context

[ADR 0017](0017-credential-isolation-for-sandboxed-agents.md) established a two-tier credential isolation model: prefetch + post-process as the default, and a host-side REST server with L7 enforcement as the fallback. The REST server requires per-endpoint proxy code, input validation, response sanitization, and server lifecycle management — cost that scales linearly with each new external service. Two problems motivate extending the model: reducing the proxy maintenance burden for services where it is unnecessary, and formalizing how agents get fine-grained operation control over external APIs — not just credential isolation, but capability scoping.

OpenShell's native provider system addresses the first problem. Providers inject credentials as opaque placeholder tokens that the gateway proxy swaps for real values at the HTTP layer, so credentials never enter the sandbox. The `fullsend run` command already supports providers: the harness layer loads provider definitions from the agent's `providers/` directory, creates them on the gateway via `openshell provider create`, and passes them to sandbox creation — so the infrastructure for tiers 2 and 4 is operational today. Tier 3 (host-side REST server) is not yet implemented. L7 egress policies add two enforcement axes: HTTP method + path restrictions, and binary-level restrictions — the proxy identifies the calling binary via `/proc/pid/exe` and walks the process tree, so policies can restrict which executables may reach each endpoint (see [openshell-policy-bypass experiment](https://github.com/fullsend-ai/experiments/pull/5) for validation). Together, providers and L7 policies replace the REST server for services with static API key/token auth, with no custom proxy code.

For fine-grained operation control beyond what L7 path filtering can express, two mechanisms complement providers. First, custom wrapper binaries baked into the OpenShell sandbox image (e.g. a `safe-push` that wraps `git push` and rejects force pushes) — placed on a read-only path via Landlock so the agent cannot modify them, with L7 binary filtering ensuring only the wrapper can reach the upstream service. Second, the host-side REST server from ADR 0017, which can inspect request bodies, restrict GraphQL operations, or transform responses. Both mechanisms provide operation-level control that providers and L7 path matching alone cannot.

Providers do not eliminate all exfiltration risk. The gateway resolves placeholders to real credentials at supported injection locations (headers, path segments, query params). If an L7 policy permits endpoints that accept free text — search queries, file creation paths, commit messages — a compromised agent can embed a placeholder in those fields, causing the proxy to resolve the real credential into a location visible in server logs, CDN caches, or public content. For example, GitHub's Contents API (`PUT /repos/{owner}/{repo}/contents/{path}`) accepts a user-controlled path segment; if the agent places a placeholder token in the path, the resolved credential becomes a filename visible in the repository. Cross-provider leaking is also possible: an agent with access to multiple providers can pass one provider's placeholder to a different server via a query param or non-auth header. Agent designers must audit every permitted endpoint for free-text fields that could carry placeholders. Per-endpoint provider scoping at the OpenShell level (restricting which provider placeholders are resolved for which endpoints) would mitigate cross-provider leaking but is not yet available ([NVIDIA/OpenShell#734](https://github.com/NVIDIA/OpenShell/issues/734)).

Not all services fit the provider model. Providers cannot inject credentials into request bodies (GraphQL mutations, sensitive POST payloads) and cannot handle file-based auth flows. GCP Vertex AI is the canonical example: the Anthropic SDK's Vertex integration delegates authentication to Google's `google-auth-library`, which reads a service account JSON file (containing a private key), signs a JWT locally (RS256), and exchanges it with `oauth2.googleapis.com` for a short-lived access token. This is a multi-step flow where the credential is a file (not a string), cryptographic operations happen inside the sandbox, and the auth library manages token refresh internally. The provider placeholder model cannot intercept or replace any of these steps — the private key must be present on the sandbox filesystem (see [security-threat-model.md](../problems/security-threat-model.md)).

## Decision

Adopt a four-tier credential delivery model, extending ADR 0017's two-tier model:

1. **Prefetch + post-process** (unchanged from ADR 0017). Agent runs with zero credential access. Use for agents with fully enumerable inputs. This remains the default — the first question for any new agent is whether it can run without runtime credential access.

2. **OpenShell providers + L7 egress policies** (new). Provider injects credentials as opaque placeholders swapped at the gateway proxy layer. L7 policies scope access by method + path and by binary. For operation-level control beyond path filtering, custom wrapper binaries in the sandbox image (e.g. a `safe-push` that rejects force pushes) restrict what the agent can do with the API — the wrapper is placed on a read-only path (Landlock) so the agent cannot modify it, and L7 binary filtering ensures only the wrapper can reach the upstream service. Use for services with static API key/token auth: GitHub API, OpenAI, Anthropic direct API. Credentials never enter the sandbox.

3. **Host-side REST server + L7 enforcement** (retained from ADR 0017). A host-side server holds credentials and exposes scoped endpoints. Use when providers hit their limits: credentials must appear in request bodies (e.g. GraphQL mutations, sensitive POST payloads), response transformation or scanning is required, or operation restrictions need request body inspection that neither L7 path filtering nor wrapper binaries can express. This tier carries ongoing proxy maintenance cost but provides the deepest operation-level control.

4. **Host files + L7 egress policies** (new explicit tier). Credential files are copied into the sandbox via the harness `host_files` mechanism. L7 policies restrict egress to only the necessary endpoints and binaries. Use when the provider placeholder model and REST server cannot work: services with file-based auth, multi-step OAuth2 flows, or in-sandbox cryptographic operations (e.g. GCP Vertex AI, where `google-auth-library` must read a service account JSON to sign JWTs locally). The security boundary is the network policy, not credential isolation — real credentials exist on the sandbox filesystem. This makes the single-responsibility agent model ([ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md)) especially important: the narrower the agent's responsibility and the fewer endpoints its policy permits, the smaller the attack surface if the agent is compromised.

Agent definitions should use the highest tier possible: prefer providers over REST servers, REST servers over host files. The decision tree for a new integration: can prefetch handle it, and is a deterministic input set sufficient (or does the agent need to explore dynamically at runtime)? → if prefetch suffices, use tier 1. Does the service use static token auth in headers? → use tier 2. Do credentials need to appear in request bodies or responses need transformation? → use tier 3. Does auth require credential files or in-sandbox cryptographic ops? → use tier 4.

## Consequences

- Services with static token auth (GitHub, OpenAI, Anthropic) no longer require custom proxy endpoints, reducing per-service maintenance to provider YAML and L7 policy definitions.
- The host-side REST server from ADR 0017 is retained for cases requiring request body credential injection or response transformation — its role narrows from "default fallback" to a specific tier.
- The `host_files` mechanism is formalized as the explicit fallback for file-based auth flows (GCP Vertex AI), documenting a pattern already present in the scaffold harness files.
- L7 policy authoring remains security-critical across all tiers — providers reduce proxy code but do not reduce the need for correct path patterns, and a path pattern typo can over-permit access.
- Custom wrapper binaries used for operation-level control (tier 2) must be placed on read-only paths enforced by Landlock; if the agent can modify the wrapper, the restriction is bypassed.
- Agents using host files (tier 4) have real credentials on the sandbox filesystem; per-agent documentation must explicitly state that the security boundary is network-only.
- Fullsend should provide validation tooling that checks agent harness definitions for compliance with this model — auditing L7 policies for free-text endpoints that could carry placeholders, verifying wrapper binaries are on read-only paths, and flagging tier mismatches — for both internal development and for users crafting new agents.
