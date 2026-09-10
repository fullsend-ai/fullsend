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
  `repos install --inference-project` (see [Inference setup](#inference-setup)
  below).
* Download the latest [fullsend](https://github.com/fullsend-ai/fullsend/releases) CLI.
* A GitLab personal or group access token with `api` scope and
  Maintainer (or Owner) role on the target project. The CLI does **not**
  fall back to `glab auth token`. Set `GITLAB_TOKEN` in the environment
  before running setup, or pass `--gitlab-token` on each `fullsend repos`
  command.
* A GitLab runner that can execute agent jobs. Shared GitLab.com runners
  are usually not enough (polling consumes CI minutes). See
  [Runner configuration](#runner-configuration).

> **GitLab tier:** Project access tokens require GitLab Premium or
> Ultimate **on gitlab.com**; self-managed Community Edition can create
> them without a paid tier. `repos install` also creates two pipeline
> schedules with sub-hourly cron intervals (`*/5 * * * *` and
> `2,17,32,47 * * * *`); **GitLab.com Free** typically rejects schedules
> below its 60-minute minimum, so install will fail to create them
> there — self-managed Community Edition does not carry that documented
> restriction, so install usually succeeds on it. Treat
> `--gitlab-bot-token` and off-system polling below as fallbacks for
> when schedule creation or PAT provisioning actually fails on your
> instance (expected on GitLab.com Free), not as the default path for
> every Free/CE install. When they're needed, pass `--gitlab-bot-token`
> with a personal access token that has `api` scope, and run
> `fullsend poll` on an external scheduler (a VM cron job or Kubernetes
> CronJob) instead of relying on in-CI pipeline schedules — see
> [Off-system polling](#off-system-polling) below and
> [ADR 0067](../../ADRs/0067-gitlab-cron-polling-event-dispatch.md) for
> the design. Self-hosted runners are required on GitLab.com Free.

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
* Creates a `fullsend-bot` project access token (Maintainer, `api` scope)
  and stores it as the protected CI/CD variable `FULLSEND_FORGE_TOKEN`.
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

### Off-system polling

`fullsend poll` is a **hidden command** — it won't appear in `fullsend
--help` — for running the same poll loop that `repos install`'s pipeline
schedules would otherwise run in-CI. Use it when schedule creation fails
(the expected case on GitLab.com Free; see the [GitLab tier
note](#prerequisites) above), from cron on a VM, a Kubernetes CronJob, or
any scheduler with network access to your GitLab instance:

```bash
export FULLSEND_FORGE_TOKEN="<bot-pat>"   # not GITLAB_TOKEN
export CI_DEFAULT_BRANCH="main"           # or set CI_COMMIT_REF_NAME

fullsend poll \
  --forge gitlab \
  --fullsend-dir /path/to/.fullsend \
  --project "<group/project>" \
  --gitlab-url https://gitlab.example.com   # omit for gitlab.com
```

`--forge gitlab` and `--fullsend-dir` are required flags. `FULLSEND_FORGE_TOKEN`
(the same bot PAT from [Free-tier bot token](#free-tier-bot-token) above,
**not** the `GITLAB_TOKEN` named in [Prerequisites](#prerequisites)) must be
set in the environment. `--project` falls back to `CI_PROJECT_PATH`, and the
pipeline-ref falls back to `CI_COMMIT_REF_NAME` then `CI_DEFAULT_BRANCH` —
one of each pair is required or the command errors.

## Inference setup

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
at runtime even though install succeeds.

To create it, add a GitLab-specific provider to the same
`fullsend-inference` pool described in
[Advanced setup → Custom inference WIF configuration](../infrastructure/advanced-setup.md#custom-inference-wif-configuration).
The GitHub recipe there does not carry over as-is: it points the issuer
at GitHub, maps `assertion.repository*` claims that GitLab tokens don't
have, and omits `--allowed-audiences`, so a naively adapted provider
rejects the `aud: "fullsend"` token agent jobs present. Use GitLab's
issuer, id-token claims, and an explicit allowed audience instead:

```bash
export GCP_PROJECT="<gcp-project>"
export GITLAB_URL="https://gitlab.com"   # or your self-hosted instance URL
export GROUP_PATH="<group>"              # e.g. "my-group" or "my-group/subgroup"

gcloud iam workload-identity-pools providers create-oidc gitlab-oidc \
  --location=global \
  --workload-identity-pool=fullsend-inference \
  --issuer-uri="$GITLAB_URL" \
  --allowed-audiences="fullsend" \
  --attribute-mapping="google.subject=assertion.sub,attribute.namespace_path=assertion.namespace_path,attribute.project_path=assertion.project_path" \
  --attribute-condition="assertion.namespace_path == '$GROUP_PATH'" \
  --project="$GCP_PROJECT"
```

```bash
export PROJECT_NUMBER=$(gcloud projects describe "$GCP_PROJECT" --format='value(projectNumber)')
export WIF_PRINCIPAL="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/attribute.namespace_path/$GROUP_PATH"

gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
  --role="roles/aiplatform.user" \
  --member="$WIF_PRINCIPAL" \
  --condition=None
```

Create the `fullsend-inference` pool first if it doesn't already exist
(see the Advanced setup steps linked above). Agent jobs obtain a GitLab
`id_tokens` OIDC token (`FULLSEND_ID_TOKEN`, audience `fullsend`) and
exchange it through this provider.

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

## Runner configuration

Agent jobs run on GitLab CI using the `fullsend-runner` image and the
tags embedded in the scaffold. Empty tags (`[]`) match untagged runners;
tagged jobs only match runners that carry those tags.

The runner VM scripts default to tag `fullsend-gitlab-runner`. Set that
tag in the manifest, then re-run install so the scaffold picks it up:

```bash
fullsend repos set-default gitlab.runner_tags fullsend-gitlab-runner
fullsend repos install -f repos.yaml
```

The target project (or its group) must have a runner registered with
that tag. For provisioning runner VMs on OpenShift Virtualization or
GCE, see the
[GitLab Runner VM](https://github.com/fullsend-ai/fullsend/blob/main/hack/gitlab-runner-vm/README.md)
README.

## Verifying the installation

Compare the manifest against the project:

```bash
fullsend repos status -f repos.yaml
```

Confirm:

* Status is `installed` with `DRIFT` `none`.
* **Pipeline schedules** — Settings → CI/CD → Pipeline schedules shows
  `fullsend slash poll` and `fullsend event poll`, both active.
* **Bot token** — Settings → Access Tokens shows `fullsend-bot` (skipped
  on Free when you passed `--gitlab-bot-token`).
* **CI/CD variables** — `FULLSEND_FORGE_TOKEN`, `FULLSEND_GCP_PROJECT_ID`,
  and `FULLSEND_GCP_WIF_PROVIDER` exist and are protected. All three,
  including `FULLSEND_FORGE_TOKEN`, are also requested as masked, but
  GitLab silently falls back to unmasked if it rejects a value (for
  example, one that doesn't meet its masking character-set rules) — treat
  masking as best-effort rather than a pass/fail check.

## Testing Fullsend

After the scaffold merge request is merged (or after a `--direct`
install), comment `/fs-triage` on an issue. GitLab has no issue-comment
webhook equivalent — the slash-command schedule polls every 5 minutes
on Premium/Ultimate. Visit **Build → Pipelines** to watch the poll and
agent jobs. In some minutes the `fullsend-bot` identity should post a
comment on the issue. On instances where `repos install` couldn't create
the in-CI schedules (expected on GitLab.com Free; see the [GitLab tier
note](#prerequisites) under Prerequisites), run `fullsend poll` on your
external scheduler instead — see [Off-system polling](#off-system-polling)
— and check its output for the same comment.

## Differences from GitHub

| Topic | GitHub | GitLab |
|---|---|---|
| Install command | `fullsend github setup` | `fullsend repos install --forge gitlab` |
| Bot identity | Per-role GitHub Apps | `fullsend-bot` project access token (`FULLSEND_FORGE_TOKEN`) |
| Token mint | Required for App installation tokens | Not used — GitLab uses the stored PAT |
| Event dispatch | Native Actions webhooks | Cron polling (`fullsend slash poll` / `fullsend event poll`) |
| Inference WIF | Per-repo provider from `inference provision` | Shared `gitlab-oidc` provider via `--inference-project` |
| CI entrypoint | `.github/workflows/fullsend.yaml` | `.gitlab/ci/fullsend-*.yml` included from `.gitlab-ci.yml` |

### `workflow:` block and `auto_cancel`

Install merges an include, `dispatch` / `poll` / `agent` stages, and
workflow rules into the existing `.gitlab-ci.yml`. It does not overwrite
unrelated jobs.

When the file already has a `workflow:` block, fullsend sets
`workflow.auto_cancel.on_new_commit: none` if that key is missing, and
does not overwrite an existing value. When no `workflow:` block exists,
fullsend leaves it absent so push-triggered pipelines keep running.

Repos with `on_new_commit: interruptible` (or other non-`none` values)
may see agent pipelines canceled by later commits. Fullsend needs
`on_new_commit: none` for reliable agent runs. If pipelines disappear
unexpectedly, set that value in the root `workflow:` block.

## Self-hosted GitLab instances

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
