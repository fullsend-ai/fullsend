# Kubernetes repository identity v1

This contract defines how Kubernetes namespace provisioners and token mints
agree on repository identity under
[ADR 0037](../../../ADRs/0037-kubernetes-repository-security-boundaries.md).
It is a target design for enabling Kubernetes as a Fullsend runtime;
implementation and forge-specific credential adapters remain follow-up work.

## Versioning

Changes to URL canonicalization, namespace derivation, or required token claims
that alter accepted identities require a new major contract version and an
explicit migration for provisioners and mints. Authentication failure must not
trigger fallback to an older or alternate identity derivation. Additive display
labels that do not affect authentication are compatible with v1.

## Namespace names

Names follow `[provider]-[core_name]-[hash]`, with a strict 63-character maximum:

1. **Provider:** a registered one- or two-character lowercase Git host type ID:
   `gh` for GitHub, `gl` for GitLab, and `fj` for Forgejo.
2. **Hash:** the full SHA-256 digest of the UTF-8 canonical repository URL,
   encoded as lowercase, unpadded [RFC 4648 base32][base32] (52 characters),
   before name sanitization or truncation.
   Use the forge-resolved HTTPS repository URL, including the host, any
   non-default port or installation path prefix, and the complete owner/subgroup
   and repository path, without credentials, query, fragment, clone `.git`
   suffix, or trailing slash; retain the forge's canonical path spelling.
3. **Core name:** sanitize the complete organization/owner path and repository
   name separately by lowercasing and replacing each character outside ASCII
   `[a-z0-9]` with `-`, then trim leading and trailing hyphens and join the two
   components with `-`; reject an empty sanitized input component.
4. **Truncation:** cap the core at seven characters even for a one-character
   provider ID. Preserve the repository name first, then take the rightmost
   characters of the organization path that fit in the remaining space,
   including the joining hyphen. This discards the left of the organization
   path and retains its deepest subgroup names. Trim boundary hyphens after
   truncation and omit an empty organization component and its separator.
   If the repository alone exceeds seven characters, retain its first seven
   characters, trim trailing hyphens, and omit the organization entirely.
5. **Collisions:** before creating or reusing a namespace, compare its full
   repository identity with the intended repository; a name collision with a
   different repository fails provisioning rather than reusing the namespace.
   Retaining all 256 digest bits provides SHA-256's
   [128-bit collision resistance][hash-strength], avoiding dependence on an
   exact per-repository enrollment table at the mint.

The maximum is `2 + 1 + 7 + 1 + 52 = 63` characters. Canonicalization precedes
hashing so alternate clone URL spellings do not create separate namespaces.
The full hash belongs in the namespace name because that name appears in the
signed service account token; namespace labels and annotations do not. A hash
stored only in metadata would require a trusted Kubernetes API lookup.

## Readable namespace labels

Labels expose the repository's names independently of the namespace name's
seven-character core budget:

- `fullsend.sh/provider`: the provider ID.
- `fullsend.sh/host`: the canonical forge hostname, with a separate
  `fullsend.sh/port` label for a non-default port.
- `fullsend.sh/owner-0`, `fullsend.sh/owner-1`, and so on: the organization and
  each successive subgroup, in path order.
- `fullsend.sh/repository`: the repository name itself.

Derive these from the complete canonical identity, never from the namespace
name. For name values, preserve ASCII letters, digits, `.`, `_`, and `-`,
replacing other characters with `-`. Split values longer than 63 characters
into consecutive parts: the first uses the base key and subsequent parts use
`-part-1`, `-part-2`, and so on (for example,
`fullsend.sh/owner-1-part-1`). Trim non-alphanumeric boundary characters from
each part to satisfy [label syntax][labels]; an empty display part is permitted.
This retains readable organization, subgroup, and repository names, including
the portions omitted from the namespace name.

For example:

```yaml
metadata:
  name: gl-agent-r-tagjnwfpqlnnfrylqrmk2djlxqzesivqzw45c627dtvntl23zqfq
  labels:
    fullsend.sh/provider: gl
    fullsend.sh/host: gitlab.example.com
    fullsend.sh/owner-0: acme
    fullsend.sh/owner-1: platform
    fullsend.sh/repository: agent.runtime
```

These labels support human inspection and selectors. The complete canonical
repository URL travels in the mint request, so labels need not encode it
losslessly and neither labels nor annotations participate in mint authorization.

## Mint authentication and scoping

1. The trusted runner uses a short-lived, pod-bound projected service account
   token with the mint's audience, reloading it when Kubernetes rotates it.
   Keep this token outside the agent sandbox under
   [ADR 0017](../../../ADRs/0017-credential-isolation-for-sandboxed-agents.md).
   Each request includes the full canonical HTTPS repository URL, including
   forge origin and complete owner/subgroup/repository path, for example
   `https://gitlab.example.com/acme/platform/agent.runtime`.
2. The mint accepts only trusted cluster issuers and their trusted discovery/key
   endpoints; each issuer identifies one cluster. This is infrastructure trust,
   not per-repository enrollment. A public mint may authorize any repository
   served by those issuers, subject to forge installation and permission checks.
   It verifies the signature against that issuer's [discovered JWKS][discovery],
   checks the signing algorithm, exact issuer, audience, expiry, not-before,
   issued-at, and maximum permitted lifetime, and requires consistent namespace
   and service account claims and `sub`. Token-supplied URLs cannot introduce
   a new issuer or key source.
3. Treat the requested repository URL as untrusted until validated. Require its
   canonical form under the supported forge's URL rules, derive the provider ID
   from a recognized forge origin rather than a caller-supplied prefix, and
   apply the namespace naming algorithm above, including sanitization,
   truncation, and hashing. Compare the **entire resulting namespace name**
   with the token's verified namespace claim, not just its hash suffix; reject
   missing fields, malformed URLs, or any mismatch. Neither the token's namespace
   nor the validated forge origin may be overridden by another request field.
4. Only after these checks does the requested URL establish the caller's forge,
   owner path, and repository identity. Operator-owned service-account-to-role
   policy limits which roles the caller may request without requiring a mint
   registration for every repository. The mint verifies forge installation and
   permission policy and issues credentials scoped to that repository; public
   mode requires no per-repository mint enrollment, while private deployments
   may impose additional allowlists. Any broader request requires a separately
   authorized grant, such as
   [ADR 0083](../../../ADRs/0083-repo-level-foreign-allow-list.md). Kubernetes service account
   admission replaces GitHub workflow provenance for this authentication path;
   it does not bypass authorization or relax the GitHub path's checks.

The mint needs no Kubernetes namespace or metadata read permission for this
path: repository matching uses the request and verified token.
[JWKS verification does not check object liveness][bound-tokens],
so deleting a pod, service account, or namespace alone does not immediately
revoke an unexpired token at the mint. Short lifetimes bound replay; deployments
requiring live Kubernetes revocation checks must also use TokenReview against
the trusted cluster with the mint audience. Already issued forge credentials
remain valid until expiry or forge-side revocation.

## Enforcement

Only the trusted provisioner may create repository namespaces and admit their
workloads, after verifying that the requesting party is authorized to run for
that repository. Agents and repository-controlled workloads cannot create or
rebind namespaces or impersonate other service accounts. Trusting an arbitrary
cluster's JWKS is insufficient: its administrator could create a namespace for
any repository, regardless of hash strength. Public mint access therefore
retains issuer and provisioner trust without a repository registry at the mint.

Renames and transfers require migration into the namespace derived from the new
URL and retirement of the old workloads. The provisioner tracks immutable forge
repository identity to detect URL reuse; a namespace or URL must not be assigned
to a different repository while old workload credentials remain usable.

A namespace is the unit at which repository policy is applied, not sufficient
containment by itself. Require namespace-scoped least-privilege RBAC, enforced
default-deny network policies with explicit required traffic, resource quotas,
and admission/runtime controls that prevent host access or access to another
repository's storage or credentials; [Kubernetes multi-tenancy guidance][tenancy]
describes these complementary controls. Protect those policies from workload
mutation, including workload creation that selects a more privileged service
account. The mint and provisioner run outside repository namespaces; cluster
administrators and the control plane remain trusted infrastructure.

[labels]: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set
[discovery]: https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/#service-account-issuer-discovery
[bound-tokens]: https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/#schema-for-service-account-private-claims
[tenancy]: https://kubernetes.io/docs/concepts/security/multi-tenancy/
[base32]: https://www.rfc-editor.org/rfc/rfc4648.html#section-6
[hash-strength]: https://csrc.nist.gov/projects/hash-functions
