---
sidebar_position: 2
---

# Getting Inference For Fullsend

> Background: [How fullsend authenticates](../user/how-fullsend-authenticates.md).

> **Before you start: who you need.** The Vertex setup on this page takes two roles
> ([Who does what](../user/how-fullsend-authenticates.md#who-does-what)):
>
> - **GCP project admin**, with IAM admin on the GCP project: enables the APIs and runs
>   `fullsend inference provision`, which needs `roles/iam.workloadIdentityPoolAdmin` and
>   `roles/resourcemanager.projectIamAdmin`. They hand over the **GCP project ID** and the
>   **WIF provider**.
> - **Repo admin**: passes those two values to `fullsend github setup`
>   ([Configuring GitHub](configuring-github.md)).
>
> If you are not the GCP project admin, send them this page and ask for those two values.

Fullsend agents can get model inference in three ways:

- **Vertex AI with Workload Identity Federation (WIF):** GCP grants short-lived tokens to your
  GitHub or GitLab jobs, with no stored key; this page covers it.
- **OpenAI Workload Identity Federation:** GPT models on the pi or codex runtime get short-lived
  OpenAI credentials; see [OpenAI Workload Identity](../infrastructure/openai-workload-identity.md).
- **An inference gateway:** an OpenAI/Anthropic-compatible gateway that your organisation runs
  holds the provider credential; see [Using an inference gateway](../user/inference-gateway.md).

The rest of this document explains how to acquire a GCP WIF provider URL for Vertex inference.
Skip it if your agents use only OpenAI; both [GitHub setup](configuring-github.md)
and [GitLab install](configuring-gitlab.md) accept no GCP inference inputs.

Fullsend supports GCP Vertex AI inference using Workload Identity Federation (WIF) on both
GitHub and GitLab. WIF grants short-lived tokens to requesters that meet certain requirements.
For GitHub, this requires an OIDC token signed by GitHub with its origin (org, repository and
other details). For GitLab, the OIDC token is signed by GitLab's built-in `id_tokens` mechanism.
If the WIF finds the request valid, it provides a short-lived token.

GPT models on the pi or codex runtime use OpenAI Workload Identity Federation instead, so an
OpenAI agent run needs no GCP credentials and stores no key on the recommended path; see
[OpenAI Workload Identity](../infrastructure/openai-workload-identity.md). Initial repository
setup can omit the GCP project and WIF provider pair. If you cannot enrol an OpenAI WIF
provider, that guide's Route C uses a `FULLSEND_OPENAI_API_KEY` repository secret; on GitHub we
recommend keeping that key behind an [inference gateway](#inference-gateway) instead.

For **GitLab repos**, create the GCP project as described below, but skip
`fullsend inference provision`, which is GitHub-only. A platform operator creates a shared
`gitlab-oidc` provider and its IAM bindings. Declare `inference.project` in
`repos.yaml` or pass `repos install --vertex-project` to write the CI/CD variables
that reference it. See [Configuring GitLab](configuring-gitlab.md#inference-setup).
If GCP cannot reach your GitLab instance over the public internet, see
[Private OIDC issuers (uploaded JWKS)](configuring-gitlab.md#private-oidc-issuers-uploaded-jwks).

You may need to create a new GCP project or reuse one. The output of this process is a WIF provider
URL resembling:

```text
projects/<number>/locations/global/workloadIdentityPools/<pool-name>/providers/<provider-name>
```

Where `<number>` is the number of the GCP project. If someone else is the GCP project admin, they
hand this URL over with the project ID. Otherwise you create and configure the GCP project
yourself.

## Prerequisites

* Download the latest [gcloud](https://docs.cloud.google.com/sdk/docs/install-sdk) CLI and
authenticate with it.
* Download the latest [fullsend](https://github.com/fullsend-ai/fullsend/releases) CLI.

## Create a GCP project

Head over to [GCloud](https://console.cloud.google.com/) and create a new project. Then
enable the following APIs:

```bash
gcloud services enable \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  aiplatform.googleapis.com \
  --project="$GCP_PROJECT"
```

**Note**: enable Anthropic's models Opus and Sonnet as well.

Then give yourself the roles `roles/iam.workloadIdentityPoolAdmin`
and `roles/resourcemanager.projectIamAdmin`.

## Configure inference in your GCP project

Run the command to configure provision for your GitHub repository:

```bash
fullsend inference provision <org>/<repo> --project <gcp-project>
```

Where `<org>/<repo>` refers to the GitHub organization and repository you want to enable inference
for, and `<gcp-project>` is your GCP project name. The output resembles:

```text
⚡ fullsend <version>
  Autonomous agentic development for Git-hosted organizations

→ Provisioning WIF for repo-scoped inference: <org>/<repo>

  • Provisioning WIF infrastructure
  ✓ WIF infrastructure ready

    WIF Provider: projects/<number>/locations/global/workloadIdentityPools/fullsend-inference/providers/gh-<org>-<repo>

    Pass this value to the GitHub setup command:
      fullsend github setup <org>/<repo> \
        --inference-project=<gcp-project> \
        --inference-wif-provider=projects/<number>/locations/global/workloadIdentityPools/fullsend-inference/providers/gh-<org>-<repo>
```

The important piece of information is the `WIF Provider` which you need to pass to the next step.

## Inference gateway

An inference gateway is an OpenAI/Anthropic-compatible endpoint that holds the upstream provider
credential, so agents on the pi, Claude Code and Codex runtimes reach their models through it.
Choose this route when your organisation runs or buys a gateway and you want no provider key
stored in the forge. `auth: oidc` is the recommended mode: direct OIDC trust (no token exchange),
where the gateway validates the job's GitHub OIDC token itself. `auth: api-key` is supported, but
use it with caution because it relies on a long-lived secret. See [Using an inference gateway](../user/inference-gateway.md) for the walkthrough and
the [operator guide](../infrastructure/inference-gateway-operator.md) for running the gateway.

## Next steps

Head over to [Configuring GitHub](configuring-github.md) to use your WIF provider URL.

For GitLab repositories, skip this provision output and follow
[Configuring GitLab](configuring-gitlab.md#inference-setup) instead — a platform operator
creates the GitLab provider, and `repos install --vertex-project` writes the CI/CD variables
that reference it. This command does not apply.

To use an inference gateway instead, see [Using an inference gateway](../user/inference-gateway.md).
