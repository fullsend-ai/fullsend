---
sidebar_position: 4
---

# Configuring GitLab For Fullsend

The goal of this document is that you configure Fullsend for your GitLab
repository. GitLab installations are per-repo only — there is no
organization-wide GitLab install path.

GitHub repositories use a different command (`fullsend github setup`). See
[Configuring GitHub](configuring-github.md) for that flow.

## Prerequisites

* For Vertex agents, a GCP project with Vertex AI enabled, from
  [Getting Inference](getting-inference.md). OpenAI-only installation does not
  need a GCP project. GitLab does **not** use
  `fullsend inference provision` — inference credentials are written by
  `repos install` from `--vertex-project` or a declared `inference.project`
  (see [Inference Setup](#inference-setup) below). Unless you also supply a
  WIF provider (`--vertex-wif-provider` or a declared
  `inference.wif_provider`; a declared provider also requires a declared
  `inference.project`), `repos install` derives the
  project number from the project via the GCP Resource
  Manager API, so the machine running `repos install` needs
  Application Default Credentials with `cloudresourcemanager.googleapis.com`
  `projects.get` access on that project.
* Download the latest [fullsend](https://github.com/fullsend-ai/fullsend/releases) CLI.
* A GitLab personal or group access token with `api` scope and
  Maintainer (or Owner) role on the target project. The CLI does **not**
  fall back to `glab auth token`. Set `GITLAB_TOKEN` in the environment
  before running setup, or pass `--gitlab-token` on each `fullsend repos`
  command.
* A GitLab runner that can execute agent jobs. Shared GitLab.com runners
  may be insufficient for sustained polling because polling consumes CI
  minutes. See
  [Runner Configuration](#runner-configuration).
* The project's `ci_pipeline_variables_minimum_override_role` must already
  be `no_one_allowed` before typed pipeline-input dispatch can activate —
  install/converge refuse runnable template delivery on a weaker, unset,
  or unverifiable setting, even with automatic enforcement requested.
  Legacy variable-based installations are no longer supported; see
  [Typed-input dispatch migration](#typed-input-dispatch-migration). See
  [GitLab Role-Credential
  Contract](../../contributing/gitlab-role-credentials.md#how-a-job-selects-its-credential)
  for the remaining live-validation rollout gates for this transport.
* If the project has a push rule with a branch name regex (**Settings >
  Repository > Push rules > Branch name**), it must also match
  `fullsend-poll-state-slash` and `fullsend-poll-state-events`. Install
  creates those branches, so `repos install` (including `--dry-run`) reads
  the push rule and stops before any write when the regex rejects a branch
  that does not exist yet. The check is skipped when the push rule cannot
  be read, for example on editions without push rules.

## Typed-input dispatch migration

`ci_pipeline_variables_minimum_override_role` is a GitLab project setting,
not a `.fullsend` configuration field. Before installing typed-input jobs,
an administrator must set it to `no_one_allowed`; Fullsend reads it back
and refuses delivery if it is weaker, unsupported, or unreadable. Setting
`FULLSEND_GITLAB_PIPELINE_VAR_RESTRICTION=enforced` does not bypass this
pre-delivery check. A failed setting change must leave the new jobs undelivered,
not runnable under an unsafe policy.

The former variable-based dispatch path is no longer supported, and Fullsend
no longer migrates installations off it automatically. Install and converge
refuse to deliver a legacy variable-based wrapper (for example, from a
`fullsend_ref` pinned before v0.46.0): pin v0.46.0 or later, or remove the
pin. Typed activation refuses while any managed schedule still sets a
pipeline variable. That includes an obsolete `FULLSEND_POLL_MODE` left from a
legacy installation. Delete the variable, or delete the schedule so
`repos install` recreates it, then rerun `repos install`. Fullsend never
deletes schedule variables itself.

Root, wrapper, agent, and poll templates are delivered together using the
effective target version. Activation waits until the compatible wrapper
**and** agent/poll templates are on the default branch, including when a
repair MR is still open. Inspect an asynchronous installation/repair MR
before merging, keep the restriction in place, and rerun `repos install`
after it merges to complete activation. Typed jobs derive slash/event mode
from the schedule description, so managed schedules are variable-free.

The poller dispatches only with pipeline inputs. It refuses to dispatch to a
repository whose wrapper, at the dispatch ref, does not declare the complete
typed contract, and never falls back to pipeline variables. An older poller
that still sends variables cannot dispatch once `no_one_allowed` is set:
GitLab rejects the variables. Upgrade the poller.

The transport declares eleven metadata inputs and nine base64 chunks, at
most 1000 characters each. All three template layers validate the chunk
alphabet and length; fixed code reconstructs the payload as data. Variable
bridges disable expansion, and creator/HMAC authentication precedes caller
link logging and credential selection. Non-command note bodies, such as bot
review notes, are bounded to 800 Unicode characters with a truncation marker.
A `/fs-*` command body is carried untruncated, and the dispatcher fails closed
before a pipeline is created when a command payload exceeds the nine-chunk
budget. Other oversized payloads also fail before a pipeline is created. GitLab's 20-input limit leaves no spare root
inputs: conflicting declarations or excess inputs fail explicitly.

Compatible user input declarations, jobs, includes, and header settings
survive merge/uninstall. Declarations referenced directly by surviving user
configuration, and the `STAGE` bridge/input used indirectly through `$STAGE`,
are retained. Different version pins need matching upstream templates; a
recorded ref matching the running release uses embedded templates even
without GitHub credentials, including status checks. GitLab vendor mode
and typed pins predating the secured contract are unsupported.

> **GitLab tier:** Project access tokens require GitLab Premium or
> Ultimate **on gitlab.com**; self-managed Community Edition can create
> them without a paid tier. `repos install` also creates two pipeline
> schedules with sub-hourly cron intervals (`*/5 * * * *` and
> `2,17,32,47 * * * *`). GitLab.com Free allows schedules, but limits each
> schedule to 24 pipeline triggers per day, so the five-minute schedules
> are effectively throttled to about once per hour; this is separate from
> the project-access-token restriction. Treat manual role-token enrollment and
> off-system polling below as fallbacks for when PAT provisioning or the
> resulting schedule behavior is unsuitable on your instance, not as the
> default path for every Free/CE install. When personal tokens are needed,
> enroll each role with a token that has `api` scope, and run
> `fullsend poll` on an external scheduler (a VM cron job or Kubernetes
> CronJob) instead of relying on in-CI pipeline schedules — see
> [Off-system polling](#off-system-polling) below. A runner with
> sufficient CI capacity is still required for the scheduled jobs
> themselves.
>
> This gap does not stay quiet: only the *first* `repos install` merely
> warns when schedule creation fails and still exits successfully. Any
> later `repos install` run on an already-installed repo takes the
> converge path instead, which retries creating the missing schedules
> and treats a repeat failure as a hard `convergence errors` failure
> instead of a warning. There is currently no flag to make `repos
> install` skip repairing schedules, so on a repo where
> schedules were never created, expect any such re-run to fail even when
> it's otherwise applying an unrelated change. Set manifest options like
> `gitlab.agent_runner_tags` (see [Runner Configuration](#runner-configuration)
> below) with `repos set` *before* your first install so they're
> captured by that first, non-converge run instead of requiring a
> converge-path re-run later. Rely on [Off-system
> polling](#off-system-polling) for pickup on repos where schedule
> creation already failed and a converge re-run to fix something else
> isn't an option.

## Installing Fullsend

`repos install` reads every persistent setting, and the list of projects
it manages, from a `repos.yaml` manifest. Declare the project first, then
install it:

1. Declare the GitLab instance, the inference method, and the project:

   ```bash
   fullsend repos set gitlab.url https://gitlab.com
   fullsend repos set gitlab.inference.auth vertex-wif
   fullsend repos set 'gitlab.repos["<group/project>"]' '{}'
   ```

   The first command creates `repos.yaml` if it does not exist.

2. Preview, then install:

   ```bash
   fullsend repos install <group/project> --dry-run --vertex-project "<gcp-project>"
   fullsend repos install <group/project> --vertex-project "<gcp-project>"
   ```

Where `<group/project>` is the GitLab project path (nested groups are
supported, for example `group/subgroup/project`), and `<gcp-project>` is
the GCP project from [Getting Inference](getting-inference.md). Instead of
passing `--vertex-project` on every run, you can declare it once with
`fullsend repos set gitlab.config.inference.project <gcp-project>`; the
value is then committed to `repos.yaml` and the project's
`.fullsend/config.yaml`.

The positional argument only selects a project already declared in
`repos.yaml`; install fails before any write when it matches no entry.
`repos install` has no `--forge`, `--gitlab-url`, or `--inference-auth`
flag. See
[Migrating to manifest-driven `repos install`](migrating-from-github-setup.md)
if your scripts used them.

`inference.auth` selects the inference authentication method
(`vertex-wif` or `openai-api-key`; `openai-wif` is GitHub only and is
rejected for GitLab projects). It can be set under `defaults`, in the
`gitlab` section, or on the project's entry. There is no default: when the
manifest does not provide a selection, `repos install` fails before
changing the project. See
[Repository management § Inference authentication](repo-management.md#inference-authentication).

For an installation without Vertex credentials, run
`fullsend repos set gitlab.inference.auth openai-api-key`, then pass
`--openai-api-key <key>` to `repos install` and omit
`--vertex-project`. The key is written as the masked
`FULLSEND_OPENAI_API_KEY` CI/CD variable, and no GCP inference secrets are
written or looked up. An unprefixed `OPENAI_API_KEY` CI/CD variable is no
longer used; see the
[upgrade steps](../../cli/repos.md#gitlab-fullsend_openai_api_key-replaces-openai_api_key-breaking).
Configure each enabled agent's runtime and model for OpenAI before it runs.
GitLab OpenAI runs use an API key, not GitHub Actions WIF; see
[OpenAI Workload Identity](../infrastructure/openai-workload-identity.md).

`gitlab.url` is required in every case, including gitlab.com: `repos.yaml`
fails validation (`gitlab.url is required when GitLab repos are present`)
whenever it's omitted, and nothing auto-populates it. Use
`https://gitlab.com` for gitlab.com, or your instance URL for a self-hosted
install (`fullsend repos set gitlab.url https://gitlab.example.com`). Set it
before declaring the first GitLab project; `repos set` rejects a GitLab
entry without it.

Install then converges the project:

* Scaffolds `.gitlab/ci/fullsend-*.yml` and merges an include, stages, and
  workflow rules into `.gitlab-ci.yml` without overwriting unrelated CI.
  Dispatch uses typed pipeline inputs, which requires
  `ci_pipeline_variables_minimum_override_role=no_one_allowed` (see
  [Prerequisites](#prerequisites) above) — installation fails closed when
  the project doesn't already have it set.
* Provisions the built-in and registered custom role credentials as protected
  CI/CD variables. Runtime jobs select the registered role credential
  unconditionally; there is no legacy shared-token fallback.
  `repos status` reports `protected-ref-pipeline` drift if that pipeline
  permission is later removed. Neither install nor uninstall cleans up a
  leftover shared token from a repository installed before the role-only
  rollout — that requires manual cleanup.
* Creates two pipeline schedules: `fullsend slash poll` (every 5 minutes)
  and `fullsend event poll` (at minutes 2, 17, 32, 47). Re-running install
  reports either schedule as drift if it exists but has been disabled,
  without changing it; pass `--reactivate-schedules` to have install
  re-enable it. Leave that flag unset on repos using [Off-system
  polling](#off-system-polling), where the schedules are disabled on
  purpose.
* Provisions the webhook fast path:
  a pipeline trigger token stored as the protected, masked
  `FULLSEND_TRIGGER_TOKEN` variable, a generated `FULLSEND_WEBHOOK_SECRET`,
  and a project webhook (issues, merge requests, comments) that triggers a
  pipeline on the protected default branch. Opening an issue dispatches
  triage through the webhook fast path; events-mode cron polling recovers
  the issue open if webhook delivery is missed. The agent job runs triage
  only when the issue author has Developer access or higher; for a
  Reporter, Guest, or non-member author it exits successfully without
  running triage. Existing credential variables
  are reused only when masked, protected, and wildcard-scoped; otherwise they
  are regenerated. Only webhooks Fullsend owns are updated or deleted. This
  step is deferred until the dispatcher and its scripts are on the default
  branch, the default branch is protected, and
  `no_one_allowed` is verified, so on a merge-request install re-run
  `repos install` after the scaffold merges. A project, group, or
  instance-level CI/CD variable that redefines a predefined variable such
  as `CI_PIPELINE_SOURCE` or `CI_COMMIT_REF_PROTECTED` also defers it.
  Instance-level variables can only be listed with administrator access
  and group variables with group Owner access. Every scope your
  credential can read is checked; a scope it cannot read is skipped as
  unverified and neither defers the fast path nor revokes an existing
  one, so a project Maintainer can enable it without administrator or
  group Owner access. Fullsend does not verify those scopes; see
  [Inherited CI/CD variable trust](#inherited-cicd-variable-trust)
  for the security assumption this relies on. Re-runs are no-ops and repair a
  missing or drifted webhook or token; `--rotate-gitlab-trigger-token`
  rotates the token and revokes the old one after the webhook is updated.
  The polling schedules stay in place as the backstop. Token and secret
  values are never printed. Once install records the trigger-account
  dispatch activation, the webhook uses the trigger-only account's trigger
  token instead (see [Trigger-only service account](#trigger-only-service-account)).
  The install-time Maintainer access is used only to create and revoke
  these resources; it is not a runtime credential. A trigger token runs
  pipelines as its owner, so install verifies the owner's effective
  project role when it mints a token and whenever it reuses one, and
  rejects an owner with Maintainer or Owner access (or one it cannot look
  up): it revokes the token, removes the managed webhook, and leaves the
  fast path disabled. A rejected owner is reported as a deferral rather
  than an installation failure once cleanup succeeded; owner-lookup and
  cleanup failures are still reported as errors. For the Poller fallback
  route, a Developer-owned trigger token also needs push or merge access to
  the protected default branch; an owner without it defers the fast path
  with remediation guidance. The trigger-only account instead uses its
  default-branch deploy-key grant for repository access. The
  webhook is enabled only after the committed wrapper, root, and
  agent/poll templates carry the typed pipeline-input contract, and the
  default branch must stay protected. Because GitLab ties a trigger
  token to the Maintainer who creates it, the fast path stays disabled
  when the only available creator is a Maintainer or Owner; the polling
  schedules keep working, and the poller, dispatcher, and agent
  credentials stay Developer-level. Where the project is eligible, the
  Poller fallback trigger is owned by a fresh Poller service account that is
  verified at Developer before its credential is published. When dispatch
  activation is recorded, the webhook instead uses the trigger-only account's
  published trigger; see
  [Trigger-only service account](#trigger-only-service-account).
* Writes inference CI/CD variables when a GCP project is supplied, by
  `--vertex-project` or a declared `inference.project` (see
  [Inference Setup](#inference-setup)).

By default the scaffold lands as a merge request. Pass `--direct` to push
to the default branch instead. Preview with `--dry-run`.

For the full list of install flags, see the
[CLI reference](../../cli/repos.md#repos-install).

### Least-privilege dispatch contract

The webhook fast path and the polling backstop start agents without raising
any runtime credential above Developer and without accepting caller-supplied
pipeline variables:

* **Developer-level API pipeline creation with typed inputs.** The poller and
  the webhook dispatcher start agent pipelines with a Developer-level
  credential: through the API with the Poller credential, or with the
  trigger-only account's `CI_JOB_TOKEN` once its dispatch activation is
  recorded. Dispatch fields travel as declared typed pipeline inputs,
  never as pipeline variables.
* **User-defined pipeline variables are rejected.**
  `ci_pipeline_variables_minimum_override_role=no_one_allowed` makes GitLab
  reject pipeline variables from every caller, including a trigger token, so
  no caller can override values such as `CI_JOB_TOKEN`, `CI_API_V4_URL`, or
  `FULLSEND_DISPATCH_SECRET`. Install verifies the setting before it delivers
  runnable typed jobs or provisions the webhook.
* **Native `TRIGGER_PAYLOAD` inside the job.** The fast path needs no
  pipeline variables. GitLab's native webhook trigger exposes the webhook
  body to the dispatcher job as the file-type `TRIGGER_PAYLOAD` variable,
  which keeps working under `no_one_allowed`. The dispatcher treats its
  contents as untrusted hints and re-fetches every referenced resource from
  the GitLab API before it dispatches.
* **Authenticated opaque dispatch data for larger payloads.** The event
  payload is too large for one input, so it travels as base64 chunks
  (`event_payload_chunk_00` through `event_payload_chunk_08`; see
  [Typed-input dispatch migration](#typed-input-dispatch-migration)). The
  agent job reassembles them as data, never as shell, and verifies its
  creator and the dispatch HMAC, computed over the whole payload, before it
  selects credentials or acts on the payload.

### Inherited CI/CD variable trust

> **Security assumption:** the webhook fast path and the trigger-account
> schedule ownership both trust GitLab instance administrators and group
> Owners not to define CI/CD variables that override dispatch values such as
> `CI_PIPELINE_SOURCE`, `CI_API_V4_URL`, `CI_PROJECT_DIR`, `CI_JOB_TOKEN`,
> `CI_PIPELINE_SCHEDULE_DESCRIPTION`,
> `FULLSEND_POLL_MODE`, or the agent input bridge's `STAGE`. It also trusts
> them not to override the execution-controlling variables the scheduled poll
> job's bootstrap runs under, such as `PATH`, `LD_PRELOAD`, `BASH_ENV`,
> `GIT_TEMPLATE_DIR`, `GIT_STRATEGY`, `GIT_CHECKOUT`, `GOROOT`, `GOFLAGS`,
> `GOCACHEPROG`, or `PYTHONPATH`. An inherited
> override of one of those runs code of the overrider's choosing in the poll
> job's `before_script`, **before** the job verifies its creator or HMAC, and
> with the trigger account's credentials and `CI_JOB_TOKEN` present. Fullsend
> checks only the scopes the installing credential can read. It does not
> verify the others and cannot prove that no override exists there.

GitLab gives instance and group CI/CD variables
[precedence](https://docs.gitlab.com/ci/variables/#ci-cd-variable-precedence)
over predefined and job-level values. Fullsend decides statically which committed jobs
can run in a trigger pipeline. An inherited override of a rule variable can
make that check wrongly conclude that a user-owned job is excluded. GitLab
would then run that job in a protected trigger pipeline,
where it receives the protected, wildcard-scoped project credentials and
its GitLab job token before the dispatcher's runtime checks run. The same
trust assumption covers schedule identity, poll mode, and agent input bridge
overrides: these can select the wrong poll job or stop child dispatch.
The same
applies to an override added **after** installation: later `repos install`
and `repos status` runs cannot inspect that scope either.

Without administrator access, `repos install` cannot list instance-level
variables. Without group Owner access, it cannot list the variables of an
ancestor group. A forbidden response means the scope is unverified, not
unsafe: the fast path is activated or retained, and the polling schedules
move to the trigger account, when the remaining checks pass. When the
schedules move, `repos install` lists the inherited scopes it could not read
(`instance` or a group path) so you can confirm with the scope's owner that no
reserved variable is defined there. These checks still apply:

* A collision found in any scope the credential **can** read defers the
  fast path and revokes existing managed credentials. So does a
  collision in a project variable.
* Errors other than forbidden from those listings fail the step instead
  of being treated as unverified.
* The committed-CI job and include exclusion checks, protected-ref
  checks, the `no_one_allowed` pipeline-variable restriction, the
  debug-trace gate, dispatcher job identity verification, and the
  polling fallback all still apply.

Environment-scoped variables do not mitigate this risk. A job can
declare the matching environment, and Fullsend stores and reads these
credentials at the wildcard scope.

### Enabling a subset of agents

By default, install configures
`triage,coder,review,fix,retro,prioritize`. To enable only specific
agents, declare `config.roles` for the project (or in `gitlab.config` for
every GitLab project), then install:

```bash
fullsend repos set 'gitlab.repos["<group/project>"].config.roles' '[triage, review]'
fullsend repos install <group/project> --vertex-project "<gcp-project>"
```

### Choosing a runtime

`repos install` records the agent runtime in `.fullsend/config.yaml`.
Declare it in a `repos.yaml` config section — `defaults.config`,
`gitlab.config`, or the project entry's `config` — then run
`fullsend repos install` (`claude` is the stable default; `pi` and
`codex` are experimental). There is no `--runtime` install flag:

```yaml
gitlab:
  url: https://gitlab.com
  config:
    runtime: claude
  repos:
    - name: <group/project>
```

See [Choose a Runtime](choosing-a-runtime.md) for what the runtimes are
and how to change the selection after setup.

### Free-tier role-token enrollment

On instances that cannot create project access tokens, enroll each required
role with a personal access token:

```bash
fullsend repos install <group/project> \
  --vertex-project "<gcp-project>" \
  --gitlab-role-token "poller=<poller-pat>" \
  --gitlab-role-token "analyst=<analyst-pat>" \
  --gitlab-role-token "coder=<coder-pat>"
```

### Role identities and GitLab Free

Fresh installs create separate Developer-level project service accounts for
the Poller, Analyst, and Coder roles, falling back to project access tokens
where service accounts are unavailable. These are genuinely different GitLab identities, so GitLab can audit
which responsibility acted and the Analyst can approve a merge request
created by the Coder. The credential variables are described in
the [role-credential contract](../../contributing/gitlab-role-credentials.md).
Per-role project service accounts are the primary path; see
[Project service accounts](#project-service-accounts).

This automatic per-role provisioning is **not available on GitLab.com Free**
because that tier cannot create project access tokens. You can still enroll
role credentials manually with personal access tokens:

```bash
fullsend repos install <group/project> \
  --gitlab-role-token "poller=$POLLER_PAT" \
  --gitlab-role-token "analyst=$ANALYST_PAT" \
  --gitlab-role-token "coder=$CODER_PAT"
```

There are two practical Free-tier arrangements:

1. Use three PATs belonging to one dedicated GitLab user. This supplies the
   three role variables, but all three tokens authenticate as the same GitLab
   identity. In particular, when Coder creates a merge request, Analyst is
   still the same user and GitLab will not accept that identity's approval of
   its own merge request. Separate PATs do not solve same-user approval rules.
2. Create three dedicated GitLab users and issue one PAT per user, assigning
   each user the required access to the project. Use those PATs for Poller,
   Analyst, and Coder respectively when distinct audit and approval identities
   are required.

Each supplied token must satisfy the same policy as a credential Fullsend
mints, or enrollment rejects it before anything is stored: it must be active,
carry the role's token name (`fullsend-poller`, `fullsend-analyst` or
`fullsend-coder`) and exactly the `api` scope, and expire no later than one
year from today. Its owning user must also hold exactly Developer access on the
project, including inherited group membership, so a Maintainer's or Owner's
token is rejected. Create each token with that name, scope and expiry. See the
[`repos install` reference](../../cli/repos.md) for details.

On GitLab.com Free, treat these as administrator-managed personal
credentials: their project/group reach follows the owning users, and rotation
or revocation is manual. Never use a personal administrator PAT
for an autonomous role. If you use the one-user arrangement, document the
approval limitation explicitly and do not assume the role names imply
different GitLab identities.

### Role identity model and credential lifecycle

GitLab runtime authentication uses three built-in responsibility identities,
decided in [#7424](https://github.com/fullsend-ai/fullsend/issues/7424) and
implemented under [#7496](https://github.com/fullsend-ai/fullsend/issues/7496):

| Role | Responsibility | Must not |
| --- | --- | --- |
| **Poller** | Event/issue reads, pipeline dispatch, poll-state writes on `fullsend-poll-state-slash` and `fullsend-poll-state-events` | Modify application code or act as the Analyst approval identity |
| **Analyst** | Review, triage, prioritization, retrospectives, issue/reporting, notes, labels | Modify repository code or poll-state branches |
| **Coder** | Repository writes, code/fix work, merge-request creation and updates | Be used as the Analyst approval identity |

These are **audit identities**, not GitLab ACL grants. Every role token is
still Developer (30) with the `api` scope. Registering a custom role does
not create finer GitLab API permissions. When Analyst and Coder are distinct
GitLab users, Analyst can natively approve a Coder-authored merge request;
see [Role identities and GitLab Free](#role-identities-and-gitlab-free) for
the same-user exception. The full contract is in the
[role-credential contract](../../contributing/gitlab-role-credentials.md).

#### Custom roles

Administrators may register extra roles with `--gitlab-role-registry` (JSON
references and policy, never secret values). Repository files, harness
`role:` fields, and merge-request diffs may *reference* a registered name;
they cannot create or elevate a role.

```bash
fullsend repos install <group/project> \
  --gitlab-role-registry ./gitlab-roles.json
```

A custom role can have its own PAT (`credential: own`) or reuse another
registered role's credential (`credential: reuse`). See the
[registry JSON shape](../../contributing/gitlab-role-credentials.md#registry-json-shape).

#### Fresh install and the legacy shared token

On a fresh install, `repos install` provisions Poller, Analyst, and Coder
(plus any registered custom roles) and never creates `fullsend-bot`. On an
existing installation that predates the role-only model and still has
`FULLSEND_FORGE_TOKEN`, ordinary install provisions the missing role
tokens but does not retire the shared secret — there is no automated
path that does. Runtime never authenticates as the shared token, even
while it remains; removing that leftover secret (and revoking the
matching `fullsend-bot` project access token) is a manual administrator
step.

There is **no rollback** to the shared token, no migration flag, and no
migration-gate variable anywhere in the install or runtime contract. A
missing role secret fails the job closed.

#### Rotation, recovery, and in-flight jobs

GitLab project access tokens expire in at most one year. `repos install`
rotates a role when its token is expiring (within 30 days), expired,
revoked, or unverified. `--rotate-gitlab-roles` force-rotates every
own-credential role; `--rotate-gitlab-role=<name>` limits the run.

Rotation creates a new PAT, writes it to the existing masked CI variable,
and leaves the previous PAT active for a 24-hour grace so in-flight jobs
can finish. If creation fails, nothing is written. If distribution fails,
the unused replacement is revoked and the previous secret stays in place.
Rotation never selects `FULLSEND_FORGE_TOKEN`.

On GitLab.com Free, personal PATs enrolled with `--gitlab-role-token` are
rotated by the administrator, not by `repos install`.

#### Readiness, drift, reinstall, and uninstall

`repos status` reports per-role readiness and treats missing, expired, or
revoked credentials as `gitlab-role:<name>` drift. Re-running
`repos install` repairs missing role secrets. `repos uninstall` deletes
the registry, role secrets, matching role project access tokens, and
verified Fullsend-managed project service accounts (see
[Project service accounts](#project-service-accounts)), and
removes the rotation document last. If the rotation document records
administrator-supplied account exclusions, an exclusions-only document is
retained, and uninstall fails closed, deleting nothing, when the rotation
state is unreadable or malformed. It does not delete a leftover `FULLSEND_FORGE_TOKEN`
secret or revoke a matching `fullsend-bot` project access token — a
repository installed before the role-only rollout requires manual
cleanup of those. See [Operations § Uninstalling](operations.md#uninstalling).

#### Project service accounts

> **Status: active
> ([#8242](https://github.com/fullsend-ai/fullsend/issues/8242)).** Install
> converges Fullsend-managed role credentials to project service accounts and
> writes rotation state in a versioned format. Every CLI that runs
> `repos install`, converge or uninstall against a project must be upgraded
> first: older rotation and status runs reject the new state, but older
> provisioning can still publish secrets before recording distribution state,
> and older uninstall revokes active role-named tokens, supplied ones included
> (see
> [Upgrade compatibility](../../cli/repos.md#upgrade-compatibility)). The
> live fresh-identity Poller handoff
> ([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)) activates the
> webhook fast path where the project is eligible. Ineligible or ambiguous
> projects defer, and the polling schedules stay the dispatch path.

> **Poller elevation safety:** Re-elevating a Poller identity that ever held a
> distributed runtime credential requires a verified server-side guarantee
> that requests accepted before credential revocation, including asynchronous
> credential and job creation, have finished. The current GitLab adapter
> cannot establish that guarantee, so it never re-elevates the current Poller
> and creates new triggers through a fresh Poller generation instead. Polling
> continues with Developer credentials; compliant existing triggers can still
> be reused. Revocation and empty resource inventories alone do not prove that
> requests have drained.

Fresh installs create a separate project service account for
the Poller, Analyst, and Coder roles where the instance supports project
service accounts, and issue each a Developer-level personal access token.
Where project service accounts are unavailable, installs fall back to separate
Developer-level project access tokens if the instance supports them. Either
way these are genuinely different GitLab identities.

Re-running `repos install` also replaces positively identified Fullsend-managed
legacy role credentials with service-account credentials. There is no separate
migration command. Supplied credentials are retained; old managed tokens stay
valid for the ordinary 24-hour in-flight-job grace period before cleanup on a
later install; when you switch a role to a supplied credential (below), the
earlier managed tokens are not cleaned up automatically and stay usable until
they expire or you revoke them. To switch a role that already has a managed credential to a
credential you supply, re-run `repos install` with
`--gitlab-role-token <role>=<token>`. The supplied token takes priority over
automatic convergence and is enrolled over the existing managed credential, even
when that credential is still fresh; there is no need to delete the variable or
pass `--rotate-gitlab-roles`. If you also pass `--rotate-gitlab-role`, the role
must be within that filter. Before publishing a replacement Poller credential,
install grants and verifies its protected-default-branch pipeline access. A
failed grant revokes the unpublished replacement and preserves the installed legacy
credential. Interrupted replacement resumes through ordinary rotation state.

An installed role credential that rotation state does not record (for example,
one installed before rotation state existed) is left untouched, and install
reports it instead of migrating it. To re-adopt it, remove that role's
credential variable and re-run install, either passing the credential once with
`--gitlab-role-token` or letting install provision a managed one; see
[Re-adopting a role credential of unknown provenance](../../cli/repos.md#re-adopting-a-role-credential-of-unknown-provenance).

Service-account provisioning is the primary path; the project-access-token
fallback is what GitLab.com Free restricts, because that tier cannot create
project access tokens. Whether the service-account path works on GitLab.com
Free has not been verified against a live instance, so do not rely on it there.
Where neither automatic path is available, or you prefer to own the identities,
enroll role credentials manually with personal access tokens as shown in
[Role identities and GitLab Free](#role-identities-and-gitlab-free).

On instances with project service accounts, `fullsend-poller`,
`fullsend-analyst`, and `fullsend-coder` (plus any `fullsend-role-*` accounts)
appear under Project information → Members; their personal access tokens do
not appear under Settings → Access Tokens. Installs that fall back to project
access tokens show the tokens under Settings → Access Tokens instead.

Status also shows service-account IDs, ownership and effective access; a
managed account outside Developer is drift. Uninstall deletes durably
identified managed service accounts only after credentials and owned resources
are retired. It preserves supplied or unverified accounts and contributions.
Remaining SSH credentials, unfinished jobs, schedules or triggers prevent
account deletion; address the reported resource and retry uninstall.

GitLab ties a trigger token to the user who creates it, and creating one needs
Maintainer. Install therefore creates it as a fresh replacement
Poller service account that has never held a distributed credential, instead
of the installing Maintainer or the current Poller
([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)). Re-elevating an
identity whose runtime credential was distributed is unsafe, because GitLab has
no drain barrier for requests accepted before revocation. Install:

1. Creates the replacement Poller service account at Developer and records
   its ID in `FULLSEND_GITLAB_POLLER_GENERATIONS` before acting. Creating the
   replacement account never changes branch protection. Fullsend checks
   default-branch pipeline access read-only and defers the handoff before
   elevation when the existing policy (for example a protected default branch
   that only lists individual users) excludes the replacement Poller; it never
   writes a merge grant (an individual user's entry under **Allowed to merge**
   on a protected branch) for the replacement account.
2. Creates an installer-only bootstrap personal access token for the
   replacement account. The bootstrap token is never stored in a CI/CD
   variable, log, or agent environment.
3. Grants the replacement account Maintainer temporarily and creates the
   trigger authenticated with the bootstrap credential. The trigger-create
   request is attempted once and never retried.
4. Revokes the bootstrap token, restores Developer, and verifies the
   replacement account's effective access and the revocation.
5. Confirms the trigger is owned by the replacement account, re-checks the
   project-wide trigger-safety invariants (pipeline-variable override policy,
   no other protected branches or tags, the standard CI configuration path,
   and trigger pipelines that run only the dispatcher), and confirms the
   replacement account, at Developer access, may start pipelines on the
   protected default branch under the existing branch rule. This check reads
   the rule without changing it or starting a pipeline. The same invariants
   are checked before the account is raised, so an unsafe or unverifiable
   project defers the handoff before any trigger exists. A trigger owned by a
   different user than the replacement account is quarantined with an
   instruction to delete that exact trigger and confirm it is gone.
6. Records that the trigger may be distributed, stores the trigger secret
   (`FULLSEND_TRIGGER_TOKEN`), and creates or updates the webhook. Then, only
   after both succeed, publishes the replacement runtime credential and cuts
   over. The previous Poller stays at Developer as the retiring generation,
   and a later install retires it (see below); until then a forced trigger
   rotation defers and keeps the existing fast path.

The current Poller is never elevated or modified, and keeps polling
throughout, so polling and in-flight jobs that authenticate as the current
Poller are not interrupted. The webhook is enabled or updated only after the
replacement is verified. If the trigger request, its verification, or the
final checks fail, install publishes nothing and quarantines the generation
with an operator-facing reason. A quarantined generation or a lost account ID
blocks any new generation until an administrator reviews
`FULLSEND_GITLAB_POLLER_GENERATIONS`, deletes any pipeline trigger the
replacement account owns, removes the replacement account, and then removes
only the `pending` generation, keeping `version`, `current_user_id`,
`retiring_user_id` and `abandoned_user_ids` so the running Poller is still
recorded and retired at the next cutover. Removing `pending` forgets the
account and its trigger, so do it only after both are gone.

A failure while storing the trigger secret (including a write whose outcome is
unknown) or while creating or updating the webhook does not quarantine the
generation and records no cutover; the current Poller stays in service. The
trigger may already have reached protected jobs, so the next install revokes
it, confirms by listing again that it is gone, and never raises that account
again: the account is recorded in `abandoned_user_ids`, stays at Developer
without credentials, and a fresh account finishes the handoff. Uninstall
verifies and deletes abandoned, retiring and pending accounts. After
several abandoned accounts, install stops creating new ones until they are
deleted and `abandoned_user_ids` is cleared. A cutover interrupted after the
webhook and credential are live is finished by the next install, which does
not create another trigger.

An absent `FULLSEND_GITLAB_POLLER_GENERATIONS` variable is an empty state,
which is also the starting point after an administrator deletes it. A variable
that exists but is blank fails closed, because recorded state may have been
lost; see the variable table in the
[operations guide](operations.md) for the full recovery steps.

An unresolved retiring Poller is a separate condition: it blocks any new
generation and cutover, and removing `pending` does not clear it. A later
`repos install` retires it
([#8244](https://github.com/fullsend-ai/fullsend/issues/8244)) under the project
lease, once all of these hold:

- the cutover is confirmed, meaning the managed Poller is the recorded current
  generation;
- the 24-hour rotation grace has elapsed since the replacement credential was
  distributed, so in-flight jobs that read the previous credential can finish;
- no unfinished job runs as the retiring account.

Until then install reports why retirement is waiting and changes nothing.
Retirement revokes the pipeline triggers the retiring account owns, never the
trigger a webhook uses: install re-reads the webhook, the trigger inventory and
the trigger-account ledger immediately before revoking, on both the
trigger-account route and the installer-owned Poller route, and waits when a
managed webhook's trigger cannot be identified. When GitLab does not report the
owner of a trigger, fullsend revokes it only if it carries the managed webhook
trigger description and no webhook uses it. Any other trigger without an owner
cannot be attributed, so install keeps the retirement waiting and names the
trigger: revoke it in **Settings → CI/CD → Pipeline trigger tokens** if it is
not in use, then rerun install. A failing retirement never stops install from
repairing the webhook; the error is reported and retirement is retried by the
next install. It does not change any merge grant (an individual user's entry
under **Allowed to merge** on a protected branch) on the protected default
branch, because fullsend does not record who created a per-user grant
and it may be an administrator's. Retirement revokes the account's credentials,
verifies that it holds no active token, SSH key, unfinished job, schedule or
trigger, deletes it, confirms by listing again that it is gone, and only then
clears `retiring_user_id`. If the protected default branch still lists a merge
grant for the retired account, install reports it so you can remove it in
**Settings → Repository → Protected branches** if it is no longer needed. Each
step can be safely repeated. A step that fails or
cannot be verified leaves `retiring_user_id` in place for the next install to
retry. An account that owns an administrator-supplied credential is never
deleted. Polling continues with the current Poller throughout.

Retirement also enforces the GitLab Free-tier account quota. Fullsend does not
rely on a numeric Free-tier limit, so the bound is that at most one old/new
pair of Poller accounts exists at a time: the current Poller plus either one
pending replacement or one retiring Poller. At most three abandoned
replacements may also await deletion. Repeated installs and rotations
therefore return to a single Poller instead of accumulating service accounts.

The current Poller is not raised, so its own credentials do not block the
handoff. The checks that guard the temporary Maintainer elevation apply to the
fresh replacement account instead: install defers the fast path, before any
trigger exists, when the replacement holds an active personal access token,
owns a pipeline trigger that fullsend does not manage, or holds a usable SSH
key, an unfinished job or a pipeline schedule, when the project-wide
trigger-safety invariants do not hold, or when its credentials cannot be
listed. Fullsend never revokes credentials it does not manage, and an
administrator-supplied Poller credential is left untouched.

When the Poller is a project access token bot, no generation can be recorded
for it, so the fast path stays deferred. Rotating the Poller migrates it to a
service account. In every deferral the polling schedules keep working, and the
poller, dispatcher, and agent credentials stay Developer-level. See the
[CLI reference](../../cli/repos.md#gitlab-project-service-account-lifecycle)
and the
[role-credential contract](../../contributing/gitlab-role-credentials.md#project-service-accounts)
for details.

### Trigger-only service account

`repos install` also sets up a separate, trigger-only project service account
for the protected-branch dispatch route that
[#8438](https://github.com/fullsend-ai/fullsend/issues/8438) activates. Once
install records the dispatch activation, scheduled polling moves onto
the trigger-only account and dispatches through the child-pipeline route (see
"Polling schedule ownership" below), and the managed webhook moves onto the
account's trigger token
([#8509](https://github.com/fullsend-ai/fullsend/issues/8509)): a
webhook-triggered pipeline then runs as the account and dispatches through the
same child-pipeline route. Install points the webhook at the published
`FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN` and records the account's trigger ID in
the webhook description, then revokes the Poller-owned webhook trigger and
deletes `FULLSEND_TRIGGER_TOKEN`. Install converges the webhook again after the
trigger account stage, so an account rotation re-points it, and a withdrawn
activation returns it to a Poller-owned trigger on the Poller `api` route.
If a Poller role stage fails, install leaves a requested trigger-account
rotation pending rather than revoking the token still embedded in the webhook.
While the activation is not recorded (for example where the trigger account
lacks merge access to the protected default branch and the schedules stay
installer-owned), the webhook keeps its Poller-owned trigger, and the Poller
protected-branch merge grant stays in use. `--rotate-gitlab-trigger-token` does
not rotate the account's trigger token; use `--rotate-gitlab-trigger-account`. It needs only project Maintainer (or Owner) access: no
instance administrator and no persistent Maintainer token or private key.

Install creates the account (`fullsend-trigger-<nonce>`), temporarily raises it
to Maintainer so it can own a deploy key and a pipeline trigger token, revokes
its short-lived token and returns it to Developer, grants the deploy key push
on the exact-name protected default branch, and stores the trigger token in the
masked, protected `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN` variable. It is
never the Poller account. The `FULLSEND_GITLAB_TRIGGER_ACCOUNT` ledger records
account, deploy-key, and trigger-token obligations before creation. Managed
schedules are instead recognized by their exact descriptions and verified
owner. An interrupted run is finished by running install again.
The poller, dispatcher, and agent job scripts unset this bearer before doing
work, and `fullsend run` keeps it out of harness expansion and child processes.
If project-wide trigger safety checks fail after publication, install removes
the variable and revokes the account-owned trigger using the ledger's cleanup
record. It will not republish the token until those checks pass.
If `ci_push_repository_for_job_token_allowed` becomes enabled or cannot be
verified, install removes the published account token and revokes its trigger;
withdrawing dispatch activation alone does not contain a copied bearer.
#8509 adds two keys to the strict ledger schema: `trigger_token_digest` (the
token fingerprint) and `publication_pending` (the replacement-publication
marker). The ledger stays at version 1, and older strict decoders reject
either key before they read the version. Use
the first CLI release or source commit containing #8509 (or newer) for
`repos install`, `repos status`, rotation, and `repos uninstall` after it writes
the ledger. There is no in-place downgrade path for an installed account:
uninstall it with a compatible CLI before using an older installer. Older
CLIs may report an unknown ledger field instead of an unsupported version.
#8509 also starts automatic triage for newly opened GitLab issues. After
upgrading, a project with a `triage` agent and an active webhook fast path
triages each issue opened by a Developer-or-higher member. Before #8509,
triage started only from a `/fs-triage` comment. Issues opened by Reporter,
Guest, or non-member authors are not triaged automatically.

Install defers, creates nothing, and reports the reason (polling stays in
place) when the default branch is covered only by a wildcard protected-branch
rule (fullsend does not edit wildcard rules), when project service accounts are
unavailable, when `ci_pipeline_variables_minimum_override_role` is not
`no_one_allowed` (no trigger credential is published while pipeline variable
overrides are allowed, and a rotation defers before it starts), or when the
protected default branch is not the one the ledger recorded. Install fails instead, before the account is raised, when the account
owns an SSH key, a pipeline schedule, or a trigger token that fullsend did not
create, or when any pipeline trigger token or schedule has no reported owner
(its ID is named; fullsend never deletes it by inference); remove or reassign
it, then re-run install. The managed polling schedules the account owns once
dispatch is activated (see below) are fullsend's and do not block install. As
soon as the manifest and forge clients are
available, every install runs a containment-only recovery that revokes an
unfinished bootstrap token and restores Developer access without creating
anything, before the configuration preflights and every later stage can fail.
The same early pass removes a published token variable and revokes the owned
triggers across the current and retired generations when the override role is
weakened or unreadable or the variable is unsafe, so a later stage failure
cannot leave a published credential live.
Only validation of command-line flags and loading the manifest come first.
`repos status` shows the account as `not-installed`, `ready`, `drifted`,
`deferred`, `in-progress`, `cleanup-pending`, or `unreadable` with the exact
reason and resource IDs.

- **Rotate:** `repos install --rotate-gitlab-trigger-account` replaces the
  whole identity with a fresh account generation (account, deploy key, push
  entry, and trigger token) and retires the previous generation after the new
  token is published and the managed webhook is verified on its new route. A
  failed webhook cutover keeps the old generation for retry. A published identity is never raised to Maintainer again,
  because a trigger request accepted earlier can still become a job. Install
  does the same when the token variable is missing or unsafe (not masked, not
  protected, file-typed, or environment-scoped) or the deploy key is gone. If a
  rotation or retirement was interrupted, run install or the same command
  again.
- **GitLab.com Free:** GitLab refuses the legacy per-user protected-branch
  merge grant for the Poller. Install still prepares the trigger account in
  that case. The Poller access failure is reported once for the repo, and the
  full dispatch-route stages that follow it stay skipped. Install still
  refreshes the webhook after ordinary trigger-account repair if the account
  route is active, because repair can replace its token. It does not move an
  account webhook onto an unverified Poller route; a requested rotation waits
  for a successful Poller stage.
- **Dispatch activation gate:** whenever install leaves the trigger account
  ready, including after the Poller grant is refused, it rechecks the
  prerequisites for the child-pipeline route that uses the account's job token and
  reports either `GitLab trigger dispatch activation deferred: <reasons>` or
  `GitLab trigger dispatch prerequisites verified`. The check only reads:
  every managed polling schedule (`fullsend slash poll`, `fullsend event poll`)
  must carry no pipeline variables (legacy schedules still set
  `FULLSEND_POLL_MODE`), and the live
  `ci_pipeline_variables_minimum_override_role` must be exactly
  `no_one_allowed`. A weaker, unreadable, or unsupported setting defers, even
  when a trigger token is already published. CI job-token repository push
  (**Settings > CI/CD > Job token permissions**, reported as
  `ci_push_repository_for_job_token_allowed`) must be disabled, because the
  trigger account's deploy key can push to the protected default branch; an
  enabled, unreadable, or unreported setting defers. The trigger account must
  also be ready on the default branch, verified against live GitLab state. A
  deferral keeps the Poller API dispatch route; install clears any prior
  activation and hands account-owned schedules back to the installer. An
  interrupted run can leave an account-owned schedule using that route until
  retry. Only when every check
  passes does install record the activation, as a separate protected
  `FULLSEND_GITLAB_DISPATCH_ACTIVATION` variable bound to the exact ledger
  generation (nonce and resource IDs); each later install, repair, rotation,
  containment pass or uninstall deletes it first, so the child-pipeline
  dispatch route is never selected or admitted on a ready account alone. The
  dispatch activation stays outside the `FULLSEND_GITLAB_TRIGGER_ACCOUNT`
  ledger. This version adds the `trigger_token_digest` and
  `publication_pending` keys to that ledger, however, so older strict CLI
  decoders reject it; use this version or newer for later install,
  status, repair, rotation, or uninstall, even if schedules remain
  installer-owned. The gate also defers when a committed CI
  job, other than Fullsend's own, would run in a trigger, agent child
  (`source=pipeline`) or scheduled (`source=schedule`) pipeline, and while the
  committed dispatch scripts, including helpers that run before authorization,
  do not match the shipped compatible scripts, or the managed poll, agent,
  and dispatcher jobs do not keep their shipped job configuration, including
  image, cache, automatic admission, and scripts. The committed root and
  wrapper must admit scheduled polls and include the `poll` stage. The
  committed files that consume an
  agent child (`source=pipeline`) pipeline (the root workflow, the root and
  wrapper includes, the agent job rules and input bridges, the shipped input
  schemas that constrain values before job-name expansion, and
  `run-agent-job.sh`) must admit it.
  Customized or older scripts and templates need a merged
  scaffold repair; a selector name in a comment or unexecuted code does not
  establish readiness. The gate also defers when the project selects a custom
  CI configuration path, or when a project, group, or instance CI/CD variable
  overrides a dispatch or pre-authorization value such as `CI_PIPELINE_SOURCE`,
  `CI_API_V4_URL`, `CI_PROJECT_DIR`, `CI_JOB_TOKEN`,
  `CI_PIPELINE_SCHEDULE_DESCRIPTION`, `FULLSEND_POLL_MODE`, or an agent input
  bridge; group and
  instance scopes the installing credential cannot read follow the documented
  [administrator trust assumption](#inherited-cicd-variable-trust). A newer
  scaffold with an older CLI is refused
  explicitly: before choosing the child-pipeline route the job runs
  `fullsend poll --print-dispatch-routes`, and a CLI that does not list
  `child-pipeline` stops the job before any dispatch with a message to upgrade
  the pinned CLI. Upgrade the installer and the project's `fullsend_ref` to a
  release or source commit containing #8508, then merge the scaffold repair
  **before** running `repos install` to transfer schedule ownership. The
  route probe only gates polling: a pinned CLI that implements
  `fullsend poll --print-dispatch-routes` and lists `child-pipeline` (any CLI
  from #8482 onward) can still poll after the transfer. A pinned CLI older than
  #8482 cannot poll once that transfer is active. For installer operations
  (`repos install` and `repos uninstall`) after the transfer, the minimum CLI
  is the first release or commit containing #8508, because earlier CLIs treat
  account-owned schedules as foreign and refuse to proceed. The `repos install`
  run that transfers the schedules prints a warning naming this minimum CLI and
  the upgrade order. Use a current CLI
  to hand schedules back to the installer while upgrading. Keep the generated agent job name: renaming it prevents the
  activation gate from proving that the dispatched pipeline runs the guarded
  job, so restore the shipped name in the scaffold repair. A scaffold that
  bridges the agent inputs under a custom or renamed agent job needs that
  repair first; until then the gate refuses the child-pipeline route.
  When the pinned `fullsend_ref` is an untagged SHA, `install-fullsend-cli.sh`
  builds the CLI from source and clears the Git and Go environment variables
  that would change the build (`GIT_CONFIG_*`, `GIT_TEMPLATE_DIR`, `GOFLAGS`,
  `GOENV`, `GOTOOLCHAIN`, `GOROOT`, `GOCACHEPROG`). Runners that rely on one of
  them, such as `GOTOOLCHAIN=local` on an air-gapped runner, should pin a release
  version instead, which downloads the release binary and leaves them untouched.
  The job log names (never the values of) each of these settings the runner had
  set and the build cleared. If the source build fails, the log repeats that
  list and points to the release-version pin.
  A managed schedule left by a previous installer credential remains eligible
  for adoption or hand-back when its reported owner still has project
  Maintainer access. An unknown or lower-privilege owner requires manual
  reconciliation.
- **Polling schedule ownership:** once install records the activation, it
  recreates the managed polling schedules (`fullsend slash poll`,
  `fullsend event poll`) under the trigger account, so a scheduled poll runs as
  that account and dispatches the agent through the child-pipeline route
  (`CI_JOB_TOKEN`, `source=pipeline`). The account creates them with a
  short-lived token while it stays at Developer, and that token is revoked in
  the same run. GitLab requires that account to have merge permission on the
  protected default branch. If it lacks that permission, install keeps the
  schedules installer-owned on the Poller route; it does not widen merge
  access. On GitLab Free, individual Developer merge grants are unavailable,
  so a Maintainers-only merge rule uses this fallback. Each new account-owned
  schedule uses the managed spec's UTC cron and the protected default branch,
  and the enabled or disabled state of the schedule it replaces. The
  installer-owned originals are deleted only after GitLab reports the new
  schedules as owned by the account. A failure keeps the originals but can
  leave an active duplicate until the next install finishes cleanup.
  When the activation is not recorded (the account or the gate deferred, or the
  account stage failed), install hands any account-owned schedule back to the
  installer instead. This also happens before the account is ever raised to
  Maintainer and when rotation or repair retires an account while the
  activation is not recorded. When rotation or repair records the activation
  for the replacement generation, the schedules the retired account still owns
  move straight to the replacement account in the same run. While the
  activation is recorded, `repos status` reports any managed schedule the
  trigger account does not own as drift; re-run install to move it. Install
  refuses a schedule whose owner is not a retired generation or a verified
  installer Maintainer; an administrator must remove or reassign that one first.
  Each move creates
  the replacement before deleting the original, so an interrupted run leaves a
  duplicate that the next install removes. If an install stops before it
  reaches the trigger-account stage, the activation stays withdrawn and the
  poll job selects the `api` route, so the account-owned schedules keep
  dispatching with the Poller credential until an install completes. Re-run
  install. Use this release or newer for subsequent `repos install`,
  `repos status`, and `repos uninstall` runs: older strict CLIs reject the
  `trigger_token_digest` and `publication_pending` ledger keys even while
  schedules are installer-owned.
  Older CLIs also treat account-owned managed schedules as foreign and refuse
  their cleanup. Upgrade the CLI before retrying.
- **Deferral and unsafe token variable:** a branch-change, override-role, or
  capability deferral first removes an unsafe published token variable and
  revokes its ownership-verified trigger. A weakened or unreadable override
  role removes the variable and revokes the trigger whatever the variable's
  attributes. The ledger keeps the cleanup obligation, and
  `repos status` keeps reporting that the token was removed until install
  publishes a replacement.
- **Change the default branch:** run `repos uninstall` (it removes the
  resources from the recorded branch), then `repos install`.
- **Uninstall:** `repos uninstall` removes the published token, trigger token,
  push entry, deploy key, the managed polling schedules the account owns, and
  the account, then the ledger. It does not delete the
  account while it still owns an active personal access token, SSH key,
  pipeline schedule, or trigger token that fullsend did not create, and
  rotation retires a previous account under the same rule; remove the
  reported resource, then re-run. If it stops, the
  ledger stays; fix the reported cause (for example a resource that no longer
  matches what fullsend recorded, which is left for you to inspect) and run
  uninstall again. Do not edit or delete `FULLSEND_GITLAB_TRIGGER_ACCOUNT` by
  hand.
- Resources of other accounts (administrator-owned keys, push entries, tokens,
  triggers) are never touched.

See [GitLab role credentials](../../contributing/gitlab-role-credentials.md#trigger-only-service-account)
for the ownership proofs and recovery details.

### Off-system polling

`fullsend poll` is a **hidden command** — it won't appear in `fullsend
--help` — for running the same poll loop that `repos install`'s pipeline
schedules would otherwise run in-CI. Use it when schedule creation fails or
when the instance's schedule cadence is too slow, from cron on a VM, a
Kubernetes CronJob, or any scheduler with network access to your GitLab
instance. Off-system polling replaces the in-CI schedules: disable or delete
both `fullsend slash poll` and `fullsend event poll` before enabling the
external jobs, or slash commands can be dispatched twice. Prefer disabling
over deleting: a later `repos install` reports a disabled required
schedule as drift but leaves it disabled unless you pass
`--reactivate-schedules`, while a deleted required schedule is always
recreated (active) on the next install, since a missing schedule is
treated as repairable drift regardless of that flag. Do not pass
`--reactivate-schedules` on repos using off-system polling — it opts back
into re-enabling a disabled schedule, restoring in-CI dispatch and the
double-dispatch risk this section describes.

```bash
export FULLSEND_GITLAB_POLLER_TOKEN="<poller-bot-pat>"   # not GITLAB_TOKEN or FULLSEND_FORGE_TOKEN
export FULLSEND_DISPATCH_SECRET="<dispatch-secret>"  # from the protected CI/CD variable
export CI_DEFAULT_BRANCH="main"           # or set CI_COMMIT_REF_NAME

fullsend poll \
  --forge gitlab \
  --fullsend-dir /path/to/.fullsend \
  --project "<group/project>" \
  --mode slash \
  --gitlab-url https://gitlab.example.com   # omit for gitlab.com
```

Run a second scheduler entry with the same environment and arguments but
`--mode events` to mirror the event-discovery schedule. Use `--mode slash`
every five minutes and `--mode events` at minutes `2,17,32,47` of each hour.
Do not omit `--mode`: an unmodeled invocation uses the legacy combined path
and can share neither the slash-mode state nor the event-mode cadence safely.

`--forge gitlab` and `--fullsend-dir` are required flags. `fullsend poll`
resolves its credential via the registered Poller role
(there is no shared-token fallback), so `FULLSEND_GITLAB_POLLER_TOKEN`
(**not** `FULLSEND_FORGE_TOKEN` and **not** the `GITLAB_TOKEN` named in
[Prerequisites](#prerequisites)) and
`FULLSEND_DISPATCH_SECRET` (the protected secret provisioned by install) must
be set in the environment. `--mode` must be `slash` or `events` for an
external replacement of the corresponding schedule. `--project` falls back
to `CI_PROJECT_PATH`, and
the pipeline-ref falls back to `CI_COMMIT_REF_NAME` then
`CI_DEFAULT_BRANCH` — one of each pair is required or the command errors.

## Inference Setup

Pick the route that matches what you can change in GCP:

| You can | Route |
|---------|-------|
| Create a Workload Identity provider and grant roles in your GCP project | Your own `gitlab-oidc` provider: the steps directly below. If Google cannot reach your GitLab instance, add [Private OIDC issuers (uploaded JWKS)](#private-oidc-issuers-uploaded-jwks) |
| Only manage a service account, while a platform team runs a provider that trusts your GitLab instance | [Using an existing WIF provider](#using-an-existing-wif-provider); skip the own-provider steps below |
| Skip GCP entirely | OpenAI with an API key; see [Installing Fullsend](#installing-fullsend) |

**Own-provider route.** From here up to
[Using an existing WIF provider](#using-an-existing-wif-provider), this
section applies only when you run the `gitlab-oidc` provider yourself.

Pass `--vertex-project`, or declare `inference.project` in the managed
configuration (for example
`fullsend repos set gitlab.config.inference.project <gcp-project>`), so
install writes `FULLSEND_GCP_PROJECT_ID` and `FULLSEND_GCP_WIF_PROVIDER`.
A declared project is a supplied credential: every install rewrites both
CI/CD variables from it, and a `--vertex-project` value that disagrees with
it fails before any write. **This only writes CI/CD variables
that reference the shared `gitlab-oidc` provider's resource name — it
does not create the Workload Identity Pool, the `gitlab-oidc`
provider, or its IAM bindings.** GitLab has no equivalent of
`fullsend inference provision` (which auto-provisions that
infrastructure for GitHub); a platform operator must create the
shared `gitlab-oidc` provider once per GCP project before agent jobs
can exchange tokens through it, or the CI/CD variables above will
point at a provider that doesn't exist and token exchange will fail
at runtime even though install succeeds. Creating the provider once
per project does not by itself trust every repo on that project — the
default recipe below scopes trust to a single repo; see
[Authorizing multiple specific projects](#authorizing-multiple-specific-projects-alternative)
if you're installing more than one repo against the same GCP project.

To create it, add a GitLab-specific provider to the same
`fullsend-inference` pool described in
[Advanced setup → Custom inference WIF configuration](../infrastructure/advanced-setup.md#custom-inference-wif-configuration).
The GitHub recipe there does not carry over as-is: it points the issuer
at GitHub, maps `assertion.repository*` claims that GitLab tokens don't
have, omits `--allowed-audiences`, and maps
`google.subject=assertion.sub`. A naively adapted provider therefore
rejects the `aud: "fullsend"` token agent jobs present, and GitLab's
structured `sub` claim (project path plus ref) can exceed Google
Workload Identity Federation's 127-byte `google.subject` limit even
when the audience is correct. Use GitLab's issuer, id-token claims,
numeric `project_id` as `google.subject`, and an explicit allowed
audience instead.

This guide is single-repo, so the default recipe below scopes trust to
the exact project being installed. Map `google.subject` to GitLab's
numeric `project_id` — not `assertion.sub` — and bind both that
immutable ID and the readable `project_path` in the provider condition.
`project_id` is unique within a GitLab instance, and the provider's
fixed issuer isolates identities from other instances. Keeping the path
condition preserves readable configuration and prevents a deleted
project path being reused by a different project.

Find the numeric project ID on the project's **Settings → General**
page (labeled Project ID).

```bash
export GCP_PROJECT="<gcp-project>"
export GITLAB_URL="https://gitlab.com"   # or your self-hosted instance URL
export PROJECT_PATH="<group/project>"    # the exact project path being installed
export GITLAB_PROJECT_ID="<numeric-id>"  # Settings → General → Project ID

gcloud iam workload-identity-pools providers create-oidc gitlab-oidc \
  --location=global \
  --workload-identity-pool=fullsend-inference \
  --issuer-uri="$GITLAB_URL" \
  --allowed-audiences="fullsend" \
  --attribute-mapping="google.subject=assertion.project_id,attribute.namespace_path=assertion.namespace_path,attribute.project_path=assertion.project_path" \
  --attribute-condition="assertion.project_id == '$GITLAB_PROJECT_ID' && assertion.project_path == '$PROJECT_PATH'" \
  --project="$GCP_PROJECT"
```

If `gitlab-oidc` already exists with `google.subject=assertion.sub`,
update just the mapping. This applies no matter which recipe set the
provider's `--attribute-condition` — this page's default single-project
condition, [multiple specific projects](#authorizing-multiple-specific-projects-alternative),
or [a group or group tree](#authorizing-a-group-or-group-tree-alternative):

```bash
gcloud iam workload-identity-pools providers update-oidc gitlab-oidc \
  --location=global \
  --workload-identity-pool=fullsend-inference \
  --attribute-mapping="google.subject=assertion.project_id,attribute.namespace_path=assertion.namespace_path,attribute.project_path=assertion.project_path" \
  --project="$GCP_PROJECT"
```

Omitting `--attribute-condition` leaves your existing condition — set
by whichever recipe you used — untouched. Changing only the condition
instead of the mapping leaves the oversize `sub` mapping in place, and
STS will still reject the exchange.

If you're on the default single-project recipe and want the added
project-ID protection against a deleted path being reused (see above),
also update the condition to bind both claims:

```bash
gcloud iam workload-identity-pools providers update-oidc gitlab-oidc \
  --location=global \
  --workload-identity-pool=fullsend-inference \
  --attribute-condition="assertion.project_id == '$GITLAB_PROJECT_ID' && assertion.project_path == '$PROJECT_PATH'" \
  --project="$GCP_PROJECT"
```

The multiple-projects and namespace-wide conditions don't reference
`project_id`, so no condition change is required there after migrating
the mapping.

The provider's `--issuer-uri` is a trust-boundary setting: it must exactly
match the GitLab instance that signs the `id_tokens` used by this repository.
Do not leave the `https://gitlab.com` value when installing against a
self-hosted instance, and do not point a self-hosted provider at a different
instance. If GCP cannot reach that instance over the public internet, also
upload its signing keys to the provider; see
[Private OIDC issuers (uploaded JWKS)](#private-oidc-issuers-uploaded-jwks).

The combined project-id and project-path condition scopes trust to this
project, but any job in that project that can request an OIDC token with
audience `fullsend` can satisfy it. If every polling and agent job runs
on protected refs, operators may further restrict the condition with
`&& assertion.ref_protected == 'true'`; otherwise keep the
project-level condition and treat all token-issuing jobs in the project
as trusted.

```bash
export PROJECT_NUMBER=$(gcloud projects describe "$GCP_PROJECT" --format='value(projectNumber)')
export WIF_PRINCIPAL="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/attribute.project_path/$PROJECT_PATH"

gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
  --role="roles/aiplatform.user" \
  --member="$WIF_PRINCIPAL" \
  --condition=None
```

The documented IAM bindings use
`principalSet://.../attribute.project_path/...` (and
`attribute.namespace_path` in the group-wide alternative). Those do not
change. If you bound a direct `principal://.../subject/<value>` IAM
member instead, the subject value must be the GitLab numeric project
ID, not the OIDC `sub` claim.

Create the `fullsend-inference` pool first if it doesn't already exist
(see the Advanced setup steps linked above). Agent jobs obtain a GitLab
`id_tokens` OIDC token (`FULLSEND_ID_TOKEN`, audience `fullsend`) and
exchange it through this provider. To use a provider someone else runs
instead, see
[Using an existing WIF provider](#using-an-existing-wif-provider).

When you later remove a repo, this IAM binding and (if unshared)
`--attribute-condition` value need explicit teardown — see [Operations §
Per-repo teardown](operations.md#per-repo-teardown), step 6.
`fullsend inference deprovision` does not cover `gitlab-oidc`.

### Authorizing multiple specific projects (alternative)

The `gitlab-oidc` provider is shared across every GitLab repo on the same
GCP project, but its default `--attribute-condition` above pins trust to a
single project. Installing a second repo against the same GCP
project does not require widening trust to an entire namespace — keep
the same dual `project_id`-and-`project_path` bind from the create
recipe for each project you're installing, OR-ing the per-project pairs
in the condition and binding one principalSet per project. Leave the
`google.subject=assertion.project_id` mapping from the create recipe
unchanged; look up each project's numeric ID (Settings → General →
Project ID) before running the update below:

```bash
export GCP_PROJECT="<gcp-project>"
export GITLAB_URL="https://gitlab.com"   # or your self-hosted instance URL

gcloud iam workload-identity-pools providers update-oidc gitlab-oidc \
  --location=global \
  --workload-identity-pool=fullsend-inference \
  --attribute-condition="(assertion.project_id == '<id-a>' && assertion.project_path == 'group/project-a') || (assertion.project_id == '<id-b>' && assertion.project_path == 'group/project-b')" \
  --project="$GCP_PROJECT"

export PROJECT_NUMBER=$(gcloud projects describe "$GCP_PROJECT" --format='value(projectNumber)')

for PROJECT_PATH in "group/project-a" "group/project-b"; do
  gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
    --role="roles/aiplatform.user" \
    --member="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/attribute.project_path/$PROJECT_PATH" \
    --condition=None
done
```

Repeat the `--attribute-condition` update (adding another
`project_id`-and-`project_path` clause) and the IAM binding loop each
time you install another repo. Keeping the `project_id` conjunct for
each project preserves the same protection against a deleted path
being reused that the default recipe adds; dropping to path-only ORs
would silently undo it for every project added this way. This keeps
trust scoped to exactly the repos you've installed, unlike the
namespace-wide option below. Prefer the namespace-wide alternative only
when the repo set isn't enumerable in advance.

### Authorizing a group or group tree (alternative)

> **Warning:** this widens trust beyond the single repo above — every
> project in the namespace(s) you allow can obtain the `id_tokens` OIDC
> token, federate through this provider, and receive
> `roles/aiplatform.user`. Prefer the per-project recipe above unless
> you are deliberately provisioning inference for many repos in the
> same namespace.

To authorize every project under one immediate parent namespace
instead of a single project, use the `namespace_path` claim for
authorization. `google.subject` still maps `assertion.project_id` —
the required Google subject — while the provider condition and
principalSet stay on `namespace_path`. GitLab's
`namespace_path` ID-token claim is the project's immediate parent
namespace path (for example, a project at `my-group/subgroup/project`
has `namespace_path=my-group/subgroup`) — it is not any ancestor
group. `GROUP_PATH` below must equal that exact path; setting it to a
higher-level group (`my-group`) to try to cover every project
underneath it will not match, and jobs from nested projects will be
rejected at STS.

```bash
export GROUP_PATH="<group>"   # the exact immediate parent namespace, e.g. "my-group" or "my-group/subgroup"

gcloud iam workload-identity-pools providers create-oidc gitlab-oidc \
  --location=global \
  --workload-identity-pool=fullsend-inference \
  --issuer-uri="$GITLAB_URL" \
  --allowed-audiences="fullsend" \
  --attribute-mapping="google.subject=assertion.project_id,attribute.namespace_path=assertion.namespace_path,attribute.project_path=assertion.project_path" \
  --attribute-condition="assertion.namespace_path == '$GROUP_PATH'" \
  --project="$GCP_PROJECT"

export WIF_PRINCIPAL="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/attribute.namespace_path/$GROUP_PATH"

gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
  --role="roles/aiplatform.user" \
  --member="$WIF_PRINCIPAL" \
  --condition=None
```

This grants `roles/aiplatform.user` to every GitLab project whose
immediate parent namespace is exactly `$GROUP_PATH` — not just the one
repo being installed.

Covering a group tree (a group and its subgroups) takes changes at
both layers, not just one. GCP IAM
`principalSet://.../attribute.namespace_path/$VALUE` bindings are
exact-match on the mapped attribute, so the single `$GROUP_PATH`
principalSet above only ever admits that one namespace — and binding
additional principalSets for nested namespaces has no effect by
itself, because the single-value `--attribute-condition` above still
makes STS refuse to mint a token for any assertion whose
`namespace_path` isn't exactly `$GROUP_PATH`. To cover a tree, do
both:

1. Widen `--attribute-condition` so STS accepts every `namespace_path`
   you intend to allow — for example, an OR of exact values, or a
   delimiter-safe prefix check such as
   `assertion.namespace_path == 'my-group' || assertion.namespace_path.startsWith('my-group/')`.
   **Do not** write `assertion.namespace_path.startsWith('$GROUP_PATH')`
   without the trailing `/` — that fails open and also matches
   unrelated namespaces like `my-group-evil`.
2. Bind a separate `principalSet` for each of those same values (or use
   a custom attribute mapping that collapses nested paths to a single
   bindable value).

Do not grant the broader
`principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/*`
(every identity in the pool) as a shortcut. That IAM member is
authorized regardless of the GitLab-specific `--attribute-condition`
above — the condition only gates which assertions STS will exchange
for the `gitlab-oidc` provider, it does not narrow the IAM member
itself, and it has no effect on `github-oidc` or any other provider
already federated into the same shared pool. Binding it grants Vertex
AI access to every identity in the pool, not just the GitLab
namespaces you intend to cover.

If a platform operator already provisioned a WIF provider, pass the full
resource name instead of relying on the default `gitlab-oidc` path (or
declare it as `inference.wif_provider`). Supplying the provider also skips
the project-number lookup through the GCP Resource Manager API. A declared
`inference.wif_provider` also requires a declared `config.inference.project`
(for example `fullsend repos set gitlab.config.inference.project <gcp-project>`);
`--vertex-project` alone does not satisfy it, so `repos status` and install
agree. Passing `--vertex-wif-provider` alongside `--vertex-project` needs no
declaration:

```bash
fullsend repos install <group/project> \
  --vertex-project "<gcp-project>" \
  --vertex-wif-provider "projects/<number>/locations/global/workloadIdentityPools/fullsend-inference/providers/gitlab-oidc"
```

The GCP project still needs the Vertex AI APIs enabled as described in
[Getting Inference](getting-inference.md). Install writes the
`FULLSEND_GCP_REGION` variable as `global` when it is missing. To use
another region, declare it, for example
`fullsend repos set gitlab.config.inference.region us-east5`; a declared
region is managed, so install repairs a variable that differs. Without a
declared region, install keeps an existing variable as it is. See
[Effective infrastructure values](../../cli/repos.md#effective-infrastructure-values).

### Using an existing WIF provider

Use this route when a platform team already runs a Workload Identity
provider that trusts your GitLab instance, and you cannot create the
`gitlab-oidc` provider or grant roles in your GCP project yourself. The
agent job requests a token for that provider, exchanges it, and then
impersonates a service account you control.

What you need:

- a GitLab project that uses `inference.auth` `vertex-wif`
  (`fullsend repos set gitlab.inference.auth vertex-wif`; see
  [Installing Fullsend](#installing-fullsend));
- a GCP project with Vertex AI enabled (see
  [Getting Inference](getting-inference.md));
- permission to manage one service account in that project.

This route is an alternative to running your own `gitlab-oidc` provider.
If you run your own, skip this section.

#### 1. Collect the values

Ask the provider's administrator for the provider resource name, the
audience it accepts, and the principal it maps your GitLab project to.
Then set the values once in your shell; the later steps use these
variables:

```bash
export GCP_PROJECT="<gcp-project>"            # your project that serves Vertex AI
export PROJECT_PATH="<group/project>"         # the GitLab project to install
export WIF_PROVIDER="projects/<pool-project-number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>"
export WIF_AUDIENCE="//iam.googleapis.com/${WIF_PROVIDER}"
export PRINCIPAL="principalSet://iam.googleapis.com/projects/<pool-project-number>/locations/global/workloadIdentityPools/<pool>/attribute.project_id/<gitlab-project-id>"
export SERVICE_ACCOUNT="<name>@${GCP_PROJECT}.iam.gserviceaccount.com"
```

- Use the principal exactly as the administrator gives it. The template
  above is the common shape for a provider that maps
  `attribute.project_id`: put your GitLab project ID (**Settings →
  General → Project ID**) in place of `<gitlab-project-id>`.
- `WIF_AUDIENCE` above is right when the provider has no allowed audiences
  configured: Google then accepts only the provider's full resource name.
  If the administrator gives you an audience, use theirs.
- The pool's project number usually belongs to the platform team, not to
  your GCP project. Copy the administrator's values exactly.
- `SERVICE_ACCOUNT` must be a user-managed service account. The Compute
  Engine and App Engine default accounts are not accepted.

#### 2. Let the provider's principal use your service account

Grant the principal `roles/iam.workloadIdentityUser` on the service
account. The binding is on the service account, not the project, so you
need permission to manage the service account only:

```bash
gcloud iam service-accounts add-iam-policy-binding "$SERVICE_ACCOUNT" \
  --role="roles/iam.workloadIdentityUser" \
  --member="$PRINCIPAL"
```

Check the binding:

```bash
gcloud iam service-accounts get-iam-policy "$SERVICE_ACCOUNT" \
  --flatten="bindings[].members" \
  --format="table(bindings.role, bindings.members)"
```

The output lists the binding:

```text
ROLE                            MEMBERS
roles/iam.workloadIdentityUser  principalSet://iam.googleapis.com/projects/<pool-project-number>/locations/global/workloadIdentityPools/<pool>/attribute.project_id/<gitlab-project-id>
```

The service account also needs `roles/aiplatform.user` in `$GCP_PROJECT`.
If it does not have it yet, send this command, with your values filled in,
to that project's administrator:

```bash
gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
  --role="roles/aiplatform.user" \
  --member="serviceAccount:$SERVICE_ACCOUNT" \
  --condition=None
```

> **Trust trade-off.** The principal you bind decides who can act as the
> service account.
>
> - A principal set bound by GitLab project ID trusts every pipeline in
>   that project that can request a token with the provider's audience,
>   not only Fullsend's agent jobs.
> - The agent sandbox receives that token (at
>   `/sandbox/workspace/.gcp-oidc-token`) and can exchange it itself. The
>   agent can reach everything granted to the provider's principal for
>   your project, not only this service account. Ask the administrator
>   which bindings exist for that principal.
>
> Bind the narrowest principal the provider offers: one per project rather
> than per group or instance.

#### 3. Declare the provider and install

```bash
fullsend repos set gitlab.config.inference.project "$GCP_PROJECT"
fullsend repos set gitlab.config.inference.wif_provider "$WIF_PROVIDER"
fullsend repos set gitlab.config.inference.wif_audience "$WIF_AUDIENCE"
fullsend repos set gitlab.config.inference.service_account "$SERVICE_ACCOUNT"
fullsend repos install "$PROJECT_PATH"
```

To use the provider for some projects only, set the keys on each
project's entry instead, for example
`fullsend repos set 'gitlab.repos["<group/project>"].config.inference.wif_provider' "$WIF_PROVIDER"`,
and the same for `project`, `wif_audience` and `service_account`.

A first install prints, among its other steps:

```text
    [group/project] Set 2 repository variables
    [group/project] Set 2 repository secrets
  ✓ Created PR #1: https://gitlab.example.com/group/project/-/merge_requests/1
    Merge the PR to apply these changes
  ✓ Install complete: 1 installed, 0 converged, 0 already current, 0 failed
```

Merge that merge request. Agent jobs use the files on the default branch,
so the new audience takes effect only after the merge. Install has then:

- written `FULLSEND_GCP_PROJECT_ID` and `FULLSEND_GCP_WIF_PROVIDER` (CI/CD
  secrets) and `FULLSEND_GCP_SERVICE_ACCOUNT` (a plain CI/CD variable);
- set the token audience in `.gitlab/ci/fullsend-agent.yml`:

  ```yaml
  id_tokens:
    FULLSEND_ID_TOKEN:
      aud: "//iam.googleapis.com/projects/<pool-project-number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>"
  ```

Install checks the values without calling GCP, and fails before any write
when:

- `inference.wif_provider` is not set: these keys cannot be combined with
  the derived per-project `gitlab-oidc` provider;
- the service account is not a full `.iam.gserviceaccount.com` email;
- the audience is not a single line of printable ASCII, or contains `$`
  (GitLab would expand it as a CI/CD variable);
- the `run-agent-job.sh` the project will run does not read
  `FULLSEND_GCP_SERVICE_ACCOUNT`, for example because `gitlab.fullsend_ref`
  pins an older release. Move the pin to a release with impersonation
  support.

**Already installed?** Install opens a new merge request that changes
the audience in `.gitlab/ci/fullsend-agent.yml`. It can do this only when
`gitlab.fullsend_ref` is set or you run a released `fullsend` binary. A
locally built binary without `gitlab.fullsend_ref` prints
`scaffold content drift not checked: gitlab.fullsend_ref is not set and this build has no release ref`,
and the job keeps `aud: "fullsend"`. Set `gitlab.fullsend_ref` and run
`repos install` again.
`repos status` reports drift while the variable or the audience does not
match the configuration.

#### 4. Check an agent job

Run an agent job as described in [Testing Fullsend](#testing-fullsend). A
working setup produces no Google credential errors in the agent job log,
and the agent finishes. For example, a triage job on Claude through Vertex
AI logged:

```text
  ✓ Provider ready: vertex-ai (0.1s)
→ Agent: claude-opus-5-5 (v2.1.306)
→ Result: success
  ✓ Agent exited with code 0 (56.0s)
```

The job log does not show the token exchange. If the provider's
principal has no role in your project, a successful model call proves the
job used your service account. If impersonation is not set up, the call
fails with `aiplatform.endpoints.predict` denied.

#### Troubleshooting

| Error in the agent job log | Cause | Fix |
|----------------------------|-------|-----|
| `invalid_target` ("The target service indicated by the "audience" parameters is invalid. This might either be because the pool or provider is disabled or deleted or because it doesn't exist.") | `inference.wif_provider` does not name an active provider: a wrong project number, pool ID or provider ID, or a disabled or deleted provider | Compare `inference.wif_provider` with the administrator's value, then run `repos install` again |
| `invalid_grant` ("The audience in ID Token [fullsend] does not match the expected audience.") | The token audience is not one the provider accepts. `[fullsend]` means the new audience is not on the default branch yet | Merge the install merge request, or set `inference.wif_audience` to an audience the provider accepts and run `repos install` again |
| `unauthorized_client` ("The given credential is rejected by the attribute condition.") | The provider's condition does not admit this GitLab project or ref | Ask the provider's administrator to admit the project |
| `invalid_grant` ("Error connecting to the given credential's issuer") | Google cannot reach the GitLab instance's keys | The provider's administrator applies [Private OIDC issuers](#private-oidc-issuers-uploaded-jwks) |
| `Permission 'iam.serviceAccounts.getAccessToken' denied on resource (or it may not exist)` | The principal cannot impersonate the service account: the binding from step 2 is missing or names another principal, or `inference.service_account` names another account | Repeat step 2 with the administrator's exact principal and the exact email in `inference.service_account` |
| `Permission 'aiplatform.endpoints.predict' denied` | The service account lacks `roles/aiplatform.user` in `inference.project`, or `inference.service_account` is not set, so the job used the provider's principal, which has no role in your project | Grant the role (step 2), correct `inference.project`, or set `inference.service_account` |

#### Leaving the existing provider

To move back to your own `gitlab-oidc` provider, create it as described
at the top of this section, then remove the three keys that point at the
platform team's provider and install again:

```bash
fullsend repos set --unset gitlab.config.inference.service_account
fullsend repos set --unset gitlab.config.inference.wif_audience
fullsend repos set --unset gitlab.config.inference.wif_provider
fullsend repos install "$PROJECT_PATH"
```

Keep `inference.wif_provider`, and remove only the other two keys, only if
you want to keep the platform team's provider without impersonation. That
works only when the provider accepts the audience `fullsend` and its
principal has `roles/aiplatform.user` in `$GCP_PROJECT`.

Install opens a merge request that restores `aud: "fullsend"` and prints
`Keeping obsolete variable FULLSEND_GCP_SERVICE_ACCOUNT until the scaffold change is merged`.
Until you merge it, `repos status` shows `FULLSEND_GCP_SERVICE_ACCOUNT differs`
(`--json` gives the reason: `obsolete variable (inference.service_account is not declared)`).
After the merge, the next `repos install` prints
`Deleted obsolete variable FULLSEND_GCP_SERVICE_ACCOUNT`. `repos uninstall`
also removes the variable with the other `FULLSEND_GCP_*` values.

### Private OIDC issuers (uploaded JWKS)

By default, GCP's Security Token Service (STS) checks each GitLab
`id_tokens` token by fetching the issuer's OIDC discovery document and
public signing keys (its JSON Web Key Set, or JWKS) from the provider's
`--issuer-uri`. If the GitLab instance is reachable only on a private
network, over VPN, or through corporate DNS, STS cannot fetch them and
agent jobs fail the token exchange with:

```text
Error code invalid_grant: Error connecting to the given credential's issuer
```

Upload the instance's public JWKS to the `gitlab-oidc` provider instead.
STS then verifies token signatures against the uploaded keys and does not
contact the instance. This changes only where STS gets the signing keys.
A platform operator still creates and manages the pool, the provider, and
its IAM bindings as described above, and `repos install --vertex-project`
still only writes the CI/CD variables that reference the provider.

1. From a machine that can reach the GitLab instance, download its public
   signing keys:

   ```bash
   curl -fsS "$GITLAB_URL/oauth/discovery/keys" > gitlab-jwks.json
   ```

   This is the `jwks_uri` listed in
   `$GITLAB_URL/.well-known/openid-configuration`. The file holds only
   public keys.

2. Upload the keys to the provider:

   ```bash
   gcloud iam workload-identity-pools providers update-oidc gitlab-oidc \
     --location=global \
     --workload-identity-pool=fullsend-inference \
     --jwk-json-path=gitlab-jwks.json \
     --project="$GCP_PROJECT"
   ```

   When creating the provider, you can pass the same `--jwk-json-path`
   flag to the `create-oidc` command above instead. In both cases, keep
   `--issuer-uri` set to the instance URL: the token's `iss` claim must
   still match it, even though STS no longer connects to it.

3. Run an agent job and confirm it gets Vertex AI credentials.
   `repos install` writes CI/CD variables but never performs the STS token
   exchange. Install success therefore does not prove the provider accepts
   the instance's tokens. The first proof is an agent job that completes
   the exchange.

**Key refresh is your responsibility.** The uploaded JWKS is a static
copy. When the GitLab instance rotates its OIDC signing key, tokens
signed with the new key fail the exchange until you upload a fresh JWKS.
Repeat steps 1 and 2 after every signing-key rotation. Ask the instance
administrators to tell you about planned rotations, or run a scheduled
job inside the network that fetches the JWKS and re-uploads it when it
changes.

Treat key removal as a security event, not only an availability one.
Because the uploaded JWKS is static, STS keeps trusting every key in it
after GitLab retires or revokes that key. Removing or revoking a signing
key in GitLab does not revoke the uploaded copy in GCP. Anyone who holds a
compromised key can keep minting tokens that satisfy the provider's issuer,
audience, project-id, and project-path conditions until you replace the
uploaded JWKS. Issuer-URI discovery drops such keys automatically; an
uploaded JWKS does not.

- Include emergency revocations, not only planned rotations, in the
  notification and refresh procedure with the instance administrators.
- After any retirement or revocation, promptly re-upload a JWKS that
  omits the retired or compromised keys (repeat steps 1 and 2).
- If you cannot obtain a trustworthy replacement JWKS during an incident,
  disable the provider until you can.

## Runner Configuration

Agent jobs run on GitLab CI using the `fullsend-runner` image (see
[Runner image version](#runner-image-version)) and the
tags embedded in the scaffold. Empty tags (`[]`) match untagged runners;
tagged jobs only match runners that carry those tags.

The runner VM scripts default to tag `fullsend-gitlab-runner`. Set that
tag in the manifest **before your first `repos install`** so the initial
install already embeds it in the scaffold, with no second install
needed. `gitlab.agent_runner_tags` routes sandbox agent jobs;
`gitlab.control_runner_tags` routes the poll job (and, later, the
webhook dispatcher). The two fields are fully independent — there is no
cross-fallback. An unset `gitlab.control_runner_tags` renders `tags: []`
(untagged), **not** the agent tags; an unset `gitlab.agent_runner_tags`
likewise renders `tags: []`. If your runner fleet is entirely
tag-restricted with no untagged pool, set `gitlab.control_runner_tags`
explicitly or the poll job will sit pending indefinitely:

```bash
fullsend repos set gitlab.agent_runner_tags '[fullsend-gitlab-runner]'
fullsend repos install <group/project> \
  --vertex-project "<gcp-project>"
```

To route the poll job to a different fleet than the sandbox agents (for
example, a cheaper high-concurrency runner without sandbox capacity),
set `gitlab.control_runner_tags` explicitly:

```bash
fullsend repos set gitlab.control_runner_tags '[fullsend-api]'
```

> **Migration note.** Upgrading an existing install that only had the
> legacy `gitlab.runner_tags` key changes the poll job's default: the
> value still resolves to `gitlab.agent_runner_tags` for rendering the
> agent job, but the control-plane (poll) job is untagged unless
> `gitlab.control_runner_tags` is set explicitly — an unset
> `gitlab.control_runner_tags` renders `tags: []`, it does not inherit
> the old fleet tag. This is an intentional, documented default, not a
> behavior-preserving fallback: an instance whose runner fleet is
> entirely tag-restricted with no untagged pool must set
> `gitlab.control_runner_tags` explicitly before or immediately after
> upgrading, or the poll job sits pending. `fullsend repos converge`
> auto-remediates the rendered `fullsend-poll.yml` with a repair commit
> for unpinned or post-split-pinned installs (GitLab vendor mode is no
> longer supported and is rejected by install/converge, so a previously
> vendored install is not repaired this way); a
> `gitlab.fullsend_ref` still pinned to a pre-split ref keeps the
> leftover `__RUNNER_TAGS__` placeholder stamped with the agent tags, so
> no drift is detected and no repair commit runs there — that install
> keeps its old rendered tag until it moves past the pre-split pin. The
> on-disk `gitlab.runner_tags` key is separately rewritten to
> `gitlab.agent_runner_tags` the next time a command rewrites
> `repos.yaml`, such as `repos install` persisting install defaults or
> `repos uninstall` removing entries. [`repos
> set`](../../cli/repos.md#repos-set) edits the file in place and leaves
> the old key alone; remove it with `fullsend repos set --unset
> gitlab.runner_tags`. That key rewrite is unrelated to the poll job's
> tag default described above.

If the repo is already installed, set the tags with `repos set` and
re-run `fullsend repos install -f repos.yaml`. The re-run takes the
converge path instead of a fresh install and rewrites the `tags:` lines
in `.gitlab/ci/fullsend-agent.yml` (agent tags) and in
`.gitlab/ci/fullsend-poll.yml` and `.gitlab/ci/fullsend-dispatcher.yml`
(control tags). Until then, `fullsend repos status` reports those files
as content drift. The converge path also retries any
not-yet-created pipeline schedules, and on any instance where schedule
creation previously failed that retry fails
hard with a `convergence errors` error — even though the runner-tag
change itself would otherwise apply cleanly. See the [GitLab tier
note](#prerequisites) above, and rely on [Off-system
polling](#off-system-polling) for pickup instead of re-running install
just to add the tag in that case.

The target project (or its group) must have a runner registered with
that tag — see [Assigning runners](#assigning-runners). If
`gitlab.control_runner_tags` is set to a different value than
`gitlab.agent_runner_tags` (as in the example above), the project also
needs a runner registered for the control tag, or the poll job sits
pending indefinitely. For provisioning
your own runner VMs on OpenShift Virtualization or GCE instead of joining
a hub, see the
[GitLab Runner VM](https://github.com/fullsend-ai/fullsend/blob/main/hack/gitlab-runner-vm/README.md)
README.

### Runner image version

The poll, webhook dispatcher, and agent jobs
(`.gitlab/ci/fullsend-poll.yml`, `.gitlab/ci/fullsend-dispatcher.yml`,
`.gitlab/ci/fullsend-agent.yml`) run
`ghcr.io/fullsend-ai/fullsend-runner` at a tag rendered from
`gitlab.fullsend_ref`, the same ref that selects the CLI the jobs install:

| `gitlab.fullsend_ref` | Runner image tag |
|---|---|
| A release, `vX.Y.Z` (the default: install writes the CLI's release here) | `X.Y.Z`, the image published for that release |
| Unset, with a locally built CLI | `latest`, which moves only when a release is published (the jobs also install the latest release CLI) |
| A branch such as `main`, or an untagged commit SHA | `dev`, which is rebuilt from `main` |

A release-pinned project therefore keeps its OpenShell, `gh`, gitleaks,
pre-commit, Go, and gcloud versions until you change `gitlab.fullsend_ref`;
runner image changes merged to `main` do not reach it. When you upgrade
`gitlab.fullsend_ref` and re-run `fullsend repos install`, the same converge
run re-renders the image tag in all three templates. `fullsend repos status`
reports a template whose image tag does not match `gitlab.fullsend_ref` as
content drift, and `repos install` repairs it. Templates fetched for a pin
that predates this rendering hardcode `fullsend-runner:dev`. Install replaces
that with the pinned release's image too.

Tracking `main` (the `dev` image) is an opt-in for Fullsend maintainers and
early testers, selected by pinning `gitlab.fullsend_ref` to `main` or a
commit SHA. It is **not supported for production**: every runner image change
merged to `main`, including an OpenShell bump, reaches the project as soon as
it is published, before any release has been tested with it. A commit SHA
cannot select an image of its own, because the runner image is built only for
`main` commits that change it. A SHA pin therefore always resolves to `dev`,
whichever CLI renders the templates, even when the SHA is a release's commit.
To run a release's image, pin the release tag (`vX.Y.Z`) rather than its commit
SHA.

Use a CLI newer than v0.51.0 to install or upgrade. Earlier CLIs do not render
the image tag, so they would commit the unrendered placeholder, which is not a
valid image tag. The `:dev` rewrite for templates fetched from an older pin
applies to releases from v0.32.0, the first release that published a runner
image.

### Assigning runners

Setting `gitlab.agent_runner_tags` / `gitlab.control_runner_tags` only
chooses which tags the scaffold embeds.
GitLab still has to **assign** a runner that carries that tag to the
target project or a parent group, or the job never gets a runner.

Use the Fullsend runner hub on the **same GitLab instance** you set as
`gitlab.url`. A hub on a different instance cannot pick up the job.

* **GitLab.com** (`https://gitlab.com`) — use the
  [GitLab.com Fullsend runner hub](https://gitlab.com/fullsend/runner-hub).
  Follow that project's
  [README](https://gitlab.com/fullsend/runner-hub/-/blob/main/README.md)
  to have its runners assigned to your project or group. The hub README
  is the source of truth for onboarding; this guide does not repeat those
  steps.
* **Self-managed instances** — use that instance's own Fullsend runner
  hub, typically a `fullsend/runner-hub` project on the same host.
  Follow **that** project's `README.md`, not the GitLab.com one.
  Onboarding tokens, groups, and runner registration are instance-local.
  Pointing a self-managed project at the GitLab.com hub (or the reverse)
  will not work.

After the hub assigns a runner, confirm the project (or its group) lists
a runner with the tag you set in `gitlab.agent_runner_tags` (Settings → CI/CD
→ Runners). When `gitlab.control_runner_tags` differs from
`gitlab.agent_runner_tags`, repeat this check for the control tag too —
both tag sets need their own registered runner under Settings → CI/CD →
Runners.

## Verifying the Installation

Compare the manifest against the project:

```bash
fullsend repos status -f repos.yaml
```

Confirm:

* Status is `installed` with `DRIFT` `none`. On instances where schedule
  creation failed, the `DRIFT` column will
  show `slash-poll differs, event-poll differs` instead (JSON: `field`
  values `slash-poll`/`event-poll` with `actual` `missing`) — that is
  expected; verify off-system `fullsend poll` instead, per
  [Off-system polling](#off-system-polling) above. A schedule that exists
  but has been intentionally disabled for off-system polling reports the
  same way (JSON: `expected` `active`, `actual` `inactive`) and is
  likewise expected — `repos install` does not clear this drift unless
  `--reactivate-schedules` is passed.
* **Pipeline schedules** — Settings → CI/CD → Pipeline schedules shows
  `fullsend slash poll` and `fullsend event poll`, both active. On
  GitLab.com Free, the schedules may run at most 24 times per day; verify
  the observed cadence is acceptable, or use off-system `fullsend poll`
  instead when five-minute pickup is required. If these schedules never
  run, confirm a runner carrying `gitlab.control_runner_tags` is
  registered under Settings → CI/CD → Runners — when that tag differs
  from `gitlab.agent_runner_tags`, both need their own registered runner
  (see [Assigning runners](#assigning-runners)).
* **Dispatcher pipelines** — With the webhook fast path enabled, every
  issue, merge request, and comment event creates a `trigger` pipeline
  that runs the dispatcher job. GitLab creates that pipeline before
  Fullsend can read `TRIGGER_PAYLOAD`, so the pipeline appears even for
  events that dispatch nothing. An issue edit that does not change labels
  (title, description, assignee) is ignored: the job logs
  `ignoring issue #N update without changes.labels` and succeeds without
  starting an agent pipeline. An edited comment on an issue or merge
  request is ignored the same way: the job logs
  `webhook: ignoring note edit on ...` and succeeds without dispatching.
  A failed dispatcher job means the payload
  was rejected, not ignored. Typical causes are a label change the API
  re-fetch contradicts or cannot tie to the acting user, a stale or
  replayed event, or an unsupported action such as a merge request edit
  or an issue reopen. Nothing is dispatched for a rejected payload, and
  the job log names the check that failed.
* **Role identities** — On instances with project service accounts, a fresh
  install shows `fullsend-poller`, `fullsend-analyst`, and `fullsend-coder`
  (plus any `fullsend-role-*` accounts) under Project information → Members
  at Developer access; their personal access tokens do not appear under
  Settings → Access Tokens. Where service accounts are unavailable and the
  install falls back to project access tokens (Premium/Ultimate or
  self-managed instances), those tokens appear under Settings → Access
  Tokens instead. `fullsend-bot`
  is a legacy, pre-role-only artifact — it appears only on an install that
  predates per-role credentials. No automated path retires it, regardless
  of role readiness; an administrator must manually revoke it. On
  GitLab.com Free with `--gitlab-role-token`, expect the dedicated PAT
  owner's username instead; no project access token is created.
* **CI/CD variables** — `FULLSEND_DISPATCH_SECRET`, `FULLSEND_GCP_PROJECT_ID`,
  and `FULLSEND_GCP_WIF_PROVIDER` exist and are protected.
  When the webhook fast-path is enabled, `FULLSEND_WEBHOOK_SECRET` is also
  stored as a masked, protected variable, together with
  `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN` on the trigger-account route or
  `FULLSEND_TRIGGER_TOKEN` on the Poller-owned route (`FULLSEND_TRIGGER_TOKEN`
  is deleted once the account route is active, so its absence there is not a
  provisioning failure). These values must never appear in logs. The poller, dispatcher, and agent jobs unset
  the applicable trigger token and webhook secret before doing any work, and the runner never exposes them to
  harness scripts or the sandbox. If the stored trigger token's privilege
  cannot be bounded on the Poller route (an unmanaged pipeline trigger owned at
  or above Maintainer, or one whose owner cannot be verified, could be that token),
  `repos install` deletes the `FULLSEND_TRIGGER_TOKEN` variable and the
  Fullsend-managed webhook and trigger tokens instead of leaving the bearer
  in storage; unmanaged trigger tokens are never revoked. On the account route,
  install compares the published value with the digest recorded when the
  account trigger was created. An existing healthy account without a digest
  keeps its token through a fresh-generation cutover; a mismatched digest
  tears down the managed webhook and contains the account's published token
  and trigger.
  `FULLSEND_FORGE_TOKEN` may remain only as a legacy artifact; no automated
  path retires it, so an administrator must manually remove it (see
  above). Role-aware installs also provision
  `FULLSEND_GITLAB_POLLER_TOKEN`, `FULLSEND_GITLAB_ANALYST_TOKEN`, and
  `FULLSEND_GITLAB_CODER_TOKEN`; custom role enrollments may add
  `FULLSEND_GITLAB_ROLE_*_TOKEN`. Secrets are requested as masked. For
  the role tokens above, storing one fails if GitLab rejects masking (for
  example, a value that doesn't meet its masking character-set rules); it is
  never stored unmasked. For other secrets, GitLab silently falls back to
  unmasked if it rejects a value. If any credential secret is unmasked, rotate
  it or restrict job-log visibility before running agents against untrusted
  content; revoke old personal PATs on their issuing accounts.

## Testing Fullsend

After the scaffold merge request is merged (or after a `--direct`
install), comment `/fs-triage` on an issue. GitLab has no issue-comment
webhook equivalent — the slash-command schedule polls every 5 minutes on
tiers and instances that support that cadence. GitLab.com Free throttles
each schedule to at most 24 pipeline triggers per day. Visit **Build →
Pipelines** to watch the poll and
agent jobs. On a role-aware install, the Poller identity handles polling and
the Analyst identity (normally `fullsend-analyst`) should post the triage
comment. Runtime credential selection requires the registered role
credential. Without provisioned role secrets, runtime fails closed instead
of posting — enroll role credentials via `--gitlab-role-token` (or complete
role provisioning) so `fullsend-analyst` can authenticate. If
`repos install` couldn't create the in-CI schedules,
or if their cadence is too slow for the instance, run `fullsend poll` on your
external scheduler instead — see [Off-system polling](#off-system-polling)
— and check its output for the same comment.

## Differences from GitHub

| Topic | GitHub | GitLab |
|---|---|---|
| Install command | `fullsend github setup` | `fullsend repos install`, after declaring the project in `repos.yaml` |
| Bot identity | Per-role GitHub Apps | Role-specific project service accounts (`fullsend-poller`, `fullsend-analyst`, `fullsend-coder`), with project access tokens as the fallback; Free tier must enroll these via `--gitlab-role-token` since runtime authentication never falls back to the shared PAT or `fullsend-bot` |
| Token mint | Required for App installation tokens | Not used — GitLab uses the stored PAT |
| Event dispatch | Native Actions webhooks | Cron polling (`fullsend slash poll` / `fullsend event poll`) |
| Inference WIF | Per-repo provider from `inference provision` | Operator-created shared `gitlab-oidc` provider, referenced via `--vertex-project` or a declared `inference.project` |
| CI entrypoint | `.github/workflows/fullsend.yaml` | `.gitlab/ci/fullsend-*.yml` included from `.gitlab-ci.yml` |

### `workflow:` block and `auto_cancel`

Install merges an include, `dispatch` / `poll` / `agent` stages, and workflow
rules into the existing `.gitlab-ci.yml`. The `dispatch` stage hosts the
webhook dispatcher job (`fullsend-dispatcher.yml`), which runs only in
`trigger` pipelines started by a GitLab pipeline trigger on the protected
default branch; such a pipeline runs the dispatcher alone, never an agent
job. Install does not overwrite unrelated jobs.

When an existing, non-empty file already has a `workflow:` block, fullsend sets
`workflow.auto_cancel.on_new_commit: none` if that key is missing, and does not
overwrite an existing value. It also adds a `CI_DEBUG_TRACE` deny-before-admit
rule (`when: never`) followed by the protected-ref `schedule`/`api` rules and
the protected-default-branch `trigger` rule if the block has no `rules:` key. Because `workflow.rules` is an
allowlist, ordinary push pipelines stop running in that case unless the block
already has a matching rule; add a catch-all/push rule (or an explicit
`when: always` rule) before installing, or remove the name-only `workflow:`
block so fullsend can leave it absent. When an existing, non-empty file has no
`workflow:` block, fullsend leaves it absent so push-triggered pipelines keep
running. For a missing or empty `.gitlab-ci.yml`, fullsend instead writes a
fullsend-owned `workflow:` block with a name, `auto_cancel.on_new_commit:
none`, the `CI_DEBUG_TRACE` deny rule, protected-ref `schedule`/`api`
rules, and the protected-default-branch `pipeline` (child-pipeline agent
dispatch) and `trigger` rules. Later ordinary push jobs added to that file likewise need additional
`workflow.rules` (or an explicit `when: always` rule), or GitLab will skip
them.

Because `workflow:rules` gates pipeline *creation*, not individual jobs, a
truthy `CI_DEBUG_TRACE` anywhere in the pipeline skips creating the entire
run — including any non-fullsend jobs in that same pipeline — not only
fullsend's own jobs, whenever fullsend owns this `workflow:` block. This is
inherent to `workflow:rules` (CI/CD variables are visible at job init, before
any job-scoped guard could run) and is required to keep dispatch secrets from
materializing when debug tracing is enabled.

### In-job identity pin

Before any credential-bearing call, the poll and agent job scripts also pin
their own identity from the `CI_JOB_TOKEN` job record and fail closed unless
both hold: the job's pipeline source matches that job's allowlist (poller =
`schedule` only, dispatcher = `trigger` only, agent = `api` or `pipeline` only
— `parent_pipeline` is not admitted), and
the job's ref is the project's **protected default branch**, not merely any
protected ref. The YAML `workflow:` rules above still admit any protected
ref, so a poll or agent pipeline dispatched against a protected
non-default branch (for example a release branch) starts and then aborts in
this in-job check. Keep fullsend's schedules and dispatches targeting the
project's registered default branch to avoid this fail-closed abort; see
[gitlab-role-credentials.md](../../contributing/gitlab-role-credentials.md)
for the full trust model.

Repos with `on_new_commit: interruptible` (or other non-`none` values)
may see agent pipelines canceled by later commits. Fullsend needs
`on_new_commit: none` for reliable agent runs. If pipelines disappear
unexpectedly, set that value in the root `workflow:` block.

## Self-Hosted GitLab Instances

Record the instance URL in `repos.yaml` (`gitlab.url`) before declaring
the project:

```bash
fullsend repos set gitlab.url https://gitlab.example.com
fullsend repos set 'gitlab.repos["<group/project>"]' '{}'
fullsend repos install <group/project> --vertex-project "<gcp-project>"
```

That env-var fallback (`FULLSEND_GITLAB_URL` → `GITLAB_API_URL` →
`CI_SERVER_URL`, then `gitlab.com`) is used by the agent's runtime
forge-client construction inside CI jobs — it does **not** apply to
`repos.yaml` manifest validation. `gitlab.url` must be set in the
manifest whenever GitLab repos are present; there is no default, and
`repos install`/`repos status` fail with `gitlab.url is required when
GitLab repos are present` otherwise. `repos install` has no
`--gitlab-url` flag; change the URL with:

```bash
fullsend repos set gitlab.url https://gitlab.example.com
```

Self-hosted instances sign OIDC tokens with their own issuer. The GCP
WIF provider's issuer must match that instance; API URL flags do not
configure WIF. If agent jobs fail token exchange, confirm the
`gitlab-oidc` provider issuer matches the GitLab instance URL. If the
instance is not reachable from the public internet, upload its JWKS to
the provider and refresh it after key rotations; see
[Private OIDC issuers (uploaded JWKS)](#private-oidc-issuers-uploaded-jwks).

## Next steps

* Read [Repo Management](repo-management.md) for multi-repo manifests,
  drift detection, and version upgrades.
* Read [Operations](operations.md) for day-2 updates, uninstall, and
  GitLab CI status-notification variables.
* Read the [Agents](../../agents/README.md) section to learn about the
  default agents Fullsend ships with.

### Inherited variables and authentication changes

Fullsend agent jobs have no deployment environment, so only wildcard (`*`) CI/CD variables
participate in installation checks. Fullsend already creates its variables at this scope;
Fullsend installations require no migration.

On self-managed instances, a project Maintainer token cannot normally read instance CI/CD
variables. Installation can proceed with unverified inherited scopes, but switching to
`openai-api-key` retains obsolete inference credentials until those scopes can be checked.
Confirm that no inherited `FULLSEND_OPENAI_*` identifiers override the key, then manually remove
credentials that are no longer needed. See [repository status](../../cli/repos.md#repos-status).
