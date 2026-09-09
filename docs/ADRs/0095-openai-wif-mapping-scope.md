---
title: "95. OpenAI WIF mapping scope — repository-only by default"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - security
  - runtime
---

# 95. OpenAI WIF mapping scope — repository-only by default

Date: 2026-08-30

## Status

Accepted

## Context

ADR 0092 accepted the OpenAI WIF credential path (runner-side exchange into a
run-scoped provider) but did not decide what the OpenAI service-account mapping
should assert. The enrolment tooling shipped in #6779 filled that gap with a
default of `ref = refs/heads/main`, on the assumption that agent runs start from
the default branch.

That assumption is wrong for fullsend's own workflows. GitHub's OIDC `ref` claim
follows the triggering event, not the installation:

| Event | `ref` claim |
|---|---|
| `issues`, `issue_comment`, `pull_request_target` | the repository's default branch |
| `pull_request_review` | `refs/pull/<N>/merge` |
| `workflow_dispatch` | the dispatched ref |

A mapping asserting `refs/heads/main` therefore refuses the exchange on every
PR-review-triggered run, and on any repository whose default branch is not
`main` — a failure that surfaces as a 4xx from the token endpoint, far from its
cause. OpenAI assertions are exact scalar values AND-ed within a mapping, so
there is no single `ref` value that covers the set.

The GCP/Vertex path already faced this and resolved it the other way: its
attribute mapping carries `repository`, `repository_owner` and `actor` but no
ref attribute, and its condition asserts the repository
(`internal/dispatch/gcf/provisioner.go`, `buildAttributeCondition`).

## Decision

Generate mappings that assert `iss`, `aud` and `repository` only. No `ref`
assertion by default, matching the Vertex path's repository scoping.

`--ref` becomes an explicit tightening option rather than a default. When set,
`fullsend inference openai request` emits **two** mappings per repository — one
asserting the named ref and one asserting `refs/pull/*` — because a single value
cannot cover both branch-triggered and PR-review-triggered runs. OpenAI permits
one trailing wildcard with a non-empty prefix per assertion value, and OR-s
assertions across mappings, so the pair expresses "this ref, or any pull-request
merge ref".

## Consequences

The trust boundary is the repository, and the guide and the generated request
document both state it in those words: the mapping trusts **any job in that
repository that can request an OIDC token**. It is worth being blunt about what
that does and does not include. A workflow in the repository can reach OpenAI's
token endpoint directly; fullsend's dispatch authorization and the mint's
prevalidation govern which agent runs fullsend itself starts, and do not stand
between such a workflow and OpenAI. Anyone with write access to the repository
can therefore obtain a model token, and the service account's project spend
limit is what bounds the cost. This is the same boundary the Vertex path has
carried since it shipped, made explicit rather than newly taken on.

Enrolment stops failing in a way that is hard to attribute: the default now
works for every event fullsend dispatches on, including `pull_request_review`,
and for repositories whose default branch is not `main`.

Teams that want ref-level narrowing still have it, and pay for it in mapping
budget: two mappings per repository against OpenAI's 50-per-provider ceiling
halves the enrolment capacity of one provider from 50 repositories to 25.
`request` warns when a generated document exceeds that ceiling.

## Related

- ADR 0092 — OpenAI WIF credential delivery (the path this scopes)
- ADR 0025 — provider-based credential delivery
- #6782 — drop the ref assertion by default
- #6779 — `fullsend inference openai request|import|status`
