---
title: "5. Forge abstraction layer"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - forge
  - portability
  - interfaces
---

# 5. Forge abstraction layer

Date: 2026-04-02

## Status

Accepted

## Context

Fullsend must eventually support GitHub, GitLab, and Forgejo. Every operation that touches the git forge — creating repos, managing secrets, writing files, listing installations — must work across all three. Without a shared abstraction, forge-specific logic would spread throughout the codebase, making multi-forge support a rewrite rather than an extension.

## Decision

All forge operations go through the `forge.Client` interface (`internal/forge/forge.go`). The interface uses forge-neutral vocabulary: `ChangeProposal` instead of "pull request" or "merge request," `CreateChangeProposal` instead of `CreatePR`. Forge-specific implementations live in sub-packages (`internal/forge/github/`). A thread-safe `FakeClient` exists for testing without forge access.

No code outside `internal/forge/` imports forge-specific packages directly.

## Consequences

- Adding a new forge (GitLab, Forgejo) requires implementing `forge.Client` — no changes to layers, CLI, or app setup code.
- Forge-neutral naming occasionally feels awkward (e.g., `ChangeProposal`), but prevents GitHub-centric thinking from leaking into the model.
- The interface will grow as new operations are needed; keeping it cohesive requires discipline.
- The `FakeClient` enables deterministic testing of every layer without network calls.
- Sentinel errors (`ErrNotFound`, `ErrBranchProtected`, `ErrAlreadyExists`) with `errors.Is()` helpers provide forge-agnostic error classification. `ErrNotFound` and `ErrAlreadyExists` are mapped in `APIError.Unwrap()` for automatic propagation. `ErrBranchProtected` is wrapped contextually at the call site (e.g., `commitFilesTo`) where the operation context disambiguates branch-protection 422s from other validation failures.
- `CommitFilesToBranch` complements `CommitFiles` (default branch) by targeting a specific branch, enabling the protected-branch fallback path where scaffold files are committed to a feature branch and delivered via PR.
