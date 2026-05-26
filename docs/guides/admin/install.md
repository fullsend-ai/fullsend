# How To Install Fullsend

## Overview

This guide explains how to install Fullsend, both in organization and repository mode.
We recommend using repository mode as it is simpler.

Note: this guide is not intended to be a reference for the installation parameters.
It is intended to a be simplified version to help you get started and that covers most
of use cases. For a detailed refernce of the install command check [/reference/install.md].

## Google Cloud Platform (GCP) Project

Fullsend needs a GCP Project to connect run the inference. Create it and then enable the following
APIs:

* [Agent Platform](https://console.cloud.google.com/apis/library/aiplatform.googleapis.com).
* [IAM Credentials](https://console.cloud.google.com/apis/library/iamcredentials.googleapis.com).
* [Cloud Resource Manager](https://console.cloud.google.com/apis/library/cloudresourcemanager.googleapis.com).

## Local Tools and CLIs

* Download and authenticate with [`gh`](https://cli.github.com/).
* Download and authenticate with [`gcloud`](https://cloud.google.com/cli).
* Download [`fullsend` CLI](https://github.com/fullsend-ai/fullsend/releases).

## Install Fullsend GitHub Applications

Install (or request the installation of) the official Fullsend GitHub applications
into your organization scoped to the repositories you want:

* [fullsend-ai-coder](https://github.com/apps/fullsend-ai-coder)
* [fullsend-ai-triage](https://github.com/apps/fullsend-ai-triage)
* [fullsend-ai-fullsend](https://github.com/apps/fullsend-ai-fullsend)
* [fullsend-ai-retro](https://github.com/apps/fullsend-ai-retro)
* [fullsend-ai-review](https://github.com/apps/fullsend-ai-review)

You can continue with the installation, but fullsend won't work until those applications
get installed.

## Export Variables (optional)

The commands on this how-to use the bash variable expansion notation to indicate
that you should provide your own values. If you want, you can export these variables
and then copy-paste the commands, otherwise you need to edit the commands before
executing them. Run the following code:

```bash
export ORG_NAME="<your-org-name>"
export REPO_NAME="<a-repository-within-that-org>"
export GCP_PROJECT="<your-gcp-project-slug>"
```

## Repository mode installation (recommended)

<!-- TODO: --mint-url and --skip-mint-check will be the default in the future so they will need to be removed from here -->
```bash
fullsend admin install $ORG_NAME/$REPO_NAME --inference-project $GCP_PROJECT_NAME --mint-url=https://fullsend-mint-gljhbkcloq-uc.a.run.app --skip-mint-check
```

This creates the appropriate secrets, variables and files in your repository.

## Organization mode installation

<!-- TODO: --mint-url and --skip-mint-check will be the default in the future so they will need to be removed from here -->
```bash
fullsend admin install $ORG_NAME --inference-project $GCP_PROJECT_NAME --mint-url=https://fullsend-mint-gljhbkcloq-uc.a.run.app --skip-mint-check --enroll-none
```

This creates the appropriate secrets, variables and files in your organization and provides.
After installing, enroll the repositories you want with:

```bash
fullsend admin enable repos $ORG_NAME $REPO_NAME
```

## Test Fullsend

By default Fullsend will:

* Triage new issues or triage on demand with `/fs-triage` on an issue.
* Implement changes for bugfixes (flagged by triage) or on demand with `/fs-code` on an issue.
* Review changes on new PRs or on demand with `/fs-review` on a PR.
* Automatically address PR feedback (for bot users) or on demand with `/fs-fix` on a PR.
* Analyze execution and discussions when a PR closes (merged or closed) or on demand with `/fs-retro` on
a PR.

## Next steps

Explore our documentation to see:

* How to customize default agents.
* How to provide skills for agents.
* How to provide custom agents.
* How to run agents locally.
* How to provide your own GitHub applications.
