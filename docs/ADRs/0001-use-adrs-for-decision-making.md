---
title: "1. Use ADRs for decision making"
status: Accepted
relates_to:
  - "*"
topics:
  - process
---

# 1. Use ADRs for decision making

Date: 2026-03-20

## Status

Accepted

## Context

Fullsend is a design exploration repo with multiple problem documents that
evolve independently. As thinking matures in these problem areas, we need a way
to crystallize specific decisions without rushing to conclusions. The existing
problem documents are good for exploring the space, but they don't clearly
separate "options we're considering" from "decisions we've made."

We want a lightweight process that lets us:

- Propose decisions that we know need to be made, even before we've chosen an
  answer.
- Describe options and trade-offs in a structured way.
- Record the final decision and its rationale once consensus forms.
- Keep a clear history of what was decided and why.

## Decision

We adopt Architecture Decision Records (ADRs), following the format described
by Michael Nygard, adapted for this repo's needs.

ADRs live in `docs/ADRs/` and follow the naming convention
`NNNN-short-description.md` where `NNNN` is a unique four-digit number.

Each ADR has a Status field. Valid statuses are:

- **Accepted** -- The decision has been made.
- **Deprecated** -- The decision is no longer relevant.
- **Superseded** -- The decision has been replaced by a later ADR.

Each ADR includes YAML frontmatter with structured metadata:

- **title** -- The ADR title (required).
- **status** -- Must match the `## Status` section in the body (required).
- **relates_to** -- A list of problem document names (filenames without `.md`
  from `docs/problems/`) that this ADR relates to. Use `"*"` for ADRs that
  apply broadly across all problem areas.
- **topics** -- Free-form tags for discoverability.

This frontmatter makes it possible to discover which ADRs relate to a given
problem area without manually maintaining cross-reference lists.

ADR linting is borrowed from the
[konflux-ci/architecture](https://github.com/konflux-ci/architecture) repo and
runs in CI to validate statuses, number uniqueness, and frontmatter correctness
(including cross-references to problem docs).

## Consequences

- Problem documents in `docs/problems/` remain the place for open-ended
  exploration. ADRs are for when a specific decision point has been identified.
- Contributors propose ADRs via pull requests for discussion before merging.
- The linting ensures ADRs follow the expected format, catching mistakes early.
- We inherit a proven format from the broader konflux-ci organization, making it
  familiar to contributors who work across repos.
