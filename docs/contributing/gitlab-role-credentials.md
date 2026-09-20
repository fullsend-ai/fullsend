---
title: GitLab Role-Credential Contract
---

# GitLab Role-Credential Contract

This is the internal contract for GitLab Poller, Analyst, and Coder
credentials. It is the implementation of [#7497](https://github.com/fullsend-ai/fullsend/issues/7497)
under the three-role decision in [#7424](https://github.com/fullsend-ai/fullsend/issues/7424)
and parent [#7496](https://github.com/fullsend-ai/fullsend/issues/7496).

The Go package is [`internal/gitlabroles`](../../internal/gitlabroles/).
Provisioning, routing, rotation, and shared-token retirement are
follow-up issues; this document is the contract those issues implement
against.

**Current runtime is unchanged.** When the migration gate is unset or
`disabled`, jobs continue to authenticate with the shared
`FULLSEND_FORGE_TOKEN` project access token described in
[ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md).

## Roles

These are responsibility identities, not one identity per agent name.
GitLab project-token scopes cannot express endpoint-level least
privilege; separate credentials give distinct audit identities, keep
Analyst eligible for native MR approval when Coder committed, and limit
the blast radius of a single compromise.

| Role | Responsibility | Must not |
| --- | --- | --- |
| **Poller** | Event/issue reads, pipeline dispatch, poll-state writes on `fullsend-poll-state-slash` and `fullsend-poll-state-events` | Modify application code or act as the Analyst approval identity |
| **Analyst** | Review, triage, prioritization, retrospectives, issue/reporting, notes, labels | Modify repository code or poll-state branches |
| **Coder** | Repository writes, code/fix work, merge-request creation and updates | Be used as the Analyst approval identity |

Stable Go names: `poller`, `analyst`, `coder`
(`gitlabroles.RolePoller` / `RoleAnalyst` / `RoleCoder`).

## Identifiers

### CI/CD variables

Role tokens are **masked, protected** project CI/CD variables, same
storage as today's shared bot PAT. The migration gate is **protected
and unmasked** so status and logs can print the mode without exposing
secrets.

| Name | Kind | Purpose |
| --- | --- | --- |
| `FULLSEND_FORGE_TOKEN` | masked secret | Shared bot PAT. Unchanged default path. |
| `FULLSEND_GITLAB_POLLER_TOKEN` | masked secret | Poller PAT. Optional until provisioning. |
| `FULLSEND_GITLAB_ANALYST_TOKEN` | masked secret | Analyst PAT. Optional until provisioning. |
| `FULLSEND_GITLAB_CODER_TOKEN` | masked secret | Coder PAT. Optional until provisioning. |
| `FULLSEND_GITLAB_ROLE_MIGRATION` | unmasked variable | Feature gate. Absent or empty = `disabled`. |

Canonical constants live in [`internal/forge/forge.go`](../../internal/forge/forge.go)
(`SecretForgeToken`, `SecretGitLabPollerToken`,
`SecretGitLabAnalystToken`, `SecretGitLabCoderToken`,
`VarGitLabRoleMigration`).

Role secrets **must not** be added to `requiredSecretsForForge` while
the gate is disabled. Existing installations would otherwise fail
health checks for secrets they do not have.

### Project access token names

| Role | PAT name | Access | Scopes |
| --- | --- | --- | --- |
| Shared (today) | `fullsend-bot` | Developer (30) | `api` |
| Poller | `fullsend-poller` | Developer (30) | `api` |
| Analyst | `fullsend-analyst` | Developer (30) | `api` |
| Coder | `fullsend-coder` | Developer (30) | `api` |

Provisioning (#7498) creates these tokens. This contract only names
them. Access level and scopes match the current shared bot; do not
claim finer GitLab permissions than the implementation uses.

## Job → role mapping

| Job | Role |
| --- | --- |
| GitLab poller/controller (`fullsend poll`, `fullsend-poll.yml`) | Poller |
| Agents / harness roles `review`, `triage`, `prioritize`, `retro`, `scribe` | Analyst |
| Agents / harness roles `code`, `fix`, `coder` | Coder |

`gitlabroles.RoleFor` accepts either an agent name or a harness `role:`
value. Unmapped jobs (for example `e2e` or a custom agent) keep working
on the shared token when the gate is `disabled` or `rollback`. In
`migrating` and `enforced` they fail closed (`ErrUnknownJob`) rather
than guessing an identity.

## Migration gate

`FULLSEND_GITLAB_ROLE_MIGRATION` is the only switch that changes
credential selection. Values are case-insensitive; unknown values fail
closed (`ErrInvalidMode`) so a typo cannot silently disable the gate.

| Mode | When | Shared token used | Missing role secret |
| --- | --- | --- | --- |
| `disabled` (default, unset) | Existing installations | Always | Ignored |
| `migrating` | Additive rollout after #7498 | Only as **explicit** fallback when that role is unconfigured | Use shared token; report pending |
| `rollback` | Operator-initiated rollback | Always | Ignored (role secrets unused) |
| `enforced` | After verification (#7501) | Never | Fail (`ErrUnconfigured`) |

The shared token is **not** selected after an arbitrary
role-credential failure. The only legitimate shared-token uses are:

1. `disabled` (legacy path)
2. `rollback` (explicit operator action)
3. `migrating` **and** the role secret is absent/empty (not yet provisioned)

## How a job selects its credential

Call `gitlabroles.Resolve` with:

- `Mode` from `gitlabroles.ModeFrom` (the gate variable)
- `Job` (`PollerJob()` or `AgentJob(name)`)
- `Present`: a boolean map of whether each secret *name* is non-empty
  (`PresenceFrom`). **Never put token values in this map.**
- `FailedSecret`: the secret *name* that already failed authentication
  in this job, or empty

The result is a `Source` whose `SecretName` is the CI/CD variable to
read. Callers then `os.Getenv(src.SecretName)`.

Routing (#7499) is what wires `Resolve` into `fullsend poll`,
`fullsend run`, and GitLab CI templates. Until then, those paths keep
reading `FULLSEND_FORGE_TOKEN` directly.

## Unconfigured vs failed

These are different errors. Do not collapse them.

| Situation | Sentinel | Meaning |
| --- | --- | --- |
| Role secret absent or empty | `ErrUnconfigured` | Not provisioned yet |
| Shared secret absent in `disabled`/`rollback` | `ErrSharedUnconfigured` | Legacy path broken |
| Runtime 401/403 (or equivalent) from a selected credential | `ErrAuthFailed` | Credential is present but unusable |
| Job name has no mapping in a role-aware mode | `ErrUnknownJob` | No identity to select |
| Gate value is not a known mode | `ErrInvalidMode` | Fail closed |

`ErrUnconfigured` in `migrating` may still resolve to the shared token
(explicit fallback). `ErrAuthFailed` must not.

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

`gitlabroles.Diagnose(mode, present)` is the observable report:

- Per-role state: `configured` or `unconfigured` (presence only)
- `Partial`: some but not all of the three role secrets exist
- `Ready`:
  - `disabled` / `rollback`: shared token present
  - `migrating` / `enforced`: all three role secrets present
- `Missing`: roles whose secrets are absent
- `Diagnostics`: human-readable lines with **names only**

Classification of a missing role secret:

- `disabled` / `rollback`: not required (not drift)
- `migrating`: pending (informational; expected during rollout)
- `enforced`: missing/required (drift / fail closed)

A role secret that is present while the gate is `disabled` or
`rollback` is reported as "configured but unused". That is not an
error; leftover secrets after rollback are expected until uninstall or
rotation (#7500) removes them.

**Never** put token values in logs, status output, issue comments, or
`Error` strings. Presence booleans and variable names are the only
safe signals.

`repos status` / converge health checks stay on the shared-token
required set until provisioning (#7498) starts writing role secrets
under an enabled gate.

## What this contract does not do

Leave these to the follow-up issues. Do not implement them under #7497.

| Issue | Work |
| --- | --- |
| [#7498](https://github.com/fullsend-ai/fullsend/issues/7498) | Create/enroll the three PATs, store them as protected masked CI variables, set the gate, report partial provisioning, preserve the shared token, reinstall/drift/uninstall |
| [#7499](https://github.com/fullsend-ai/fullsend/issues/7499) | Wire `Resolve` into poll, agent, and forge operations so each job uses only its role |
| [#7500](https://github.com/fullsend-ai/fullsend/issues/7500) | Rotation, recovery, in-flight jobs, expiry/revocation diagnostics |
| [#7501](https://github.com/fullsend-ai/fullsend/issues/7501) | Verification, enable `enforced`, retire the shared token |
| [#7502](https://github.com/fullsend-ai/fullsend/issues/7502) | ADR 0067 status annotation and operator-facing lifecycle docs |

## Security notes

- Threat priority remains external injection > insider > drift >
  supply chain. Separate identities reduce insider/compromise blast
  radius; they do not replace protected-variable and protected-branch
  controls from ADR 0067.
- All role secrets stay protected and masked. The gate variable is
  protected so only protected-branch pipelines observe a mode change.
- GitLab `Developer` + `api` is still coarse. Do not document these
  tokens as least-privilege API grants.
- `CI_DEBUG_TRACE` remains forbidden on jobs that hold any of these
  variables.
