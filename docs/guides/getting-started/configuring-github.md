---
sidebar_position: 3
---

# Configuring GitHub For Fullsend

The goal of this document is that you configure Fullsend for your GitHub repository.

## Prerequisites

* Your org or repo is enrolled in a fullsend token mint service (see [Getting Started](README.md) step 1).
* You have your WIF provider URL from [Getting Inference](getting-inference.md).
* Download the latest [fullsend](https://github.com/fullsend-ai/fullsend/releases) CLI.
* Download the latest [gh](https://cli.github.com/) CLI and authenticate with it.

### Token resolution

The `fullsend` CLI resolves a GitHub token in this order:

1. `GH_TOKEN` environment variable
2. `GITHUB_TOKEN` environment variable
3. `gh auth token` (GitHub CLI)

For most organizations, the token from `gh auth login` works. However, if
your organization restricts personal access token types (Settings → Personal
access tokens → Restrict access via PAT type), `gh auth login` may produce a
token that GitHub rejects with a 403 error.

In that case, create a **fine-grained personal access token** at
<https://github.com/settings/personal-access-tokens/new> scoped to the
target repository with these permissions:

| Permission | Level | Why |
|---|---|---|
| Contents | Read and write | Commits `.fullsend/config.yaml` and scaffold files |
| Workflows | Read and write | Writes/updates files under `.github/workflows/` |
| Secrets | Read and write | Sets `FULLSEND_GCP_PROJECT_ID` / `FULLSEND_GCP_WIF_PROVIDER` |
| Variables | Read and write | Sets `FULLSEND_MINT_URL` / `FULLSEND_GCP_REGION` |
| Metadata | Read-only | GitHub-required baseline |
| Pull requests | Read and write | Only needed without `--direct` |

Export it before running setup so it takes priority:

```bash
export GH_TOKEN=github_pat_...
fullsend github setup <org>/<repo> ...
```

## Installing GitHub Applications

Install the following agent applications to your organization
and provide them permissions to the repository you want to install Fullsend to.

| Role | Installation URL |
|------|-----------------|
| triage | <https://github.com/apps/fullsend-ai-triage/installations/new> |
| coder | <https://github.com/apps/fullsend-ai-coder/installations/new> |
| review | <https://github.com/apps/fullsend-ai-review/installations/new> |
| retro | <https://github.com/apps/fullsend-ai-retro/installations/new> |
| prioritize | <https://github.com/apps/fullsend-ai-prioritize/installations/new> |

> **Note:** The `fullsend` dispatch app (`fullsend-ai-fullsend`) is only
> required for [organization-mode](org-mode.md) installations. Per-repo
> mode uses the repository's own shim workflow for dispatch and does not
> need the `fullsend` app.

> **Note:** Installing a subset of GitHub Apps does **not** automatically
> limit which agents are active. You must also pass the `--agents` flag
> (see below) to match the set of apps you installed. For example, if you
> only install the triage and review apps, pass `--agents triage,review`
> when running setup.

## Configuring GitHub

Run the command:

```bash
fullsend github setup <org>/<repo> \
  --inference-project "<gcp-project>" \
  --inference-wif-provider "<wif-provider-url>"
```

Where `<org>/<repo>` refers to the GitHub organization and repository you want to enable inference
for, `<gcp-project>` is your GCP project name, and `<wif-provider-url>` is the WIF Provider URL
created at [Getting Inference](getting-inference.md).

The command creates files, secrets and variables in your repository.

### Enabling a subset of agents

By default, the setup command configures all available agent roles. To enable
only specific agents, pass the `--agents` flag with a comma-separated list of
roles:

```bash
fullsend github setup <org>/<repo> \
  --inference-project "<gcp-project>" \
  --inference-wif-provider "<wif-provider-url>" \
  --agents triage,review
```

Only the listed agents will be configured. Make sure you have installed the
corresponding GitHub Apps for each agent you enable (see the table above).

For the full list of setup flags, see the
[CLI reference](../../cli/github.md#flags).

## Testing Fullsend

After installing open a new issue or comment `/fs-triage` in an open issue. Then visit the
Actions tab to see the Fullsend workflow in action. In some minutes the
`fullsend-ai-triage` bot should post a comment in the issue.

## Next steps

* Read [Organization installation mode](org-mode.md) to learn how to share GCP project with other repositories
within your GitHub organization.
* Read the [Agents](../../agents/README.md) section to learn about the default agents Fullsend
ships with.
* Explore other sections of this documentation for more information.
