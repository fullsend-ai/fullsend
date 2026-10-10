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
| `FULLSEND_GITLAB_POLLER_GENERATIONS` | unmasked variable | Version 1 Poller identity-generation state (current, pending, and retiring Poller account IDs and the pending generation's handoff phase). Never stores token values. Not yet written by any command; see [Poller identity generations](#poller-identity-generations). |

Canonical constants live in [`internal/forge/forge.go`](../../internal/forge/forge.go)
(`SecretForgeToken`, `SecretGitLabPollerToken`,
`SecretGitLabAnalystToken`, `SecretGitLabCoderToken`,
`VarGitLabRoleRegistry`,
`VarGitLabRoleRotation`, `VarGitLabPollerGenerations`). Custom secret names
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

> **Status: rolling out, not active yet.** This section and
> [Poller-owned webhook trigger token](#poller-owned-webhook-trigger-token)
> record the target contract that
> [#7772](https://github.com/fullsend-ai/fullsend/issues/7772) (originating
> issue [#8083](https://github.com/fullsend-ai/fullsend/issues/8083)) delivers
> as a series of small changes. The type, function, and package names below
> (`internal/gitlablifecycle`, `repos.GitLabManagedAccountCleaner`, and
> others) are introduced by those changes and may not exist yet. Until the
> CLI activation change
> ([#8242](https://github.com/fullsend-ai/fullsend/issues/8242)) merges,
> install provisions role project access tokens as described in
> [Project access token names](#project-access-token-names), and rotation
> state stays in the unversioned format. The webhook fast path stays deferred,
> with polling retained, until the live fresh-identity Poller handoff
> ([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)) merges and is
> validated.

Once activated, a newly provisioned or rotated own-credential role is a
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
  migration command or runtime compatibility gate. Replacement credentials
  are authenticated and checked for identity, `api` scope and effective
  Developer access before publication. Rotation of an existing role mints the
  new credential on a service account. The old project access token is
  revoked after the usual 24-hour grace.

A failed credential-publication response does not prove the write failed.
The forge secret interface cannot read values back, so rotation keeps the
incoming credential active and retains recovery state, invalidating any prior
supplied distribution proof. The next rotation replaces it safely and
preserves the usual grace period before revocation.

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

Provisioning and rotation accept one `ProvidedRoleCredential` per registered
role, bundling the sensitive token with its optional resolved owner and token
IDs. Neither credential values nor remote error text are reported. Ownership
lookup errors intentionally expose only the unresolved-ownership sentinel:
redacted remote causes are flattened so an inner 403/404 cannot authorize
legacy fallback or imply that managed accounts are absent.

Rotation-state writes use schema version 1 once the writer flips in
[#8242](https://github.com/fullsend-ai/fullsend/issues/8242); the tolerant
reader ([#8233](https://github.com/fullsend-ai/fullsend/issues/8233)) lands
first. Version 1 state adds managed service-account IDs and supplied-account
ownership/exclusions to the `FULLSEND_GITLAB_ROLE_ROTATION` document and still
never stores token values. Unversioned state remains readable; unsupported
versions fail closed and are not rewritten. The marker cannot protect against
older CLIs that ignore it; mixed-version lifecycle operations remain
unsupported. If ownership recording fails immediately after account creation,
install requests deletion of that newly created ID on a detached cleanup
context before any membership or PAT is issued. It never deletes an
unverified same-named account; failed deletion is reported for administrator
cleanup.

Legacy project-token creation ownership is tracked by `created_token_ids` in
version 1 role state. Only IDs returned by token creation are recorded there;
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
even when legacy inventory is unavailable. Unavailable or forbidden
inventories retain the obligation; a revocation 404 alone is insufficient.
Generic revocation still reports unknown/unowned tokens as errors.

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
generation state and handoff sequence (#8210). No adapter implements the
`repos.GitLabPollerHandoff` capability yet, so nothing writes
`FULLSEND_GITLAB_POLLER_GENERATIONS` and install still defers new trigger
creation as described below.

The handoff refuses to run unless the caller passes the lease capability
issued by `LockGitLabProjectLease` after a successful non-dry-run remote
acquisition (a dry-run lock never yields one, and it is invalid once released),
and it rejects a generation document that is not exactly one JSON object
(trailing content, duplicate or non-lowercase keys, or inconsistent phase and trigger fields).
It records each step in `FULLSEND_GITLAB_POLLER_GENERATIONS` before acting:

1. Record the account request, create the fresh service account at Developer,
   and record its numeric ID. A lost create response leaves no ID, so the
   generation needs manual reconciliation; fullsend never deletes an account
   it cannot positively identify. A 404 means project service accounts are
   unsupported and polling stays the only path.
2. Verify the account holds no personal access token, record the elevation,
   create the installer-held bootstrap token, and raise the account to
   Maintainer.
3. Record the generation's single trigger-create attempt, then send it. A
   create request cannot be fenced once sent, so it is never retried.
4. Revoke the bootstrap token, verify no personal access token is active,
   demote to Developer, and verify Developer through an independent read.
5. Require a confirmed trigger owned by the new account, re-check the
   project-wide trigger-safety invariants, and prove the trigger starts a
   pipeline on the protected default branch at Developer access. Branch
   protection is never broadened. Only after both checks pass is the
   generation recorded as verified; a failed safety check or probe attempts to
   revoke the trigger and quarantines the generation, so it never reaches the
   verified phase.

A failure after step 3 publishes nothing and quarantines the generation with an
operator-facing reason. A quarantined generation, a lost account ID, a verified
generation whose cutover did not complete, or an unresolved retiring Poller
each block any new generation. The current Poller is never elevated or
modified, and its polling continues throughout. At most one old/new pair
exists: cutover makes the verified account current and the old one retiring,
and no further generation starts until retirement completes.

### Poller-owned webhook trigger token

> **Poller elevation safety:** Re-elevating a Poller identity that ever held a
> distributed runtime credential requires a verified server-side guarantee
> that requests accepted before credential revocation, including asynchronous
> credential and job creation, have finished. The current GitLab adapter
> cannot establish that guarantee, so it defers temporary Maintainer elevation
> and new trigger creation. Polling continues with Developer credentials;
> compliant existing triggers can still be reused. Revocation and empty
> resource inventories alone do not prove that requests have drained.

This lifecycle is part of the rolling-out contract described under
[Project service accounts](#project-service-accounts). The target way to
obtain a Poller-owned trigger is the fresh replacement identity described in
[Poller identity generations](#poller-identity-generations), which never
elevates a Poller that held a distributed credential; the live handoff that
enables it is [#8243](https://github.com/fullsend-ai/fullsend/issues/8243).
Until that handoff merges, no command creates a replacement identity. The
steps below describe the existing-identity elevation lifecycle that the
current code implements behind the quiescence-verifier gate. The current
adapter does not implement that gate, so it defers at step 2 and never
runs steps 3 onward.

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
   credentials; the current live adapter lacks it and defers here.
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
There is no fourth identity and no custom webhook receiver.

## Job → role mapping

| Job | Role |
| --- | --- |
| GitLab poller/controller (`fullsend poll`, `fullsend-poll.yml`) | Poller |
| GitLab webhook dispatcher (`fullsend poll --input-driver gitlab-webhook`, `fullsend-dispatcher.yml`) | Poller |
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
job's disjoint allowlist (poller = `schedule` only, agent = `api` only;
`parent_pipeline` is not admitted) and the job ref is the project's
protected default branch. YAML `workflow:` and job `rules:` also deny
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
`api`-sourced dispatch passed HMAC verification. This is required for
every job:
`select-gitlab-role-token.sh` has no shared-token path left
(it mirrors `gitlabroles.Resolve` on the Go side), so a sibling
analyst/coder secret is a real higher-privilege credential
as well, and an unverified STAGE must not be
allowed to select it there either. A missing `FULLSEND_DISPATCH_SECRET`
now fails the job closed in every gate mode instead of silently
skipping HMAC verification. A
`parent_pipeline`-sourced dispatch (legacy child-pipeline installs;
current installs only ever dispatch via `api`) is already denied at the
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
installation. Prepare legacy upgrades in a maintenance window before delivery;
see [Typed-input dispatch migration](../guides/getting-started/configuring-gitlab.md#typed-input-dispatch-migration).
Managed schedules use no pipeline variables; job rules derive poll mode
from `CI_PIPELINE_SCHEDULE_DESCRIPTION`. Activation removes the obsolete
`FULLSEND_POLL_MODE` schedule override only after the wrapper and compatible
agent/poll templates are confirmed on the default branch, even on already-restricted projects,
and fails if other user-owned schedule variables remain. The typed restriction
does not apply to pinned legacy variable wrappers. GitLab vendor mode and
version pins without matching upstream templates are rejected before writes.
Agent input bridges explicitly disable expansion, and poll-job links are
logged only after creator/HMAC authentication.
The webhook fast-path's `TRIGGER_PAYLOAD` classification
and the `CI_JOB_TOKEN` running-job identity pin are still open,
live-GitLab-only ship gates tracked in ADR 0125 (Caveats). Flip the env
var to `enforced` fleet-wide only after those close. Poll jobs
resolve `FULLSEND_GITLAB_POLLER_TOKEN` once, after the schedule-only pin.
The Go CLI then overrides `GITLAB_TOKEN` / `PUSH_TOKEN` from the
registered role credential; nothing restores `FULLSEND_FORGE_TOKEN`.

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

`repos uninstall` deletes the registry, rotation document, built-in and
custom role secrets, and matching `fullsend-poller` / `fullsend-analyst`
/ `fullsend-coder` / `fullsend-role-*` project access tokens. It does
**not** delete a leftover `FULLSEND_FORGE_TOKEN` secret or revoke a
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
credential) stores the supplied value directly. Its own GitLab token ID
cannot be resolved from the value alone, so it cannot be excluded from
the same-named project access tokens GitLab already lists — recording
all of them for grace revocation risks revoking the just-enrolled
replacement itself. Enrolling a replacement this way therefore does not
schedule any other active same-named PAT for revocation; if one exists,
confirm it is not the replacement and revoke it manually.

**Identity continuity.** GitLab assigns a new bot user per PAT, so the
GitLab user ID changes on replacement. Fullsend preserves the role
name, token name (`fullsend-poller`, `fullsend-role-<name>`), CI
variable, and capability set. Rotation state records the old and new
token IDs (never secret values) for internal use by
`RotateGitLabRoleCredentials`: serialization between runs, crash
recovery, and grace-period revocation tracking during `repos install`.
It is not read or displayed by `repos status`.

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
| [#8083](https://github.com/fullsend-ai/fullsend/issues/8083) | **In progress** under [#7772](https://github.com/fullsend-ai/fullsend/issues/7772). Role service-account provisioning, install reconciliation, rotation/recovery, status/drift, ownership-gated uninstall cleanup, and the Poller-owned trigger lifecycle are delivered as a series of small changes; none is active until CLI activation ([#8242](https://github.com/fullsend-ai/fullsend/issues/8242)) merges. The webhook fast path stays deferred until the live handoff ([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)) merges, and live acceptance validation, including GitLab.com Free and actual webhook/role-job execution, remains required. See [Project service accounts](#project-service-accounts). |
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
