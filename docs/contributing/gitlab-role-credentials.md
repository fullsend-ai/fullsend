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
`repos status`). Ordinary unflagged `repos install` enables `enforced` and
retires the shared token once every registered role is ready. Explicit
`--gitlab-role-cutover --gitlab-role-cutover-drained` remains available as a
fail-closed retry. Both rotation and retirement must follow the
[credential-routing security checklist](#credential-routing-security-checklist).

Built-in and custom roles are the same kind of registry entry. Job
credential selection walks that registry; it does not switch on a
three-role enum.

**Role credentials are the only runtime path.** `fullsend poll` and
`fullsend run` select the registered role credential via
`gitlabroles.Select` / `SelectAgent` and fail closed if that secret is
missing. There is no shared-token fallback. The role-identity gate still
exists as install/converge state (`FULLSEND_GITLAB_ROLE_MIGRATION`); leftover
unset/`disabled` and explicit `rollback` no longer change runtime
authentication. Ordinary `repos install` still converges leftover
shared-token installs. Custom roles remain optional on existing
installations.

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
first-class registry entry: the same `Resolve` path, the same
unconfigured / unregistered / auth-failed distinction, and the same
role-identity gate as the built-ins.

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
dispatch time in every gate mode; this contract defines the check.

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

Role tokens are **masked, protected** project CI/CD variables, same
storage as today's shared bot PAT. The role-identity gate and the registry
document are **protected and unmasked** so status and logs can print
mode and policy without exposing secrets.

| Name | Kind | Purpose |
| --- | --- | --- |
| `FULLSEND_FORGE_TOKEN` | masked secret | Shared bot PAT. Leftover install/uninstall state; runtime no longer authenticates with it. Retired by ordinary `repos install` cutover. |
| `FULLSEND_GITLAB_POLLER_TOKEN` | masked secret | Poller PAT. Provisioned by `repos install`. |
| `FULLSEND_GITLAB_ANALYST_TOKEN` | masked secret | Analyst PAT. Provisioned by `repos install`. |
| `FULLSEND_GITLAB_CODER_TOKEN` | masked secret | Coder PAT. Provisioned by `repos install`. |
| `FULLSEND_GITLAB_ROLE_<NAME>_TOKEN` | masked secret | Custom role PAT when `credential` is `own`. Provisioned when the role is registered. |
| `FULLSEND_GITLAB_ROLE_MIGRATION` | unmasked variable | Role-identity gate. Absent or empty = leftover `disabled`. Operator-settable values are `enforced` and `rollback`. |
| `FULLSEND_GITLAB_ROLE_REGISTRY` | unmasked variable | Administrator registry JSON. Absent or empty = built-ins only. |
| `FULLSEND_GITLAB_ROLE_ROTATION` | unmasked variable | Per-role rotation state (lock, token IDs, expiry dates, phase). Never stores token values. |

Canonical constants live in [`internal/forge/forge.go`](../../internal/forge/forge.go)
(`SecretForgeToken`, `SecretGitLabPollerToken`,
`SecretGitLabAnalystToken`, `SecretGitLabCoderToken`,
`VarGitLabRoleMigration`, `VarGitLabRoleRegistry`,
`VarGitLabRoleRotation`). Custom secret names
are derived by `gitlabroles.CustomSecretName`.

Role secrets and the registry **must not** be added to
`requiredSecretsForForge` while the gate is leftover `disabled` or
`rollback`. Existing shared-token installations would otherwise fail
health checks for secrets they do not have.

### Project access token names

| Role | PAT name | Access | Scopes |
| --- | --- | --- | --- |
| Shared (today) | `fullsend-bot` | Developer (30) | `api` |
| Poller | `fullsend-poller` | Developer (30) | `api` |
| Analyst | `fullsend-analyst` | Developer (30) | `api` |
| Coder | `fullsend-coder` | Developer (30) | `api` |
| Custom `own` | `fullsend-role-<name>` | Developer (30) | `api` |
| Custom `reuse` | (none; uses the target role's PAT) | — | — |

`repos install` creates these tokens on a fresh GitLab install and on an
existing shared-token install. Ordinary unflagged install then cuts over to
`enforced` and retires the shared `fullsend-bot` token when every registered
role is ready. Access level and scopes match the current shared bot; do not
claim finer GitLab permissions than the implementation uses.

## Job → role mapping

| Job | Role |
| --- | --- |
| GitLab poller/controller (`fullsend poll`, `fullsend-poll.yml`) | Poller |
| Agents / harness roles `review`, `triage`, `prioritize`, `retro`, `scribe` | Analyst |
| Agents / harness roles `code`, `fix`, `coder` | Coder |
| Custom agent whose name or harness `role:` is listed on a registered custom role | That custom role |

`Registry.RoleFor` accepts either an agent name or a harness `role:`
value. Built-in aliases and custom agent names share this lookup.

Unmapped jobs (for example `e2e` or an unregistered custom agent) fail
closed (`ErrUnregistered`) rather than guessing an identity.
`ValidateAgent` itself takes no mode and always rejects an unmapped
name; `Select` / `SelectAgent` call it as a pre-check ahead of
`Resolve` in every mode.

## Role-identity gate

`FULLSEND_GITLAB_ROLE_MIGRATION` is install/converge state. Runtime
credential selection no longer switches on it: every valid mode requires
the registered role secret. Values are case-insensitive; unknown values
fail closed (`ErrInvalidMode`) so a typo cannot silently disable the
gate. `--gitlab-role-migration` accepts only `enforced` and `rollback`.
Leftover `disabled` and `migrating` values remain parseable so ordinary
`repos install` can converge them.

| Mode | When | Runtime credential | Missing role secret |
| --- | --- | --- | --- |
| `disabled` (unset) | Leftover shared-token install (not operator-settable) | Registered role secret | Fail (`ErrUnconfigured`) |
| `migrating` | Internal install intermediate before cutover (not operator-settable) | Registered role secret | Fail (`ErrUnconfigured`) |
| `rollback` | Operator-initiated emergency recovery (`--gitlab-role-migration=rollback --gitlab-role-rollback-confirmed`) | Registered role secret | Fail (`ErrUnconfigured`) |
| `enforced` | Desired state after ordinary `repos install` | Registered role secret | Fail (`ErrUnconfigured`) |

The shared token is **not** selected at runtime. Leftover
`FULLSEND_FORGE_TOKEN` remains install/uninstall state until ordinary
`repos install` retires it. An unregistered name is never a reason to
use the shared token. There is no shared-token fallback for an
unconfigured role.

## How a job selects its credential

Call `gitlabroles.Select` (poller) or `gitlabroles.SelectAgent` (agent
jobs). Those helpers load the gate, registry, and presence map, call
`ValidateAgent` in every mode, then `Resolve`:

- `Mode` from `gitlabroles.ModeFrom` (the gate variable)
- `Job` (`PollerJob()` or `AgentJob(name)`)
- `Registry` from `LoadRegistry` (zero value = built-ins only)
- `Present`: a boolean map of whether each secret *name* is non-empty
  (`PresenceFrom`). **Never put token values in this map.**

`Select` and `SelectAgent` never set `FailedSecret` on the `Request` they
build — it stays at its zero value. `FailedSecret` only matters when a
caller constructs a `Request` directly and calls `Resolve` after an
authentication failure. `fullsend poll`'s `wrapGitLabAuthFailure` uses the
`AuthFailed` helper for this instead of re-resolving: on a 401/403, it
wraps the error with `gitlabroles.AuthFailed(role, mode, secret)` rather
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
GitLab CI templates (`fullsend-poll.yml`, `fullsend-agent.yml`) source
`run-poll-job.sh` and `run-agent-job.sh`, which resolve credentials via
`select-gitlab-role-token.sh`. In `run-agent-job.sh`,
the STAGE pipeline variable that would otherwise select the role is not
yet authenticated when the job starts, so the *pre-verification*
bootstrap calls (resource-group PUT, pipeline-metadata GET, bot-identity
`/user` call) always resolve the Poller credential
(`FULLSEND_GITLAB_POLLER_TOKEN`), regardless of stage — this avoids
handing a forged dispatch a higher-privilege token before the
pipeline-source/bot-identity check and HMAC verification pass. The
template only re-resolves the credential for the job's actual stage —
analyst stages use `FULLSEND_GITLAB_ANALYST_TOKEN`, coder stages use
`FULLSEND_GITLAB_CODER_TOKEN` — once `DISPATCH_VERIFIED` is true: the
`api`-sourced dispatch passed HMAC verification. This is required in
every gate mode, including leftover `disabled` and explicit `rollback`:
`select-gitlab-role-token.sh` has no shared-token path left in any
mode (it mirrors `gitlabroles.Resolve` on the Go side), so a sibling
analyst/coder secret is a real higher-privilege credential in
`disabled`/`rollback` too, and an unverified STAGE must not be
allowed to select it there either. A missing `FULLSEND_DISPATCH_SECRET`
now fails the job closed in every gate mode instead of silently
skipping HMAC verification, and a
`parent_pipeline`-sourced dispatch (legacy child-pipeline installs;
current installs only ever dispatch via `api`) has no HMAC to check, so
`DISPATCH_VERIFIED` stays false. The job
now fails closed at that point (`exit 1`) in every gate mode, rather than continuing on the
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
unverified STAGE from reaching a role-specific credential. This closes a
gap where a forged dispatch that spoofed the pipeline source (via an
overridden `CI_API_V4_URL`, the documented residual risk in ADR 0067 and
`run-agent-job.sh`) could otherwise obtain a higher-privilege role
token before any credential separation existed to matter. Poll jobs have
no such pre-verification window and resolve `FULLSEND_GITLAB_POLLER_TOKEN`
once. The Go CLI then overrides `GITLAB_TOKEN` / `PUSH_TOKEN` from the
registered role credential; leftover `disabled`/`rollback` gates do not
restore `FULLSEND_FORGE_TOKEN`.

The `STAGE=fix` review-body pre-fetch is a separate case: it looks up
the prior review note, which is always authored by the Analyst identity
(`STAGE=review` maps to the analyst role) regardless of which role is
running the fix stage. It cannot reuse the fix stage's own
`FULLSEND_JOB_TOKEN` (Coder) or the discarded Poller `BOT_USER_ID` for
that author match — neither identity is the note's author once
analyst/coder resolve to distinct tokens. This lookup only runs once
STAGE has already been authenticated, in every gate mode, since the job
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
| Shared secret absent (leftover Token() of a constructed shared Source) | `ErrSharedUnconfigured` | Legacy sentinel; Resolve never returns it |
| Runtime 401/403 (or equivalent) from a selected credential | `ErrAuthFailed` | Credential is present but unusable |
| Job kind is empty or unrecognized | `ErrUnknownJob` | No identity to select |
| Gate value is not a known mode | `ErrInvalidMode` | Fail closed |
| Registry JSON is malformed or untrusted | `ErrInvalidRegistry` | Fail closed; do not load custom roles |

A registered role whose secret is absent is `ErrUnconfigured` even when
`FULLSEND_FORGE_TOKEN` is present. `ErrUnregistered` and `ErrAuthFailed`
never fall back to the shared token in any mode.

## No silent fallback on authentication failure

If a selected credential fails authentication or authorization, the job
fails. It does **not** retry as another identity, including the shared
bot.

`Resolve` enforces this when `FailedSecret` is set: it returns
`ErrAuthFailed` in every mode and returns a zero `Source`. Callers that
observe an auth failure must either pass that secret name back into
`Resolve` or stop; they must not call `Resolve` again with a different
job or a cleared `FailedSecret` in order to pick a substitute.

## Status, drift, and diagnostics

`gitlabroles.Diagnose(mode, present, registry)` is the observable
report:

- Per-role state: `configured` or `unconfigured` (presence only),
  including custom roles and reuse targets
- `Partial`: some but not all registered role secrets exist
- `Ready`: install/converge-state readiness, **not** a runtime-readiness
  signal — `Resolve`/`Select`/`SelectAgent` always require the
  registered per-role secret in every mode and never fall back to the
  shared token:
  - `disabled` / `rollback`: shared token present
  - `migrating` / `enforced`: every registered role's credential is present
- `Missing`: registered roles whose secrets are absent
- `Diagnostics`: human-readable lines with **names only**

Classification of a missing role secret for converge-readiness
purposes only — `fullsend poll`/`fullsend run` still fail closed on a
missing role secret in every mode, including `disabled`/`rollback`:

- `disabled` / `rollback`: not required for converge readiness (not drift)
- `migrating` / `enforced`: missing/required (drift / fail closed)

A role secret that is present while the gate is `disabled` or
`rollback` is reported as "configured but unused" for converge
readiness; runtime (`fullsend poll`/`run`) already requires that same
secret. That is not an error; leftover secrets after rollback are
expected until uninstall removes them. `repos install
--rotate-gitlab-roles` refreshes those leftover secrets without
changing the gate; it does not remove them.
`repos uninstall` deletes the gate, registry, rotation document,
built-in and custom role secrets, leftover `FULLSEND_FORGE_TOKEN`, and
matching `fullsend-bot` / `fullsend-poller` / `fullsend-analyst` /
`fullsend-coder` / `fullsend-role-*` project access tokens. A token-
revocation failure fails uninstall so the manifest entry remains for
an idempotent retry. Ordinary reinstall after a complete uninstall
does not recreate those retired artifacts until a fresh provision
writes a new role-aware gate.

**Never** put token values in logs, status output, issue comments, or
`Error` strings. Presence booleans and variable names are the only
safe signals.

`repos status` reports per-role diagnostics (names only). Missing role
secrets are not health drift while the gate is leftover `disabled` or
`rollback`; they are drift when the gate is `migrating` or `enforced`.
Converge health checks stay on the shared-token required set until
cutover writes `enforced`, so a partial install cannot fail an otherwise
healthy leftover shared-token install.

## Built-in role readiness (#7501)

`gitlabroles.CheckBuiltinReadiness(present, registry)` is the
verification check for the three built-in roles. It is independent of
the role-identity gate and does **not** enable `enforced` or retire the
shared token.

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
- `Resolve` in `enforced` selects that role's own secret. A missing
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
resolves to its registered credential in enforced mode.

`BuiltinReadiness.Ready` is true only when all built-in roles pass.
`RegisteredReadiness.Ready` separately covers every registered role. Overall
repos status combines both results. Diagnostics carry role names and secret
*names* only, and status appends these lines after the Diagnose report without
changing the gate.

## Verification and cutover

Ordinary unflagged `repos install` calls `repos.CutoverGitLabRoleCredentials`
after provisioning when the live gate is `migrating` or `enforced`. If roles
are not ready, inventory is unavailable, or state changes during verification,
cutover is deferred: the gate stays at the internal `migrating` intermediate,
`FULLSEND_FORGE_TOKEN` is not recreated, and a later unflagged install retries.
Jobs in that intermediate fail closed on a missing role secret. Explicit
`--gitlab-role-cutover --gitlab-role-cutover-drained` is a fail-closed retry
that does not defer.

Cutover checks the built-in capability contract, verifies every registered
role has a provisioned credential and at least one authorized agent mapping,
rejects expired, revoked, or unverified project tokens, and resolves each
mapping in `enforced` mode. It then writes
`FULLSEND_GITLAB_ROLE_MIGRATION=enforced`, deletes the protected
`FULLSEND_FORGE_TOKEN` secret, and revokes every listed active
`fullsend-bot` project access token. If secret deletion fails, the gate is
restored to `migrating` when this operation enabled it. If token revocation
fails after the secret is deleted, enforced mode remains active and the
operation should be retried. A personal or group PAT supplied through
`--gitlab-bot-token` may not be visible to this API and must be revoked
manually. `--dry-run` performs the checks without changing the gate or
secrets. Operators must still drain in-flight shared-token jobs, verify
deployment-specific GitLab permissions and branch rules, and exercise the
role-specific operations in the target environment; those remain deployment
prerequisites that local readiness cannot prove. Emergency rollback uses
`--gitlab-role-migration=rollback --gitlab-role-rollback-confirmed` and is
not part of ordinary converge.

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
present but no matching project access token), and the gate is
`migrating` or `enforced`. `--rotate-gitlab-roles` force-rotates every
own-credential role. `--rotate-gitlab-role=<name>` limits the run to
that role (repeatable). `--rotate-gitlab-roles` on leftover `disabled` or
`rollback` refreshes leftover role secrets without changing the gate.

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
attempts for the same role are serialized (in-process lock plus a
protected rotation-state document) and idempotent within a five-minute
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
a role rotation failed. Leftover `disabled` and explicit `rollback` are
install/uninstall state only — runtime credential selection never
selects or falls back to the shared token in any gate mode. Runtime
401/403 of a selected role credential is still `ErrAuthFailed`.

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
`unconfigured`. `repos status` reports those names and, in `migrating` and `enforced`
modes, treats expired and revoked credentials as drift. Lines carry role
names, secret names, and dates only.

## What this contract does not do

Leave these to the follow-up issues.

| Issue | Work |
| --- | --- |
| [#7498](https://github.com/fullsend-ai/fullsend/issues/7498) | **Implemented.** `repos install` creates/enrolls built-in and custom PATs, stores them as protected masked CI variables, writes the registry, sets the gate, reports partial provisioning, preserves the shared token, and handles reinstall/drift/uninstall without deleting credentials still in use |
| [#7499](https://github.com/fullsend-ai/fullsend/issues/7499) | **Implemented.** `fullsend poll`, `fullsend run`, and `fullsend post-review` select the registered role credential, enforce `ValidateAgent` / `Registration.Has` in role-aware modes, and fail closed on authentication failure without switching identities |
| [#7500](https://github.com/fullsend-ai/fullsend/issues/7500) | **Implemented.** Role-aware rotation, recovery, in-flight overlap, and expiry/revocation diagnostics. See [Rotation and recovery](#rotation-and-recovery) and follow the [credential-routing security checklist](#credential-routing-security-checklist) |
| [#7501](https://github.com/fullsend-ai/fullsend/issues/7501) | **Implemented locally.** Built-in and registered-role readiness is surfaced on `repos status`; guarded `--gitlab-role-cutover` enables `enforced` and retires the shared-token secret with rollback on retirement failure. Live GitLab ACL/operation probes, deployment branch-rule verification, and in-flight drain remain deployment prerequisites. Hold the [credential-routing security checklist](#credential-routing-security-checklist) |
| [#7524](https://github.com/fullsend-ai/fullsend/issues/7524) | **Implemented.** Ordinary unflagged `repos install` detects shared-token installs, provisions role credentials, and cuts over to `enforced` when ready. Partial enrollment fails closed (defers cutover, never recreates `FULLSEND_FORGE_TOKEN`). Drift repair preserves enforced mode. `--gitlab-role-migration=enforced` is accepted. Emergency rollback remains explicit (`--gitlab-role-migration=rollback --gitlab-role-rollback-confirmed`). |
| [#7558](https://github.com/fullsend-ai/fullsend/issues/7558) | **Implemented.** `repos uninstall` removes migration-era GitLab identity state (gate, registry, rotation, built-in and custom role secrets, leftover shared token, and matching project access tokens) and fails closed if token revocation cannot complete. Ordinary install still converges leftover shared-token or partial installs to `enforced` without recreating retired shared-token artifacts. Partial enrollment, retries, drift, revoked credentials, custom roles, and uninstall/reinstall are covered by lifecycle tests. |
| [#7559](https://github.com/fullsend-ai/fullsend/issues/7559) | **Implemented.** Shared-token fallback in `migrating` is removed; `--gitlab-role-migration` accepts only `enforced` and `rollback`. Leftover `disabled`/`migrating` values remain parseable for converge. |
| [#7502](https://github.com/fullsend-ai/fullsend/issues/7502) | ADR 0067 status annotation and operator-facing lifecycle docs |

## Credential-routing security checklist

Hold these four code invariants and the documentation-terminology rule
below when changing `internal/gitlabroles`, GitLab credential handling
in `internal/cli`, or GitLab role cutover
([#7524](https://github.com/fullsend-ai/fullsend/issues/7524)). They are
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
whole process environment via `childScriptEnv`. A leftover
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
`FULLSEND_FORGE_TOKEN` selection path, a leftover `disabled`/`rollback`
shared-token branch, or a local direct-`GITLAB_TOKEN` no-op when a role
secret is missing. Local GitLab runs must set the matching role secret
(`FULLSEND_GITLAB_POLLER_TOKEN`, `FULLSEND_GITLAB_ANALYST_TOKEN`,
`FULLSEND_GITLAB_CODER_TOKEN`, or a registered custom-role secret).
Ordinary `repos install` still retires leftover `FULLSEND_FORGE_TOKEN`
after role checks pass.

- [ ] Runtime `Select` / `SelectAgent` / `Resolve` never return
      `Source.Shared` or `FULLSEND_FORGE_TOKEN`.
- [ ] Missing role secrets fail closed as `ErrUnconfigured` in every
      gate mode, including leftover `disabled` and explicit `rollback`.

### Keep fallback terminology consistent across docs

Two distinct leftover terms share similar wording and are easy to
conflate. Use these terms, and do not mix them:

- **shared-token path** — historical selection of `FULLSEND_FORGE_TOKEN`
  as the runtime credential. Runtime no longer uses this path. The
  secret remains install/uninstall state until ordinary `repos install`
  retires it.
- **local direct-GITLAB_TOKEN fallback** — the retired local-dev
  workflow where `GITLAB_TOKEN` was set with no role secret. `fullsend
  run --forge gitlab` now fails closed unless the matching role secret
  is present.

These two are described independently in four documents:

- this file (`docs/contributing/gitlab-role-credentials.md`)
- [`docs/cli/run.md`](../cli/run.md)
- [`docs/guides/user/running-agents-locally.md`](../guides/user/running-agents-locally.md)
- [`docs/problems/security-threat-model.md`](../problems/security-threat-model.md)

- [ ] Any change that touches fallback or role-identity gate behavior
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
- All role secrets stay protected and masked. The gate and registry
  variables are protected so only protected-branch pipelines observe a
  mode or policy change.
- GitLab `Developer` + `api` is still coarse. Do not document these
  tokens as least-privilege API grants.
- `CI_DEBUG_TRACE` remains forbidden on jobs that hold any of these
  variables.
- When changing credential routing, rotation, or cutover, follow the
  [credential-routing security checklist](#credential-routing-security-checklist).
