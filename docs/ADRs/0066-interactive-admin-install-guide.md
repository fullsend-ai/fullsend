---
title: "66. Interactive admin install as guided installation orchestrator"
status: Accepted
relates_to:
  - agent-infrastructure
  - human-factors
  - contributor-guidance
topics:
  - install
  - cli
  - skills
  - interactive
---

# 66. Interactive admin install as guided installation orchestrator

Date: 2026-07-05

## Status

Accepted

## Context

Fullsend installation spans mint trust, inference WIF, GitHub Apps, scaffold
delivery, and per-repo vs per-org scope. Standalone commands (`mint`,
`github`, `inference`, `repos`) already exist ([ADR 0029](0029-central-token-mint-secretless-fullsend.md),
[ADR 0033](0033-per-repo-installation-mode.md), [ADR 0057](0057-repos-management.md)),
and user-facing docs recently deprecated the all-in-one `admin install` as a
monolithic path in favor of those phases.

That split reduced coupling but increased cognitive load: adopters must
understand which role runs which phase, whether to use the hosted mint or a
private one, how many GCP projects are involved, and whether
[ADR 0047](0047-vendored-installs-with-vendor-flag.md) vendoring applies — from
a single repo on hosted services through a multi-org footprint with private
mints and several inference accounts. Agents assisting onboarding face the same
matrix without a single machine-readable decision model.

## Options

### Option A: Standalone commands only (status quo docs)

Users and agents read guides and invoke `mint` / `github` / `inference`
directly. Low CLI complexity; high expertise burden.

### Option B: Interactive `admin install` orchestrator (chosen)

`admin install` becomes a guided entry point that walks the decision tree,
then delegates execution to existing subcommands. Same tree drives agent
skills.

### Option C: New top-level `fullsend install`

Same behavior as B but a new command. Rejected — `admin install` is already the
familiar entry point and matches org-admin mental models.

## Decision

1. **Revive `admin install` as a guide, not a monolith.** The command
   orchestrates installation; it does not re-inline GCP provisioning, mint
   deploy, or GitHub layer logic. Each step invokes the existing standalone
   commands (or their library equivalents) with composed flags.

2. **Interactive by default on a TTY.** When stdin is a terminal and required
   flags are absent, `admin install` runs a guided flow. Non-interactive use
   keeps today's explicit flags (`--mint-url`, `--inference-project`, etc.).
   Add `--guided` (force interactive) and `--plan` (print the composed command
   sequence without executing).

3. **Published decision tree.** Maintain a versioned install decision tree
   (YAML) in-repo describing questions, branches, prerequisites, and the
   subcommand(s) each leaf executes. The CLI loads it; changes to install
   matrix update the tree, not scattered help text.

4. **Coverage.** The tree must reach at least:
   - single-repo + hosted mint + shared inference (zero-GCP GitHub path);
   - self-hosted or private mint ([ADR 0059](0059-public-mint-mode-with-wildcard-allowlists.md)
     tight vs public);
   - multiple GCP projects / inference accounts;
   - bulk per-repo rollout via `repos` ([ADR 0057](0057-repos-management.md));
   - optional `--vendor` ([ADR 0047](0047-vendored-installs-with-vendor-flag.md)).
   Per-org mode remains a deprecated branch per [ADR 0044](0044-deprecate-per-org-installation-mode.md),
   shown only when explicitly chosen, with migration guidance.

5. **Agent skills on the same tree.** Provide an install-guidance skill that
   loads the decision tree and accepts optional context (org size, existing
   mint URL, compliance constraints, repos in scope). Agents traverse the tree,
   explain trade-offs, and emit the same `--plan` output humans would get —
   without bypassing authorization or inventing flags outside the tree.

6. **Normative spec deferred.** The tree schema and leaf command templates are
   documented alongside the artifact; a `docs/normative/` contract is out of
   scope until a second consumer (e.g. web installer) needs byte-level
   interoperability ([ADR 0015](0015-normative-specifications-directory.md)).

## Consequences

- User-facing docs that deprecated all-in-one `admin install` should be
  annotated: deprecated as an opaque monolith, not as the guided entry point.
- The CLI must keep standalone commands as the execution layer so automation
  and skills do not depend on TTY prompts.
- The decision tree becomes a maintained product artifact; install changes
  require tree updates and tests.
- Agents gain a bounded, testable install guide instead of improvising from
  prose docs.
- Implementation scope (wizard UI, tree loader, skill packaging) is follow-on
  work; this ADR records the architectural choice only.
