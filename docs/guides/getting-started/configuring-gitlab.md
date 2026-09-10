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

> **GitLab tier:** Project access tokens and 5-minute pipeline schedules
> require GitLab Premium or Ultimate. On Free or Community Edition, pass
> `--gitlab-bot-token` with a personal access token that has `api` scope,
> and expect slower slash-command pickup (GitLab Free's schedule minimum
> is 60 minutes). Self-hosted runners are required on Free.

## Installing Fullsend

Run the command:

```bash
fullsend repos install <group/project> \
  --forge gitlab \
  --inference-project "<gcp-project>"
```

Where `<group/project>` is the GitLab project path (nested groups are
supported, for example `group/subgroup/project`), and `<gcp-project>` is
the GCP project from [Getting Inference](getting-inference.md).

On gitlab.com you can omit `--gitlab-url`. For a self-hosted instance, add
`--gitlab-url https://gitlab.example.com` (this also implies
`--forge=gitlab` when no forge is specified).

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
  --inference-project "<gcp-project>" \
  --gitlab-bot-token "<bot-pat>"
```

`FULLSEND_GITLAB_BOT_TOKEN` is the equivalent environment variable.

## Inference setup

Pass `--inference-project` so install writes `FULLSEND_GCP_PROJECT_ID`
and `FULLSEND_GCP_WIF_PROVIDER`. GitLab uses a shared WIF provider named
`gitlab-oidc` in the `fullsend-inference` pool — not the per-repo GitHub
providers created by `fullsend inference provision`. Agent jobs obtain a
GitLab `id_tokens` OIDC token (`FULLSEND_ID_TOKEN`, audience `fullsend`)
and exchange it through that provider.

If a platform operator already provisioned a WIF provider, pass the full
resource name instead of relying on the default `gitlab-oidc` path:

```bash
fullsend repos install <group/project> \
  --forge gitlab \
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
  and `FULLSEND_GCP_WIF_PROVIDER` exist and are protected (inference
  secrets are also masked).

## Testing Fullsend

After the scaffold merge request is merged (or after a `--direct`
install), comment `/fs-triage` on an issue. GitLab has no issue-comment
webhook equivalent — the slash-command schedule polls every 5 minutes
on Premium (60 minutes on Free). Visit **Build → Pipelines** to watch
the poll and agent jobs. In some minutes the `fullsend-bot` identity
should post a comment on the issue.

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
fullsend repos install group/project \
  --gitlab-url https://gitlab.example.com \
  --inference-project "<gcp-project>"
```

When the manifest has no URL, the CLI falls back through
`FULLSEND_GITLAB_URL` → `GITLAB_API_URL` → `CI_SERVER_URL`, then
defaults to `gitlab.com`. You can also set the URL later:

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
