---
title: "71. Share GitHub API caching through ghproxy and read-only mint tokens"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
  - security-threat-model
topics:
  - github
  - caching
  - mint
  - least-privilege
---

# 71. Share GitHub API caching through ghproxy and read-only mint tokens

Date: 2026-11-02

## Status

Accepted

## Context

Different agents and processes fetch the same GitHub entities, spending API
quota repeatedly (see [agent infrastructure](../problems/agent-infrastructure.md)).
Fullsend already keeps an ETag cache for selected GETs within each
[`LiveClient`][client-code], but separate clients cannot share it.
[ghproxy][ghproxy] shares an HTTP response cache across clients;
[ghcache][ghcache] revalidates cached responses with conditional requests and
coalesces concurrent reads. Authenticated, unchanged responses returning
`304` do not consume GitHub's [primary REST rate limit][github-limits].

ghproxy [partitions caches by the authorization header][ghcache-code], so
independently minted tokens fragment the cache even when they grant identical
access. The current [`mintToken`][mint-code] selects the requested role's App
and creates a token per request. This builds on the named permission levels
in [ADR 0073](0073-named-mint-privilege-levels.md) and introduces a narrow
read-only sharing exception to the per-role identities in
[ADR 0007](0007-per-role-github-apps.md). It complements the discovery-query
coalescing in [ADR 0149](0149-coalesce-entity-source-discovery-queries.md).
Sharing credentials must preserve the authorization boundaries described in
[agent architecture](../problems/agent-architecture.md) and the
[security threat model](../problems/security-threat-model.md).

This ADR aims to conserve GitHub API quota across agents and processes by
sharing response caches and equivalent read-only credentials without widening
any caller's authorized access.

## Options

### Process-level caching

Keep cached responses within each Fullsend process, extending the existing
client cache where useful. This avoids another service and keeps credential
lifetimes unchanged, but separate agents and processes still fetch and cache
the same entities independently; restarting a process also loses its cache.
It saves quota within a process without addressing reuse across processes.

### ghproxy without further Fullsend changes

Route existing clients through an externally deployed ghproxy while leaving
Fullsend's token issuance unchanged. This adds shared cache infrastructure
without changing the mint, and requests using the same token can benefit.
However, independently minted tokens still select different cache partitions,
so agents and processes with equivalent access cannot reliably share cached
responses. Deployment-specific routing also leaves consistent client and
agent-tool configuration to each operator.

### ghproxy with a mint-level read-token cache

Configure ghproxy routing in Fullsend and reuse mint-issued read-only tokens
by effective permission set and authorized scope. Equivalent requests from
different roles and processes use the same token and cache partition. This
addresses both response-cache sharing and token fragmentation, at the cost of
credential-cache storage, shared token lifecycle management, and explicit
audit attribution for cross-role reuse.

## Decision

We adopt optional ghproxy routing with a permission-keyed, read-only token
cache in the mint to make repeated GitHub reads share cached responses.
Configure the proxy through Fullsend's usual
[layered configuration](../guides/infrastructure/layered-config-reference.md),
following [ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md), with no
separate environment-variable configuration surface. The resolved settings
apply to Fullsend's GitHub clients and supported agent API tools. With no
configured endpoint, retain direct GitHub access.

Cache only tokens whose **actual granted permissions are all read-only**;
role and level names do not establish eligibility. Key entries by the
order-independent set of effective permission name/value pairs, within the
mint trust domain, GitHub host, target organization, and exact repository
scope. Equivalent roles and separate processes may receive the **same token
value**; tokens granting write or admin access remain unshared.

Authorize each request, including the requested role's installation access,
before reusing a token, and never return broader access than that caller is
authorized to receive. A shared token may come from another authorized role's
App: this is the read-only exception to ADR 0007. Record the requesting role
and actual issuer separately, and use unshared credentials for operations
that require a particular App identity. Reuse must honor expiry, revocation,
and changes to the issuer's access or the authorized scope.

Treat the proxy and mint cache as trusted credential-handling infrastructure.
Preserve credential isolation, authorization boundaries, and ghproxy's
authenticated cache partitions and conditional revalidation; a cache failure
must not broaden access or substitute stale data for successful revalidation.
Storage, replica coordination, transport wiring, and telemetry remain
implementation concerns.

## Forge scope

GitHub only, including GitHub Enterprise Server where the configured ghproxy
supports that API origin; hosts have separate cache namespaces. GitLab and
Forgejo are excluded because ghproxy and GitHub App installation tokens are
GitHub-specific; their credential and API routing contracts are unchanged.

## Consequences

- Equivalent reads across agents and processes can save primary REST quota,
  but cache misses, changed resources, and secondary limits still matter.
- Read-only roles can share a token without sharing write authority, while
  mint audit records preserve attribution that the shared GitHub identity loses.
- The proxy and token cache become credential-handling infrastructure whose
  access controls and availability need operational ownership.
- Expiry and revocation affect all users of a shared token, so refresh and
  cleanup must respect shared ownership and preserve authorization checks.

[client-code]: https://github.com/fullsend-ai/fullsend/blob/93ff87e845f898f4d344ad90433bc30f5bddbfa1/internal/forge/github/github.go#L60-L80
[mint-code]: https://github.com/fullsend-ai/fullsend/blob/93ff87e845f898f4d344ad90433bc30f5bddbfa1/internal/mintcore/handler.go#L517-L553
[ghproxy]: https://docs.prow.k8s.io/docs/ghproxy/
[ghcache]: https://docs.prow.k8s.io/docs/ghproxy/ghcache/
[ghcache-code]: https://github.com/kubernetes-sigs/prow/blob/c06373a3e2cdcf0de2a087b4ed1db426366694b8/pkg/ghcache/ghcache.go#L390-L437
[github-limits]: https://docs.github.com/en/rest/guides/best-practices-for-using-the-rest-api#use-conditional-requests
