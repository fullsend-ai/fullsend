---
order: 4
---

# Installing Fullsend in Organization Mode

The goal of this document is that you install Fullsend for your whole
GitHub organization, so different repositories share inference and infrastructure.

**Note**: this document assumes you already read and used [Getting Inference](getting-inference.md)
and [Configuring GitHub](configuring-github.md). If that is not the case, read those guides first.

## Differences With Repository Installation

When you install Fullsend in Organization Mode there are a few key differences:

* `fullsend inference provision` is executed once for the whole organization.
* GitHub workflows are executed in a `.fullsend` repository within the organization.
* Individual repositories need to be enrolled to be able to execute Fullsend.


## Getting Inference For The Organization

Similar to the command ran at [Getting Inference](getting-inference.md) you need to run:

```bash
fullsend inference provision <org> --project <gcp-project>
```

Where `<org>` is the GitHub organization and `<gcp-project>` is your GCP project.

```text
⚡ fullsend <version>
  Autonomous agentic development for GitHub organizations

→ Provisioning WIF for org-scoped inference: <org>

  • Provisioning WIF infrastructure
  ✓ WIF infrastructure ready

    WIF Provider: projects/<number>/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc

    Pass this value to the GitHub setup command:
      fullsend github setup <org> \
        --inference-project=<gcp-project> \
        --inference-wif-provider=projects/<number>/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc
```

Note down the `WIF Provider` URL which is used in the next step to configure the organization.

## Configure GitHub Apps

If you previously ran the [Configuring GitHub](configuring-github.md) guide the Fullsend apps you
installed are configured just for a single repository. Change the permissions so they can access
all repositories or `.fullsend` (not created yet) and any other repository you want to enable
Fullsend for.

## Configure GitHub

Now similar to the command executed on [Configuring GitHub](configuring-github.md), execute:

```bash
fullsend github setup <org> \
  --inference-project <gcp-project> \
  --inference-wif-provider <wif-provider-url>
```

Where `<org>` is the GitHub organization, `<gcp-project>` is your GCP project and `<wif-provider-url>` is
the URL from the previous step.

This command creates a `.fullsend` repository in your organization and starts a workflow that enrolls
repositories if needed.


## Enroll Repositories

After installing enroll repositories by running:

```bash
fullsend github enroll <org> <repo> [<repo>...]
```

This changes the `config.yaml` present in the `.fullsend` repository and that starts a workflow there.
The workflow adds or removes the `.github/workflows/fullsend.yaml` of the repositories.

## Testing Fullsend

After merging the `.github/workflows/fullsend.yaml` workflow in the enrolled repositories, open
a new issue or comment `/fs-triage` in an issue of one of the enrolled repositories to see Fullsend in
action.


## Next Steps

* Read the [Default Agents](../../agents/README.md) section to learn about the default agents Fullsend
ships with.
* Explore other sections of this documentation for more information.
