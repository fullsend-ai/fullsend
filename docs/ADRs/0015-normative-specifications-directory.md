---
title: "15. Normative implementation contracts live under docs/normative/"
status: Proposed
relates_to:
  - architectural-invariants
  - governance
  - contributor-guidance
topics:
  - documentation
  - conventions
  - versioning
---

# 15. Normative implementation contracts live under docs/normative/

Date: 2026-04-06

## Status

Proposed

## Context

This repository separates **exploration** (problem docs), **decisions** (ADRs),
and **current narrative state** (`docs/architecture.md`). ADRs work best when
they state one decision briefly and link out for depth
([ADR 0001](0001-use-adrs-for-decision-making.md)).

Some decisions imply **implementation contracts**: byte- or field-level rules,
schemas, canonical examples, or golden artifacts that alternative tools must
satisfy and that CI or tests can check. Embedding those inside an ADR makes the
record hard to read, hard to diff, and awkward for machine consumption. A
dedicated, **versioned** tree keeps the ADR as the ratification layer while the
heavy artifacts evolve on their own lifecycle (for example admin-install
contracts, whether or not present in this repo yet).

## Decision

We adopt **`docs/normative/<topic>/v<major>/...`** as the canonical home for
**normative**, **versioned** material that defines what implementations must
conform to. Typical contents include JSON Schema or equivalent, canonical YAML
or snapshots, compatibility fixtures, and other machine-checkable or
exhaustive human specifications.

- **`<topic>`** is a short, stable slug (e.g. `admin-install`).
- **`<major>`** is a non-negative integer. Breaking contract changes require a
  new major directory (`v1`, `v2`, …). Non-breaking additions within a major
  stay under the same `v<major>` unless an ADR explicitly re-versions.

An **ADR** that introduces or changes such a contract must:

1. State the **decision** and **rationale** at a high level (including when to
   bump major versions).
2. **Link** to the normative tree path (and optionally to problem docs), not
   paste multi-page schemas or large fixture bodies into the ADR.

Problem docs remain the place for trade-offs and open questions; they do not
replace the normative tree for exhaustive contract text.

## Consequences

- Multiple implementations can share one **explicit, testable** contract
  without growing ADR files into specification dumps.
- **Versioning** is visible in the path; deprecating an old major can be
  documented in an ADR while keeping historical trees for readers and tools.
- Contributors have a **single convention** for where to add schemas and golden
  files when a decision needs them.
- **Linting and link checks** can treat `docs/normative/` as the contract root
  without special-casing individual ADRs.
