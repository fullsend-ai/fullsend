# Configuring GitHub For Fullsend

The goal of this document is that you configure Fullsend for your GitHub repository.

## Prerequisites

* You have your WIF provider URL from [Getting Inference](getting-inference.md).
* Download the latest [fullsend](https://github.com/fullsend-ai/fullsend/releases) CLI.
* Download the latest [gh](https://cli.github.com/) CLI and authenticate with it.

## Installing GitHub Applications

In order to use Fullsend install the following applications to your organization
and provide them permissions to the repository you want to install Fullsend to.

| Role | Installation URL |
|------|-----------------|
| fullsend | <https://github.com/apps/fullsend-ai-fullsend/installations/new> |
| triage | <https://github.com/apps/fullsend-ai-triage/installations/new> |
| coder | <https://github.com/apps/fullsend-ai-coder/installations/new> |
| review | <https://github.com/apps/fullsend-ai-review/installations/new> |
| retro | <https://github.com/apps/fullsend-ai-retro/installations/new> |
| prioritize | <https://github.com/apps/fullsend-ai-prioritize/installations/new> |

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

## Testing Fullsend

After installing open a new issue or comment `/fs-triage` in an open issue. Then visit the
Actions tab to see the Fullsend workflow in action. In some minutes the
`fullsend-ai-triage` bot should post a comment in the issue.

## Jira integration: private repository requirement

> **⚠ Warning:** If you plan to connect a **private Jira instance** to this repository, the repository **must be private**. Refinement pipelines produce JSONL workflow artifacts that contain Jira issue content (titles, descriptions, comments, custom fields). In a public repository, these artifacts are downloadable by anyone, exposing private Jira data. See [Private repositories — Private Jira instances and public repositories](../infrastructure/private-repositories.md#private-jira-instances-and-public-repositories) for configuration details and the full risk analysis.

## Next steps

* Read [Organization installation mode](org-mode.md) to learn how to share GCP project with other repositories
within your GitHub organization.
* Read the [Default Agents](../../agents/README.md) section to learn about the default agents Fullsend
ships with.
* Explore other sections of this documentation for more information.
