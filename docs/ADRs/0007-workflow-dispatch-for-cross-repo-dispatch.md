---
title: "7. workflow_dispatch for cross-repo agent dispatch"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - dispatch
  - secrets
  - workflows
---

# 7. workflow_dispatch for cross-repo agent dispatch

Date: 2026-04-02

## Status

Accepted

## Context

Enrolled repos must route events (issues, PRs, comments) to the agent dispatch workflow in the `.fullsend` config repo. The original design used `workflow_call` (reusable workflows), which requires the calling workflow to pass secrets explicitly — every enrolled repo's shim workflow would contain secret references, and the called workflow's secrets are scoped to the *caller's* repo, not the config repo where the App PEMs live.

See [security-threat-model.md](../problems/security-threat-model.md) and [agent-infrastructure.md](../problems/agent-infrastructure.md).

## Decision

Use `workflow_dispatch` instead of `workflow_call`. Enrolled repos trigger a dispatch event on `.fullsend` via a curl call authenticated with `FULLSEND_DISPATCH_TOKEN` — a fine-grained PAT scoped to `.fullsend` with `actions:write`. The dispatch token is stored as an org-level Actions secret with visibility restricted to enrolled repos only.

This means secrets are separated by layer: the dispatch token (org secret, visible to enrolled repos) enables triggering; the App PEMs (repo secrets on `.fullsend`) are only accessible to workflows running *in* `.fullsend`. Enrolled repos never see the PEMs.

## Consequences

- App PEM secrets stay in the config repo. No secret passing across repo boundaries.
- The dispatch token is a single PAT with narrow scope — the blast radius of its compromise is limited to triggering workflow_dispatch events on `.fullsend`, not credential theft.
- `workflow_dispatch` is compute-platform-agnostic: any CI system that can receive dispatch events works.
- The dispatch token must be manually created (fine-grained PATs cannot be created via API). This is a one-time step during install.
- Adding or removing enrolled repos requires updating the org secret's repo access list.
