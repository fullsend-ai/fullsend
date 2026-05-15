---
title: "37. Cross-org token minting for issue proposals"
status: Proposed
relates_to:
  - downstream-upstream
  - security-threat-model
topics:
  - identity
  - oidc
  - cross-org
  - federation
---

# 37. Cross-org token minting for issue proposals

Date: 2026-05-14

## Status

Proposed

<!-- Once this ADR is Accepted, its content is frozen. Do not edit the Context,
     Decision, or Consequences sections. If circumstances change, write a new
     ADR that supersedes this one. Only status changes and links to superseding
     ADRs should be added after acceptance. -->

## Context

The [downstream/upstream federation model](../architecture.md#downstreamupstream-federation) describes how a downstream fullsend instance can propose improvements upstream via the forge. The retro agent is the first concrete use case: retros in an end-user org identify improvements that belong in `fullsend-ai/fullsend`. A second, less well-defined use case is a general upstreaming agent that proposes project-level changes to any upstream project the downstream org contributes to.

Both use cases require filing issues in a repo owned by a different GitHub org. The central token mint ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)) issues short-lived, org-scoped installation tokens from OIDC-authenticated workflows. Today the mint is stateless — it validates OIDC claims and hardcoded env vars, but never reads repository config. Tokens are scoped to the requesting org's installations, so cross-org issue filing fails at the GitHub API level with no graceful fallback.

## Decision

Extend the token mint with a `cross-org-propose` role that can mint installation tokens scoped to `issues: write` in a different org's repos. Authorization is bidirectional and config-driven:

1. **Downstream declares intent.** `send_issues_to` in the downstream's `config.yaml` lists target repos (e.g., `fullsend-ai/fullsend`). The post-script checks this before using any cross-org token. The mint does not read this.

2. **Upstream grants permission.** `accept_issues_from` in the upstream's `config.yaml` lists org names authorized to send proposals. The mint uses the App JWT to fetch and read this when processing a `cross-org-propose` request and refuses if the requesting org is not listed.

3. **Config lookup order.** For both keys, check `{repo}/.fullsend/config.yaml` (per-repo) first, then `{org}/.fullsend/config.yaml` (per-org). If neither exists, treat the key as absent.

4. **New mint behavior.** When the mint receives `role: cross-org-propose`, it uses its App JWT to read the target org's config and check `accept_issues_from`. This is the only role that causes the mint to read repository content. All other roles remain stateless OIDC validation.

5. **Workflow integration.** Reusable workflows make a separate `mint-token` call per cross-org target, passing tokens to post-scripts via a `$CROSSORG_TOKENS_DIR` directory. The primary org token flow is unchanged.

6. **Agent-agnostic.** The `cross-org-propose` role, `send_issues_to`, and `accept_issues_from` are not tied to any specific agent. The retro agent uses them first; future agents (including upstreaming agents for project-level proposals to arbitrary upstreams) reuse the same mechanism.

## Consequences

- The mint gains a config-reading capability for exactly one role (`cross-org-propose`), changing its character from purely stateless to config-aware for cross-org authorization.
- Cross-org issue filing requires both sides to opt in — neither side can unilaterally enable it.
- The App must be installed in both the upstream and downstream orgs. The mint can only read config and mint tokens for repos where the App has an installation.
- Unauthorized cross-org proposals are skipped gracefully; remaining same-org proposals still file. This replaces the current crash-on-failure behavior.
- **Open question: cross-forge federation.** This ADR assumes both downstream and upstream are on GitHub. A downstream fullsend instance running in GitLab CI could use GitLab's OIDC tokens for WIF, but the mint would need to validate GitLab's OIDC issuer and claims (not just GitHub's `token.actions.githubusercontent.com`), and the `accept_issues_from` config would need to identify the downstream by something other than a GitHub org name. Cross-forge cross-org minting — e.g., a GitLab-based agent filing issues in a GitHub upstream — is a natural extension of this design but requires separate mint changes beyond what is proposed here. ADR 0029 already notes extensibility to other CI platforms via their OIDC tokens; [ADR 0028](0028-gitlab-support.md) covers GitLab support more broadly.
- Follow-on work: the [design spec](../superpowers/specs/2026-05-14-cross-org-issue-filing-design.md) covers implementation details including post-script changes, workflow steps, and testing.
