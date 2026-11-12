---
title: "155. Select native or Vault sources for runner secrets"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - secrets
  - configuration
  - workload-identity
---

# 155. Select native or Vault sources for runner secrets

Date: 2026-11-03

## Status

Accepted

## Context

Fullsend reads selected long-lived credentials in the runner process, including
Jira credentials, OTLP collector headers, and the optional GitHub bundle for
host-side scripts. Today these values come from forge-native secrets or
variables, or from the local process environment. Jira runner-secret examples
are described in [ADR 0024](0024-harness-definitions.md); OpenTelemetry
settings are in [ADR 0050](0050-distributed-tracing-instrumentation.md).
[ADR 0136](0136-runner-secrets-through-one-stored-secret.md) records the
GitHub bundle and anticipates an external secret manager accessed with the
job's OIDC identity.

Copying a long-lived value into each repository's forge secret store is
unnecessary when the organization already operates a central secret manager.
Repository-shared configuration also needs to select different Vault paths for
different target repositories. This ADR aims to let trusted runner code resolve
selected user-managed secrets from HashiCorp Vault through workload identity,
while retaining native delivery by default and keeping resolved values out of
agent sandboxes.

## Options

1. **Keep forge-native storage only.** This preserves the current delivery
   model, but each repository must continue to maintain its own copy of every
   selected long-lived value.
2. **Choose one secret store for an installation.** This simplifies
   configuration, but forces all secrets into one backend even when some
   should remain forge-native.
3. **Define named connections to secret stores and select one for each
   secret.** This lets multiple secrets reuse one Vault endpoint and
   authentication setup, while a shared configuration layer can route
   selected values to Vault and leave the rest on the existing native source.

## Decision

We adopt named connections to secret stores in layered configuration. Each
connection names a service kind and its connection settings; a secret selects
the named connection it uses. This lets multiple secrets share one Vault
configuration without adding a long-lived Vault credential to a forge.

The top-level `connections` mapping names reusable connections to secret
services. Each entry has a required `kind` that identifies the service. The
top-level `secrets` mapping names a registered Fullsend runner secret and
selects a connection. A Vault configuration has this shape:

```yaml
connections:
  vault:
    kind: vault
    vault_address: https://vault.example.com
    vault_auth_path: jwt
    vault_role: fullsend-runner
    vault_audience: https://fullsend.sh/vault

secrets:
  JIRA_TOKEN:
    connection: vault
    vault_secrets:
      - kv/data/fullsend/repositories/{{repo.host}}/{{repo.full_name}}/jira
      - kv/data/fullsend/organizations/{{repo.host}}/{{org}}/shared/jira
```

- A connection name, such as `vault`, is an arbitrary alias. Each declared
  connection must specify `kind`; there is no default and Fullsend does not
  infer it from the connection name. `kind` identifies the secret service and
  how Fullsend connects to it. V1 defines `kind: vault`; unknown or missing
  kinds fail closed. Additional kinds can be added without changing the
  `secrets` shape. `native` is a reserved built-in connection name and cannot
  be declared under `connections`.
- `vault_address` is required for a Vault connection, is the Vault API base
  URL, and must use HTTPS. `vault_namespace` is optional and selects a Vault
  Enterprise namespace. `vault_auth_path` is optional and defaults to `jwt`;
  it names the mount path of the JWT auth method, without the `auth/` prefix.
  `vault_role` defaults to `fullsend-runner`, and `vault_audience` defaults to
  `https://fullsend.sh/vault`. These defaults may be overridden for a Vault
  deployment that uses different names or audiences. Vault-specific connection
  fields are valid only when `kind: vault`.
- `secrets.<name>.connection` selects a named entry from `connections`. If it
  is omitted, the secret continues to use the forge-native source. The
  built-in connection name `native` may be used to select that source
  explicitly; it does not need a declaration under `connections`.
  References to undeclared connection names are configuration errors.
- `vault_secrets` is valid only when the selected connection has `kind: vault`
  and must be a non-empty ordered list of Vault KV v2 API read paths, relative
  to `/v1/` and including the mount's `/data/` segment (see the
  [KV v2 read API](https://developer.hashicorp.com/vault/api-docs/secret/kv/kv-v2#read-secret-version)).
  Fullsend reads the latest version and takes the field named for the configured
  Fullsend secret from the response's `data.data` map, such as `JIRA_TOKEN`.
- Fullsend requests the job's OIDC token for `vault_audience` and submits it to
  the configured JWT auth mount and role (including the default role when
  omitted). The audience must match a `bound_audiences` value on that Vault
  role. The role must be configured in Vault to bind the expected issuer and
  trusted repository/workflow identity, and to issue a short-lived token
  limited to the required secret paths. The defaults do not grant access;
  Vault's role and policy remain the authorization boundary.
- Fullsend tries candidates in order. A missing path, a path denied by the
  Vault policy, or a record without the named field advances to the next
  candidate. The first readable candidate containing a non-empty string wins.
  Vault authentication, transport, and server errors fail the resolution
  immediately. If no candidate supplies a value, the run fails; it does not
  fall back to the forge-native value.
- Configuration layers merge `connections` by connection name and `secrets`
  by secret name. A more-specific definition replaces the entire matching
  entry; fields within that entry and Vault candidate lists are not merged.
  This keeps connection identity and secret-path precedence explicit.
- Connection definitions and secret entries must come from trusted
  configuration revisions. This includes each secret's `connection` selection
  and complete ordered `vault_secrets` list. A PR-head or event-provided layer
  cannot define or override these entries, including by removing a trusted
  Vault selection or omitting its `connection` to restore the native default.
  A more-specific layer may replace an entry only when that layer is trusted.
  Fullsend rejects an untrusted override before using the job's OIDC token.

Service-specific endpoint and authentication fields live on the connection;
secret locators, such as `vault_secrets`, live on each secret entry. Future
secret services can add their own connection kinds and fields without
changing the top-level `connections` and `secrets` shape. Connection settings
include the Vault address, so the job's bearer OIDC token is never sent to an
address selected by untrusted PR-head or event-provided configuration. The
Vault address must also pass deployment egress policy. Fullsend requests a
short-lived Vault token for the current invocation, reads the configured
values, and neither persists that token nor caches values between
invocations. There is no static `VAULT_TOKEN` or AppRole-secret fallback.

Vault paths may use `{{forge}}`, `{{repo.host}}`, `{{repo.owner}}`,
`{{repo.name}}`, `{{repo.full_name}}`, and `{{org}}`. `repo.owner` is the
canonical owner or top-level namespace; `repo.full_name` is the canonical
namespace/project path, including nested GitLab groups; `org` is available
only when the target belongs to an organization or group. Repository-isolated
paths must include both `repo.host` and `repo.full_name`, preserving every
nested namespace segment. Organization-shared paths use a separate
`organizations/` prefix so they cannot collide with repository paths or a
repository named `shared`. Values come from the trusted target-repository
context, not PR-head or event-supplied repository names. `forge`, `repo.host`,
`repo.owner`, `repo.name`, and `org` each form one path component. Fullsend
splits `repo.full_name` on `/` and handles each namespace/project component
separately, preserving nested groups. Every component must be non-empty, must
not be `.` or `..`, and must not contain `/`, `\`, or control characters.
Fullsend encodes each validated component using path-segment semantics and
joins the encoded components; it never inserts raw template values into the
Vault request URL. After expansion, Fullsend validates the complete relative
KV v2 API path: it has no leading `/`, scheme, authority, query, fragment,
empty component, `.` or `..` component, or path escape. Unknown or unavailable
variables and invalid paths fail closed before Vault access. The variable
syntax is reserved for path interpolation and is distinct from shell
environment expansion.

The initial Vault-eligible set is user-managed runner secrets whose lifecycle
Fullsend does not create or rotate: `JIRA_TOKEN`,
`OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_TRACES_HEADERS`, and
`FULLSEND_GITHUB_RUNNER_SECRETS`. The bundle retains the host-side-only,
reference-only rules in ADR 0136 and ADR 0145. Forge CLI authentication such as
`GH_TOKEN` and `GITLAB_TOKEN` remains explicit operator authentication outside
this per-repository configuration. Installer-managed GitLab role, dispatch,
webhook, and trigger credentials remain native in this decision: their
creation and rotation require a write and ownership contract with the selected
secret store.

Fetched values are scoped to their registered Fullsend consumers. They are not
written into config files, logs, or the agent sandbox. A Vault lookup failure
for a configured Vault source is a hard error rather than a reason to use a
different credential source.

## Forge scope

The configuration and connection-selection contract is forge-neutral. Vault
resolution via WIF is supported for GitHub Actions and GitLab CI, using each
platform's job-issued OIDC token and a Vault JWT role configured for that
issuer. The job token's audience must match the configured value; the
Fullsend audience is the default. GitLab workflows must declare an ID token
with the selected audience, because Fullsend cannot change the audience of an
already-issued token. Forgejo uses `native` when a secret omits its
connection or selects `native`; an explicit Vault selection fails closed until
Fullsend has a trusted Forgejo job OIDC token integration. Local runs likewise
use `native` only when no Vault connection is selected. An explicit Vault
selection without a supported WIF identity fails before any native credential
is read. Local runs do not accept a static Vault token as a fallback.

## Consequences

- Operators can reuse named connections across runner secrets and
  use shared, repository-templated configuration to select per-repository
  paths.
- Forge-native storage remains the default, and an explicitly selected Vault
  source fails closed rather than silently using a stale native value.
- Vault-backed names in this decision are read-only and user-managed;
  installer-generated GitLab credentials stay native until secret-store writes and
  their rotation lifecycle are decided.
- Vault availability and WIF policy become run prerequisites for entries that
  select Vault, while secret values and Vault tokens are not persisted between
  runs.
- New secret services can be added as connection `kind` values with
  service-specific configuration; each requires its own authentication,
  access-policy, and failure-semantics decision.
