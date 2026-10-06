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
  `repos install --vertex-project` (see [Inference Setup](#inference-setup)
  below). Unless you also pass `--vertex-wif-provider` (see
  [Inference Setup](#inference-setup)), `repos install` derives the
  project number from `--vertex-project` via the GCP Resource
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
  Follow [Typed-input dispatch migration](#typed-input-dispatch-migration)
  before upgrading a legacy installation; legacy variable-based pins keep
  their compatible setting. See [GitLab Role-Credential
  Contract](../../contributing/gitlab-role-credentials.md#how-a-job-selects-its-credential)
  for the remaining live-validation rollout gates for this transport.

## Typed-input dispatch migration

`ci_pipeline_variables_minimum_override_role` is a GitLab project setting,
not a `.fullsend` configuration field. Before installing typed-input jobs,
an administrator must set it to `no_one_allowed`; Fullsend reads it back
and refuses delivery if it is weaker, unsupported, or unreadable. Setting
`FULLSEND_GITLAB_PIPELINE_VAR_RESTRICTION=enforced` does not bypass this
pre-delivery check. A failed setting change must leave the new jobs undelivered,
not runnable under an unsafe policy.

For an existing variable-based installation, plan a maintenance window:
stop its poller and prevent its legacy credential-bearing jobs from running,
remove the managed schedules' obsolete `FULLSEND_POLL_MODE` variables, and
apply and verify the GitLab restriction before installing the new templates.
Do not apply the restriction to a running legacy installation: its dispatch
and schedule variables would stop working. If preparation fails, keep jobs
stopped and do not deliver or merge the typed setup. Inspect an asynchronous
installation/repair MR before merging, keep the restriction in place, and
rerun `repos install` after it merges to complete activation. Other user-owned
schedule variables must be migrated separately; Fullsend does not delete them.

Root, wrapper, agent, and poll templates are delivered together using the
effective target version. Fullsend does not migrate managed schedule variables
until the compatible wrapper **and** agent/poll templates are on the default
branch, including when a repair MR is still open. Dry runs never mutate them.
Typed jobs derive slash/event mode from the schedule description; legacy
variable wrappers retain their existing restriction and schedule variables.

Version skew between the poller and the installed wrapper behaves as follows:

| Poller | Installed wrapper | Result |
| --- | --- | --- |
| New | Typed | Dispatches with pipeline inputs. |
| New | Legacy | Detects the legacy wrapper and falls back to pipeline variables. |
| Old (sends variables) | Legacy | Dispatches with pipeline variables, as before. |
| Old (sends variables) | Typed, after activation set `no_one_allowed` | Cannot dispatch: GitLab rejects the variables. Upgrade the poller. |

The transport declares eleven metadata inputs and nine base64 chunks, at
most 1000 characters each. All three template layers validate the chunk
alphabet and length; fixed code reconstructs the payload as data. Variable
bridges disable expansion, and creator/HMAC authentication precedes caller
link logging and credential selection. Note bodies are bounded to 800 Unicode
characters with a truncation marker and note ID; oversized payloads fail
before a pipeline is created. GitLab's 20-input limit leaves no spare root
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
> [Off-system polling](#off-system-polling) below and
> [ADR 0067](../../ADRs/0067-gitlab-cron-polling-event-dispatch.md) for
> the design. A runner with sufficient CI capacity is still required for
> the scheduled jobs themselves.
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
> below) with `repos set-default` *before* your first install so they're
> captured by that first, non-converge run instead of requiring a
> converge-path re-run later. Rely on [Off-system
> polling](#off-system-polling) for pickup on repos where schedule
> creation already failed and a converge re-run to fix something else
> isn't an option.

## Installing Fullsend

Run the command:

```bash
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
  --inference-auth vertex-wif \
  --vertex-project "<gcp-project>"
```

Where `<group/project>` is the GitLab project path (nested groups are
supported, for example `group/subgroup/project`), and `<gcp-project>` is
the GCP project from [Getting Inference](getting-inference.md).

`--inference-auth` selects the inference authentication method
(`vertex-wif` or `openai-api-key`) and is persisted as `inference.auth` on
the project's manifest entry. There is no default: when the manifest does
not already provide a selection, `repos install` fails before changing the
project. See
[Repository management § Inference authentication](repo-management.md#inference-authentication).

For an installation without Vertex credentials, pass
`--inference-auth openai-api-key --openai-api-key <key>` and omit
`--vertex-project`. The key is written as the masked
`FULLSEND_OPENAI_API_KEY` CI/CD variable, and no GCP inference secrets are
written or looked up. An unprefixed `OPENAI_API_KEY` CI/CD variable is no
longer used; see the
[upgrade steps](../../cli/repos.md#gitlab-fullsend_openai_api_key-replaces-openai_api_key-breaking).
Configure each enabled agent's runtime and model for OpenAI before it runs.
GitLab OpenAI runs use an API key, not GitHub Actions WIF; see
[OpenAI Workload Identity](../infrastructure/openai-workload-identity.md).

`--gitlab-url` is required in every case, including gitlab.com: `repos.yaml`
fails validation (`gitlab.url is required when GitLab repos are present`)
whenever it's omitted, and nothing auto-populates it. Pass
`https://gitlab.com` for gitlab.com, or your instance URL for a self-hosted
install (`--gitlab-url https://gitlab.example.com`); either form also
implies `--forge=gitlab` when no forge is specified. You can set it after
the fact instead with `fullsend repos set-default gitlab.url <url>`.

The command bootstraps a `repos.yaml` manifest if one does not exist,
then converges the project:

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
* Provisions the webhook fast path ([ADR 0125](../../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)):
  a pipeline trigger token stored as the protected, masked
  `FULLSEND_TRIGGER_TOKEN` variable, a generated `FULLSEND_WEBHOOK_SECRET`,
  and a project webhook (issues, merge requests, comments) that triggers a
  pipeline on the protected default branch. Existing credential variables
  are reused only when masked, protected, and wildcard-scoped; otherwise they
  are regenerated. Only webhooks Fullsend owns are updated or deleted. This
  step is deferred until the dispatcher and its scripts are on the default
  branch, the default branch is protected, and
  `no_one_allowed` is verified, so on a merge-request install re-run
  `repos install` after the scaffold merges. Re-runs are no-ops and repair a
  missing or drifted webhook or token; `--rotate-gitlab-trigger-token`
  rotates the token and revokes the old one after the webhook is updated.
  The polling schedules stay in place as the backstop. Token and secret
  values are never printed.
  The install-time Maintainer access is used only to create and revoke
  these resources; it is not a runtime credential. A trigger token runs
  pipelines as its owner, so install verifies the owner's effective
  project role when it mints a token and whenever it reuses one, and
  rejects an owner with Maintainer or Owner access (or one it cannot look
  up): it revokes the token, removes the managed webhook, and leaves the
  fast path disabled. A rejected owner is reported as a deferral rather
  than an installation failure once cleanup succeeded; owner-lookup and
  cleanup failures are still reported as errors. A Developer-owned token
  also needs push or merge access to the protected default branch; an
  owner without it defers the fast path with remediation guidance. The
  webhook is enabled only after the committed wrapper, root, and
  agent/poll templates carry the typed pipeline-input contract, and the
  default branch must stay protected. Because GitLab ties a trigger
  token to the Maintainer who creates it, the fast path stays disabled
  when the only available creator is a Maintainer or Owner; the polling
  schedules keep working, and the poller, dispatcher, and agent
  credentials stay Developer-level.
* Writes inference CI/CD variables when `--vertex-project` is set.

By default the scaffold lands as a merge request. Pass `--direct` to push
to the default branch instead. Preview with `--dry-run`.

For the full list of install flags, see the
[CLI reference](../../cli/repos.md#repos-install).

### Enabling a subset of agents

By default, install configures
`triage,coder,review,fix,retro,prioritize`. To enable only specific
agents, pass `--roles`:

```bash
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
  --vertex-project "<gcp-project>" \
  --roles triage,review
```

### Choosing a runtime

`repos install` records the agent runtime in `.fullsend/config.yaml`.
Pass `--runtime` to set it (`claude` is the stable default; `pi` and
`codex` are experimental):

```bash
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
  --vertex-project "<gcp-project>" \
  --runtime claude
```

See [Choose a Runtime](choosing-a-runtime.md) for what the runtimes are
and how to change the selection after setup.

### Free-tier role-token enrollment

On instances that cannot create project access tokens, enroll each required
role with a personal access token:

```bash
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
  --vertex-project "<gcp-project>" \
  --gitlab-role-token "poller=<poller-pat>" \
  --gitlab-role-token "analyst=<analyst-pat>" \
  --gitlab-role-token "coder=<coder-pat>"
```

### Role identities and GitLab Free

Fresh installs on an instance that supports project access tokens create
separate Developer-level project tokens for the Poller, Analyst, and Coder
roles. These are genuinely different GitLab identities, so GitLab can audit
which responsibility acted and the Analyst can approve a merge request
created by the Coder. The credential variables are described in
the [role-credential contract](../../contributing/gitlab-role-credentials.md).

This automatic per-role provisioning is **not available on GitLab.com Free**
because that tier cannot create project access tokens. You can still enroll
role credentials manually with personal access tokens:

```bash
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
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

On GitLab.com Free, treat these as administrator-managed personal
credentials: their scopes and project/group reach follow the owning users,
and rotation or revocation is manual. Never use a personal administrator PAT
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
  --forge gitlab \
  --gitlab-url https://gitlab.com \
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
the registry, rotation document, role secrets, and matching role project
access tokens. It does not delete a leftover `FULLSEND_FORGE_TOKEN`
secret or revoke a matching `fullsend-bot` project access token — a
repository installed before the role-only rollout requires manual
cleanup of those. See [Operations § Uninstalling](operations.md#uninstalling).

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

Pass `--vertex-project` so install writes `FULLSEND_GCP_PROJECT_ID`
and `FULLSEND_GCP_WIF_PROVIDER`. **This only writes CI/CD variables
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
instance.

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
exchange it through this provider.

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
resource name instead of relying on the default `gitlab-oidc` path:

```bash
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
  --vertex-project "<gcp-project>" \
  --vertex-wif-provider "projects/<number>/locations/global/workloadIdentityPools/fullsend-inference/providers/gitlab-oidc"
```

The GCP project still needs the Vertex AI APIs enabled as described in
[Getting Inference](getting-inference.md). `--vertex-region` defaults
to `global` when `--vertex-project` is set.

## Runner Configuration

Agent jobs run on GitLab CI using the `fullsend-runner` image and the
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
fullsend repos set-default gitlab.agent_runner_tags fullsend-gitlab-runner
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
  --vertex-project "<gcp-project>"
```

To route the poll job to a different fleet than the sandbox agents (for
example, a cheaper high-concurrency runner without sandbox capacity),
set `gitlab.control_runner_tags` explicitly:

```bash
fullsend repos set-default gitlab.control_runner_tags fullsend-api
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
> `gitlab.agent_runner_tags` the next time a manifest-writing command
> runs (see [`repos
> set-default`](../../cli/repos.md#repos-set-default) for exactly which
> commands persist the rewrite); that key rewrite is unrelated to the
> poll job's tag default described above.

If the repo is already installed, setting the tag with `repos
set-default` and re-running `fullsend repos install -f repos.yaml`
takes the converge path instead of a fresh install: it retries any
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

### Assigning runners

Setting `gitlab.agent_runner_tags` / `gitlab.control_runner_tags` only
chooses which tags the scaffold embeds.
GitLab still has to **assign** a runner that carries that tag to the
target project or a parent group, or the job never gets a runner.

Use the Fullsend runner hub on the **same GitLab instance** you pass to
`--gitlab-url`. A hub on a different instance cannot pick up the job.

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
* **Access tokens** — On Premium/Ultimate or self-managed instances where
  project access tokens are available, a fresh install shows
  `fullsend-poller`, `fullsend-analyst`, and `fullsend-coder` (plus any
  `fullsend-role-*` tokens) under Settings → Access Tokens. `fullsend-bot`
  is a legacy, pre-role-only artifact — it appears only on an install that
  predates per-role credentials. No automated path retires it, regardless
  of role readiness; an administrator must manually revoke it. On
  GitLab.com Free with `--gitlab-role-token`, expect the dedicated PAT
  owner's username instead; no project access token is created.
* **CI/CD variables** — `FULLSEND_DISPATCH_SECRET`, `FULLSEND_GCP_PROJECT_ID`,
  and `FULLSEND_GCP_WIF_PROVIDER` exist and are protected.
  When the webhook fast-path is enabled, `FULLSEND_TRIGGER_TOKEN` and
  `FULLSEND_WEBHOOK_SECRET` are also stored as masked, protected variables
  and must never appear in logs. The poller, dispatcher, and agent jobs unset
  both variables before doing any work, and the runner never exposes them to
  harness scripts or the sandbox. If the stored trigger token's privilege
  cannot be bounded (an unmanaged pipeline trigger owned at or above
  Maintainer, or one whose owner cannot be verified, could be that token),
  `repos install` deletes the `FULLSEND_TRIGGER_TOKEN` variable and the
  Fullsend-managed webhook and trigger tokens instead of leaving the bearer
  in storage; unmanaged trigger tokens are never revoked.
  `FULLSEND_FORGE_TOKEN` may remain only as a legacy artifact; no automated
  path retires it, so an administrator must manually remove it (see
  above). Role-aware installs also provision
  `FULLSEND_GITLAB_POLLER_TOKEN`, `FULLSEND_GITLAB_ANALYST_TOKEN`, and
  `FULLSEND_GITLAB_CODER_TOKEN`; custom role enrollments may add
  `FULLSEND_GITLAB_ROLE_*_TOKEN`. Secrets are requested as masked, but GitLab
  silently falls back to unmasked if it rejects a value (for example, one
  that doesn't meet its masking character-set rules). If any credential
  secret is unmasked, rotate it or restrict job-log visibility before running
  agents against untrusted content; revoke old personal PATs on their
  issuing accounts.

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
| Install command | `fullsend github setup` | `fullsend repos install --forge gitlab` |
| Bot identity | Per-role GitHub Apps | Role-specific project access tokens (`fullsend-poller`, `fullsend-analyst`, `fullsend-coder`); Free tier must enroll these via `--gitlab-role-token` since runtime authentication never falls back to the shared PAT or `fullsend-bot` |
| Token mint | Required for App installation tokens | Not used — GitLab uses the stored PAT |
| Event dispatch | Native Actions webhooks | Cron polling (`fullsend slash poll` / `fullsend event poll`) |
| Inference WIF | Per-repo provider from `inference provision` | Shared `gitlab-oidc` provider via `--vertex-project` |
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
rules, and the protected-default-branch `trigger` rule. Later ordinary push jobs added to that file likewise need additional
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
`schedule` only, agent = `api` only — `parent_pipeline` is not admitted), and
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

Pass `--gitlab-url` at install time to record the instance URL in
`repos.yaml` (`gitlab.url`):

```bash
fullsend repos install <group/project> \
  --gitlab-url https://gitlab.example.com \
  --vertex-project "<gcp-project>"
```

That env-var fallback (`FULLSEND_GITLAB_URL` → `GITLAB_API_URL` →
`CI_SERVER_URL`, then `gitlab.com`) is used by the agent's runtime
forge-client construction inside CI jobs — it does **not** apply to
`repos.yaml` manifest validation. `gitlab.url` must be set in the
manifest whenever GitLab repos are present; there is no default, and
`repos install`/`repos status` fail with `gitlab.url is required when
GitLab repos are present` otherwise. Set it via `--gitlab-url` at
install time, or later with:

```bash
fullsend repos set-default gitlab.url https://gitlab.example.com
```

Self-hosted instances sign OIDC tokens with their own issuer. The GCP
WIF provider's issuer must match that instance; API URL flags do not
configure WIF. If agent jobs fail token exchange, confirm the
`gitlab-oidc` provider issuer matches the GitLab instance URL.

## Next steps

* Read [Repo Management](repo-management.md) for multi-repo manifests,
  drift detection, and version upgrades.
* Read [Operations](operations.md) for day-2 updates, uninstall, and
  GitLab CI status-notification variables.
* Read the [Agents](../../agents/README.md) section to learn about the
  default agents Fullsend ships with.
