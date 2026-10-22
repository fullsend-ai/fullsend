---
title: "107. Resolve bot identity before dispatch authorization"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - authorization
  - identity
  - dispatch
  - bots
---

# 107. Resolve bot identity before dispatch authorization

Date: 2026-09-27

## Status

Accepted

Extends [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)
and defines the bot behavior to be added to the existing v1 contracts. This ADR
explicitly permits the deferred migration from legacy bot exceptions to
provider-backed bot identity within authorization v1: the normalized-event
fields are additive, and existing compatibility behavior remains authoritative
until each adapter is updated. The migration does not
require a normalized-event schema version change or expand the supported bot
population; it replaces legacy permission handling with registered identity
handling for supported bots. The affected contracts are
[Authorization v1](../normative/authorization/v1/) and
[NormalizedEvent v1](../normative/normalized-event/v1/).

## Context

ADR 0054 authorizes human event actors from current repository permissions. It
also preserves bot-to-bot dispatch through labels and contains implementation
exceptions for bot-authored reviews and pull requests, because a forge
collaborator lookup often cannot resolve an installed App bot. The source
system's bot signal, the mint's registered bot identity, and the bot's
credential permissions are separate concerns.

The mint already knows the GitHub Apps and roles it serves. Authorization needs
that identity knowledge rather than independent, forge-specific guesses. Forges
and deployments without the hosted mint need an equivalent trusted provider.
The historical drift and contract gap are tracked in
[#7764](https://github.com/fullsend-ai/fullsend/issues/7764).

At the time of this ADR, this is a target contract; the Go runtime, adapters,
resolver, and dispatch authorization have not yet migrated. Existing
compatibility behavior remains authoritative until those components migrate.

## Decision

Fullsend adds a provider-backed bot-role resolution step before authorization
for bot-originated dispatch, except for the permanent label-triggered
exception defined below. The resolver receives the verified source system,
target repository or project, and actor identity. It returns a recognized bot
role, a successful no-match result, or a resolution error.

The provider MUST classify the verified actor using authoritative source metadata.
It MAY use a provider-controlled naming convention, such as GitHub's `[bot]`
login semantics. Labels, review types, and arbitrary event content are not bot
identity evidence. The resolver MUST match the verified identity exactly
against a registered bot identity and MUST NOT accept a role or authorization
claim from the event payload or CEL. A non-mint deployment MUST provide an
equivalent trusted lookup.

For a bot, the normalized representation keeps `actor.role` as `none` and
places a recognized canonical role, such as `review`, in `actor.bot_role`. For
a human, `actor.bot_role` is absent and `actor.role` contains the
verified forge permission. `bot_role` identifies the registered agent; it does
not make the bot's content trustworthy.

The platform authorization rules are:

1. For non-label bot dispatch, a provider-positive bot classification and a
   successful, recognized `bot_role` are required. After that recognition,
   `actor.role`-keyed observation and mutation thresholds do not apply to the
   bot. Platform policy still requires a valid normalized event, an applicable
   source and target, and a transition supported by the selected harness.
   Harness/CEL policy may further restrict the recognized bot, transition,
   label, review, fork, or target, but cannot authorize an unrecognized bot.
   This boundary prevents any bot capable of producing a forge event from
   satisfying a CEL routing expression and thereby becoming an eligible
   dispatch signal without the required Fullsend/provider trust relationship.
   The event content remains untrusted; the restriction concerns admission of
   the event as a trigger. Supporting selected third-party bots beyond the
   registered provider identities will require a future ADR defining a
   provider-backed allowlist, including its scope, ownership, and revocation.
2. The label-added exception is preserved as current and target behavior; this
   ADR does not introduce it or make it temporary. For GitHub, the source
   forge's accepted label mutation, with authoritative actor-to-transition
   provenance, is a separate platform authorization grant and bypasses the
   ordinary `actor.role` stage threshold. At the time of this ADR, GitHub is
   the only implementation. Any other adapter MUST provide equivalent
   forge-authoritative evidence before enabling the same grant; absent or
   unverifiable evidence denies the event. For a bot actor, the adapter MUST
   positively classify it using provider-controlled metadata, but `bot_role`
   resolution is not required. The platform gate MUST NOT inspect label names
   or maintain an agent-role label allowlist; harness/CEL routing owns that
   agent-specific mapping and may further narrow the dispatch.
3. A bot with no recognized role, or a bot whose lookup fails, is denied on
   non-label paths before CEL evaluation. The label exception is the explicit
   exception to this rule. Human actors continue to use ADR 0054's permission
   thresholds and configured providers, including `OWNERS` where enabled.
4. `role_verified` is additive lookup status: for humans it is true only for a
   verified forge role; for bots it is true for a completed bot-role lookup,
   including a successful no-match result, and false for lookup failure. The
   label exception does not require that lookup, so `role_verified` is not an
   authorization input on that path; omission there means not applicable, not
   resolver failure. Pre-migration events without the field retain v1
   compatibility behavior; new adapters MUST emit it.

The mint's installation permission map remains a credential-scoping check. It
is not converted into `actor.role`, `bot_role`, or a bot event threshold.
Recognized bot registration is intentionally a separate platform dispatch
grant: it MUST be scoped to the exact provider identity and target, and the
resolver MUST honor registration removal or lookup failure before dispatch.
It does not elevate forge permissions or token capabilities, and CEL remains a
narrowing filter rather than an additional authorization grant.

## Consequences

- Non-label bot authorization is based on an exact registered identity rather
  than a naming convention or broad review exception; CEL remains a routing
  filter, not a source of bot identity.
- Human permission thresholds and the existing label-triggered handoff behavior
  remain unchanged; label names remain harness-specific rather than platform
  authorization policy.
- Mint installation permissions continue to enforce least-privilege token
  capabilities, but are deliberately not reused as event actor roles.
- Existing CEL rules that route reviews from non-Fullsend bots will no longer
  match after non-label bot resolution is deployed unless the provider
  recognizes those bots; this is an intentional migration consequence.
- A registered bot can be admitted to a mutation harness without a
  `write`-valued `actor.role`; that is an intentional platform dispatch grant
  recorded by registration, not an inference from forge permissions. The
  registration must therefore be reviewed, scoped, and revocable like any
  other platform authorization input.
- Non-mint providers, including the GitLab path, must supply equivalent bot-role
  lookup for non-label bot dispatch; bot classification alone is insufficient.
