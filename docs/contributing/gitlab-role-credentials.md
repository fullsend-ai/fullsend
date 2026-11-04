---
title: GitLab Role-Credential Contract
---

# GitLab Role-Credential Contract

This is the internal contract for GitLab responsibility identities:
built-in **Poller**, **Analyst**, and **Coder**, plus optional
administrator-registered **custom roles**. It implements
[#7497](https://github.com/fullsend-ai/fullsend/issues/7497) under the
three-role decision in [#7424](https://github.com/fullsend-ai/fullsend/issues/7424)
and parent [#7496](https://github.com/fullsend-ai/fullsend/issues/7496).

The Go package is [`internal/gitlabroles`](../../internal/gitlabroles/).
Provisioning of built-in and custom role credentials is implemented by
`repos install` (`internal/repos` / `internal/cli`). Routing of jobs and
forge operations by registered role is implemented by `fullsend poll`,
`fullsend run`, and `fullsend post-review`. Rotation, recovery, and
in-flight overlap are implemented by `RotateGitLabRoleCredentials`
(`internal/repos`) and invoked from `repos install`. Built-in Poller,
Analyst, and Coder readiness is `CheckBuiltinReadiness` (surfaced on
`repos status`). Ordinary unflagged `repos install` provisions every
registered role; it does not retire a leftover legacy shared token —
there is no automated path for that. Rotation must follow the
[credential-routing security checklist](#credential-routing-security-checklist).

Built-in and custom roles are the same kind of registry entry. Job
credential selection walks that registry; it does not switch on a
three-role enum.

**Role credentials are the only runtime path.** `fullsend poll` and
`fullsend run` select the registered role credential via
`gitlabroles.Select` / `SelectAgent` and fail closed if that secret is
missing. There is no shared-token fallback. There is no migration-gate
variable, flag, or state in the runtime, install, or uninstall contract.
A repository installed before this role-only model shipped may still
carry a leftover `FULLSEND_FORGE_TOKEN` secret; no automated path reads,
writes, or cleans it up, so removing it is a manual administrator step.
Custom roles remain optional on existing installations.

## Registered roles

A **registered role** is an allowlisted GitLab responsibility identity.
It has:

- A stable name (`poller`, `analyst`, `coder`, or an administrator-
  chosen custom name).
- A kind: `builtin` or `custom`.
- Responsibility metadata (what the identity is for).
- A credential **reference** (own secret, or reuse of another
  registered role's secret). The registry never stores token values.
- Capability flags used for validation (not GitLab ACL grants).
- Agent / harness-role names that map onto it.

GitLab project-token scopes cannot express endpoint-level least
privilege; separate credentials give distinct audit identities, keep
Analyst eligible for native MR approval when Coder committed, and limit
the blast radius of a single compromise.

### Built-in roles

These three are always in the registry. Existing installations do not
need to declare them.

| Role | Responsibility | Must not |
| --- | --- | --- |
| **Poller** | Event/issue reads, pipeline dispatch, poll-state writes on `fullsend-poll-state-slash` and `fullsend-poll-state-events` | Modify application code or act as the Analyst approval identity |
| **Analyst** | Review, triage, prioritization, retrospectives, issue/reporting, notes, labels | Modify repository code or poll-state branches |
| **Coder** | Repository writes, code/fix work, merge-request creation and updates | Be used as the Analyst approval identity |

Stable Go names: `poller`, `analyst`, `coder`
(`gitlabroles.RolePoller` / `RoleAnalyst` / `RoleCoder`).

### Custom roles

An administrator may register additional roles. A custom role is a
first-class registry entry: the same `Resolve` path and the same
unconfigured / unregistered / auth-failed distinction as the built-ins.

Custom roles are optional. An empty registry variable means built-ins
only.

## Trusted registry

The registry is installation state, not repository content.

| Source | Allowed? |
| --- | --- |
| Built-in table in `internal/gitlabroles` | Yes (always present) |
| Protected CI/CD variable `FULLSEND_GITLAB_ROLE_REGISTRY` | Yes (administrator-controlled JSON) |
| `.fullsend/config.yaml`, harness files, merge-request diffs, issue bodies | **No.** These may *reference* a registered name (for example a harness `role:` field). They must not create, rename, or elevate a role. |

`gitlabroles.LoadRegistry` / `ParseRegistry` are the only constructors
for custom roles. The JSON decoder rejects unknown fields, so a leaked
token cannot hide under a key such as `token`. `secret_name` must be a
CI/CD variable name (`FULLSEND_GITLAB_ROLE_SCANNER_TOKEN`); values that
look like GitLab PATs (`glpat-…`) are rejected.

A custom role cannot:

- Reuse a built-in name (`poller`, `analyst`, `coder`).
- Steal a built-in agent mapping (`review`, `code`, `fix`, …).
- Point `reuse` at an unregistered name or create a reuse cycle.
- Declare an unknown capability.

Harness `role:` and custom-agent names are validated with
`Registry.ValidateAgent`. An unregistered name returns
`ErrUnregistered`. `fullsend poll` / `fullsend run` call that check at
dispatch time; this contract defines the check.

## Credential references

Each registration names how the role authenticates. The registry stores
**references and policy**, never raw secret values.

| `credential` | Meaning |
| --- | --- |
| `own` (default) | The role has its own masked CI/CD variable. Built-in names are listed below. Custom names derive `FULLSEND_GITLAB_ROLE_<NAME>_TOKEN` (hyphens become underscores). |
| `reuse` | The role shares another **registered** role's credential. `reuse` is the target role name. Presence and rotation follow the target. |

Reuse is how a custom agent can share Coder (or another role) without
minting a second PAT. It is not a silent fallback: the job still
selects that role's identity, and a runtime auth failure of the shared
secret still fails closed.

## Capabilities

Capabilities are contract metadata for validation. Every role token is
still GitLab Developer (30) with the `api` scope; do not document these
flags as least-privilege API grants.

| Capability | Typical holder |
| --- | --- |
| `read_issues` | Poller, Analyst, Coder |
| `write_issues`, `write_notes`, `write_labels` | Analyst |
| `approve_merge_request` | Analyst |
| `dispatch_pipeline`, `write_poll_state` | Poller |
| `write_repository`, `write_merge_request` | Coder |

`Registration.Has` is the check routing uses so Analyst cannot perform
code writes through the normal role configuration, a Coder job cannot
approve a merge request, and a custom role cannot exceed the
capabilities the administrator declared.

## Identifiers

### CI/CD variables

Role tokens are **masked, protected** project CI/CD variables. The registry
document is protected and unmasked because it contains policy and credential
references, never secret values.

| Name | Kind | Purpose |
| --- | --- | --- |
| `FULLSEND_FORGE_TOKEN` | masked secret | Legacy shared bot PAT. Runtime never authenticates with it. Neither `repos install` nor `repos uninstall` retires it; a repository installed before the role-only rollout requires manual cleanup. |
| `FULLSEND_GITLAB_POLLER_TOKEN` | masked secret | Poller PAT. Provisioned by `repos install`. |
| `FULLSEND_GITLAB_ANALYST_TOKEN` | masked secret | Analyst PAT. Provisioned by `repos install`. |
| `FULLSEND_GITLAB_CODER_TOKEN` | masked secret | Coder PAT. Provisioned by `repos install`. |
| `FULLSEND_GITLAB_ROLE_<NAME>_TOKEN` | masked secret | Custom role PAT when `credential` is `own`. Provisioned when the role is registered. |
| `FULLSEND_GITLAB_ROLE_REGISTRY` | unmasked variable | Administrator registry JSON. Absent or empty = built-ins only. |
| `FULLSEND_GITLAB_ROLE_ROTATION` | unmasked variable | Per-role rotation state (lock, token IDs, expiry dates, phase). Never stores token values. |
| `FULLSEND_GITLAB_TRIGGER_ACCOUNT` | unmasked variable | Version 1 ledger for the trigger-only service account (phase, nonce, branch, and the account, bootstrap PAT, deploy key, branch-grant entry, and trigger token IDs, plus the SHA-256 digest of the trigger token and a `publication_pending` marker for an unfinished token replacement). Never stores token values or private keys. Written by `repos install` and removed by `repos uninstall` only after every resource it records is gone, so it is the durable cleanup obligation until then. See [Trigger-only service account](#trigger-only-service-account). |
| `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN` | masked, protected variable | The pipeline trigger token owned by the trigger-only service account. Written only after GitLab reports the variable masked, protected, and not file-typed; deleted before its token is revoked. Scheduled polling uses the account's job token, not this variable; while the dispatch activation is recorded, the managed webhook embeds this token ([#8509](https://github.com/fullsend-ai/fullsend/issues/8509)). See [Trigger-only service account](#trigger-only-service-account). |
| `FULLSEND_GITLAB_DISPATCH_ACTIVATION` | protected, unmasked variable | Activation record for the trigger-account dispatch route: the ledger generation it was recorded for, no secrets. Written by `repos install` only after the dispatch activation gate passes; deleted first by every install, repair, rotation, containment pass and uninstall. A record that no longer matches the ledger is ignored. See the dispatch activation gate under [Trigger-only service account](#trigger-only-service-account). |
| `FULLSEND_GITLAB_POLLER_GENERATIONS` | unmasked variable | Version 1 Poller identity-generation state (current, pending, and retiring Poller account IDs and the pending generation's handoff phase). Never stores token values. Written by `repos install` when it creates or rotates the webhook trigger, and when it retires the superseded generation. Absent = empty state; present but blank fails closed. See [Poller identity generations](#poller-identity-generations). |

Canonical constants live in [`internal/forge/forge.go`](../../internal/forge/forge.go)
(`SecretForgeToken`, `SecretGitLabPollerToken`,
`SecretGitLabAnalystToken`, `SecretGitLabCoderToken`,
`VarGitLabRoleRegistry`,
`VarGitLabRoleRotation`, `VarGitLabPollerGenerations`,
`VarGitLabTriggerAccount`, `VarGitLabDispatchActivation`, `SecretGitLabTriggerAccountToken`). Custom secret names
are derived by `gitlabroles.CustomSecretName`.

Role readiness is checked through the registry/status paths rather than the
generic forge secret list; role provisioning repairs an existing
shared-token installation by provisioning the missing role credentials.
It does not touch the leftover shared secret.

### Project access token names

| Role | PAT name | Access | Scopes |
| --- | --- | --- | --- |
| Shared (legacy, pre-role-only installs only) | `fullsend-bot` | Developer (30) | `api` |
| Poller | `fullsend-poller` | Developer (30) | `api` |
| Analyst | `fullsend-analyst` | Developer (30) | `api` |
| Coder | `fullsend-coder` | Developer (30) | `api` |
| Custom `own` | `fullsend-role-<name>` | Developer (30) | `api` |
| Custom `reuse` | (none; uses the target role's PAT) | — | — |

`repos install` provisions the role tokens directly on a fresh GitLab
install; it never creates `fullsend-bot`. On an existing installation
that predates the role-only model and still has the shared `fullsend-bot`
token, ordinary install provisions the missing role tokens but does not
revoke `fullsend-bot` — an administrator must revoke that token manually.
Access level and scopes match the legacy shared bot; do not claim finer
GitLab permissions than the implementation uses.

### Project service accounts

> **Status: active.** The CLI activation change
> ([#8242](https://github.com/fullsend-ai/fullsend/issues/8242)) of
> [#7772](https://github.com/fullsend-ai/fullsend/issues/7772) (originating
> issue [#8083](https://github.com/fullsend-ai/fullsend/issues/8083)) wires the
> live `ServiceAccountTokenClient` (`newGitLabRoleTokenClient` in
> `internal/cli/repos_gitlab_service_accounts.go`) and
> `gitlablifecycle.NewTriggerOwner` into install, rotation, status and
> uninstall, and writes rotation state as version 2. The live `TriggerOwner`
> implements the fresh-identity Poller handoff
> ([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)), so the webhook
> fast path activates where the project is eligible; ineligible or ambiguous
> projects defer and keep polling. It does not implement
> `repos.GitLabPollerQuiescenceVerifier`, so the current Poller is never
> re-elevated.

A newly provisioned or rotated own-credential role is a
**project service account** named after the token name above (for example
`fullsend-poller`). Its credential is a personal access token of that
service account with the same name, `api` scope, and expiry. The
service account is a direct project member at Developer (30). Install
refuses to provision any role identity above Developer. A project
access token bot cannot change role, but a service account's membership
can. That lets a Poller service account own the webhook trigger token (see
below); the target is a fresh replacement identity, not re-elevation of the
current Poller.

- When the project has more than one service account with the same role
  name, only durably verified managed, non-supplied candidates participate;
  among those, the one with the lowest ID wins. Unverified or supplied
  same-named accounts never participate, whatever their ID.
- Inventory merges service-account tokens with legacy project access
  tokens. This applies to provisioning, rotation, `repos status`,
  Poller pipeline access, and uninstall token revocation.
- When the instance or plan does not offer project service accounts,
  install falls back to a project access token, as before. Install
  treats a 404, a 403, or a not-supported answer from the service-account
  API as "not offered". Rotation, `repos status`, and protected-ref
  reconciliation likewise use the inventory that is available when the
  other source answers 403. Uninstall does not: a 403 from either
  source leaves credentials the installer cannot see, so uninstall fails
  rather than reporting success.
- Unflagged install reconciles a positively identified managed legacy role
  onto a service account using the existing rotation/recovery state. It
  preserves administrator-supplied identities and does not require a separate
  migration command or runtime compatibility gate. An installed credential
  with no recorded provenance is fail-static: it is left untouched with a
  diagnostic, never migrated automatically. Operators re-adopt it by
  following the CLI reference's
  [re-adoption runbook](../cli/repos.md#re-adopting-a-role-credential-of-unknown-provenance). Replacement credentials
  are authenticated and checked for identity, `api` scope and effective
  Developer access before publication. Rotation of an existing role mints the
  new credential on a service account. The old project access token is
  revoked after the usual 24-hour grace; this does not apply when you switch
  the role to a supplied credential, which leaves earlier managed tokens usable
  until they expire or are revoked manually.

A failed credential-publication response does not prove the write failed.
The forge secret interface cannot read values back, so rotation keeps the
incoming credential active and retains recovery state, invalidating any prior
supplied distribution proof. For a managed replacement the next rotation
replaces it safely and preserves the usual grace period before revocation.

An interrupted or ambiguous publication of an administrator-supplied
replacement behaves differently, because the installed value may be the
supplied one and an automatic managed replacement would overwrite it.
`providedPublicationUnresolved` (which also covers the owner-unresolved
variants) holds automatic replacement: install convergence and unforced
rotation fail closed for that role. The administrator recovers by re-enrolling
with `--gitlab-role-token` or forcing rotation with `--rotate-gitlab-roles`.
When the same install requests that recovery, convergence defers to the
rotation pass instead of failing provisioning. "Requests" is judged against the
role set rotation processes: the `--rotate-gitlab-role` filter expanded through
credential reuse (`wantedRoles`), so a supplied credential for a role the
filter excludes does not release the hold. An explicit supplied credential for
a role in that set also wins over automatic service-account convergence, and
the rotation pass enrolls it over a managed credential even when that
credential is fresh.

`repos uninstall` revokes service-account tokens with the other role
tokens, then deletes accounts whose IDs are durably recorded as managed.
Names alone never authorize membership changes, credential management or
deletion. Operational inventories omit unverified same-named accounts so their
tokens cannot become outgoing rotation-cleanup candidates. Explicitly enrolled
supplied credentials retain read-only lifecycle metadata. Supplied accounts,
accounts without ownership proof, and accounts with remaining credentials,
unfinished jobs or owned schedules/triggers are preserved. An unsafe or
unverifiable deletion fails uninstall and retains the ownership state for
retry. Managed accounts renamed away from their recorded role name also fail
inventory and retain ownership; restore the role name before retrying cleanup.
Poller permission recovery still uses the recorded account ID after a rename,
so name drift cannot bypass restoration to Developer. Contributions are
preserved (the deletion API never uses `hard_delete`). GitLab processes
account deletion asynchronously: successful uninstall means the deletion
requests were accepted, not that the accounts have already disappeared from
the inventory.

Uninstall also keeps supplied-account exclusions in an exclusions-only
rotation document, so preserved administrator-owned accounts are never treated
as managed on a later install or uninstall. The rotation document is retained
whole when any cleanup step fails.

Token clients used for uninstall expose managed-account cleanup through
`repos.GitLabManagedAccountCleaner`, whose sole method is
`DeleteManagedServiceAccounts(ctx, client, owner, repo) (int, error)`.
Both `ServiceAccountTokenClient` and the CLI uninstall wrapper
implement this interface. Cleanup runs after token revocation and before
rotation-state retirement; its error retains the full ownership document.
The CLI wrapper configures `VerifyAccountDeletion` to check SSH credentials,
unfinished jobs, schedules and triggers before deletion.
`repos.VerifyGitLabAccountDeletion` owns that safety decision; the CLI supplies
the four resource-inventory callbacks in `GitLabAccountDeletionInventory`.
Missing callbacks or unreadable inventories refuse deletion, and independent
inventory errors are returned together.

`repos.VerifyGitLabPollerCredentials` and `repos.ContainGitLabPoller` own
elevation-safety decisions and failure containment. The GitLab lifecycle
package supplies read-only `GitLabPollerCredentialInventory` callbacks for
complete inventories. `GitLabPollerContainmentOperations` embeds that
inventory and adds revocation, installed-credential attribution and deletion
callbacks; `repos` controls ordering, detached cleanup budgets and the refusal
to remove unmanaged credentials. Missing callbacks fail closed. Trigger
bootstrap and Developer restoration remain orchestrated by the repository
webhook lifecycle.

`internal/gitlablifecycle` owns GitLab-specific account/credential attribution
and the trigger-owner implementation. The CLI constructs the lifecycle and
connects it to commands; it does not implement account safety or containment.
`gitlablifecycle.UninstallTokenClient` owns uninstall reconciliation and the
per-project supplied-attribution cache. The CLI constructs live inventory
adapters and passes the role client to `NewUninstallTokenClient`; it does not
implement domain reconciliation. Enrolled supplied PATs shared by multiple
roles produce one inventory snapshot per matching role reference, so reporting
and unforced rotation retain every enrolled role.

Provisioning and rotation accept administrator-supplied credentials as
`ProvidedTokens` (role name to token value). Enrollment authenticates the
supplied value through the client's `SuppliedCredentialIdentity` callback and
records the credential's owner, token ID and the owner's permanent exclusion
in rotation state, alongside the distribution proof, while the credential still
authenticates. Enrollment is an explicit supplied transition: it replaces any
rotation-state entry the role still holds (a managed one, or a supplied one left
when the credential variable was removed), records the new credential's own
owner and keeps every previous owner excluded, while creation records and
earlier exclusions are preserved. Replacing a supplied credential with another
supplied credential does the same. A managed replacement of a supplied
credential clears the supplied provenance and keeps the old owner excluded. The
project inventory lists only project access tokens and service-account tokens,
so an unforced rotation authenticates the installed value of a supplied
personal access token it cannot list (`InstalledRoleCredentialSelf`): one that
authenticates and has not expired is retained rather than replaced as
unverified, so replace it by enrolling a new credential or with a forced
rotation; one GitLab refuses with 401, whose secret is empty, or that is
revoked, inactive or expired is unhealthy and replaced normally; any other
failure (outage, throttling) leaves it unverifiable, so it is left untouched
and reported as a role failure. A client without that capability retains it
unverified. At enrollment, `SuppliedCredentialIdentity` also returns the
authenticated token's name, scopes, expiry and state, and enrollment rejects
(`validateSuppliedCredentialPolicy`) a credential that is not active, does not
carry the role's token name and exactly the minted scopes, or has no expiry, an
elapsed expiry or one beyond the permitted lifetime, before recording state or
writing the secret, so the installed credential is never replaced by an
unsuitable one. An
expired or revoked credential therefore keeps its owner excluded and can be
replaced normally, and `SuppliedTokenIDs` reports only the enrolled token under
the role's token name. A credential enrolled earlier without a recorded owner is
attributed by authenticating with the installed secret
(`TriggerOwner.SuppliedAccountIDs`, `CurrentSuppliedAccountIDs`), and rotation
persists that owner through `AttributeSuppliedOwners` before it can replace
anything; failure to attribute or persist it fails closed. An owner whose
enrolled token ID was not recorded keeps every one of its tokens excluded.
`TestNewGitLabRoleTokenClient_SuppliedCredentialAttribution` and
`TestProvisionGitLabRoleCredentials_EnrollmentRecordsSuppliedIdentity` cover
this. Neither credential values nor remote error text are reported.
Ownership lookup errors intentionally expose only the unresolved-ownership
sentinel: redacted remote causes are flattened so an inner 403/404 cannot
authorize legacy fallback or imply that managed accounts are absent.

Rotation-state writes use schema version 2
([#8242](https://github.com/fullsend-ai/fullsend/issues/8242)). Version 2
state adds managed service-account IDs and supplied-account
ownership/exclusions to the `FULLSEND_GITLAB_ROLE_ROTATION` document and still
never stores token values. Version 2 is the only version read: unversioned
and version 1 state, unsupported future versions, and documents with unknown
fields fail closed and are not rewritten
([#8246](https://github.com/fullsend-ai/fullsend/issues/8246)). Because older
readers reject unknown fields, every schema change, additive ones included,
must bump the state version. Version 1 is
never written: CLIs built with the tolerant reader
([#8233](https://github.com/fullsend-ai/fullsend/issues/8233)) accept it but
ignore the ownership fields during outgoing-token selection and grace cleanup.
They reject version 2 before touching any credential, and CLIs older still
reject the unknown `version` key, so older rotation and status operations,
which consult rotation state before mutating, fail closed. Older provisioning
publishes missing supplied or minted secrets before recording distribution
state, and older uninstall cleanup never reads rotation state: it deletes
identity variables and revokes active role-named project tokens, supplied ones
included, so those paths are not protected by the version gate.
`gitlab_roles_rotate_version_gate_test.go` runs rotation against unversioned
and version 1 documents with a supplied PAT. It proves rotation refuses them
before it selects, schedules or revokes that PAT, and never rewrites the
document. Operators must still upgrade every CLI that runs install, converge
or uninstall.

Enrollment fails the role, recording nothing and publishing nothing, when the
supplied value cannot be authenticated (a rejection or a transient failure),
when a client that can describe credentials returns an identity without token
metadata, when it violates the role's credential policy, or when the optional
`VerifySuppliedAccess` callback rejects its owner's effective project access
(the live client requires exactly Developer). A client that cannot authenticate
supplied credentials at all, as in library use, resolves no owner: the
credential is then recorded as supplied without an owner, and `rotateProvided`
keeps that state when a replacement write fails ambiguously, because the
installed value cannot be read back.
There is no automatic re-enrollment from it: attribution authenticates with the
installed credential, and project-wide exclusions are resolved before
`ProvidedTokens` is processed, so `--gitlab-role-token` alone cannot recover a
credential that no longer authenticates. The failure diagnostics
(`ownerlessSuppliedRecoveryHint`) and the runbook in
[`docs/cli/repos.md`](../cli/repos.md#recovering-a-supplied-credential-recorded-without-an-owner)
direct the administrator to set a working credential in the role variable by
hand and rerun install. On that rerun, owner attribution only records the
owner: for an ambiguous replacement it does not release the
`providedPublicationUnresolved` hold, and resuming the normal lifecycle still
requires re-enrollment with `--gitlab-role-token` or a forced rotation.
If ownership recording fails immediately after account creation,
install requests deletion of that newly created ID on a detached cleanup
context before any membership or PAT is issued. It never deletes an
unverified same-named account; failed deletion is reported for administrator
cleanup.

Legacy project-token creation ownership is tracked by `created_token_ids` in
version 2 role state. Only IDs returned by token creation are recorded there;
`incoming_id`, `outgoing_ids`, token names, and distribution backfill alone do
not authorize legacy convergence or revocation. Unverified same-named legacy
tokens are preserved and require manual recovery or explicit supplied
enrollment. All token-client constructors used for installation and uninstall
load these records through `ManagedLegacyTokenIDs`; live fallback creation
records them through `LegacyTokenCreated` before publication. Failure to
record creation revokes only the newly issued token using the creation call's
authority.

`GitLabOutgoingTokenVerifier.ConfirmOutgoingTokenInactive` can retire an
outgoing rotation obligation when an authoritative inventory positively
locates an inactive token, or complete service-account and configured legacy
inventories confirm absence. An active service-account token can be revoked
even when legacy inventory is unavailable. The inactivity check is only an
optimization: when it cannot establish inactivity (no service-account client,
or an unavailable or forbidden inventory), grace cleanup falls back to the
authorization-enforcing revocation and retains the obligation only if that
revocation fails; a revocation 404 alone is insufficient. Generic revocation
still reports unknown/unowned tokens as errors.

Service-account PAT rotation preserves the account's user ID; replacing a
legacy project-token bot changes it. Status reads the recorded managed-account
ownership but never displays token values. Uninstall uses creation provenance
to authorize account deletion.

`repos status` includes account IDs, names, effective access and managed
ownership in text diagnostics and `gitlab_service_accounts` JSON. Managed
accounts not exactly Developer are drift and clear role readiness. Reinstall
repairs direct membership for every positively owned, non-supplied role account
and verifies effective Developer access before retaining its credential or
provisioning a replacement when the role secret is missing. The token creation
boundary also contains positively owned roles on failed membership repair or
effective-access verification during direct rotation. Replacement Poller PATs
must receive verified protected-default-branch pipeline access before
publication. Inherited higher access or failed repair/verification fails
provisioning and contains that positively owned, non-supplied role. The
complete supplied-owner exclusion set must resolve before membership changes
or containment; unresolved ownership preserves credentials and fails
provisioning. Successful attribution is reused throughout that operation.
During containment, managed PATs are revoked and relisted on bounded detached
contexts, and the installed secret is removed only after live account
attribution. Ownership records are retained. Unknown tokens and containment
failures are reported for administrator recovery. Renamed currently supplied
accounts remain in operational inventory by recorded owner ID, and enrolled
token IDs retain their recorded role names; destructive inventory and
membership reconciliation continue to exclude them. An unreadable inventory
is reported, not interpreted as no accounts.

Project service accounts and their Free-tier availability are generally
available starting with GitLab 18.11. GitLab.com Free permits 100 per
top-level group; Self-Managed Free permits 100 per instance. Installation
uses capability checks rather than a version guess, reuses existing accounts,
and retains supported legacy/supplied credentials on restricted instances.
Quota exhaustion or insufficient permissions requires operator action; it
must not delete unrelated accounts to make space. See the
[GitLab API contract](https://docs.gitlab.com/api/service_accounts/).

### Poller identity generations

Re-elevating a Poller identity whose runtime credential was ever distributed
is unsafe, because GitLab has no drain barrier for requests accepted before
revocation (#8205). The replacement-identity design validated in #8209 instead
raises a fresh Poller service account that has never held a distributed
credential. `internal/repos/gitlab_poller_generation.go` implements its
generation state and handoff sequence (#8210). The live
`gitlablifecycle.TriggerOwner` implements the `repos.GitLabPollerHandoff`
capability (#8243), and `repos install` runs the handoff under the project
lease whenever it needs to create or rotate the webhook trigger. Before the
first generation, install records the running managed Poller as
`current_user_id` so a later cutover records it as retiring.

An absent `FULLSEND_GITLAB_POLLER_GENERATIONS` is an empty state: the first
handoff, or the documented recovery after an administrator deleted the
variable. A variable that exists but is blank or whitespace fails closed,
because a blank value may mean recorded current, retiring, or quarantine state
was lost. Reconcile the `fullsend-poller` accounts and pipeline triggers, then
delete the variable to start again.

The handoff refuses to run unless the caller passes the lease capability
issued by `LockGitLabProjectLease` after a successful non-dry-run remote
acquisition (a dry-run lock never yields one, and it is invalid once released),
and it rejects a generation document that is not exactly one JSON object
(trailing content, duplicate or non-lowercase keys, or inconsistent phase and trigger fields).
It records each step in `FULLSEND_GITLAB_POLLER_GENERATIONS` before acting:

1. Record the account request, create the fresh service account at Developer,
   and record its numeric ID. Creation never touches branch protection. Before
   every elevation attempt, including a resumed `account_recorded` generation
   after an earlier membership failure, the handoff idempotently re-establishes
   Developer membership and checks, read-only, that the existing
   protected-default-branch rule lets the account start pipelines; a policy
   that excludes it defers the handoff before any elevation, and fullsend never
   writes a merge grant for a replacement account. A lost create response leaves no ID, so the
   generation needs manual reconciliation; fullsend never deletes an account
   it cannot positively identify. A refused (403) or unsupported (404) create
   made no account, so polling stays the only path.
2. Verify the account holds no active personal access token, no unmanaged
   pipeline trigger, no usable SSH key, no unfinished job and no pipeline
   schedule, and that the project-wide trigger-safety invariants hold. Then
   record the elevation, create the installer-held bootstrap token, and raise
   the account to Maintainer.
3. Record the generation's single trigger-create attempt, then send it. A
   create request cannot be fenced once sent, so it is never retried.
4. Revoke the bootstrap token, verify no personal access token is active,
   demote to Developer, and verify Developer through an independent read.
5. Require a confirmed trigger owned by the new account, re-check the
   project-wide trigger-safety invariants, and prove the trigger starts a
   pipeline on the protected default branch at Developer access. The live
   probe confirms the trigger owner is at exactly Developer and that the
   existing protected-branch rule lets it create pipelines. It reads the
   policy without starting a pipeline. Branch protection is never broadened.
   Only after both checks pass is the
   generation recorded as verified; a failed safety check or probe attempts to
   revoke the trigger and quarantines the generation, so it never reaches the
   verified phase.

A failure after step 3 publishes nothing and quarantines the generation with an
operator-facing reason. A quarantined generation, a lost account ID, a verified
generation whose cutover did not complete, or an unresolved retiring Poller
each block any new generation. The current Poller is never elevated or
modified, and its polling continues throughout. At most one old/new pair
exists: cutover makes the verified account current and the old one retiring,
and no further generation starts until retirement completes. Because ADR 0125
sets no numeric Free-tier limit, this bound is the Free-tier quota for Poller
generation accounts. It allows the current Poller plus either one pending
replacement or one retiring Poller, and at most
`maxAbandonedPollerGenerations` (three) abandoned replacements awaiting
deletion.

Cutover (`internal/repos/gitlab_poller_cutover.go`) runs only after a verified
generation is returned and its trigger has been published: the caller stores
`FULLSEND_TRIGGER_TOKEN` and creates or updates the webhook first, and runs the
cutover only after both succeed. A failed trigger-secret store or webhook
publication therefore records no cutover and no retiring Poller; the current
Poller keeps its credential. Before the secret store, the caller records
`trigger_distributed` on the pending generation (`markPollerTriggerDistributed`);
when that write fails, the secret is not written and the trigger is revoked.
Once recorded, the trigger credential may have been distributed (an ambiguous
secret write counts), so the account is never re-elevated. The next install
revokes the generation's triggers and re-lists to confirm they are gone (a
DELETE that reports success proves nothing; a surviving trigger fails closed),
demotes and verifies the account, records it in `abandoned_user_ids`, clears
`pending`, and starts a genuinely fresh generation. A generation that was
never marked (the failure came before the store) may instead be reset to
`account_recorded` after the same revoke-and-confirm check. A trigger that the
webhook already uses with a missing cutover is finished by the next install
(`finalizePendingPollerCutover`): it records the cutover when the replacement's
credential is already the managed Poller, or retries the cutover when it was
contained before the credential was stored. `GitLabWebhookNeedsProvisioning`
reports verified, elevated and trigger-requested pending generations as work.
Abandoned, retiring and pending accounts are owned for uninstall. The
cutover runs in this order:

1. Create the new account's runtime personal access token. A value GitLab
   cannot mask is never stored.
2. Record the new account as the Poller's `managed_user_id`, with the token as
   its incoming credential and the role marked `distributing`, in
   `FULLSEND_GITLAB_ROLE_ROTATION`. The distribution time is not recorded yet.
3. Store the token as `FULLSEND_GITLAB_POLLER_TOKEN`. Only after this write
   succeeds, record the distribution (phase and `distributed_at`) in
   rotation state.
4. Record the cutover in `FULLSEND_GITLAB_POLLER_GENERATIONS`.

A failure before the store (steps 1 and 2) revokes the new account's
credentials and restores nothing in rotation state because none of it was
recorded as distributed. The previous Poller stays in service, the trigger
stays wired, and the verified generation blocks further generations until an
administrator reconciles it. An error storing
`FULLSEND_GITLAB_POLLER_TOKEN` (step 3) is an unknown outcome, because GitLab
may have committed the variable and lost the response: the replacement
credential and its rotation tracking are kept and not revoked, the role stays
`distributing`, and the next run reconciles it. A failed write of the
distribution record after a successful store also leaves the role
`distributing`; the cutover still completes. If step 4 fails after the
credential is live, the next install sees the verified generation as the
managed Poller and records the cutover then. Cutover preserves every token
fullsend created for the Poller (`created_token_ids`), so an outgoing legacy
token keeps its provenance until grace cleanup revokes it. Superseded managed
triggers are revoked once the webhook uses the new one. The retiring
Poller stays at Developer. Until it is retired, a forced trigger rotation
defers and the existing compliant fast path is kept.

Retirement (`internal/repos/gitlab_poller_retirement.go`,
[#8244](https://github.com/fullsend-ai/fullsend/issues/8244)) runs on an
ordinary lease-holding install. On the installer-owned Poller route it runs
before the fast path decides whether it is provisioned, so a rotation in the
same run can start the next generation; on the trigger-account route it runs
after the account-owned webhook converged, so that webhook is the evidence it
protects. A retirement failure never stops the webhook repair: install keeps
the error, completes the repair, and returns it joined with any later error.
`GitLabPollerRetirement` implementations are not safe for concurrent use and
take no lock of their own: callers hold the project lease and call one method
at a time (`retireSupersededPollerGeneration` does nothing without it).
`GitLabWebhookNeedsProvisioning` reports a recorded `retiring_user_id` as
work. Retirement waits, and changes nothing, until all of these hold:

- the trigger owner implements `GitLabPollerRetirement`;
- the managed Poller is the recorded current generation;
- rotation state records that Poller as `managed_user_id` with a durably
  recorded `distributed_at` at least `gitlabroles.DefaultRotationGrace` (24
  hours) old, the same grace rotation gives in-flight jobs;
- `PollerRetirementBlocked` reports no unfinished job running as the retiring
  account and no administrator-supplied credential owned by it.

It then:

1. Re-reads the managed webhooks, the trigger inventory and the
   trigger-account ledger immediately before revoking
   (`currentWebhookTriggerEvidence`), so the trigger the webhook uses now is
   protected on either route: the account's recorded trigger when a webhook
   embeds its published token, and the legacy `FULLSEND_TRIGGER_TOKEN` trigger
   otherwise. It waits when a managed webhook embeds neither. It then revokes
   the pipeline triggers owned by the retiring account and confirms by listing
   again that none remains. It waits, changing nothing, if a protected trigger
   is owned by the retiring account. A trigger whose owner GitLab omits is
   revoked only when it carries the managed webhook trigger description and no
   webhook uses it; any other ownerless trigger cannot be attributed, so
   retirement waits and names it for the operator to revoke, because account
   deletion verification counts it.
2. Calls `RetirePollerAccount`, which contains the account's managed
   credentials, verifies that no active personal access token, usable SSH key,
   unfinished job, schedule, or trigger remains, requests deletion of the
   service account, and re-lists the service accounts to confirm it is gone.
   It fails while the account is still listed. An account that is already gone
   counts as retired.
3. Clears `retiring_user_id` (`finishPollerRetirement`) against a fresh read of
   the generation document.
4. Counts, with `preservedPollerMergeGrants`, the merge-access entries that
   grant the retired account individually on the default branch's exact-name
   protection rule, and adds an operator note. It never removes them:
   `EnsureGitLabPollerPipelineAccess` reuses an existing grant and fullsend
   records no provenance for per-user merge grants, so an entry cannot be
   proven to be fullsend's rather than an administrator's. A failed
   protected-branch read changes only the note, not the outcome.

Every step can be safely repeated. Any failure keeps `retiring_user_id`, so the
next install retries.

### Poller-owned webhook trigger token

> **Poller elevation safety:** Re-elevating a Poller identity that ever held a
> distributed runtime credential requires a verified server-side guarantee
> that requests accepted before credential revocation, including asynchronous
> credential and job creation, have finished. The current GitLab adapter
> cannot establish that guarantee, so it never re-elevates the current Poller.
> It creates new triggers through a fresh Poller generation instead. Revocation
> and empty resource inventories alone do not prove that requests have drained.

This lifecycle is part of the contract described under
[Project service accounts](#project-service-accounts). The live GitLab adapter
obtains a Poller-owned trigger through the fresh replacement identity described
in [Poller identity generations](#poller-identity-generations)
([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)), which never
elevates a Poller that held a distributed credential. The steps below describe
the existing-identity elevation lifecycle that is retained for trigger owners
that do not implement `repos.GitLabPollerHandoff`, behind the
quiescence-verifier gate. Such an owner without that gate defers at step 2 and
never runs steps 3 onward.

GitLab binds a pipeline trigger token to its creator, and creating one
needs Maintainer. A Poller that is raised to Maintainer raises every
credential that authenticates as it, including the distributed runtime
credential that running protected-branch jobs hold. So `repos install`
never raises the Poller while a distributed runtime credential is valid.
Only an adapter that can verify server-side request draining may create the
token **authenticated as the existing Poller**. It does so through the
following lifecycle, all under installer authority and inside one project
lease:

1. Identify the managed `fullsend-poller` service account from the
   administrator's service-account inventory and its durable `managed_user_id`
   creation record in role rotation state. A matching display name or token ID
   alone does not authorize membership changes or PAT revocation; accounts
   without that creation record are preserved, including during uninstall.
   Supplied-account exclusions override the creation record. The distributed
   runtime credential is not needed for this. When it is installed and still
   authenticates, it must be the managed `fullsend-poller` token of that
   account; an administrator-supplied credential (for example
   `--gitlab-role-token`) leaves the Poller untouched. Verify the Poller's
   effective project access is exactly Developer. A leftover elevation is
   corrected first. Less than Developer, or no membership, defers the fast
   path.
2. Account for every credential on the account, and refuse elevation
   (deferring the fast path without changing membership or revoking
   anything) when one cannot be accounted for. The only active personal
   access tokens allowed are the managed `fullsend-poller` runtime token
   and the installer's `fullsend-poller-bootstrap` token, and the Poller
   must own no pipeline trigger token that fullsend does not manage:
   a trigger acts with its owner's permissions and cannot be invalidated
   safely afterwards. Revoke such a token or trigger first. An inventory
   that cannot be read also refuses elevation. Credentials fullsend does
   not manage are never revoked. Require the optional
   `repos.GitLabPollerQuiescenceVerifier` capability before revoking runtime
   credentials; a trigger owner that implements neither
   `repos.GitLabPollerHandoff` nor `GitLabPollerQuiescenceVerifier` defers
   here. The live adapter implements `GitLabPollerHandoff` and never reaches
   this step.
3. Invalidate the distributed credentials: remove
   `FULLSEND_GITLAB_POLLER_TOKEN`, revoke the account's managed runtime
   personal access tokens, and verify none is still active (removing the
   variable alone does not revoke copies held by running jobs). Then
   revoke the managed trigger tokens the Poller already owns, so neither
   they nor the webhook URL that embeds one can start a pipeline with the
   elevated role. Repeat the account safety inventory after revocation,
   before creating the bootstrap token: no active personal access token is
   allowed at this point, even if its name is `fullsend-poller` or
   `fullsend-poller-bootstrap`. A managed-name token that appeared during
   revocation still refuses elevation. Verify server-side request draining
   after this inventory, including asynchronous credential and job creation.
   If that verification fails, defer elevation and republish the runtime
   credential at Developer access.
4. Create the installer-only bootstrap personal access token
   (`fullsend-poller-bootstrap`, `api` scope, two-day expiry) with the
   admin credential. Its value lives only in installer memory: it is never
   written to CI/CD variables, logs, agent environments, or any persistent
   store, and error text is redacted against it. Any bootstrap token left
   by an interrupted run is revoked first.
5. Temporarily grant the Poller Maintainer with the admin credential and
   create the trigger authenticated with the bootstrap credential.
6. Restore Developer on every path, success or failure, retrying once.
   Then verify effective access through `members/all`.
7. Revoke the bootstrap credential and verify no active bootstrap token
   remains.
8. Publish a replacement runtime credential (`fullsend-poller`) as
   `FULLSEND_GITLAB_POLLER_TOKEN`, and record its rotation-state proof.
   This happens only after steps 6 and 7 succeeded: a runtime credential
   is never published while the Poller exceeds Developer or its effective
   access cannot be verified.
9. Check that the token's owner is the Poller. Then enforce the usual
   runtime ceiling: the owner is below Maintainer and admitted by the
   protected default branch.

Only after the restore is verified, the bootstrap credential is revoked,
and the replacement runtime credential is published is the trigger token
stored and the webhook created or updated.

**Operational impact.** Between step 3 and step 8 no runtime Poller
credential is valid. Polling schedule jobs and in-flight jobs that
authenticate as the Poller fail during trigger creation or rotation, and
the next scheduled poll picks the work up again after the replacement is
published. Run install or `--rotate-gitlab-trigger-token` when a short
polling gap is acceptable.

**One installer at a time per project, across processes and hosts.** The
whole transaction above, plus role provisioning, rotation, cleanup, Poller
reconciliation, and the full GitLab repository uninstall (webhook teardown,
scaffold removal, role cleanup, and the variable, secret, and branch
deletions), holds a project lease: the CI/CD variable
`FULLSEND_GITLAB_INSTALL_LEASE`, created atomically (GitLab rejects a second
variable with the same key and scope) and deleted when the operation ends,
even after a failure or cancellation. A second installer waits up to two
minutes, then fails without inventorying, revoking, elevating, or publishing
anything. A client that cannot take the lease is refused, and a dry run
neither takes nor needs it. If an installer is killed while holding the
lease, confirm the Poller member's project role is Developer and delete that
variable by hand; fullsend never takes over a lease on its own, because
deleting another installer's live lease would reopen the elevation window.
Older CLI versions do not honor this lease. Do not run them concurrently with
an upgraded installer; follow the
[upgrade compatibility guidance](../cli/repos.md#upgrade-compatibility)
before manually removing a stranded lease.

- **Expired, revoked, or missing Poller token:** install identifies the
  managed `fullsend-poller` service account from the administrator's
  service-account inventory and durable creation record, never from the
  installed token, so a leftover elevation is restored and the credential
  can be provisioned, rotated, or replaced. This is also how an interrupted
  run is recovered after the previous runtime token was revoked: the
  installed variable is removed before any token is revoked, so the next
  install finds it absent and provisions a replacement.
- **Interrupted run (kill, power loss):** every install first restores a
  raised Poller to Developer and then revokes any leftover
  `fullsend-poller-bootstrap` token by name, so the orphan is accounted
  for without its value ever being stored. A bootstrap token that cannot be
  revoked fails closed and contains the Poller like a failed restore. The
  project lease is held for the whole transaction, including these steps.
- **Published trigger cannot be verified:** after the webhook is
  published, install re-reads the hooks and triggers. A failed listing, an
  omitted owner, or a failed role lookup is treated as an unverified owner,
  and the managed fast path is torn down on a detached, bounded context
  with teardown failures reported.
- **Restore, verify, or bootstrap revocation fails:** install revokes the
  new token, publishes no runtime credential, disables the managed fast
  path (all managed triggers and the webhook), revokes the Poller's
  managed personal access tokens (runtime and bootstrap), removes the
  installed Poller variable, and fails with an error telling the operator
  to set the Poller member back to Developer (or to revoke the bootstrap
  token). Every compensating request, including bootstrap revocation, runs
  on its own bounded context that survives cancellation. After successful
  containment, polling and webhook dispatch are both unavailable until the
  member is Developer again and install provisions a replacement
  credential. If another active token or an unmanaged pipeline trigger
  owned by the Poller remains on the account, containment is reported as
  incomplete: revoke it or have an administrator block the account. A
  failed install attempts the same restoration and containment before it
  finishes when the failure is handled in-process. Abrupt termination (a kill
  or power loss between raising and restoring the role) or a failed
  restoration request can still leave the Poller elevated. In that case the
  administrator must verify the Poller is Developer, restore it if not, and
  clean up the bootstrap token before clearing the stranded lease and
  retrying install.
- **Poller membership cannot be raised (403/404):** this is the
  project-access-token Poller case. The fast path is deferred nonfatally,
  and an existing compliant fast path is preserved.
- **No Poller credential installed:** the trigger is minted as the admin
  identity as before, which the runtime ceiling rejects at Maintainer or
  above.

`ci_pipeline_variables_minimum_override_role` stays `no_one_allowed`.
The webhook fast path uses no custom webhook receiver. While dispatch
activation is recorded, the managed webhook uses the trigger-only service
account's published pipeline trigger and runs on the child-pipeline route.
Without activation, it uses the Poller-owned trigger on the `api` route.

### Trigger-only service account

> **Status:** `repos install`, `--rotate-gitlab-trigger-account`, `repos
> status`, and `repos uninstall` run the lifecycle implemented in
> [`internal/repos/gitlab_trigger_account.go`](../../internal/repos/gitlab_trigger_account.go).
> [#8438](https://github.com/fullsend-ai/fullsend/issues/8438) delivered
> dispatch activation, and
> [#8508](https://github.com/fullsend-ai/fullsend/issues/8508) implements
> polling schedule ownership: once the activation is recorded and the account
> has protected-branch merge permission, managed polling schedules move to
> the trigger account. Otherwise they remain installer-owned on the Poller
> route. Tracked by
> [#8437](https://github.com/fullsend-ai/fullsend/issues/8437). Install,
> rotation, status, and uninstall have real-instance evidence on GitLab.com
> Free, self-managed EE, and self-managed CE under
> [#8245](https://github.com/fullsend-ai/fullsend/issues/8245); see
> [GitLab webhook validation status](../normative/normalized-event/v1/README.md#gitlab-webhook-validation-status).
> Those runs do not show whether a project Maintainer, rather than the
> installer identity used there, can delete a deploy key or trigger token
> owned by the service account.

**Command wiring.** Install, rotation, and uninstall each take the GitLab
project lease (`LockGitLabProjectLease`) and pass the capability to the
lifecycle. Every public entry point (`ConvergeGitLabTriggerAccount`,
`RotateGitLabTriggerAccount`, `RemoveGitLabTriggerAccount`) refuses to run
unless `lease.holds(owner, repo)`, so overlapping runs cannot write the
shared ledger from stale snapshots or elevate the same account at once.
Install runs after the Poller and its generations are settled. It passes a
verified Poller identity inventory: every managed Poller account, including
the current, retiring, and abandoned generations recorded in
`FULLSEND_GITLAB_POLLER_GENERATIONS`, plus every owner of an
administrator-supplied role credential. A project with no Poller account is a
verified empty inventory; any other failure to read it stops the run.
Uninstall removes the trigger account under the lease it already holds,
after the webhook fast path is removed and before role-identity cleanup
retires the Poller generation state. It is skipped when the Poller could not be
reconciled, so the ledger remains the cleanup obligation. A removal failure
stops uninstall and keeps the manifest entry.

**Trigger token sink.** The token value goes to the masked, protected CI/CD
variable `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN`, which scheduled polling does
not read. While the dispatch activation is recorded, the managed webhook embeds
this value in its trigger URL (#8509). The
value is accepted only after GitLab reports the variable masked,
protected, and not file-typed; otherwise it is deleted and publication fails,
so the ledger keeps the obligation and a re-run revokes the token and
publishes a replacement. Deletion and revocation are attempted independently
(re-elevation, rotation, uninstall, containment): the deletion is tried first,
and a failed deletion does not prevent the revocation. Errors are joined and
unresolved obligations stay in the ledger. The value is never logged or
written to the ledger. On the trigger-account route the webhook step revokes
the Poller-owned webhook trigger and deletes `FULLSEND_TRIGGER_TOKEN` only after
the webhook carries the account's token; without a recorded activation
`FULLSEND_TRIGGER_TOKEN` keeps its Poller-owned trigger. The webhook step never
mints, rotates, or revokes the account's trigger. The webhook owner ceiling
(`gitlabTriggerOwnerProblem`) accepts the account's recorded deploy-key push
grant on the default branch in place of a user-level push or merge grant, and
still rejects an account above Developer.

Install and status verify the sink on every run: the variable exists, is
masked, is protected, is not file-typed, and is not environment-scoped. Status
reports drift; install repairs it by revoking the trigger and publishing a
replacement from a fresh account generation (see below). Generic role-variable
cleanup excludes both the ledger and the token sink, so neither is deleted
except by the trigger-account lifecycle.

**Dispatch activation gate.** `CheckGitLabTriggerDispatchGate`
([`internal/repos/gitlab_trigger_dispatch_gate.go`](../../internal/repos/gitlab_trigger_dispatch_gate.go))
must pass before any webhook or schedule dispatch path uses the trigger
account ([#8438](https://github.com/fullsend-ai/fullsend/issues/8438)).
Scheduled polling runs as the account and dispatches with its `CI_JOB_TOKEN`;
the managed webhook uses the account's trigger token
([#8509](https://github.com/fullsend-ai/fullsend/issues/8509)). It only
reads and reports every unmet prerequisite: managed scheduled polling no longer
uses pipeline variables (the canonical schedule specs and every live managed
schedule; a failed schedule read defers), the live
`ci_pipeline_variables_minimum_override_role` is exactly `no_one_allowed`
(weaker, unreadable, and unsupported all defer; a published token or
deploy-key grant is not taken as proof), the live
`ci_push_repository_for_job_token_allowed` is `false` (the deploy key can push
to the protected default branch, so a job token that could push the repository
would let a dispatched job rewrite the code later protected pipelines run;
enabled, unreadable, and unreported all defer, read through
`forge.Client.GetJobTokenRepositoryPushAllowed`), and
`DiagnoseGitLabTriggerAccount` reports `ready` for the default branch. The gate
also defers until the committed `pin-ci-job-identity.sh` and `run-poll-job.sh`
match the shipped compatible scripts. This exact-content check avoids treating
a selector name in a comment, heredoc, or uncalled function as proof that the
child-pipeline route runs. The managed poll, agent, and dispatcher jobs must
keep their entire shipped job configuration, including image entrypoint,
cache, artifact restoration, automatic admission, and scripts. The
unrelated-work scans exempt these jobs by name, so an unchecked setting could
run code before authorization or stop scheduled polling. The committed root
and wrapper must admit both managed schedules and include the `poll` stage.
The consumer of a
`source=pipeline` dispatch must also admit it: the root workflow rules, the root
and wrapper includes with their input forwarding, the agent job rules and
input bridges, and the
committed `run-agent-job.sh` (which must match the shipped script that admits
`api pipeline`). The helpers sourced before authorization, including the CLI
installer, CA trust script and role-token selector, and the dispatcher script
must match the shipped scaffold. The root, wrapper, and agent input schemas
must retain the shipped constraints before GitLab expands inputs into job names.
An older or customized script, job, or
consumer file needs a
scaffold repair merged before schedule ownership moves to the account. This
includes a renamed generated agent job: restore its shipped name so the gate
can verify the guarded job. Upgrade the installer and the project's
`fullsend_ref` to a release or source commit containing #8508 and merge that
scaffold repair before transferring ownership. The route probe
(`fullsend poll --print-dispatch-routes` listing `child-pipeline`) only gates
polling and passes for any CLI from #8482 onward; a pinned CLI older than
#8482 stops scheduled polling after a transfer. The minimum CLI for installer
operations (`repos install`, `repos uninstall`) after a transfer is the first
release or commit containing #8508; earlier CLIs treat account-owned schedules
as foreign. A current CLI can hand schedules back to the installer during an
upgrade. The gate also defers when the project selects a
custom CI configuration path (the committed-file scans would not describe the
pipeline GitLab runs; an unreadable project defers) and when a project, group,
or instance CI/CD variable overrides a dispatch value used by rule evaluation,
schedule selection, the pre-authorization identity bootstrap, or the agent
input bridge (`CI_PIPELINE_SOURCE`, `CI_API_V4_URL`, `CI_PROJECT_DIR`, `CI_JOB_TOKEN`,
`CI_PIPELINE_SCHEDULE_DESCRIPTION`, and `FULLSEND_POLL_MODE`, for example). Group and instance scopes the
installing credential cannot read are skipped, as on the webhook path; other
inventory read failures defer. GitLab requires a protected-branch schedule
owner to have merge permission. The gate verifies a Developer-role merge
grant or a grant to the exact trigger account; a deploy-key push grant does
not satisfy this requirement. Without either merge grant, schedules stay
installer-owned and use the Poller route. On GitLab Free, individual Developer
merge grants are unavailable, so a Maintainers-only merge rule uses this
fallback without widening access. The gate also
reads the committed CI files and defers when a job or include other than
the managed ones would run in a trigger, an agent child (`source=pipeline`) or
a scheduled (`source=schedule`) pipeline, or when a managed template's job-level
or inherited global execution configuration (`after_script`, `services`,
`hooks`) cannot be ruled out. The `source=schedule` check matters because the
managed polling schedules move to the trigger-only account, so a user job
restricted to schedules would otherwise run as that account with its
`CI_JOB_TOKEN`. `repos install` rechecks it whenever
the trigger account converges to ready. That includes a repository whose
managed schedules still set a leftover `FULLSEND_POLL_MODE`, where the gate
defers, and the path that prepares the trigger account after the Poller protected-ref
grant is refused. A deferral is informational and leaves polling in place. No
dispatch path uses the token yet.

**Child-pipeline agent dispatch.** A pipeline the trigger-only account creates
on the protected default branch (a webhook `trigger` pipeline started with its
trigger token, or a `schedule` pipeline it owns) can start the agent pipeline
with the running job's `CI_JOB_TOKEN` through
`POST /projects/:id/trigger/pipeline`. GitLab records that pipeline with
source `pipeline` and attributes it to the same account, so it runs on the
protected branch through the deploy-key grant and needs no Poller
protected-ref access ([#8438](https://github.com/fullsend-ai/fullsend/issues/8438)).
The route is implemented and selected per job:

- `run-poll-job.sh` and `run-dispatcher-job.sh` call
  `fullsend_select_gitlab_dispatch_route` (in `pin-ci-job-identity.sh`) after
  the identity pin and Poller credential selection. It exports
  `FULLSEND_GITLAB_DISPATCH_ROUTE=child-pipeline` only when the ledger is
  `ready` with a published trigger token, the protected
  `FULLSEND_GITLAB_DISPATCH_ACTIVATION` record names exactly that ledger
  generation (nonce, account, deploy key, grant and trigger token IDs), and
  both the pinned pipeline's creator and the user the running job (and so
  `CI_JOB_TOKEN`) authenticates as, read from the authenticated `GET /job`
  response, equal its `account_id`.
  The job user check matters because a retried job keeps the pipeline creator
  but runs as the retrier, whose child pipeline the agent job would reject. Any
  other creator or job user, an unreadable one, or a missing, malformed or
  mismatched activation record exports `api`, so polling continues through the
  Poller credential while activation is deferred. The variable is job-local; it
  is never a CI/CD variable. When the route would be `child-pipeline`, the
  script first requires the installed CLI to list it in
  `fullsend poll --print-dispatch-routes`; a CLI that predates the selector
  rejects that flag, and the job stops explicitly before any dispatch.
- The activation is recorded only by install, after
  `CheckGitLabTriggerDispatchGate` returns Ready (schedules migrated off
  pipeline variables, override role `no_one_allowed`, job-token repository
  push disabled, no unrelated committed job reachable from a trigger or agent
  child `source=pipeline` pipeline, committed poll and identity scripts that
  select the route, trigger account ready). It is stored in
  `FULLSEND_GITLAB_DISPATCH_ACTIVATION`, not in the ledger. Every converge,
  rotate, containment or uninstall run deletes it before doing anything else,
  including runs that defer, so a ready ledger with a published trigger token
  never selects or admits the route on its own. The gate is a point-in-time
  check: a later weakening is detected, and the activation withdrawn, on the
  next install run.
- `fullsend poll` (the cron and `gitlab-webhook` drivers, and
  `--input-driver jira-poll --target-forge gitlab`) then starts
  agent pipelines with `CI_JOB_TOKEN` and the same signed typed inputs as the
  Poller route. The request is sent once, without the Poller PAT, and the job
  token travels only in the request body. The route requires the typed
  pipeline-input contract; a legacy variable-based repository is refused
  rather than dispatched through pipeline variables.
- The root `workflow:rules` admits `source=pipeline` only on the protected
  default branch with the stage input set, and `fullsend-pipeline.yml`
  includes the agent template for `api` and `pipeline` only. `run-agent-job.sh`
  admits the `api pipeline` set at the identity pin, requires the recorded
  trigger account (ready ledger with a matching activation record) as creator for
  `pipeline` (never the Poller), and verifies the dispatch HMAC before any role
  credential is selected.
- **Upgrade ordering:** the root `workflow:rules` admission for
  `source=pipeline` reaches an existing install only when install converges the
  root `.gitlab-ci.yml`, so converge the root before the dispatch activation is
  recorded or a dispatch entry point moves to the trigger account; a pipeline
  rejected by an unconverged root fails closed. Likewise, upgrade the CLI
  before activation: the job scripts probe the installed CLI with
  `fullsend poll --print-dispatch-routes` before selecting `child-pipeline`,
  and a CLI that predates `FULLSEND_GITLAB_DISPATCH_ROUTE` rejects that flag,
  so a newer scaffold with an older CLI stops explicitly before any dispatch
  instead of dispatching through the Poller credential and failing late.
- **Older CLIs and rollback:** the dispatch activation remains in the separate
  `FULLSEND_GITLAB_DISPATCH_ACTIVATION` variable. It is bound to the current
  ledger generation. Before the trigger-token digest was added, an older CLI
  could change that generation; the activation then became stale and the
  scaffold selected the Poller route. This version records a digest of the
  trigger bearer in the ledger (`trigger_token_digest`) to bind the published
  variable to the recorded trigger ID, and a `publication_pending` marker
  while a replacement bearer is being published. The ledger stays at
  version 1. Older strict decoders reject either new field before they read
  `version`. Use this version or newer for
  install, status, rotate, and uninstall after it writes either field, even if
  schedules remain installer-owned. Older CLIs also
  treat account-owned schedules as foreign and refuse their cleanup; upgrade
  before retrying rather than deleting the schedules by hand.

The route becomes active for a dispatch entry point once install moves it onto
the trigger-only account. Scheduled polling moves when the activation is
recorded (below). The managed webhook moves at the same time: it embeds the
account's published trigger token, so a webhook-triggered pipeline runs as the
account and dispatches through the child-pipeline route
([#8509](https://github.com/fullsend-ai/fullsend/issues/8509)). Without a
recorded activation the webhook keeps a Poller-owned trigger on the `api`
route. The poller, dispatcher, and agent job scripts unset the account trigger
token before doing work, and `fullsend run` denies harness expansion of it.
The Poller merge-grant code (`EnsureGitLabPollerPipelineAccess`,
`GrantProtectedBranchMergeUser`, `PollerMergeGrantError`, the supplied-Poller
rollback and retained-grant diagnostics, `VerifyGitLabPollerProtectedPipelineAccess`,
`PollerCanCreatePipeline`, and the `protected-ref-pipeline` status) stays,
because both installer-owned fallbacks still use the Poller `api` route.

**Polling schedule ownership** ([#8508](https://github.com/fullsend-ai/fullsend/issues/8508)).
A pipeline schedule creates every pipeline as its owner. Once install has
recorded the activation for the current ledger generation,
`ConvergeGitLabTriggerSchedules` recreates the managed polling schedules
(`fullsend slash poll`, `fullsend event poll`) under the trigger account, so a
scheduled poll runs as that account and dispatches through the child-pipeline
route. When the activation is not recorded (the account or the gate deferred,
or the account stage failed), `ReleaseGitLabTriggerSchedules` hands every
account-owned managed schedule back to the installer instead. After a
successful reconciliation, a poll job runs as the trigger account only while
the child-pipeline route is selectable; an interrupted install can temporarily
leave account-owned schedules on the Poller API route, as described below.
The lifecycle
lives in `internal/repos/gitlab_trigger_schedules.go`:

- **Creation and adoption.** GitLab records the caller as a schedule's owner,
  so the account creates its schedules itself, with a short-lived bootstrap PAT
  minted while it stays at Developer. Creating and running a schedule on the
  protected branch also requires merge permission for the account user, which
  the activation gate verifies without changing branch permissions. The PAT
  reuses the ledger's `pat_pending`/`pat_id` obligation, so an
  interrupted run is contained like an interrupted bootstrap and the ledger
  gains no field. Each replacement runs on the recorded branch with the spec's
  UTC cron, no pipeline variables, and the active state of the schedule it
  replaces. GitLab must report every replacement as owned by the account.
  Only then are the installer-owned originals deleted, so a failure keeps
  polling on the installer's schedules. A spec with no live schedule at all is
  left to `convergeSchedules`, and the next install moves the schedule it
  creates. Install adopts a managed schedule it does not already own only
  when its owner is a verified retired generation of the account or a
  verified installer Maintainer. For any other owner, such as a Developer or
  an unverifiable user, install refuses to adopt the schedule, and an
  administrator must remove or reassign it before install is re-run.

  **Accepted temporary-PAT risk (#8519).** GitLab records the caller as the
  schedule owner, so account-owned creation currently requires a temporary
  `api`-scoped PAT for that account. Its scope grants broader Developer API
  authority than schedule creation needs. Together with the account's deploy-key
  grant, a compromised PAT could permit protected-branch writes; treat it as a
  protected-branch-write credential while it exists. Before minting, install
  records `pat_pending` in the ledger and verifies Developer access. It records
  the PAT ID before use, gives the PAT a one-day expiry, and keeps its value
  only in memory. `endBootstrap` revokes it on both success and failure;
  `containBootstrap` handles an interrupted run. This risk is accepted for
  #8508 in this PR, with no follow-up work deferred.
- **Drift.** An account-owned managed schedule on another ref, with another
  cron or timezone, or with pipeline variables is replaced the same way, and
  duplicates are deleted. One that carries pipeline variables would fail typed
  activation during convergence, which requires variable-free managed
  schedules, so install hands it back to the
  installer after the repository selection and configuration preflights succeed,
  before convergence. That repair
  inspects every ownership-verified generation, current and retired, even when
  the current one is not ready or published.
- **Hand-back.** An installer-owned copy (same description, recorded branch,
  managed spec's UTC cron and intended active state, no variables) is created
  first, unless one already exists, and
  then the account's schedule is deleted. The hand-back also runs before the
  account is ever raised to Maintainer (a schedule fires on the server's clock
  and would run with Maintainer access) and when rotation or repair retires a
  generation, before its account is deleted. When the activation is recorded
  for the replacement generation, adoption has already moved the retired
  account's managed schedules straight to the replacement account in the same
  install, before the deferred retirement, so retirement finds none to hand
  back (#8602).
- **Interrupted cleanup.** Every move creates before it deletes, so an
  interruption leaves a duplicate that the next run removes, never a project
  without a polling schedule.
- **Uninstall** deletes the account-owned managed schedules without a copy,
  because uninstall removes every fullsend schedule.

A managed schedule is recognized only by its exact description and the account
as its reported owner. Only a holder of the account's credentials can make it a
schedule's owner, and fullsend holds the only one: any other active PAT fails
closed. Any other schedule the account owns is foreign. The activation is
withdrawn at the start of every install. Withdrawing it turns off
child-pipeline route selection, and the poll job selects the `api` route. If an
install stops before the trigger-account stage, the account-owned schedules
keep running as the account on that `api` route until an install completes.
That route uses the Poller credential, which GitLab injects into every
protected-branch job whoever owns the pipeline. The account does not hold that
credential itself, and scheduled dispatch is not necessarily interrupted.

**Fresh generations; no re-elevation.** A trigger credential that was already
published is never re-elevated. A trigger request accepted before revocation
can still materialize as a job while the account holds Maintainer, and GitLab
has no server-side drain barrier to wait on. Repair and rotation therefore
create a fresh trigger-account generation (new nonce, account, deploy key, and
trigger token), publish it, verify the managed webhook cutover, and only then retire the previous generation:
revoke its trigger, revoke its branch push entry, delete its deploy key, and
delete its account, each with the ownership checks above. The previous
generation stays at Developer until retirement. Retirement is recorded in the
ledger (the `retired` list) and retried by install and uninstall; status
reports an unfinished retirement as drift.

**Foreign resources.** Each account must own no SSH authentication key, no pipeline schedule other
than the managed polling schedules, and
no pipeline trigger token other than the recorded one: any of those fails
closed, because each would let a holder act as the account, and fullsend
removes only what it can prove it owns.

**Protected-branch change.** The ledger records the branch every grant was made
on. When the default branch differs, install and rotation defer with a reason
instead of moving grants: nothing is granted on the new branch, and `repos
uninstall` removes the recorded resources from the recorded branch. Run
uninstall, then install again.

**Status.** `repos status` reports one of `not-installed`, `ready`, `drifted`,
`deferred`, `in-progress`, `cleanup-pending`, or `unreadable`, with exact
reasons: a deferral reason (wildcard-only branch, service accounts
unavailable, pipeline-variable override role not `no_one_allowed`, branch
change), the live override role of an installed account (the exact role, or
that it could not be verified), the resources a pending cleanup still holds by
ID, and drift such as a missing key, push entry, trigger, or sink, a sink that
is not masked, protected, non-file, and unscoped, an unfinished retirement of a
previous generation, an account that is not Developer, an account that no
longer exists, or a managed polling schedule that the account does not own
while the dispatch activation is recorded (`scheduleOwnershipDrift`). For
that last case the reason tells the operator to re-run install when install
can adopt the schedule's owner, and otherwise that an administrator must
remove or reassign the schedule; an owner or schedule list that could not be
read is reported as unverified. Anything other than
`not-installed`, `deferred`, and `ready` is reported as drift.

GitLab.com Free rejects per-user protected-branch merge grants, but it
accepts a deploy key in a protected branch's `allowed_to_push` list and lets
the user who added the key start pipelines on that branch. The trigger-only
service account uses that path. It is a separate project service account,
never the Poller identity: a PAT on an identity that holds a deploy-key grant
could write the protected branch, so the Poller role PAT stays on a
different account. Converge and uninstall both refuse an account ID listed as
a Poller identity.

**Resources.** All names carry a random 16-hex-character nonce
(`fullsend-trigger-<nonce>`) that is recorded in
`FULLSEND_GITLAB_TRIGGER_ACCOUNT` before anything is created:

| Resource | Created with | Ownership proof |
| --- | --- | --- |
| Project service account `fullsend-trigger-<nonce>` | installer | Recorded ID, or the single account with the recorded nonce name |
| Bootstrap PAT `fullsend-trigger-bootstrap-<nonce>` (`api`, one-day expiry) | installer | Recorded ID and nonce name on the owned account (a recorded ID with another name fails closed); always revoked before readiness |
| Push-capable deploy key `fullsend-trigger-<nonce>` | bootstrap PAT, so the trigger account owns it | Title and the SHA-256 fingerprint recorded before creation |
| Deploy-key push entry on the branch's exact-name protected-branch rule | installer | Entry read back by deploy key ID; removed only after the key's title and fingerprint are verified and while it still names that key |
| Pipeline trigger token `fullsend-trigger-<nonce>` | bootstrap PAT, so the trigger account owns it | Recorded ID, description, and owner ID, all checked before any revocation; unrecorded leftovers need the nonce description and the account as owner |
| Managed polling schedules (`fullsend slash poll`, `fullsend event poll`) | bootstrap PAT at Developer after activation is recorded; an interrupted install can leave them account-owned after activation is withdrawn | Exact managed description and the account as reported owner; handed back to the installer (or deleted on uninstall) before the account is raised or deleted |

The deploy key pair is generated in memory and the private key is discarded
immediately; only the public key reaches GitLab. Trigger token values go to
the caller's publisher and never into the ledger.

**Install** (`ConvergeGitLabTriggerAccount`):

1. Contain any bootstrap left by an interrupted run (revoke the recorded PAT
   and return an elevated account to Developer) and finish any unfinished
   rotation before anything else. Then, before any deferral, inventory read or
   replacement, contain the published sink: remove an unsafe published token
   variable, and revoke the owned triggers across every publication-owning
   generation, current and retired, including a retired generation an
   interrupted rotation left in the sink. The same containment removes the
   variable and revokes the triggers whatever the variable's attributes when
   the live override role is weakened or cannot be read. It also removes the
   sink and revokes the owned triggers when a project-wide webhook trigger
   safety invariant fails or cannot be read (for example another protected
   ref or an extra trigger-pipeline job). The lifecycle checks those same
   invariants before publishing a replacement, so convergence cannot undo
   containment while the project remains unsafe.
   `ContainGitLabTriggerAccount`, the containment-only entry point that
   `repos install` runs first, performs the same bootstrap and published-sink
   containment. Variable deletion and each generation's revocation are
   attempted independently and the errors are joined, so one failure does not
   suppress the others; unresolved obligations stay in the ledger. If
   containment cannot complete, fail with an explicit error instead of
   deferring. Then defer, creating nothing, when
   the branch has no exact-name protected-branch
   rule (fullsend never edits wildcard rules, which cover other branches),
   the live `ci_pipeline_variables_minimum_override_role` is not
   `no_one_allowed` (the lifecycle checks it itself, so a legacy repository
   that allows pipeline variable overrides never gets a live trigger
   credential, whichever install path called it; rotation defers before the
   ledger moves to a replacement generation), or
   project service accounts are unavailable (404, 403, or not supported).
2. Create or adopt the account. Fail closed if it holds an active PAT that
   fullsend did not create.
3. If the deploy key or the published trigger token is missing, record
   `elevated`, raise the account to Maintainer, mint the bootstrap PAT,
   create the missing resources with it, then revoke every bootstrap PAT and
   return the account to Developer. The revoke and demotion also run when a
   create step fails, each independently of the other and on a bounded context
   that survives cancellation of the caller's.
4. Grant the key push on the exact-name rule and record the entry ID.
5. Record phase `ready`. A re-run repairs drift: a missing grant is recreated
   and an elevated membership is restored to Developer. A missing key or
   trigger token, or sink drift, is repaired through a fresh generation rather
   than by re-elevating the published account.

**Rotation** (`RotateGitLabTriggerAccount`) records phase `rotating`, creates a
fresh generation, publishes its trigger token, verifies the webhook
cutover, and then retires the previous generation with the uninstall ownership checks (see above). A re-run of rotate
or install while the phase is `rotating` or while a retirement is recorded
finishes it.

**Uninstall** (`RemoveGitLabTriggerAccount`) records phase `removing`, then
deletes the published token variable, deletes the account-owned managed
polling schedules without an installer-owned copy, and revokes the trigger
token, the grant entry, the deploy key, any bootstrap PAT, and the account, clearing each from
the ledger only after it is gone (including any unfinished retirement of a
previous generation), and finally deletes the ledger. Generic variable cleanup
never deletes the ledger or the token sink. Install refuses to run while phase is `removing`.

**Recovery.** Every mutating call is preceded by a ledger write, so an
interrupted install, rotation, or uninstall leaves a retriable obligation:
re-run the same command. A bootstrap PAT or unpublished trigger token left by
an interruption cannot be recovered (its value is gone), so the next run
revokes it and creates a new one. During replacement, `publication_pending`
records the gap between saving the new bearer digest and finishing its
publication. A retry accepts the exact retired bearer while it is still in
the sink and keeps the retired trigger during a cleared-sink gap, so the old
webhook remains usable until cutover. If the new bearer is already in the
protected sink, a retry verifies its digest and owned trigger before marking
publication complete. A recorded resource whose name,
fingerprint, description, or owner no longer matches fails closed and is left
in place with the ledger kept; an administrator must inspect it. A recorded
account that no longer exists blocks install; run uninstall to clear the
remaining resources, then install again. Unrelated administrator accounts,
keys, push entries, PATs, and trigger tokens are never touched. Do not edit
or blank `FULLSEND_GITLAB_TRIGGER_ACCOUNT` by hand: a blank or malformed
ledger fails closed.

## Job → role mapping

| Job | Role |
| --- | --- |
| GitLab poller/controller (`fullsend poll`, `fullsend-poll.yml`) | Poller |
| GitLab webhook dispatcher (`fullsend poll --input-driver gitlab-webhook`, `fullsend-dispatcher.yml`) | Poller |
| GitLab Jira poller (`fullsend poll --input-driver jira-poll --target-forge gitlab`) | Poller |
| Agents / harness roles `review`, `triage`, `prioritize`, `retro`, `scribe` | Analyst |
| Agents / harness roles `code`, `fix`, `coder` | Coder |
| Custom agent whose name or harness `role:` is listed on a registered custom role | That custom role |

`Registry.RoleFor` accepts either an agent name or a harness `role:`
value. Built-in aliases and custom agent names share this lookup.

Unmapped jobs (for example `e2e` or an unregistered custom agent) fail
closed (`ErrUnregistered`) rather than guessing an identity.
`ValidateAgent` itself takes no mode and always rejects an unmapped
name; `Select` / `SelectAgent` call it as a pre-check ahead of
`Resolve`.

## Role-identity state

There is no migration-gate variable, flag, or public rollback to the
shared token anywhere in the runtime, install, or uninstall contract.
Every job requires its registered role secret and fails closed when
that secret is missing.

The shared token is **not** selected at runtime. A repository installed
before this role-only model shipped may still carry a leftover
`FULLSEND_FORGE_TOKEN` secret and a leftover `fullsend-bot` project
access token; no automated path — not install, not uninstall — reads,
writes, or removes them. Removing that leftover state is a manual
administrator step. An unregistered name is never a reason to use the
shared token. There is no shared-token fallback for an unconfigured
role.

## How a job selects its credential

Call `gitlabroles.Select` (poller) or `gitlabroles.SelectAgent` (agent
jobs). Those helpers load the registry and presence map, call
`ValidateAgent`, then `Resolve`:

- `Job` (`PollerJob()` or `AgentJob(name)`)
- `Registry` from `LoadRegistry` (zero value = built-ins only)
- `Present`: a boolean map of whether each secret *name* is non-empty
  (`PresenceFrom`). **Never put token values in this map.**

`Select` and `SelectAgent` never set `FailedSecret` on the `Request` they
build — it stays at its zero value. `FailedSecret` only matters when a
caller constructs a `Request` directly and calls `Resolve` after an
authentication failure. `fullsend poll`'s `wrapGitLabAuthFailure` uses the
`AuthFailed` helper for this instead of re-resolving: on a 401/403, it
wraps the error with `gitlabroles.AuthFailed(role, secret)` rather
than calling `Select`/`SelectAgent`/`Resolve` again for that job. Per
`AuthFailed`'s doc comment, callers must fail closed on an authentication
failure, not re-Select with a different job or a cleared `FailedSecret`.

The result is a `Source` whose `SecretName` is the CI/CD variable to
read. Callers then `os.Getenv(src.SecretName)`. Built-in and custom
roles return through this same function.

`fullsend poll` selects the Poller credential. `fullsend run` selects
the agent identity (agent name, or harness `role:` if the agent name is
unlisted), exports `GITLAB_TOKEN` from that secret, and sets
`PUSH_TOKEN` only when the registration declares `write_repository`.
`fullsend post-review` refuses GitLab `APPROVE` when the identity lacks
`approve_merge_request`. Selection also publishes non-secret diagnostic
env vars `FULLSEND_GITLAB_ROLE`, `FULLSEND_GITLAB_ROLE_SECRET`, and
`FULLSEND_GITLAB_ROLE_SOURCE`.
GitLab CI templates (`fullsend-poll.yml`, `fullsend-dispatcher.yml`,
`fullsend-agent.yml`) source `run-poll-job.sh`, `run-dispatcher-job.sh`, and
`run-agent-job.sh`, which resolve credentials via
`select-gitlab-role-token.sh`. All three job scripts first source
`pin-ci-job-identity.sh`, which takes project, pipeline, and ref from
the `CI_JOB_TOKEN` job record (`GET /api/v4/job`) rather than from the
overridable `CI_PROJECT_ID` / `CI_PIPELINE_ID` / `CI_COMMIT_REF_PROTECTED`
env vars, and fails closed unless the server-side `.source` matches that
job's disjoint allowlist (poller = `schedule` only, dispatcher = `trigger`
only, agent = `api` or `pipeline` only; `parent_pipeline` is not admitted)
and the job ref is the project's protected default branch. The agent job then
requires the pipeline creator that source implies: the Poller bot for `api`,
and for `pipeline` the account recorded in the protected
`FULLSEND_GITLAB_TRIGGER_ACCOUNT` ledger in phase `ready`, which must not be
the Poller identity (see **Child-pipeline agent dispatch** under
[Trigger-only service account](#trigger-only-service-account)). YAML `workflow:` and job `rules:` also deny
truthy `CI_DEBUG_TRACE` values (the gitlab-runner `ParseBool` truthy set:
`1`, `t`/`T`, `true`/`TRUE`/`True`) before any admit rule, because secrets
materialize at job init. In `run-agent-job.sh`,
the STAGE pipeline variable that would otherwise select the role is not
yet authenticated when the job starts, so the *pre-verification*
bootstrap calls (resource-group PUT, bot-identity `/user` call) always
resolve the Poller credential (`FULLSEND_GITLAB_POLLER_TOKEN`), regardless
of stage — this avoids handing a forged dispatch a higher-privilege token
before HMAC verification passes. The template only re-resolves the
credential for the job's actual stage — analyst stages use
`FULLSEND_GITLAB_ANALYST_TOKEN`, coder stages use
`FULLSEND_GITLAB_CODER_TOKEN` — once `DISPATCH_VERIFIED` is true: the
`api`- or `pipeline`-sourced dispatch passed HMAC verification. This is required for
every job:
`select-gitlab-role-token.sh` has no shared-token path left
(it mirrors `gitlabroles.Resolve` on the Go side), so a sibling
analyst/coder secret is a real higher-privilege credential
as well, and an unverified STAGE must not be
allowed to select it there either. A missing `FULLSEND_DISPATCH_SECRET`
now fails the job closed in every gate mode instead of silently
skipping HMAC verification. A
`parent_pipeline`-sourced dispatch (legacy child-pipeline installs;
current installs dispatch via `api` or the `pipeline` trigger-API route) is
already denied at the
identity pin before reaching this gate, so it never contributes a
`DISPATCH_VERIFIED=true`. The job fails closed at that point
(`exit 1`) in every gate mode, rather than continuing on the
lower-privileged Poller credential. An earlier revision of this template
continued the job on the Poller credential instead, but that was not
sufficient: `fullsend run` resolves its own GitLab credential internally
via `gitlabroles.SelectAgent(agentName, harnessRole, os.Getenv)`
(`internal/cli/gitlab_role.go`), using the same unverified `STAGE` value
and reading role secrets directly from the process environment — a
shell-local `DISPATCH_VERIFIED` flag has no effect on that Go-side
selection, so continuing on the Poller credential in the shell did not
stop the CLI from promoting the STAGE-derived role token anyway. Failing
the whole job closed, before `fullsend run` or the `STAGE=fix`
review-body pre-fetch below ever execute, is the only way to keep an
unverified STAGE from reaching a role-specific credential. The identity
pin plus HMAC close the gap where a forged dispatch that spoofed the
pipeline source via overridable CI variables (an overridden
`CI_API_V4_URL`, the residual risk previously documented here and in
ADR 0067) could otherwise obtain a higher-privilege role token. The
GitLab-side `ci_pipeline_variables_minimum_override_role=no_one_allowed`
restriction remains the required control against `CI_JOB_TOKEN` /
`CI_API_V4_URL` outranking; it is applied at install/converge time by
this repo (not a separate issue); the in-job pin is defense-in-depth.
`no_one_allowed` (not `owner`) is the target as of #7850: the
poller/dispatcher dispatches exclusively via typed GitLab CI/CD pipeline
inputs (`forge.Client.CreatePipelineWithInputs`,
`internal/poll/dispatch.go`), which this setting does not govern, so the
restriction no longer needs an Owner-role poller/dispatcher credential —
`PollerCanCreatePipeline` / `EnsureGitLabPollerPipelineAccess` in
`internal/repos/gitlab_pipeline_access.go` stay built around the
Developer-level poller unchanged.
`repos.EnsureGitLabPipelineVariableOverrideRole`
(`internal/repos/gitlab_pipeline_var_restriction.go`) reads and converges
this setting idempotently after compatible templates have landed. Active enforcement
(actually calling `SetPipelineVariablesMinimumOverrideRole`) is gated
behind `FULLSEND_GITLAB_PIPELINE_VAR_RESTRICTION=enforced` and defaults
to no automatic setting changes. However, typed dispatch activation fails
closed before delivering runnable templates unless the project already has
verified `no_one_allowed`, even when enforcement is requested. Unset, weaker,
or unsupported settings cannot succeed as a planned update or report-only
installation. Legacy variable-based installations are no longer supported;
see [Typed-input dispatch migration](../guides/getting-started/configuring-gitlab.md#typed-input-dispatch-migration).
Managed schedules use no pipeline variables; job rules derive poll mode
from `CI_PIPELINE_SCHEDULE_DESCRIPTION`. Activation waits until the wrapper and compatible
agent/poll templates are confirmed on the default branch, and only reads
managed schedules: it fails while any of them still sets a pipeline variable,
including an obsolete `FULLSEND_POLL_MODE` override, which must be removed
manually. Install and converge refuse a legacy variable-based wrapper, such
as one from a `fullsend_ref` pinned before v0.46.0, and the poller never
falls back to pipeline-variable dispatch. GitLab vendor mode and
version pins without matching upstream templates are rejected before writes.
Agent input bridges explicitly disable expansion, and poll-job links are
logged only after creator/HMAC authentication.
Native `TRIGGER_PAYLOAD` delivery under `no_one_allowed` is verified: the
#8245 webhook runs dispatched agents on projects where install had verified
the restriction. The `CI_JOB_TOKEN` running-job identity pin is implemented,
but its invalid and valid-foreign token override cases have not been tested
on a target GitLab version, so the identity claim stays gated on the
restriction. Live GitLab validation of issue-to-agent dispatch passed on
GitLab.com Free and self-managed EE; that run covered issue-open and polling
dispatch, not issue-update dispatch. Self-managed CE is non-gating: earlier
CE runs used a patched agents helper, and the stock-agents CE reruns did not
reach issue data inside the sandbox, so stock agents on custom-port CE remain
unverified. See
[GitLab webhook validation status](../normative/normalized-event/v1/README.md#gitlab-webhook-validation-status)
and
[ADR 0125 security-gate status](../normative/normalized-event/v1/README.md#adr-0125-security-gate-status),
which also lists the gates still open. The
[#8245](https://github.com/fullsend-ai/fullsend/issues/8245)
live-validation gate that a fleet-wide flip of the env var to `enforced`
waits on closed on 2026-10-24. Poll jobs
resolve `FULLSEND_GITLAB_POLLER_TOKEN` once, after the schedule-only pin.
The Go CLI then overrides `GITLAB_TOKEN` / `PUSH_TOKEN` from the
registered role credential; nothing restores `FULLSEND_FORGE_TOKEN`.

The poll and dispatcher jobs also need the Analyst's numeric user ID
(#8807). The review stage posts its `<!-- fullsend:changes-requested -->`
marker as the Analyst. The poller retains a bot-authored marker note for
the fix stage only when that note's author is the Analyst. Markers from
the Poller or any other bot stay filtered. Before blanking sibling role
secrets, `run-poll-job.sh` and `run-dispatcher-job.sh` call
`fullsend_resolve_gitlab_analyst_user_id` (in `pin-ci-job-identity.sh`).
That function selects the Analyst credential in a subshell for a single
`GET /user` and exports only the job-local
`FULLSEND_GITLAB_ANALYST_USER_ID`. The Analyst credential never replaces
`FULLSEND_JOB_TOKEN` and is still blanked before `fullsend poll` runs. An
inherited value is discarded first. A failed lookup only warns and leaves
the variable unset, and the poller then retains no marker.

The `STAGE=fix` review-body pre-fetch is a separate case: it looks up
the prior review note, which is always authored by the Analyst identity
(`STAGE=review` maps to the analyst role) regardless of which role is
running the fix stage. It cannot reuse the fix stage's own
`FULLSEND_JOB_TOKEN` (Coder) or the discarded Poller `BOT_USER_ID` for
that author match — neither identity is the note's author once
analyst/coder resolve to distinct tokens. This lookup only runs once
STAGE has already been authenticated, since the job
would otherwise already have exited above, so `run-agent-job.sh` temporarily re-sources
`select-gitlab-role-token.sh` with `FULLSEND_JOB_AGENT=review` to resolve
the Analyst identity for that one lookup, then restores
`FULLSEND_JOB_TOKEN` to the Coder credential before `GITLAB_TOKEN`,
`PUSH_TOKEN`, and the rest of the fix stage run.

## Unconfigured vs unregistered vs failed

These are different errors. Do not collapse them.

| Situation | Sentinel | Meaning |
| --- | --- | --- |
| Role name is not in the registry | `ErrUnregistered` | Custom agent referenced an unknown identity |
| Role secret absent or empty | `ErrUnconfigured` | Registered, not provisioned yet |
| Runtime 401/403 (or equivalent) from a selected credential | `ErrAuthFailed` | Credential is present but unusable |
| Job kind is empty or unrecognized | `ErrUnknownJob` | No identity to select |
| Registry JSON is malformed or untrusted | `ErrInvalidRegistry` | Fail closed; do not load custom roles |

A registered role whose secret is absent is `ErrUnconfigured` even when
`FULLSEND_FORGE_TOKEN` is present. `ErrUnregistered` and `ErrAuthFailed`
never fall back to the shared token.

## No silent fallback on authentication failure

If a selected credential fails authentication or authorization, the job
fails. It does **not** retry as another identity, including the shared
bot.

`Resolve` enforces this when `FailedSecret` is set: it returns
`ErrAuthFailed` and returns a zero `Source`. Callers that
observe an auth failure must either pass that secret name back into
`Resolve` or stop; they must not call `Resolve` again with a different
job or a cleared `FailedSecret` in order to pick a substitute.

## Status, drift, and diagnostics

`gitlabroles.Diagnose(present, registry)` is the observable
report:

- Per-role state: `configured` or `unconfigured` (presence only),
  including custom roles and reuse targets
- `Partial`: some but not all registered role secrets exist
- `Ready`: every registered role credential is present and satisfies the
  capability contract. Runtime `Resolve`/`Select`/`SelectAgent` always
  require the registered per-role secret and never fall back to the shared
  token.
- `Missing`: registered roles whose secrets are absent
- `Diagnostics`: human-readable lines with **names only**

`repos uninstall` deletes the registry, built-in and custom role secrets,
and matching `fullsend-poller` / `fullsend-analyst`
/ `fullsend-coder` / `fullsend-role-*` project access tokens, and removes
the rotation document last. A rotation document that records supplied-account
exclusions is retained as an exclusions-only document, and uninstall fails
closed before deleting anything when the rotation state is unreadable or
malformed. It does **not** delete a leftover `FULLSEND_FORGE_TOKEN` secret or revoke a
matching `fullsend-bot` project access token — a repository installed
before the role-only rollout requires manual cleanup of those. A
token-revocation failure fails uninstall so the manifest entry remains
for an idempotent retry. Ordinary reinstall after a complete uninstall
provisions fresh role credentials again, but it does not recreate the
retired legacy shared credential or any migration gate — those stay
retired.

**Never** put token values in logs, status output, issue comments, or
`Error` strings. Presence booleans and variable names are the only
safe signals.

`repos status` reports per-role diagnostics (names only) and reports missing,
expired, revoked, or unverified role credentials as drift.

## Built-in role readiness (#7501)

`gitlabroles.CheckBuiltinReadiness(present, registry)` is the
verification check for the three built-in roles. Readiness only reports
whether the built-in roles are configured correctly; it does not trigger
any retirement of the legacy shared token — no automated path retires it.

For each of Poller, Analyst, and Coder it confirms:

- The role secret is present (`FULLSEND_GITLAB_POLLER_TOKEN`,
  `FULLSEND_GITLAB_ANALYST_TOKEN`, `FULLSEND_GITLAB_CODER_TOKEN`).
- The registration declares the required capabilities and does not
  declare the capabilities it must not hold (Analyst cannot write
  repository code; Coder cannot approve merge requests; Poller cannot
  act as either).
- Built-in job names map onto that identity (`poller`; Analyst agents
  `review` / `triage` / `prioritize` / `retro` / `scribe`; Coder agents
  `code` / `fix` / `coder`).
- `Resolve` always selects that role's own secret. A missing
  secret fails closed as `ErrUnconfigured`; a present
  `FULLSEND_FORGE_TOKEN` is never a substitute.

When `repos status` has a GitLab project-token inventory, it also applies
`BuiltinReadiness.WithLifecycle` and `RegisteredReadiness.WithLifecycle`:
expired, revoked, or unverified project tokens downgrade an otherwise passing
role to not-ready. The base status path passes no inventory and therefore
leaves lifecycle readiness unchanged; `EnrichGitLabRoleStatus` is the path
that supplies the lifecycle data.

The readiness APIs are `gitlabroles.CheckBuiltinReadiness` for Poller,
Analyst, and Coder and `gitlabroles.CheckRegisteredReadiness` for every
registered custom role. The latter also verifies that each mapped agent
resolves to its registered credential.

`BuiltinReadiness.Ready` is true only when all built-in roles pass.
`RegisteredReadiness.Ready` separately covers every registered role. Overall
repos status combines both results. Diagnostics carry role names and secret
*names* only, and status appends these lines after the Diagnose report without
retiring the shared token.

## Verification

`repos install` verifies registered-role readiness after provisioning but
does not act on a leftover legacy shared secret either way: whether or not
all roles are ready, it never deletes `FULLSEND_FORGE_TOKEN` or revokes a
`fullsend-bot` project token. Runtime never selects that credential
regardless of readiness. A repository installed before the role-only
rollout keeps the shared credential until an administrator manually
removes the secret and revokes its matching PAT.

## Registry JSON shape

`FULLSEND_GITLAB_ROLE_REGISTRY` (protected, unmasked):

```json
{
  "roles": [
    {
      "name": "scanner",
      "responsibility": "read-only scanning",
      "credential": "own",
      "capabilities": ["read_issues", "write_notes"],
      "agents": ["scanner"]
    },
    {
      "name": "deployer",
      "credential": "reuse",
      "reuse": "coder",
      "capabilities": ["write_repository", "write_merge_request"],
      "agents": ["deploy"]
    }
  ]
}
```

`name` must match `^[a-z][a-z0-9_-]*$` with no double hyphen, the same
rule as mint role names. `secret_name` is optional on `own` and must
equal the derived `FULLSEND_GITLAB_ROLE_<NAME>_TOKEN` when set.

`repos install --gitlab-role-registry` writes this variable.
Agents and repository files do not.

## Rotation and recovery

GitLab project access tokens expire in at most one year. Fullsend
rotates each own-credential registered role independently — built-in
Poller, Analyst, and Coder, and custom `own` roles. A `reuse` role
follows its target; it is not minted a second time.

`repos install` rotates a role when `DiagnoseLifecycle` reports it as
expiring (within 30 days), expired, revoked, or unverified (secret
present but no matching project access token). `--rotate-gitlab-roles`
force-rotates every own-credential role, and `--rotate-gitlab-role=<name>`
limits the run to that role (repeatable).

**Create-then-distribute, not GitLab's rotate-in-place API.** GitLab's
token-rotate endpoint invalidates the previous secret immediately.
Fullsend creates a new PAT with the same token name, writes it to the
existing masked CI variable, and leaves the previous PAT active for a
24-hour grace so jobs that already hold the old value in their
environment can finish. A later `repos install` after the grace period
revokes the outgoing PAT. New jobs started after distribution read the
replacement from CI.

**Failed rotation does not strand a role.** If creation fails, nothing
is written. If distribution fails, only the unused replacement PAT is
revoked and the previous CI secret is left in place. Concurrent
attempts for the same role are serialized (in-process lock, the
cross-process project lease described under "One installer at a time per
project", and a protected rotation-state document) and idempotent within a five-minute
window: a retry adopts the already-distributed replacement rather than
minting another. A crash after create where distribution is unproven
(state stuck at `distributing`/`failed` with an incoming ID) is
recovered by treating that incoming PAT as possibly the live CI
secret: it is never revoked immediately, but kept in the outgoing set,
the phase is marked `failed`, and a fresh replacement is minted and
distributed. The preserved token is revoked only after the normal
24-hour grace, once the new replacement is confirmed distributed.

**No silent shared-token fallback.** Rotation never writes
`FULLSEND_FORGE_TOKEN` and never selects the shared credential because
a role rotation failed. Runtime 401/403 of a selected role credential is
still `ErrAuthFailed`.

**Administrator-provided replacement does not auto-revoke leftovers.**
`--gitlab-role-token` (free-tier enrollment or a custom `own`
credential) stores the supplied value directly. Enrollment resolves the
supplied credential's owner and token ID through
`SuppliedCredentialIdentity` and records the owner's permanent exclusion,
so the enrolled credential is never grace-revoked as a same-named
leftover. Enrolling a replacement this way still does not schedule any
other active same-named PAT for revocation; if one exists, confirm it is
not the replacement and revoke it manually. Previously tracked managed
tokens stay recorded as outgoing obligations and remain usable: the 24-hour
grace cleanup applies only after a later managed replacement runs, so the
supplied-switch path never retires them automatically. Enrollment also
recovers a role whose recorded `ManagedUserID` account was deleted: with a
complete inventory that positively lacks the account, convergence defers to the
supplied enrollment, and once enrollment completes the absent historical
account (kept as cleanup provenance) no longer blocks ordinary installs or
forced rotations. An unreadable inventory still fails closed.

**Identity continuity.** Rotating a service-account PAT keeps the
account's GitLab user ID; only a legacy project access token gets a new
bot user per PAT, so the user ID changes on replacement there. Fullsend
preserves the role name, token name (`fullsend-poller`,
`fullsend-role-<name>`), CI variable, and capability set. Rotation state
records the old and new token IDs (never secret values) for internal use
by `RotateGitLabRoleCredentials`: serialization between runs, crash
recovery, and grace-period revocation tracking during `repos install`.
`repos status` also reads it (`AppendGitLabServiceAccountStatus`) to
report service-account lifecycle state.

**Diagnostics.** `DiagnoseLifecycle` classifies each role as `ok`,
`expiring`, `expired`, `revoked`, `unverified`, `overlapping`, or
`unconfigured`. `repos status` reports those names and treats expired and
revoked credentials as drift. Lines carry role
names, secret names, and dates only.

## What this contract does not do

Leave these to the follow-up issues.

| Issue | Work |
| --- | --- |
| [#7498](https://github.com/fullsend-ai/fullsend/issues/7498) | **Implemented.** `repos install` creates/enrolls built-in and custom PATs, stores them as protected masked CI variables, writes the registry, reports partial provisioning, preserves the shared token, and handles reinstall/drift/uninstall without deleting credentials still in use |
| [#7499](https://github.com/fullsend-ai/fullsend/issues/7499) | **Implemented.** `fullsend poll`, `fullsend run`, and `fullsend post-review` select the registered role credential, enforce `ValidateAgent` / `Registration.Has`, and fail closed on authentication failure without switching identities |
| [#7500](https://github.com/fullsend-ai/fullsend/issues/7500) | **Implemented.** Role-aware rotation, recovery, in-flight overlap, and expiry/revocation diagnostics. See [Rotation and recovery](#rotation-and-recovery) and follow the [credential-routing security checklist](#credential-routing-security-checklist) |
| [#7501](https://github.com/fullsend-ai/fullsend/issues/7501) | **Implemented.** Built-in and registered-role readiness is surfaced on `repos status`. Live GitLab ACL/operation probes and deployment branch-rule verification remain deployment prerequisites. |
| [#7524](https://github.com/fullsend-ai/fullsend/issues/7524) | **Implemented.** Ordinary `repos install` provisions role credentials. |
| [#7558](https://github.com/fullsend-ai/fullsend/issues/7558) | **Implemented.** `repos uninstall` removes GitLab role-identity state: the registry, rotation document, built-in and custom role secrets, and matching project access tokens. |
| [#7559](https://github.com/fullsend-ai/fullsend/issues/7559) | **Implemented.** Shared-token fallback and the public migration/cutover/rollback controls are removed; old state is ignored by runtime. |
| [#7502](https://github.com/fullsend-ai/fullsend/issues/7502) | **Implemented.** [ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md) records that the three-role decision in [#7424](https://github.com/fullsend-ai/fullsend/issues/7424) / [#7496](https://github.com/fullsend-ai/fullsend/issues/7496) supersedes its shared-identity assumption and permits registered custom roles as an extension. Operator-facing lifecycle is in [configuring-gitlab.md](../guides/getting-started/configuring-gitlab.md#role-identity-model-and-credential-lifecycle). |
| [#8083](https://github.com/fullsend-ai/fullsend/issues/8083) | **In progress** under [#7772](https://github.com/fullsend-ai/fullsend/issues/7772). Role service-account provisioning, install reconciliation, rotation/recovery, status/drift, and ownership-gated uninstall cleanup are active: CLI activation ([#8242](https://github.com/fullsend-ai/fullsend/issues/8242)) wires the service-account lifecycle into the CLI. The live fresh-identity Poller handoff ([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)) activates the Poller-owned trigger token lifecycle and the webhook fast path where eligible, and install retires the superseded Poller generation ([#8244](https://github.com/fullsend-ai/fullsend/issues/8244)); live acceptance validation, including GitLab.com Free and actual webhook/role-job execution, remains required. See [Project service accounts](#project-service-accounts). |
| [#7931](https://github.com/fullsend-ai/fullsend/issues/7931) | **Implemented.** The `FULLSEND_GITLAB_ROLE_MIGRATION` gate constant, the cutover mechanism that retired a leftover shared credential during install, and the uninstall cleanup of that leftover secret and its project access token are all removed. Automated cleanup for a repository installed before the role-only rollout is intentionally not preserved; that repository may require manual cleanup. |

## Credential-routing security checklist

Hold these four code invariants and the documentation-terminology rule
below when changing `internal/gitlabroles` or GitLab credential handling
in `internal/cli`. They are
the review findings from [PR #7510](https://github.com/fullsend-ai/fullsend/pull/7510)
(stage 3 routing). A later change that selects, stores, or hands a
GitLab role credential to a child process can reintroduce any of them.
The documentation-terminology rule comes from
[#7513](https://github.com/fullsend-ai/fullsend/issues/7513), keeping
fallback wording consistent across docs rather than fixing a routing
bug.
Extend the helpers named below rather than adding a parallel path.

### Check the authenticating token, not a role label

Capability and permission checks must validate the **token that will
actually authenticate the call**, not a role-label env var
(`FULLSEND_GITLAB_ROLE`, `STAGE`, or equivalent). Labels select a
registration; they can diverge from the credential (for example
`--token` pointing at a different role's secret). Compare the
authenticating token against `getenv(sel.Source.SecretName)` before
trusting `gitlabroles.Require`. A mismatch fails closed with
`gitlabroles.ErrIdentityMismatch`. See `checkGitLabApprovalCapability`
in `internal/cli/gitlab_role.go`.

- [ ] New capability checks compare the authenticating token to the
      selected role's own secret value.
- [ ] A label/token mismatch fails closed; it does not check the wrong
      identity's capabilities.

### Blank sibling role secrets after selection

After selecting a credential, blank every other registered role secret
(and the shared `FULLSEND_FORGE_TOKEN`) from the process environment
**before** invoking a pre/post-script. Host-side scripts inherit the
process environment (minus OIDC and named workflow secrets) via `childScriptEnv`. A leftover
`FULLSEND_GITLAB_ANALYST_TOKEN` in a Coder job lets a script
authenticate as Analyst and bypass in-process checks such as
`checkGitLabApprovalCapability`. See `clearSiblingGitLabRoleSecrets` /
`applyGitLabRoleSelection`.

- [ ] Selection blanks sibling role secrets and the unused shared token.
- [ ] New rotation or recovery paths that write a replacement secret do
      not leave the previous or sibling raw value in the process
      environment of a subsequent child.

### Pin routing env vars against runner_env override

`GITLAB_TOKEN`, `FULLSEND_FORGE_TOKEN`, and every `FULLSEND_GITLAB_*`
var must be pinned to the process environment when building a
child-script env. A harness `runner_env` / `env.runner` entry must not
shadow the dispatch-selected identity. `childScriptEnv` drops those
keys from `runnerEnv` via `isPinnedGitLabRoleRoutingKey`.

`PUSH_TOKEN` is **not** pinned: the GitHub coder-remint path
(`syncRunnerEnvTokens`, #7231) relies on `runner_env` overriding a
stale process-env `PUSH_TOKEN`, and GitLab never writes `PUSH_TOKEN`
through that path. Do not pin `PUSH_TOKEN` to "close the set" — that
reintroduces #7231 for GitHub runs.

- [ ] New GitLab identity or credential env vars are covered by
      `isPinnedGitLabRoleRoutingKey` (or an equivalent pin).
- [ ] `PUSH_TOKEN` stays unpinned unless the GitHub remint path is
      redesigned in the same change.

### Do not revive shared-token runtime authentication

Runtime authentication is role-credential only. Do not restore a
  `FULLSEND_FORGE_TOKEN` selection path or a local direct-`GITLAB_TOKEN`
  no-op when a role
secret is missing. Local GitLab runs must set the matching role secret
(`FULLSEND_GITLAB_POLLER_TOKEN`, `FULLSEND_GITLAB_ANALYST_TOKEN`,
`FULLSEND_GITLAB_CODER_TOKEN`, or a registered custom-role secret).
Neither `repos install` nor `repos uninstall` touches a leftover
`FULLSEND_FORGE_TOKEN` secret; removing it is a manual administrator
step on a repository installed before the role-only rollout.

- [ ] Runtime `Select` / `SelectAgent` / `Resolve` never return
      `FULLSEND_FORGE_TOKEN`.
- [ ] Missing role secrets fail closed as `ErrUnconfigured`.

### Keep fallback terminology consistent across docs

Two distinct leftover terms share similar wording and are easy to
conflate. Use these terms, and do not mix them:

- **shared-token path** — historical selection of `FULLSEND_FORGE_TOKEN`
  as the runtime credential. Runtime no longer uses this path. A leftover
  secret on a repository installed before the role-only rollout is not
  touched by any automated path; removing it is a manual administrator
  step.
- **local direct-GITLAB_TOKEN fallback** — the retired local-dev
  workflow where `GITLAB_TOKEN` was set with no role secret. `fullsend
  run --forge gitlab` now fails closed unless the matching role secret
  is present.

These two are described independently in four documents:

- this file (`docs/contributing/gitlab-role-credentials.md`)
- [`docs/cli/run.md`](../cli/run.md)
- [`docs/guides/user/running-agents-locally.md`](../guides/user/running-agents-locally.md)
- [`docs/problems/security-threat-model.md`](../problems/security-threat-model.md)

- [ ] Any change that touches credential selection or fallback behavior
      re-reads all four documents and updates them with the same
      terms. Do not edit only the file under your cursor.

## Security notes

- Threat priority remains external injection > insider > drift >
  supply chain. Separate identities reduce insider/compromise blast
  radius; they do not replace protected-variable and protected-branch
  controls from ADR 0067.
- Role registration is administrator-controlled installation state.
  Arbitrary repository or pull-request content cannot create or elevate
  a role.
- All role secrets stay protected and masked. The registry variable is
  protected so only protected-branch pipelines observe a policy change.
- GitLab `Developer` + `api` is still coarse. Do not document these
  tokens as least-privilege API grants.
- `CI_DEBUG_TRACE` remains forbidden on jobs that hold any of these
  variables. YAML `workflow:` and job `rules:` deny it before any admit
  rule so the job never starts; the script-level guard is defense-in-depth.
- When changing credential routing or rotation, follow the
  [credential-routing security checklist](#credential-routing-security-checklist).
