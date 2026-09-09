---
title: "111. First-class agent promotion criteria"
status: Accepted
relates_to:
  - agent-architecture
  - testing-agents
  - governance
topics:
  - agents
  - catalog
  - process
---

# 111. First-class agent promotion criteria

Date: 2026-09-09

## Status

Accepted

## Context

New agents start as custom prototypes in a consumer repo
([ADR 0058](0058-agent-registration.md)), bake in production, and
sometimes graduate into the default catalog in
[fullsend-ai/agents](https://github.com/fullsend-ai/agents). Prioritize
already shipped that way without recorded criteria. The catalog is
what every adopter sees; it has to stay small enough to learn by heart
and general enough that most users want each entry.

## Options

**A. Promote ad hoc.** Fast, and how prioritize landed. The catalog
grows without a shared definition of "first-class."

**B. Criteria, maturity bar, and a memorability cap.** Promote only
agents that expand the SDLC (or are broadly useful regardless of
toolchain), have baked in production, and fit under a soft cap.

**C. Freeze the catalog.** Protects memorability absolutely, but
blocks expanding into new SDLC stages.

## Decision

Adopt Option B.

An agent is **first-class** when it ships in `fullsend-ai/agents` as
part of the default catalog. Anything else is **custom**, including
prototypes that might later graduate. This is catalog membership, not
the configured-default / derived / custom axis in
[default-vs-custom.md](../agents/topics/default-vs-custom.md).

**Lifecycle.** Prototype as a custom agent in a consumer repo → bake
in production → promote into `fullsend-ai/agents`, or remain custom.
Experimental directory layout is tracked separately in
[#1084](https://github.com/fullsend-ai/fullsend/issues/1084).

**Catalog fit.** All of: (1) expands fullsend into an SDLC stage most
adopters will want, **or** is broadly useful regardless of a specific
vendor toolchain; (2) is not a team-internal workflow; (3) does not
push the catalog past the soft cap unless an existing first-class
agent is retired or merged in the same change.

**Maturity bar.** Production bake in at least one non-eval repository
covering the claimed triggers, with no open class of failure that
would ship to every adopter; user-facing docs (purpose, triggers,
extension points); script tests plus at least one functional test
case ([ADR 0052](0052-functional-tests-for-agent-pipelines.md)); an
output schema
([ADR 0022](0022-harness-level-output-schema-enforcement.md)); a
sandbox scoped to the agent's responsibility
([ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md));
maintainer sign-off that catalog fit still holds.

**Soft cap.** The default catalog stays at or below **15** first-class
agents. Users should be able to name them from memory; each entry
carries a harness, identity, docs, evals, and support cost. Exceeding
15 requires retiring or merging an existing entry in the same change,
per catalog-fit criterion (3) above — there is no other path past the
cap.

The operational checklist and a point-in-time classification live in
[first-class-agents.md](../contributing/first-class-agents.md).

## Consequences

- Promotion is a reviewed decision against a shared bar, not an
  informal "it worked for us."
- Custom agents remain the default path for internal or
  toolchain-specific work;
  [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) is
  unchanged.
- Existing catalog entries are not demoted by this ADR.
- Experimental placement (directory layout, clutter) remains open in
  #1084.
