---
title: "13. Admin install: repository enrollment v1"
status: Proposed
relates_to:
  - agent-infrastructure
  - repo-readiness
topics:
  - admin-install
  - enrollment
  - github-actions
---

# 13. Admin install: repository enrollment v1

Date: 2026-04-05

## Status

Proposed

## Context

Admin install must attach each enabled repository to the shared agent pipeline without silently rewriting default branches. The established pattern is a small *shim* workflow in the target repo that calls the reusable workflow in the org’s `.fullsend` repository. Org-level configuration and the contents of `.fullsend` are decided separately (ADR 0011, ADR 0012); this ADR fixes the enrollment mechanics for target repos.

## Decision

**Enrollment v1** is defined exactly by the normative specification at [`docs/normative/admin-install/v1/adr-0013-enrollment/SPEC.md`](../normative/admin-install/v1/adr-0013-enrollment/SPEC.md). Tooling that performs enrollment MUST conform to that document for branch names, shim path, pull request title and body, base branch selection, `{org}` substitution rules, shim YAML shape, and forge operation ordering.

## Consequences

- Implementations and tests can be checked against a single written contract instead of inferring behavior from code alone.
- Changing enrollment behavior after acceptance requires a new spec version and a new ADR (or superseding this one).
- Repositories remain explicitly opted in via merge of the enrollment pull request; the installer does not bypass review on the target repo’s default branch.
