---
title: "6. Forge abstraction layer"
status: Proposed
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - portability
  - architecture
  - tooling
---

# 6. Forge abstraction layer

Date: 2026-03-27

## Status

Proposed

## Context

Fullsend currently targets GitHub-hosted organizations but intends to support
GitLab and eventually Forgejo. Many components interact with forge-specific
APIs: creating issues, opening pull/merge requests, applying labels, posting
status checks, and reading CODEOWNERS. Branch protection configuration varies
significantly across forges and is out of scope for the initial abstraction —
it remains an unsolved portability problem.

These interactions happen in two distinct contexts:

1. **Agent runtime (LLM-driven).** The agent decides to open a PR, comment on
   an issue, or check labels. LLM-based agents are naturally good at detecting
   which forge they're on and using its native CLI (`gh`, `glab`, etc.).
   Forcing them through an abstraction adds friction without clear benefit —
   the agent adapts.

2. **Deterministic code paths (scripted).** Two specific places run
   deterministic, non-LLM code that must work across forges:
   - The **agent runtime wrapper** — the script that runs inside the sandbox,
     configures the harness, and launches the agent runtime. It reads issue
     metadata, posts status updates, and fetches configuration. This code
     must work identically regardless of forge.
   - **Skill scripts** — scripts embedded in `scripts/` directories within
     skills that agents invoke as tools. These are shipped by fullsend and
     must be portable.

The forge abstraction belongs in the deterministic code, not in the agent's
mouth.

## Options

### Option 1: Abstraction everywhere

A CLI tool that all forge interactions go through, including agent-initiated
ones. Agent prompts say `fullsend pr create` instead of `gh pr create`.

**Pros:**
- Uniform interface everywhere. Easy to audit forge interactions.

**Cons:**
- Fights the LLM's natural behavior. Agents are good at using native CLIs.
- Requires teaching every agent a non-standard CLI instead of leveraging
  existing training data for `gh`, `glab`, etc.
- The abstraction is only valuable in deterministic code paths where we control
  the source. In agent-generated commands, the LLM adapts naturally.

### Option 2: Abstraction in deterministic code only

A shared library/module used by the agent runtime wrapper and skill scripts.
Agents themselves use whatever forge CLI is available.

**Pros:**
- Forge portability where it matters (our code), natural behavior where it
  doesn't (agent-generated commands).
- Fewer moving parts — no CLI binary to distribute, just a library used by
  code we already ship.
- Agents benefit from their training data on `gh`, `glab`, etc.

**Cons:**
- Agents may use forge-specific features that don't exist on other forges. This
  is acceptable — agent prompts can be tuned per-forge if needed, and the
  harness can provide forge-appropriate context.

### Option 3: No abstraction — accept GitHub coupling

Use `gh` and GitHub APIs everywhere, including deterministic code.

**Pros:**
- Simplest now.

**Cons:**
- Porting the runtime wrapper and skill scripts to GitLab requires rewriting
  every forge interaction in those code paths.

## Decision

Forge-specific interactions are abstracted in the two deterministic code paths
that fullsend controls: the **agent runtime wrapper** and **skill scripts**.
Agents themselves are free to use native forge CLIs.

### Where the abstraction lives

A shared library (working name: `forgekit`) provides functions for the forge
operations that deterministic code needs:

- Issue operations: read metadata, apply labels, post comments
- PR/MR operations: create, update status, post review comments
- Status checks: post pass/fail results
- Code ownership: query CODEOWNERS / equivalent
- Repository metadata: default branch, permissions, clone URLs

The agent runtime wrapper and skill scripts import this library. The library
detects the forge type from the repo's remote URL or from configuration in the
`.fullsend` repo, and dispatches to the appropriate backend.

### What agents do

Agents use whatever forge CLI is available in the sandbox (`gh`, `glab`, etc.).
The harness provides forge-appropriate context so agents know which system
they're on, but agents are not forced through an abstraction layer. LLMs are
naturally effective at using native CLIs based on their training data.

### Key design points

- **Labels are fullsend vocabulary.** Labels used as control signals (e.g.,
  "agent-ready", "not-reproducible") are part of the fullsend vocabulary.
  The library maps them to the appropriate forge mechanism. Agents may also
  apply these labels using native CLIs — the label names are the contract,
  not the mechanism.
- **CODEOWNERS parsing is wrapped.** Different forges have different syntax
  for code ownership. The library abstracts this behind a uniform query
  interface for use by the runtime wrapper and review logic.
- **Skill scripts use the library, not forge CLIs.** Any `scripts/` shipped
  with fullsend skills call `forgekit` functions, making skills portable
  without rewriting.

## Consequences

- **Deterministic code is forge-portable.** The runtime wrapper and skill
  scripts work across GitHub, GitLab, and Forgejo without modification.
- **Agent prompts are forge-aware, not forge-abstracted.** Agent definitions
  may include forge-specific context (e.g., "you are working on a GitHub
  repo, use `gh` for forge operations"), but this is a harness concern, not
  an architectural constraint.
- **New forge backends require implementing the library adapter.** Adding
  GitLab or Forgejo support means implementing `forgekit` backends and
  ensuring the right forge CLI is available in the sandbox.
- **The library is a fullsend deliverable** that must be versioned and tested,
  but it is simpler than a standalone CLI since it only needs to support the
  operations used by deterministic code paths.
- **The agent dispatch and coordination layer uses this library.** It runs
  deterministic code that interacts with forge APIs for event processing and
  work assignment — it goes through `forgekit`, not forge APIs directly.
- **The Agent Identity Provider uses this library for credential issuance.**
  `forgekit` is responsible for making agent identity credentials available to
  the agent runtime (e.g., generating scoped tokens from a GitHub App or
  equivalent). The sandbox is responsible for making those scoped tokens
  available to the layers it controls (harness and runtime).
- **Branch protection remains an unsolved portability problem.** Branch
  protection rules vary significantly across forges in both semantics and
  configuration mechanisms. The initial `forgekit` abstraction does not attempt
  to unify branch protection management.
