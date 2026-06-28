---
title: "12. Normative v1 Git-tracked files under `.fullsend` for admin install"
status: Accepted
relates_to:
  - governance
  - codebase-context
  - agent-infrastructure
topics:
  - configuration
  - admin-install
  - github-actions
---

# 12. Normative v1 Git-tracked files under `.fullsend` for admin install

Date: 2026-04-05

## Status

Accepted

## Context

[ADR 0003](0003-org-config-repo-convention.md) places org-level fullsend
configuration in `<org>/.fullsend`, but it sketches a broader future layout
than what the current admin installer writes today. The CLI and layers already
create a concrete set of committed paths and workflow bodies; without a
normative v1 spec, tooling and docs can drift from each other.

Separately, **ADR 0011** owns the YAML document body of `config.yaml`. This
decision covers only which paths are Git-tracked in `.fullsend` and the required
v1 contents for each path except that body.

## Decision

The **v1** set of files committed in `<org>/.fullsend` by admin install is
defined by the scaffold implementation (`internal/scaffold/`) and its test
suite. Required contents include:

- `.github/workflows/agent.yaml`
- `.github/workflows/repo-onboard.yaml`
- `CODEOWNERS` (pattern with the installing user’s GitHub login)

The `config.yaml` path is included in that tracked set; its **document body**
(schema and fields) remain specified in **ADR 0011** only.

## Consequences

- Installers, reviewers, and CI can validate `.fullsend` against the scaffold
  implementation and its test suite.
- Changes to tracked paths or workflow bodies require updating the
  implementation and tests.
- Secret and variable storage in `.fullsend` stays outside Git, as described in
  the SPEC’s out-of-scope section.
- Enrollment shims in application repositories remain outside this tracked set.
- Until accepted, downstream docs should treat this as a proposed baseline.
