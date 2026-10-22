---
title: "NUMBER. TITLE"
status: Accepted  # Valid values: Accepted, Deprecated, Superseded
relates_to:
  - problem-doc-name  # filename without .md from docs/problems/
topics:
  - topic-tag
---

# NUMBER. TITLE

Date: YYYY-MM-DD

## Status

{Accepted | Deprecated | Superseded}

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

What is the issue that we're seeing that motivates this decision or change?

_When describing current behavior, cite the implementing file or function at a
commit SHA, not `main` (e.g., `internal/cli/repos.go` at `<SHA>`, or a
permalink such as
`https://github.com/fullsend-ai/fullsend/blob/<SHA>/internal/cli/repos.go`).
Confirm the code at that SHA matches your description._

## Options

_Optional. Include only when there are genuine alternatives worth documenting.
For Accepted ADRs, include only if the rejected alternatives add useful
context. If the decision is obvious, skip this section._

## Decision

What is the change that we're proposing and/or doing?

## Forge scope

_Required. State which forges (GitHub, GitLab, Forgejo) this decision applies
to and how each is handled. For any forge left out, say so explicitly with a
reason or a tracking issue (e.g., "GitHub only; GitLab tracked in #NNN"). If
the decision is forge-neutral, say that._

## Consequences

What becomes easier or more difficult to do because of this change?
