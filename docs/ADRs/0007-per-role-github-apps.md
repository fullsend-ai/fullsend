---
title: "7. Per-role GitHub Apps with manifest-based creation"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - identity
  - github-apps
  - least-privilege
---

# 7. Per-role GitHub Apps with manifest-based creation

Date: 2026-04-02

## Status

Accepted

## Context

Agents need forge credentials to act on repos. A single shared credential for all agent roles violates least-privilege: a review agent would hold write permissions it should never use. The identity model must scope permissions per role while keeping setup automatable. See [agent-architecture.md](../problems/agent-architecture.md) and [security-threat-model.md](../problems/security-threat-model.md).

## Decision

Each agent role (triage, implementation, review) gets its own GitHub App, created via the [app manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest). Apps follow the naming convention `<org>-<role>`. The manifest defines per-role permissions (e.g., review gets read-only code access; implementation gets read-write).

Private keys (PEMs) are stored as repo-level secrets on the `.fullsend` config repo. Client IDs are stored as repo-level variables. Secrets never leave the config repo — agent dispatch workflows in `.fullsend` read them at runtime.

The installer checks for existing app installations before creating new ones. If an app exists and its PEM secret is present, it is reused. If the PEM is lost (it is only available at creation time), the user must delete the app and re-run install.

## Consequences

- Each role gets exactly the permissions it needs — compromising one app does not grant another role's permissions.
- The manifest flow requires a browser-based OAuth redirect, making fully headless installation impossible. Acceptable for an org-admin operation run infrequently.
- PEMs are write-once secrets: lost keys require app deletion and recreation.
- The per-app model scales linearly with roles. Adding a new role means creating a new app — no shared credential rotation needed.
- GitLab and Forgejo will need equivalent per-role identity mechanisms when their forge implementations are built.
- **Permission escalation note:** The `fullsend` orchestrator app and the `coder` app both require `workflows: write` (combined with `contents: write`). The orchestrator needs it to create and update shim workflow files during enrollment reconciliation. The coder needs it because issues may legitimately require modifying `.github/workflows/` files — without this permission, pushes that touch workflow files are rejected by GitHub, wasting the entire agent run. CODEOWNERS rules on `.github/workflows/` paths provide an equivalent human-approval gate without blocking the push. A compromise of either app's private key would allow an attacker to deploy arbitrary workflow files to installed repos. Mitigations: (1) the app should be installed on specific repos, not org-wide; (2) PEMs are stored as repo-level secrets on `.fullsend`, limiting access to that repo's workflows; (3) per-role workflows validate `source_repo` against the config allowlist before generating tokens; (4) CODEOWNERS enforcement ensures workflow changes require human review.
