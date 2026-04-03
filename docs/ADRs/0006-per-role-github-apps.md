---
title: "6. Per-role GitHub Apps with manifest-based creation"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - identity
  - github-apps
  - least-privilege
---

# 6. Per-role GitHub Apps with manifest-based creation

Date: 2026-04-02

## Status

Accepted

## Context

Agents need forge credentials to act on repos. A single shared credential for all agent roles violates least-privilege: a review agent would hold write permissions it should never use. The identity model must scope permissions per role while keeping setup automatable. See [agent-architecture.md](../problems/agent-architecture.md) and [security-threat-model.md](../problems/security-threat-model.md).

## Decision

Each agent role (triage, implementation, review) gets its own GitHub App, created via the [app manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest). Apps follow the naming convention `<org>-<role>`. The manifest defines per-role permissions (e.g., review gets read-only code access; implementation gets read-write).

Private keys (PEMs) are stored as repo-level secrets on the `.fullsend` config repo. App IDs are stored as repo-level variables. Secrets never leave the config repo — agent dispatch workflows in `.fullsend` read them at runtime.

The installer checks for existing app installations before creating new ones. If an app exists and its PEM secret is present, it is reused. If the PEM is lost (it is only available at creation time), the user must delete the app and re-run install.

## Consequences

- Each role gets exactly the permissions it needs — compromising one app does not grant another role's permissions.
- The manifest flow requires a browser-based OAuth redirect, making fully headless installation impossible. Acceptable for an org-admin operation run infrequently.
- PEMs are write-once secrets: lost keys require app deletion and recreation.
- The per-app model scales linearly with roles. Adding a new role means creating a new app — no shared credential rotation needed.
- GitLab and Forgejo will need equivalent per-role identity mechanisms when their forge implementations are built.
