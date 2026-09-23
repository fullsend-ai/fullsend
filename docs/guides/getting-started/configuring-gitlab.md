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

* A GCP project with Vertex AI enabled, from
  [Getting Inference](getting-inference.md). GitLab does **not** use
  `fullsend inference provision` — inference credentials are written by
  `repos install --inference-project` (see [Inference Setup](#inference-setup)
  below). Unless you also pass `--inference-wif-provider` (see
  [Inference Setup](#inference-setup)), `repos install` derives the
  project number from `--inference-project` via the GCP Resource
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

> **GitLab tier:** Project access tokens require GitLab Premium or
> Ultimate **on gitlab.com**; self-managed Community Edition can create
> them without a paid tier. `repos install` also creates two pipeline
> schedules with sub-hourly cron intervals (`*/5 * * * *` and
> `2,17,32,47 * * * *`). GitLab.com Free allows schedules, but limits each
> schedule to 24 pipeline triggers per day, so the five-minute schedules
> are effectively throttled to about once per hour; this is separate from
> the project-access-token restriction. Treat `--gitlab-bot-token` and
> off-system polling below as fallbacks for when PAT provisioning or the
> resulting schedule behavior is unsuitable on your instance, not as the
> default path for every Free/CE install. When a personal token is needed,
> pass `--gitlab-bot-token`
> with a personal access token that has `api` scope, and run
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
> `gitlab.runner_tags` (see [Runner Configuration](#runner-configuration)
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
  --inference-project "<gcp-project>"
```

Where `<group/project>` is the GitLab project path (nested groups are
supported, for example `group/subgroup/project`), and `<gcp-project>` is
the GCP project from [Getting Inference](getting-inference.md).

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
* Creates a shared `fullsend-bot` project access token at Developer (30)
  access with `api` scope and stores it as the protected CI/CD variable
  `FULLSEND_FORGE_TOKEN`, then provisions the built-in role credentials.
  When those roles are ready, the same unflagged install enables `enforced`
  mode and deletes `FULLSEND_FORGE_TOKEN`; see the [CLI reference](../../cli/repos.md#gitlab-bot-token)
  for the role-credential options, emergency rollback, and protected-branch
  caveat. Missing role credentials are drift while the migration gate is
  `enforced`.
* Creates two pipeline schedules: `fullsend slash poll` (every 5 minutes)
  and `fullsend event poll` (at minutes 2, 17, 32, 47).
* Writes inference CI/CD variables when `--inference-project` is set.

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
  --inference-project "<gcp-project>" \
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
  --inference-project "<gcp-project>" \
  --runtime claude
```

See [Choose a Runtime](choosing-a-runtime.md) for what the runtimes are
and how to change the selection after setup.

### Free-tier bot token

On instances that cannot create project access tokens, pass a personal
access token for the bot identity:

```bash
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
  --inference-project "<gcp-project>" \
  --gitlab-bot-token "<bot-pat>"
```

`FULLSEND_GITLAB_BOT_TOKEN` is the equivalent environment variable.

> **Warning:** This PAT is stored as `FULLSEND_FORGE_TOKEN` and used by
> autonomous agents processing untrusted issue and merge request
> comments. Use a token from a dedicated bot account scoped to the
> target project or group — not your personal account or an admin
> PAT — since a PAT typically carries its owner's access across every
> project and group they can reach.

### Role identities and GitLab Free

Fresh installs on an instance that supports project access tokens create
separate Developer-level project tokens for the Poller, Analyst, and Coder
roles. These are genuinely different GitLab identities, so GitLab can audit
which responsibility acted and the Analyst can approve a merge request
created by the Coder. The role gate and credential variables are described in
the [role-credential contract](../../contributing/gitlab-role-credentials.md).

This automatic per-role provisioning is **not available on GitLab.com Free**
because that tier cannot create project access tokens. You can still enroll
role credentials manually with personal access tokens:

```bash
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
  --gitlab-bot-token "$BOT_PAT" \
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

### Off-system polling

`fullsend poll` is a **hidden command** — it won't appear in `fullsend
--help` — for running the same poll loop that `repos install`'s pipeline
schedules would otherwise run in-CI. Use it when schedule creation fails or
when the instance's schedule cadence is too slow, from cron on a VM, a
Kubernetes CronJob, or any scheduler with network access to your GitLab
instance. Off-system polling replaces the in-CI schedules: disable or delete
both `fullsend slash poll` and `fullsend event poll` before enabling the
external jobs, or slash commands can be dispatched twice.

```bash
export FULLSEND_FORGE_TOKEN="<bot-pat>"   # not GITLAB_TOKEN
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

`--forge gitlab` and `--fullsend-dir` are required flags. `FULLSEND_FORGE_TOKEN`
(the same bot PAT from [Free-tier bot token](#free-tier-bot-token) above,
**not** the `GITLAB_TOKEN` named in [Prerequisites](#prerequisites)) and
`FULLSEND_DISPATCH_SECRET` (the protected secret provisioned by install) must
be set in the environment. `--mode` must be `slash` or `events` for an
external replacement of the corresponding schedule. `--project` falls back
to `CI_PROJECT_PATH`, and
the pipeline-ref falls back to `CI_COMMIT_REF_NAME` then
`CI_DEFAULT_BRANCH` — one of each pair is required or the command errors.

## Inference Setup

Pass `--inference-project` so install writes `FULLSEND_GCP_PROJECT_ID`
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
have, and omits `--allowed-audiences`, so a naively adapted provider
rejects the `aud: "fullsend"` token agent jobs present. Use GitLab's
issuer, id-token claims, and an explicit allowed audience instead.

This guide is single-repo, so the default recipe below scopes trust to
the exact project being installed, using the `project_path` claim:

```bash
export GCP_PROJECT="<gcp-project>"
export GITLAB_URL="https://gitlab.com"   # or your self-hosted instance URL
export PROJECT_PATH="<group/project>"    # the exact project path being installed

gcloud iam workload-identity-pools providers create-oidc gitlab-oidc \
  --location=global \
  --workload-identity-pool=fullsend-inference \
  --issuer-uri="$GITLAB_URL" \
  --allowed-audiences="fullsend" \
  --attribute-mapping="google.subject=assertion.sub,attribute.namespace_path=assertion.namespace_path,attribute.project_path=assertion.project_path" \
  --attribute-condition="assertion.project_path == '$PROJECT_PATH'" \
  --project="$GCP_PROJECT"
```

The provider's `--issuer-uri` is a trust-boundary setting: it must exactly
match the GitLab instance that signs the `id_tokens` used by this repository.
Do not leave the `https://gitlab.com` value when installing against a
self-hosted instance, and do not point a self-hosted provider at a different
instance.

The project-path condition scopes trust to this project, but any job in that
project that can request an OIDC token with audience `fullsend` can satisfy
it. If every polling and agent job runs on protected refs, operators may
further restrict the condition with `&& assertion.ref_protected == 'true'`;
otherwise keep the project-level condition and treat all token-issuing jobs
in the project as trusted.

```bash
export PROJECT_NUMBER=$(gcloud projects describe "$GCP_PROJECT" --format='value(projectNumber)')
export WIF_PRINCIPAL="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/attribute.project_path/$PROJECT_PATH"

gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
  --role="roles/aiplatform.user" \
  --member="$WIF_PRINCIPAL" \
  --condition=None
```

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
single `PROJECT_PATH`. Installing a second repo against the same GCP
project does not require widening trust to an entire namespace — keep
exact `project_path` matches for just the repos you're installing by
OR-ing their paths in the condition and binding one principalSet per
project:

```bash
export GCP_PROJECT="<gcp-project>"
export GITLAB_URL="https://gitlab.com"   # or your self-hosted instance URL

gcloud iam workload-identity-pools providers update-oidc gitlab-oidc \
  --location=global \
  --workload-identity-pool=fullsend-inference \
  --attribute-condition="assertion.project_path == 'group/project-a' || assertion.project_path == 'group/project-b'" \
  --project="$GCP_PROJECT"

export PROJECT_NUMBER=$(gcloud projects describe "$GCP_PROJECT" --format='value(projectNumber)')

for PROJECT_PATH in "group/project-a" "group/project-b"; do
  gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
    --role="roles/aiplatform.user" \
    --member="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/attribute.project_path/$PROJECT_PATH" \
    --condition=None
done
```

Repeat the `--attribute-condition` update (adding another `||` clause) and
the IAM binding loop each time you install another repo. This keeps trust
scoped to exactly the repos you've installed, unlike the namespace-wide
option below. Prefer the namespace-wide alternative only when the repo set
isn't enumerable in advance.

### Authorizing a group or group tree (alternative)

> **Warning:** this widens trust beyond the single repo above — every
> project in the namespace(s) you allow can obtain the `id_tokens` OIDC
> token, federate through this provider, and receive
> `roles/aiplatform.user`. Prefer the per-project recipe above unless
> you are deliberately provisioning inference for many repos in the
> same namespace.

To authorize every project under one immediate parent namespace
instead of a single project, use the `namespace_path` claim. GitLab's
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
  --attribute-mapping="google.subject=assertion.sub,attribute.namespace_path=assertion.namespace_path,attribute.project_path=assertion.project_path" \
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
  --inference-project "<gcp-project>" \
  --inference-wif-provider "projects/<number>/locations/global/workloadIdentityPools/fullsend-inference/providers/gitlab-oidc"
```

The GCP project still needs the Vertex AI APIs enabled as described in
[Getting Inference](getting-inference.md). `--inference-region` defaults
to `global` when `--inference-project` is set.

## Runner Configuration

Agent jobs run on GitLab CI using the `fullsend-runner` image and the
tags embedded in the scaffold. Empty tags (`[]`) match untagged runners;
tagged jobs only match runners that carry those tags.

The runner VM scripts default to tag `fullsend-gitlab-runner`. Set that
tag in the manifest **before your first `repos install`** so the initial
install already embeds it in the scaffold, with no second install
needed:

```bash
fullsend repos set-default gitlab.runner_tags fullsend-gitlab-runner
fullsend repos install <group/project> \
  --forge gitlab \
  --gitlab-url https://gitlab.com \
  --inference-project "<gcp-project>"
```

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
that tag. For provisioning runner VMs on OpenShift Virtualization or
GCE, see the
[GitLab Runner VM](https://github.com/fullsend-ai/fullsend/blob/main/hack/gitlab-runner-vm/README.md)
README.

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
  [Off-system polling](#off-system-polling) above.
* **Pipeline schedules** — Settings → CI/CD → Pipeline schedules shows
  `fullsend slash poll` and `fullsend event poll`, both active. On
  GitLab.com Free, the schedules may run at most 24 times per day; verify
  the observed cadence is acceptable, or use off-system `fullsend poll`
  instead when five-minute pickup is required.
* **Access tokens** — On Premium/Ultimate or self-managed instances where
  project access tokens are available, Settings → Access Tokens shows
  `fullsend-bot`, `fullsend-poller`, `fullsend-analyst`, and
  `fullsend-coder` (plus any `fullsend-role-*` tokens). On GitLab.com Free
  with `--gitlab-bot-token`, expect the dedicated PAT owner's username
  instead; no project access token is created.
* **CI/CD variables** — `FULLSEND_FORGE_TOKEN`, `FULLSEND_DISPATCH_SECRET`,
  `FULLSEND_GCP_PROJECT_ID`, and `FULLSEND_GCP_WIF_PROVIDER` exist and are
  protected. Role-aware installs also provision
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
comment. On a GitLab.com Free install using only `--gitlab-bot-token`, the
dedicated PAT owner's username posts it instead. The `fullsend-bot` identity
is expected only on the shared-token path or when role provisioning was not
completed. If `repos install` couldn't create the in-CI schedules,
or if their cadence is too slow for the instance, run `fullsend poll` on your
external scheduler instead — see [Off-system polling](#off-system-polling)
— and check its output for the same comment.

## Differences from GitHub

| Topic | GitHub | GitLab |
|---|---|---|
| Install command | `fullsend github setup` | `fullsend repos install --forge gitlab` |
| Bot identity | Per-role GitHub Apps | Role-specific project access tokens (`fullsend-poller`, `fullsend-analyst`, `fullsend-coder`); `fullsend-bot` or a dedicated PAT username on Free/shared-token fallback |
| Token mint | Required for App installation tokens | Not used — GitLab uses the stored PAT |
| Event dispatch | Native Actions webhooks | Cron polling (`fullsend slash poll` / `fullsend event poll`) |
| Inference WIF | Per-repo provider from `inference provision` | Shared `gitlab-oidc` provider via `--inference-project` |
| CI entrypoint | `.github/workflows/fullsend.yaml` | `.gitlab/ci/fullsend-*.yml` included from `.gitlab-ci.yml` |

### `workflow:` block and `auto_cancel`

Install merges an include, `poll` / `agent` stages, and workflow rules into
the existing `.gitlab-ci.yml`. Installs from before the removal of the empty
`dispatch` stage may still contain that legacy stage; converge and uninstall
clean it up. Install does not overwrite unrelated jobs.

When an existing, non-empty file already has a `workflow:` block, fullsend sets
`workflow.auto_cancel.on_new_commit: none` if that key is missing, and does not
overwrite an existing value. It also adds the protected-ref `schedule`/`api`
rules if the block has no `rules:` key. Because `workflow.rules` is an
allowlist, ordinary push pipelines stop running in that case unless the block
already has a matching rule; add a catch-all/push rule (or an explicit
`when: always` rule) before installing, or remove the name-only `workflow:`
block so fullsend can leave it absent. When an existing, non-empty file has no
`workflow:` block, fullsend leaves it absent so push-triggered pipelines keep
running. For a missing or empty `.gitlab-ci.yml`, fullsend instead writes a
fullsend-owned `workflow:` block with a name, `auto_cancel.on_new_commit:
none`, and protected-ref `schedule`/`api` rules. Later ordinary push jobs
added to that file likewise need additional `workflow.rules` (or an explicit
`when: always` rule), or GitLab will skip them.

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
  --inference-project "<gcp-project>"
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
