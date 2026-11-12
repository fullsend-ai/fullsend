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

Partially supersedes [ADR 0107][adr-0107]'s provider-backed role lookup for
non-label events arising from Fullsend-authored contributions. A valid
Sigstore-backed role attestation is preferred; ADR 0107
lookup remains a migration fallback only when no attestation is available for a
legacy object or unsupported source. A present but invalid attestation never
falls back to provider lookup. Label events are the only permanent exception
and retain their separate authorization path. Before retiring provider lookup
for Fullsend agents, every supported non-label source type, including commit
and ref events, must carry equivalent role-bound signatures.

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
updates. Workflow platforms that support OpenID Connect (OIDC) can issue a
short-lived identity token to trusted workflow code. Sigstore can bind an
ephemeral signing key to that OIDC identity and return a verifiable bundle,
avoiding a repository secret containing a reusable private signing key. The
public Fulcio service supports GitHub Actions and GitLab CI workflows; other
issuers require a compatible configured Sigstore instance.

The OIDC token is a bearer credential and is not copied into a public comment
or source property. Fulcio validates the token before issuing a certificate;
the Sigstore certificate and bundle carry the authenticated issuer, subject,
and available workflow claims that the verifier can inspect after validating
the certificate chain, signature, and transparency evidence. The selected
Fulcio service also validates the token's audience and lifetime. Fullsend then
applies repository and workflow policy to the certificate claims, comparable
to its existing workflow-token verification. Sigstore's public good instance
is the default; deployments with other OIDC issuers or trust domains can
configure their own Sigstore services and trusted roots. See the
[Fulcio OIDC claim mapping][fulcio-oidc] and
[`sigstore-go` documentation][sigstore-go-docs].

## Options

### Resolve every role from a platform actor

This uses forge-authoritative account metadata, but depends on each platform
exposing a stable mapping from the actor to a role. ADR 0107 remains useful for
existing objects and sources that have not yet adopted attestations.

### Use long-lived per-role signing keys in platform secrets — not adopted

At installation, Fullsend could generate a key pair per role, store each
private key as a platform secret, and publish the corresponding public key in
a platform variable. Writers could sign source content and attach a hidden
signature envelope. This would work without a platform OIDC issuer, but
pull-request workflows may not be able to read the required secrets. A leaked
private key would also remain usable to impersonate its role until it was
revoked or expired, requiring a rotation and historical-key policy. These
operational and key-exposure costs make long-lived role keys unsuitable as the
adopted cross-platform mechanism.

### Use Sigstore keyless signing with workflow OIDC — adopted

Trusted workflow code obtains a run-scoped OIDC token, uses Sigstore to sign a
role-bound payload with an ephemeral key, and attaches the resulting bundle to
the contribution. The bundle lets another Fullsend instance verify both the
payload and the workflow identity against a configured Sigstore trust root.

## Decision

We adopt Sigstore keyless signing as the preferred identity for Fullsend-authored
non-label events. Signing and verification SHOULD use the `sigstore-go` library
and its Sigstore bundle and DSSE support rather than a Fullsend-specific
cryptographic format. Signers use an ephemeral key for each signing operation;
Fullsend does not provision or rotate a long-lived private key per role.

The signed payload contains a versioned attestation binding the canonical
source content (or its digest), contribution kind, source-system instance,
repository identity, target entity, stable object or comment identity, agent
role, run context, and relevant workflow identity claims. The role and context
are supplied by trusted host-side dispatch code, not by the model or
agent-authored content. Verification policy maps the authenticated workflow
identity to the roles it is permitted to assert. The role and context are part
of the signed payload and therefore cannot be changed without invalidating the
signature.

The Sigstore bundle carries the signature, certificate, and transparency
evidence needed to verify the payload. The certificate's OIDC issuer, subject,
and available workflow claims are checked against repository and workflow
policy, including the relevant repository identity and workflow provenance.
The exact claim projection is issuer-specific and will be defined by the
versioned protocol. The protocol must also define the expected OIDC audience
and how the verifier relies on the trusted Fulcio service to enforce it, since
audience claims are not necessarily included in issued certificates. Fullsend
MUST NOT store the raw OIDC bearer token in the
bundle or public source metadata, and MUST keep the token confined to trusted
host-side signing code rather than the model or agent sandbox. A valid
signature proves that an allowed workflow identity signed the bound role and
content; it does not by itself grant forge permissions or make the content
safe.

By default, Fullsend uses the public Sigstore services and their TUF-managed
trusted root. A repository or installation may select another Sigstore
configuration and trust bundle through the effective trusted Fullsend
configuration. A remote JSON configuration reference MUST be an HTTPS URL with
a `#sha256=...` integrity hash, following Fullsend's existing pinned-resource
convention. The JSON configuration identifies the OIDC issuer and Sigstore
service endpoints (including Fulcio and Rekor, and a timestamp authority when
used) and supplies or identifies the trust material used to verify bundles.
Any referenced trust material must itself be integrity-protected. These
references are trusted configuration: contribution content, event fields, or
agent output MUST NOT select or override them. A missing or invalid pin,
unavailable configured trust material, or failed signature, certificate,
transparency, issuer, repository, or workflow check fails closed.

This applies to Fullsend-authored comments, reviews, issue or pull-request
creation and updates, and other non-label contribution paths as their carriers
are defined. A platform adapter stores the envelope in per-object metadata
when available, or in a versioned hidden marker when it is not. Receivers
extract and verify the envelope before ordinary content filtering, then remove
the marker from model-visible text. The source-specific protocol MUST bind the
envelope to a stable source-object identity and canonical content.

If a platform assigns an object identity only after creation, the initial
create event is not authenticated as a Fullsend role and MUST NOT dispatch
under ADR 0107 fallback. The writer waits until it can attach an
object-bound attestation, then causes a follow-up event or dispatch. If the
source emits no follow-up event, trusted host-side code must enqueue one; if it
cannot, the contribution remains undispatched and fails closed.

For a non-label event, Fullsend first verifies an available attestation and
uses its role when all signature, trust, and binding checks pass. ADR 0107
provider lookup may supply the role only when the event predates the
repository's attestation rollout or its source type/platform has no
attestation carrier. A present but malformed, invalid, misbound, or
unverifiable attestation MUST fail closed and MUST NOT downgrade to provider
lookup. Temporary bundle, trust-configuration, or metadata retrieval errors
also fail closed. Once a repository enables attestations for a source type,
missing metadata is not a fallback unless the object is covered by an explicit
legacy migration window. Label events continue through their existing,
separate authorization path and do not depend on this attestation.

The identity is scoped to contributions within the repository that configures
the verification policy. Cross-repository verification is possible only by
consulting another repository's trust configuration and is deferred to a
future ADR. Before provider lookup can be retired for Fullsend agents, the
protocol must cover every supported non-label source used by Fullsend agents,
including commit and ref events. Git commit signature encoding is not changed
by this ADR.

## Forge scope

This applies to GitHub, GitLab, Forgejo, and Jira wherever a trusted workflow
can obtain an OIDC token, the configured Sigstore services trust its issuer,
and the source offers a suitable metadata carrier. The public Sigstore
instance is the default where it supports the workflow's OIDC issuer; other
issuers require an explicitly configured compatible Sigstore instance and
trust bundle. Initial carriers cover comments, reviews, and issue or
pull-request content: GitHub, GitLab, and Forgejo adapters use hidden body
metadata or a platform property, while Jira uses issue properties for
work-item content and comment properties for comments. A source without OIDC
or a carrier may use ADR 0107 during migration, but missing or unverifiable
signatures on an attestation-enabled source fail closed. Label events are the
only permanent exception. Before provider lookup can be retired, the versioned
protocol must define canonical encoding, OIDC claim mapping and audience
enforcement, replay handling, trust configuration, and coverage for all
supported non-label sources. Cross-repository verification requires separately
trusted configuration and remains for a future ADR.

## Consequences

- Fullsend can distinguish agent roles across supported workflow platforms
  even when roles share a platform account, without provisioning long-lived
  per-role signing secrets.
- Workflow OIDC availability, identity claims, and trust policy become
  prerequisites for signed identity. Custom issuers require compatible
  Sigstore services and a pinned trusted configuration.
- The public Sigstore services and transparency evidence introduce runtime
  dependencies. Verification fails closed when required trust or verification
  material cannot be obtained.
- Provider-backed lookup remains necessary during migration, but is no longer
  the preferred Fullsend-agent identity and is intended to be retired for
  supported non-label sources.
- A valid signature authenticates the signed content and role context; it does
  not grant forge permissions, make content safe, or prove which human or
  platform account submitted it.

[adr-0107]: 0107-bot-identity-resolution-for-dispatch-authorization.md
[fulcio-oidc]: https://docs.sigstore.dev/certificate_authority/oidc-in-fulcio/
[sigstore-go-docs]: https://github.com/sigstore/sigstore-go/tree/main/docs
