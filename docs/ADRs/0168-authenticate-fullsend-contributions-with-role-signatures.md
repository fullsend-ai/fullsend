---
title: "168. Authenticate Fullsend contributions with repository-scoped role signatures"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - authorization
  - identity
  - signatures
  - comments
  - jira
  - pull-requests
---

# 168. Authenticate Fullsend contributions with repository-scoped role signatures

Date: 2026-11-10

## Status

Accepted

Partially supersedes [ADR 0107](0107-bot-identity-resolution-for-dispatch-authorization.md)'s
provider-backed role lookup for non-label events arising from Fullsend-authored
contributions. A valid signed attestation is preferred; ADR 0107 lookup remains
a migration fallback when no attestation is available. Label events are the
only permanent exception and retain their separate authorization path. Before
retiring provider lookup for Fullsend agents, every supported non-label source
type, including commit and ref events, must carry equivalent role-bound
signatures.

This accepted ADR selects target role evidence but does not change the current
Authorization v1 or NormalizedEvent v1 contracts. Implementations MUST
continue to follow those normative contracts until a versioned contract update
defines how verified attestations populate normalized actor fields and how
each source type moves from provider lookup to signed identity.

## Context

[ADR 0107](0107-bot-identity-resolution-for-dispatch-authorization.md) requires
a trusted provider to map a platform actor to a registered agent role. Some
forge, tracker, and runtime configurations cannot provide a distinct platform
identity for each role, so that mapping cannot be the common source of role
identity.

Fullsend needs a role identity that travels with each agent-authored source
artifact and can be checked by another Fullsend run for the same repository.
This applies to comments, reviews, and issue or pull-request creation and
updates. The runtime platform is assumed to provide protected secret storage
and readable repository variables; the source system must also provide a
hidden-body or custom-property carrier.
[ADR 0154](0154-correlate-work-item-and-behaviour-scenario-traces.md) chose
Ed25519 for signed trace envelopes. Ed25519 is specified in
[RFC 8032](https://www.rfc-editor.org/rfc/rfc8032.html) and is available in
Go's standard library ([`crypto/ed25519`](https://pkg.go.dev/crypto/ed25519)),
so this use does not require an OpenPGP keyring or message container.

This ADR aims to make repository-scoped signatures the preferred role evidence
for all Fullsend-authored non-label source events, with provider lookup retained
only for migration gaps.

## Options

### Resolve every role from a platform actor

This uses forge-authoritative account metadata, but depends on each platform
exposing a stable mapping from the actor to a role.

### Prefer signed contribution attestations and retain provider fallback

This makes role evidence portable across source systems while allowing legacy
objects and event sources without a carrier to continue using ADR 0107 during
migration.

## Decision

We adopt per-repository, per-role Ed25519 attestations as the preferred identity
for Fullsend-authored non-label events, so consumers can recognize the role
independently of its platform account.

At installation, Fullsend generates a distinct key pair for every configured
agent role. It stores the private key and its key ID, creation time, and
expiration time in a protected secret named under
`FULLSEND_AGENT_SIGNING_KEY_<ROLE_ID>`. It publishes the corresponding public
key record in a repository variable named
`FULLSEND_AGENT_PUBLIC_KEYS_<ROLE_ID>`. `<ROLE_ID>` is the canonical role
identifier encoded using the platform's variable-name rules. Each public-key
variable contains the current key and a bounded set of retained public keys
with their validity metadata. Repository-variable editors are trusted to
publish this keyring and therefore define which public keys may authenticate
roles in the repository. This ADR does not require a separate protected root
to authenticate the variable; signatures do not protect against an authorized
variable editor replacing the keyring.

Only trusted host-side Fullsend code may read the private key and sign a
contribution. Installed workflows explicitly forward the selected role secret
to that signer; they MUST NOT pass it to an agent sandbox or model context.
Role and run context come from trusted dispatch configuration, not from
agent-supplied attestation fields.

The attestation applies to Fullsend-authored comments, review content, and
issue or pull-request creation and revisions. It binds a versioned envelope to
the canonical source content, contribution kind, role, source-system instance,
repository, target entity, stable object or comment identity, run context, key
ID, and signing time. The signature covers the exact content and relevant event
fields according to the protocol's canonicalization rules. A platform adapter
stores the envelope in per-object metadata when available, or in a versioned
hidden marker when it is not. If the platform assigns an object identity only
after creation, the adapter adds the attestation after receiving that identity.
The initial create event is not authenticated and MUST NOT dispatch as a
Fullsend role through ADR 0107 fallback. The writer must attach the finalized
attestation and cause a follow-up event or dispatch; consumers may dispatch
only after verifying that attestation. If the source emits no follow-up event,
trusted host-side code must enqueue a follow-up after the attestation write; if
it cannot, the contribution remains undispatched and fails closed. Receivers
extract and verify the envelope before ordinary content filtering, then remove
the marker from model-visible text. Before provider fallback can
be retired, the protocol must define an equivalent role-bound signature for
every other non-label source used by Fullsend agents, including commit and ref
events; no non-label source type is permanently exempt from signing.

For a non-label event, Fullsend first verifies an available attestation and
uses its role when all signature and binding checks pass. ADR 0107 provider
lookup may supply the role only when the event predates the repository's
attestation rollout or its source type/platform has no attestation carrier. A
present but malformed, invalid, expired, misbound, or unverifiable attestation
MUST fail closed and MUST NOT downgrade to provider lookup. Temporary key or
metadata retrieval errors also fail closed. Once a repository enables
attestations for a source type, missing metadata is not a fallback unless the
object is covered by an explicit legacy migration window. Label events continue
through their existing, separate authorization path and do not depend on this
attestation.

An installation rotates a role key during install or upgrade when its
configured expiration is approaching. A standalone Fullsend command also
allows operators to rotate role keys independently of installation or upgrade
cadence. It keeps a bounded set of previous public keys so contributions
already signed by them remain verifiable for a limited period. The protocol
contract will define the command interface, key lifetime, renewal threshold,
retention cap, and legacy migration window using platform variable-size limits
and the maximum supported delayed-event window, then revisit those initial
values after production use.

The identity is scoped to contributions within the repository that publishes
the key set. Cross-repository verification is possible only by consulting
another repository's public keys and is deferred to a future ADR. Git commit
signature encoding is not changed by this ADR, but commit and ref events are
not permanently exempt from role-bound signatures.

## Forge scope

This applies to GitHub, GitLab, Forgejo, and Jira. The initial carriers cover
comments, reviews, and issue or pull-request content: GitHub, GitLab, and
Forgejo adapters use hidden body metadata or a platform property, while Jira
uses issue properties for work-item content and comment properties for
comments. Every adapter must bind the envelope to a stable source-object
identity. ADR 0107 provider lookup is a migration fallback for legacy objects
and source types until they gain a carrier; missing or unverifiable signatures
on an attestation-enabled source fail closed. Label events are the only
permanent exception. Before removing provider lookup for Fullsend agents, all
supported non-label event sources must have an attestation carrier, and their
writers and installed workflows must use the trusted signer and forward its
role secret only to host-side code.

## Consequences

- Fullsend can distinguish agent roles across comments and issue or
  pull-request content even when roles share a platform account, while each
  repository becomes responsible for provisioning and rotating role keys.
- Provider-backed lookup remains necessary during migration, but is no longer
  the preferred Fullsend-agent identity and is intended to be retired for
  supported non-label sources.
- A valid signature authenticates the signed content and role context; it does
  not make the content safe, grant forge permissions, or prove which human or
  platform account submitted it.
- A compromised role key can forge that role's attestations until the key is
  revoked or expires, so key isolation and bounded public-key retention are
  security requirements. Repository-variable editors are trusted to authorize
  role keys and can replace the keyring.
- A versioned protocol contract must define canonical encoding, carrier and
  migration details, key-name constraints, timestamp and replay handling,
  rotation thresholds, and retention periods; cross-repository role
  authentication remains for a future ADR.
