---
order: 2
---

# Getting Inference For Fullsend

The goal of this document is that you acquire a WIF provider URL to pass to the next step
of the process ([Configuring GitHub](configuring-github.md)).

Currently Fullsend only supports GCP Vertex AI inference using Workload Identity Federation (WIF).
WIF grants short-lived tokens to requesters that meet certain requirements. In the case of Fullsend
these requirements are to provide an OIDC Token signed by GitHub with their origin (org, repository
and other details). If the WIF finds the request valid, it provides a short lived token.

You may need to create a new GCP project or reuse one. The output of this process is a WIF provider
URL resembling:

```text
projects/<number>/locations/global/workloadIdentityPools/<pool-name>/providers/<provider-name>
```

Where `<number>` is the number of the GCP project. This URL may be provided to you if there is
someone handling GCP projects in your organization. Otherwise you may need to create a GCP
project and configure it yourself.

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
  Autonomous agentic development for GitHub organizations

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

## Next steps

Head over to [Configuring GitHub](configuring-github.md) to use your WIF provider URL.
