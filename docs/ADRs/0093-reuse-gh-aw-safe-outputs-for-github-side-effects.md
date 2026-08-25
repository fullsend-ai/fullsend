---
title: "93. Reuse gh-aw safe outputs for GitHub side effects"
status: Accepted
relates_to:
  - platform-nativeness
  - security-threat-model
topics:
  - github
  - safe-outputs
  - security
---

# 93. Reuse gh-aw safe outputs for GitHub side effects

Date: 2026-08-25

## Status

Accepted

## Context

Fullsend currently applies agent-requested GitHub writes in deterministic,
privileged runner-side post-scripts. Their fixed implementations already limit
agents to the actions the scripts implement, but their privileges and output
contracts are broader than each individual operation needs.

[gh-aw safe outputs](https://github.github.com/gh-aw/reference/safe-outputs/)
provide GitHub Actions handlers that separate a read-only agent from a
permission-controlled apply job. They add explicit operation declarations,
validation, target restrictions, and limits. This complements credential
isolation and output schema enforcement ([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)
and [ADR 0022](0022-harness-level-output-schema-enforcement.md)).

## Decision

For GitHub-specific agent side effects, Fullsend will migrate compatible writes
from privileged post-scripts to gh-aw safe outputs, operation by operation.
Note that [gh-aw custom safe outputs exists](https://github.github.com/gh-aw/reference/custom-safe-outputs/)
as a generic escape hatch.

Post-scripts remain responsible for deterministic Fullsend state transitions,
deduplication, and policy checks. However, we will also eventually invest
in a "generic-safe-outputs" style tool that has similar opinionated verbs
for other sources such as GitLab, Forgejo and Jira.

Safe-output declarations and limits must be explicit; migration must
retain Fullsend controls not represented by a handler.

## Consequences

- Compatible GitHub writes gain an explicit, operation-specific apply boundary.
- Advanced GitHub writes require custom safe-output schemas and apply jobs.
- Deterministic post-scripts retain Fullsend logic and non-GitHub behavior, not
  general GitHub write authority.
- gh-aw version selection and migration sequencing remain implementation work.
