---
title: "12. Normative v1 Git-tracked files under `.fullsend` for admin install"
status: Proposed
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

Proposed

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

Adopt **`docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/SPEC.md`**
as the single normative description of the **v1** set of files committed in
`<org>/.fullsend` by admin install, including exact required contents for:

- `.github/workflows/agent.yaml`
- `.github/workflows/repo-onboard.yaml`
- `CODEOWNERS` (pattern with the installing user’s GitHub login)

The `config.yaml` path is included in that tracked set; its **document body**
(schema and fields) remain specified in **ADR 0011** only.

## Consequences

- Installers, reviewers, and CI can validate `.fullsend` against one SPEC for
  file paths and workflow/CODEOWNERS bodies.
- Changes to tracked paths or workflow bodies require a SPEC revision and a
  new or superseding ADR, not silent drift in Go constants alone.
- Secret and variable storage in `.fullsend` stays outside Git, as described in
  the SPEC’s out-of-scope section.
- Enrollment shims in application repositories remain outside this tracked set.
- Until accepted, downstream docs should treat this as a proposed baseline.
