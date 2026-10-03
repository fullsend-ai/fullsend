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
and defines the bot behavior to be added to the existing v1 contracts. It does
not require a future normalized-event or authorization version.

## Context

ADR 0054 authorizes human event actors from current repository permissions. It
also preserves bot-to-bot dispatch through labels and later implementation
exceptions for bot-authored reviews and pull requests. Those exceptions are
necessary because a forge collaborator-permission lookup often cannot resolve
an installed App bot. The source system's bot identity signal and Fullsend's
registered role lookup are separate concerns.

The mint already knows the GitHub Apps and roles it serves. Authorization needs
to use that identity knowledge rather than making each forge adapter infer bot
authority independently. Forges and deployments without the hosted mint need
the same contract from their replacement identity provider.

The historical drift and remaining contract gap are tracked in issue
[#7764](https://github.com/fullsend-ai/fullsend/issues/7764), which motivates
this decision.

This decision does not yet modify the Go runtime implementation (normalized-
event structs, forge adapters, resolver, or dispatch authorization). The
accompanying normative specification documents are updated in this PR to
describe the target contract; those updates do not imply that the behavior is
already deployed.

## Options

Inferring the Fullsend role from a username suffix or forge `actor.kind` is
rejected because it cannot distinguish a registered Fullsend role from an
unrelated or spoofed automation identity. Source-native bot classification is
still allowed where the forge defines an authoritative signal, such as GitHub's
`[bot]` login convention. Repeating App-to-role mapping in every forge adapter
is also rejected because it creates inconsistent trust decisions and cannot
share the mint's authoritative installation knowledge. A provider-backed lookup
centralizes identity resolution while keeping the authorization contract
portable to non-mint deployments.

## Decision

Fullsend introduces a provider-backed bot-role resolution step before event
authorization. The resolver receives the verified source system, target
repository or project, and actor identity from the forge adapter. It returns
one of:

- a recognized bot role;
- no recognized bot role; or
- an error resolving the identity.

The resolver MUST match the verified actor name/identity against an exact
registered bot identity. It MUST NOT accept a
bot role, role name, or authorization result supplied by the event payload or
by a CEL expression. Where the mint is used, it performs this lookup from its
registered role-to-App knowledge and, where the forge exposes it, verifies the
relevant installation on the target. A non-mint deployment MUST provide an
equivalent trusted lookup.

The provider MUST determine whether the verified actor is a bot using the
source system's authoritative actor metadata. This MAY include the provider's
actor name when that forge gives the name bot-specific semantics, as GitHub
does for `[bot]` logins. Labels, review types, and arbitrary event-content
strings are not bot-identity signals. The normalized actor MUST carry the
result in `actor.kind`.

The returned bot role is the canonical registered role name, such as `review`.
It is carried in a separate optional field, so it does not share a namespace
with forge permission roles and does not require a special suffix. For every
bot, `actor.kind` is `bot` and the existing compatibility field
`actor.role` remains `none`. When recognized, `actor.bot_role` contains the
canonical role name; when it is absent, the bot is not authorized by the new
bot gate. For a human, `actor.kind` is `human`, `actor.bot_role` is absent or
`null`, and `actor.role` contains the resolved forge permission role. A bot
role identifies the registered agent identity; it is not a claim that the
bot's content is trustworthy.

Authorization then follows these rules:

1. A recognized bot satisfies only the bot-identity prerequisite for
   bot-originated dispatch. It does not by itself authorize an event, stage,
   forge mutation, or destination. The selected harness's generic
   transition/target policy remains mandatory before dispatch. After
   successful `actor.bot_role` recognition, the actor.role-keyed observation
   and mutation thresholds do not apply to bots; their authorization is
   recognition plus the harness's transition/target policy and any CEL or
   other routing restrictions. Those restrictions MAY narrow this by requiring
   a particular `actor.bot_role`, transition, label, review state, fork state,
   or other policy condition, but MUST NOT broaden authorization to an
   unrecognized bot. This same contract supports BYOA identities without a
   bot-name-to-stage allowlist.
2. A bot with no recognized role, or a bot whose lookup fails, is denied before
   CEL evaluation. Neither bot classification without a recognized role, nor
   a label transition, nor a bot-authored review is by itself sufficient
   authorization evidence.
3. Human actors continue to use ADR 0054's current permission thresholds and
   configured permission providers, including `OWNERS` where enabled.
4. The normalized event retains `actor.kind`, `actor.role: "none"`, an
   optional `actor.role_verified` flag, and the optional resolved bot role. For
   bots, the flag is true when the provider completed the bot-role lookup,
   whether or not it found a registered role; it is false when resolution
   failed. For humans, it is true only when `actor.role` is a verified forge
   permission. The resolver and audit record retain whether bot-role
   resolution was successful and recognized, successful but unrecognized, or
   failed. An unavailable or unverifiable actor-resolution result is
   authorization unusable; neither failed nor unrecognized resolution may
   trigger an agent.

Adapters MUST resolve the actor that actually caused the transition. For an
edited comment or other mutable content, authorization uses the editor rather
than the original author. All event content remains untrusted after bot
authorization; identity authorization does not authorize instructions in the
event.

## Consequences

- Bot dispatch authorization becomes based on a registered role identity rather
  than a naming convention or a broad event-specific exception.
- CEL gains a separate canonical bot-role value that can distinguish `review`
  from other recognized agent roles without becoming the authorization boundary.
- Existing v1 consumers continue to receive the bot-compatible `role: "none"`
  value; `role_verified` and `bot_role` are additive and may be absent when no
  role is resolved.
- Mint and non-mint deployments must maintain an exact bot-identity registry and
  fail closed when it is unavailable or incomplete.
- Existing `[bot]` regex carve-outs and generic label/review exceptions remain
  compatibility behavior until the resolver is implemented; they must not be
  mistaken for the target contract.
- Human authorization, least-privilege thresholds, and zero-trust treatment of
  bot-produced content remain unchanged.
