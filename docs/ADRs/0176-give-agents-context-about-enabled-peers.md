---
title: "176. Give Agents Context About Enabled Peers"
status: Accepted
relates_to:
  - agent-architecture
  - codebase-context
topics:
  - agent-context
  - harness
  - entities
---

# 176. Give Agents Context About Enabled Peers

Date: 2026-11-24

## Status

Accepted

## Context

Fullsend agents run independently, with each invocation focused on its assigned
work item or change proposal. An agent may know its own limits without knowing
what other enabled agents can do next. That can make it avoid useful work: for
example, a code agent may hesitate to update behaviour tests because it cannot
run them in its sandbox, even when repository CI runs them and the configured
fix agent can inspect failures when that agent is dispatched.

Without shared knowledge of peer capabilities, teams must repeat the same
pipeline explanations in multiple agent prompts, and agents can make cautious
choices based on an incomplete view of the process. Repository-specific facts,
such as which workflow runs behaviour tests, belong in repository guidance;
portable facts about an agent's capabilities and limits belong with its
harness. Peer context must remain distinct from a prior agent's conclusions or
live run results.

[ADR 0079](0079-bind-entity-execution-through-named-sources.md) defines typed
entity slots in harnesses and binds those slots to repository-owned sources.
This ADR aims to give each run concise, trustworthy context about enabled peer
agents that can handle the same kinds of entities, without introducing a
coordinator or changing how Fullsend decides which agent should act.

## Options

- **Filter by agent role.** Rejected because role values are tied to permission
  and do not describe the entities an agent handles.
- **Give every enabled harness every peer note.** Rejected because it sends
  irrelevant guidance to agents that do not handle the referenced entity kind.
- **Pass agent-produced messages as peer context.** Rejected as the mechanism
  for stable guidance because messages describe particular runs, may be absent,
  and have different trust and freshness properties.

## Decision

We adopt harness-declared static peer context filtered by each recipient's
effective entity slots, so agents receive relevant capability and limitation
notes without coupling context selection to permission roles.

Each peer-context declaration identifies its audience with one or more clauses.
A clause names one or more recipient slot and entity-kind pairs, such as
`source: work_item` or `target: change_proposal`. All pairs within a clause must
match; separate clauses are alternatives. A selector matches only when that
slot is declared with the specified kind and has an effective, non-empty
repository binding after harness composition, configuration layering, and
defaults are resolved. A declaration without a slot selector is invalid.
Event-only harnesses without matching typed slots receive no peer context from
this mechanism.

Fullsend collects declarations only from enabled, effectively resolved
harnesses and injects matching notes as informational context with their
harness identity and revision. Notes describe capabilities and limits; they do
not override the recipient's own harness or repository instructions. Repository
bindings and slot kinds determine relevance only: peer context does not grant
entity access, write destinations, credentials, or permissions. A `target`
match means that the recipient has that effective default destination; it does
not assert that the agent created an entity there.

Peer context describes stable harness capabilities and limits, not current run
outcomes. It does not define dispatch conditions or an agent sequence. Eligibility
and coordination continue to follow entity state and harness predicates under
ADR 0079. Repository-specific behavior, including CI coverage and whether a CI
failure can dispatch a fix agent, belongs in repository guidance and the
effective trigger configuration. Peer notes must not promise such behavior
unless that configuration provides it.

## Forge scope

This decision is forge-neutral and filters on normalized entity kinds and
harness slots. It applies where Fullsend has validated entity and model-context
support: GitHub and GitLab work items and change proposals, and Jira work items
under ADR 0079. Forgejo and additional entity kinds remain outside this scope
until their entity, discovery, context, and execution support is validated.

## Consequences

- Harness authors can describe stable capabilities once and have Fullsend
  include the note only for recipients with matching effective slots.
- Slot-based filtering avoids using permission roles as context selectors while
  leaving authorization and entity-source policy unchanged.
- Repository-specific CI instructions and live CI results remain separate from
  portable peer context.
- Harness owners must keep peer notes accurate when capabilities change;
  configuration provenance makes the selected note source identifiable.
