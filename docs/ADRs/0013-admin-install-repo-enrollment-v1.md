---
title: "13. Admin install: repository enrollment v1"
status: Accepted
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

Accepted

## Context

Admin install must attach each enabled repository to the shared agent pipeline without silently rewriting default branches. The established pattern is a small *shim* workflow in the target repo that triggers `workflow_dispatch` on `agent.yaml` in the org’s `.fullsend` repository using the org Actions secret `FULLSEND_DISPATCH_TOKEN` ([ADR 0008](0008-workflow-dispatch-for-cross-repo-dispatch.md), [ADR 0009](0009-pull-request-target-in-shim-workflows.md)). Org-level configuration and the contents of `.fullsend` are decided separately (ADR 0011, ADR 0012); this ADR fixes the enrollment mechanics for target repos.

## Decision

**Enrollment v1** is defined by the implementation in `internal/layers/enrollment.go` and its test suite. Tooling that performs enrollment MUST conform to the documented behavior for branch names, shim path, pull request title and body, base branch selection, dispatch wiring, shim YAML shape (including `pull_request_target` for PR events), and forge operation ordering.

## Consequences

- Implementations and tests can be checked against a single written contract instead of inferring behavior from code alone.
- Changing enrollment behavior after acceptance requires a new spec version and a new ADR (or superseding this one).
- Repositories remain explicitly opted in via merge of the enrollment pull request; the installer does not bypass review on the target repo’s default branch.
