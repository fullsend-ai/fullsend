---
title: "170. Align the entity-context-snapshot with the normalized entity"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - entity-context-snapshot
  - authorization
  - data-security
---

# 170. Align the entity-context-snapshot with the normalized entity

Date: 2026-11-11

## Status

Accepted

This decision publishes a new major version of the contract adopted by
[ADR 0141](0141-deterministic-filtered-entity-context-staging.md). It keeps
ADR 0141's deterministic, filtered staging decision and supersedes only its
v1 contract for new producers.

## Context

[ADR 0098](0098-entity-first-harness-evaluation.md) and
[Authorization Contract v2](../normative/authorization/v2/README.md#entity-source-cel-evaluation)
deny entity-first execution for a source and kind until both a CEL field
gate and a model-facing prompt-field gate exist.
[Normalized entity v1](../normative/normalized-entity/v1/README.md) is the CEL
gate for GitHub and GitLab work items and change proposals, GitHub
Discussions, and Jira issues.
[Entity-context-snapshot v1](../normative/entity-context-snapshot/v1/README.md)
covers only Git-forge work items and change proposals, so Jira issues and
GitHub Discussions have no prompt-field gate (issue
[#9310](https://github.com/fullsend-ai/fullsend/issues/9310)).

The two contracts also name shared data differently. The snapshot puts
`source` at the index root, uses `entity.id` for the provider ID, and splits
author identity into strings. Normalized entity v1 nests `source` under the
entity, uses a canonical `entity.id` with a separate `source_id`, and models
actors as objects. An empty snapshot document also cannot say whether its
source was collected. [ADR 0151](0151-harness-human-role-minimums.md) further
requires a versioned snapshot before the snapshot can record a `maintain` or
`admin` human minimum.

This ADR aims to give every normalized-entity provider/kind pair a model-facing
field gate whose shared fields a contributor can read with one vocabulary.

## Options

- **Amend v1 in place.** v1 schemas are closed, and its compatibility rules
  make renamed fields and changed record keys a new major version.
- **Separate gate for Jira and Discussions.** Coverage gaps close, but forge
  entities keep a second set of names and two snapshot formats must evolve
  together.
- **One aligned v2 snapshot.** One contract covers every pair and uses
  Normalized entity v1 names, while keeping the snapshot's record layout.

## Decision

We adopt
[entity-context-snapshot v2](../normative/entity-context-snapshot/v2/README.md)
as the model-facing field gate for every Normalized entity v1 provider/kind
pair, with shared fields named and defined as in Normalized entity v1.

v2 keeps ADR 0141's self-contained records, separate mutable state, filtering,
and lifecycle rules, so prompt-cache reuse is unchanged. Its canonical entity
ID becomes the record-key input, which keeps keys stable across repository and
Jira project moves. Each applicable scope has a required status in
`index.json`, so a complete empty scope differs from one that was not loaded,
is unsupported, is partial, or failed. The contract lists every remaining
shape difference from Normalized entity v1. Admission uses the ADR 0151
harness minimum as Authorization Contract v2 defines it.

## Forge scope

- GitHub: issues, pull requests, and Discussions.
- GitLab: issues and merge requests; merge-request formal reviews are
  `unsupported`, as in Normalized entity v1.
- Forgejo: issues and pull requests carry forward from v1 as snapshot-only
  pairs. They cannot satisfy the entity-first gate until Normalized entity
  adds a Forgejo mapping.
- Jira (tracker): issues; agent-run receipts are `unsupported` until a
  source-backed mapping exists.

## Consequences

- Entity-first enablement for Jira issues and GitHub Discussions no longer
  lacks a contract; producer and consumer implementation are still required.
- No shipped code produces either snapshot version; producers implement v2
  directly, and any v1 producer must migrate to the new identity and record-key
  rules.
- Consumers must read scope status before treating an empty document as
  absence.
- Staging further normalized-entity fields, such as assignees or check
  annotations, requires a later snapshot version.
