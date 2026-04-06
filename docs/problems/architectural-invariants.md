# Architectural Invariants

How do we represent and enforce the things that must always be true about the system — and how do agents use them for both review and ongoing maintenance?

## A third kind of intent

The [intent representation](intent-representation.md) doc focuses on feature-level intent: "is this change authorized?" But there's a different category of intent that doesn't map cleanly to the tier system:

- **Feature intent** (Tiers 0-3): "Build feature X" / "Fix bug Y" — time-bounded, specific to a change
- **Architectural intent**: "These things must always be true" — persistent, constraining all changes

Architectural invariants are not features. They're constraints on *how* features get implemented and *what* features are acceptable. A feature might be authorized at Tier 2, but if its implementation violates an architectural invariant, it should still be rejected — or the invariant needs to be explicitly revised through a governed process.

## Architecture documentation as invariant source

Many organizations maintain a form of declared architectural intent — an architecture repo, a wiki, ADRs in each repo, or some combination. These contain:

- **Overview documents** — the authoritative latest state of agreed technical and architectural decisions, organized per service or component
- **Architecture Decision Records (ADRs)** — formal records of significant architectural choices
- **Architecture diagrams** — documenting system structure
- **Contribution requirements** — what process is needed to change architectural decisions

This documentation was created by and for humans. But it's already a machine-readable (or at least machine-parseable) source of architectural constraints. The question is: how do agents consume and enforce it?

See [applied docs](applied/) for organization-specific architecture repo examples.

## Three uses for architectural invariants

### 1. At review time (per-PR enforcement)

Review sub-agents — particularly the correctness and intent alignment agents — can check PRs against declared invariants:

- Does this change violate the dependency flow described in the architecture overview?
- Does it contradict an ADR? (e.g., an ADR defines a trusted component model — a PR that bypasses it should be flagged)
- Does it introduce a pattern that conflicts with documented conventions? (e.g., an ADR defines log conventions)
- Does it change an API contract without updating the architecture doc?

This is different from linting. Linters catch syntax and style violations. Architectural invariant enforcement catches *structural and design* violations — wrong dependency direction, unauthorized service-to-service communication, policy bypass.

### 2. Periodic drift detection (ongoing maintenance)

Beyond per-PR checks, a drift detection agent can periodically scan the codebase against the architecture repo to find *existing* deviations — things that have already drifted without being caught:

- Are the actual service boundaries consistent with the documented architecture?
- Are the actual API contracts consistent with the documented contracts?
- Has naming convention drift occurred?
- Are deprecated patterns still present? (ADRs that supersede earlier ones)

When deviations are found, the drift agent can open cleanup PRs — similar to OpenAI's "garbage collection" concept, but grounded in declared architectural constraints rather than style preferences. These cleanup PRs would be Tier 0 (standing rules, pre-authorized) since they're enforcing already-agreed invariants.

### 3. Tier escalation detection

Architectural invariants help solve the [tier escalation problem](intent-representation.md#the-tier-escalation-problem). A change classified as Tier 1 (tactical bug fix) that violates or modifies an architectural invariant is not a bug fix — it's at minimum a Tier 2 change requiring explicit authorization. The architecture repo provides the baseline for this detection.

Examples:
- A "bug fix" that changes a naming convention defined by an ADR → architectural change, needs authorization
- A "small improvement" that adds a new service-to-service communication path → architecture change
- A "fix" that modifies RBAC roles → security-relevant architecture change

## Making invariants machine-enforceable

The architecture repo today is written for humans. To be useful to agents, invariants need to be extractable — either by LLM comprehension of the prose or by more structured representation.

### Option A: LLM comprehension of existing docs

Review agents read the architecture docs and ADRs as context. They use their understanding of the prose to evaluate PRs. This works today with no changes to the architecture repo.

**Pros:** No migration effort. Works with existing documents.
**Cons:** Unreliable — LLM comprehension of architectural constraints in prose is fuzzy. Different agents may interpret the same document differently. Hard to verify that an agent correctly applied a constraint.

### Option B: Structured invariant annotations

Add machine-readable annotations to ADRs and architecture docs — structured frontmatter, explicit invariant declarations, or a companion file that extracts the key constraints in a structured format.

```yaml
# Example: structured invariant extracted from an ADR
invariant: trusted-component-model
description: All components in the pipeline must use the trusted component model
applies_to:
  - service-a
  - service-b
enforcement: blocking  # PR-level review must flag violations
references:
  - ADR/NNNN-trusted-component-model.md
```

**Pros:** Agents can evaluate compliance mechanically. Consistent interpretation. Auditable.
**Cons:** Overhead of maintaining structured invariants alongside prose docs. Risk of structured annotations drifting from the prose they summarize.

### Option C: Structural tests

Encode invariants as executable tests — similar to ArchUnit (Java), go-arch-lint (Go), or custom linters. The tests run in CI and fail if the invariant is violated.

**Pros:** Deterministic enforcement. No LLM interpretation needed. Works for both agents and humans. Already a proven pattern.
**Cons:** Not all invariants are testable mechanically (some are design-level, not code-level). Significant upfront investment to create tests for existing invariants.

### Option D: Layered approach

Combine all three:
- **Structural tests** for invariants that can be mechanically verified (dependency direction, naming conventions, API contract compliance)
- **Structured annotations** for invariants that are design-level but can be precisely stated (service boundaries, communication patterns)
- **LLM comprehension** as a fallback for invariants that are too nuanced for either (architectural intent, design philosophy)

Each layer provides a different level of confidence. Structural tests are highest confidence. LLM comprehension is lowest but broadest.

## The invariant lifecycle

Architectural invariants aren't permanent. They evolve — sometimes an ADR supersedes an earlier one, sometimes a new feature legitimately requires relaxing a constraint. This lifecycle needs to be managed:

- **Creating invariants** — follows the existing architecture repo process (PR with ADR, 2 peer approvals)
- **Modifying invariants** — same process, with explicit documentation of what changed and why
- **Superseding invariants** — ADRs already have a supersession mechanism. When a new ADR supersedes an older one, the old ADR's status is updated and a link to the successor is added, but its content remains unchanged (ADRs are point-in-time records). `docs/architecture.md` is then updated to reflect the current decision. Agents need to recognize which ADRs are current vs. superseded.
- **Temporary exceptions** — sometimes a PR needs to violate an invariant with a plan to address it later. How is this represented? A time-bounded exception in the architecture repo? A label on the PR?

This lifecycle is a [governance](governance.md) concern — who can create, modify, and grant exceptions to invariants. But the representation and enforcement mechanism belongs here.

## Relationship to other problem areas

- **Intent representation** — architectural invariants are a form of persistent, cross-cutting intent (distinct from feature-level intent in Tiers 0-3)
- **Code review** — review sub-agents (correctness, intent alignment) consume invariants as review context
- **Security threat model** — drift from security-relevant invariants (RBAC, trusted task model, build provenance) is a security concern, not just a quality concern
- **Repo readiness** — repos with clear architectural boundaries and documented invariants are safer for agent autonomy
- **Governance** — who can create, modify, and grant exceptions to invariants

## Open questions

- How much of the existing architecture repo is practically enforceable vs. aspirational? Do some ADRs describe intent that was never fully implemented?
- Should the architecture repo itself be consumed directly by agents, or should there be a derived "agent-readable" representation?
- How do we handle invariants that span multiple repos? (e.g., an API contract between build-service and integration-service)
- Can drift detection be bidirectional — if the code has drifted from the docs, maybe the docs are wrong? How do we distinguish "code drifted from intent" from "intent was never updated to match a legitimate evolution"?
- What's the priority ordering when invariants conflict? (e.g., a security invariant vs. a performance invariant)
- How do agents handle ADRs that reference context outside the repo (Slack discussions, meeting decisions, JIRA tickets mentioned in the ADR's context section)?
- Should agents that detect invariant violations be able to propose new superseding ADRs, or should that always be human-initiated? (Note: ADRs are not amended after acceptance — proposing a change means writing a new ADR that supersedes the existing one.)
