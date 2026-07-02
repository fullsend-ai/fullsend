---
sidebar_position: 4
---

# Per-Org Mode

> **Planned deprecation.** Per-org installation mode will be deprecated in favor of per-repo installation ([ADR 0044](../../ADRs/0044-deprecate-per-org-installation-mode.md)). New installations should use the [per-repo Getting Started guides](README.md). Existing per-org installations continue to work and are fully supported during the transition.

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

Organization mode also requires the `fullsend` dispatch app, which handles
cross-repo event routing. Install it at
<https://github.com/apps/fullsend-ai-fullsend/installations/new> and grant it
access to `.fullsend` and any enrolled repositories.

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


## Day-2 Operations

### Managing repository enrollment

After installation, you can enroll or unenroll repositories at any time.

#### Enable repositories

To enroll specific repositories:

```bash
fullsend github enroll "$ORG_NAME" repo-a repo-b
```

To enroll all repositories:

```bash
fullsend github enroll "$ORG_NAME" --all
```

The enroll command:
- Updates `config.yaml` in the `.fullsend` repository
- Triggers the `repo-maintenance` workflow to create enrollment PRs
- Validates that repositories exist in the organization before making changes

#### Disable repositories

To unenroll specific repositories:

```bash
fullsend github unenroll "$ORG_NAME" repo-a repo-b
```

To unenroll all repositories:

```bash
fullsend github unenroll "$ORG_NAME" --all
```

The `--all` flag prompts for confirmation — you must type the exact organization name when prompted. To skip the confirmation prompt (e.g., in automated scripts):

```bash
fullsend github unenroll "$ORG_NAME" --all --yolo
```

The unenroll command:
- Updates `config.yaml` to mark repositories as disabled
- Triggers the `repo-maintenance` workflow to create unenrollment PRs
- Warns (but does not reject) repository names not found in the config, allowing safe cleanup of deleted repos
- Does not delete existing shim workflows (merge the unenrollment PR to remove them)

#### Merging enrollment PRs

Each enrolled or unenrolled repository will have an open PR adding or removing the agent workflow files (`.github/workflows/fullsend.yaml`). Review and merge these PRs to complete the change.

### Syncing workflow templates

After upgrading the fullsend CLI, update workflow templates across all enrolled repositories:

```bash
fullsend github sync-scaffold "$ORG_NAME"
```

### Checking status

Inspect the GitHub-side installation state for an organization:

```bash
fullsend github status "$ORG_NAME"
```

Reports on: config repo presence, workflow files, org variables, inference secrets, and enrollment state. This is a read-only operation — it makes no changes.

> **Note:** `github status` checks GitHub-side configuration only. To check GCP-side WIF health, a GCP administrator can run `fullsend inference status` (see the [standalone commands](operations.md#standalone-commands) table).

### Uninstalling

#### GitHub-only uninstall

Remove fullsend GitHub configuration from an organization:

```bash
fullsend github uninstall "$ORG_NAME"
```

This removes the `.fullsend` config repo, org variables (`FULLSEND_MINT_URL`), and org secrets (`FULLSEND_DISPATCH_TOKEN`). It also lists any installed GitHub Apps and provides links for manual deletion. Add `--yolo` to skip the confirmation prompt.

> **Note:** `github uninstall` only removes GitHub-side configuration. GCP resources (mint, WIF, PEM secrets) are managed separately via `fullsend mint unenroll` and `fullsend inference deprovision` commands by the GCP administrator.

#### Full org uninstall

To tear down the entire fullsend installation (GitHub + GCP), coordinate between roles:

| Step | Role | Command |
|------|------|---------|
| 1 | GitHub Maintainer | `fullsend github uninstall "$ORG_NAME"` |
| 2 | GCP Admin (Inference) | `fullsend inference deprovision "$ORG_NAME"` |
| 3 | GCP Admin (Mint) | `fullsend mint unenroll "$ORG_NAME"` |

Each command prompts for confirmation. Add `--yolo` to skip prompts. See the [standalone commands](operations.md#standalone-commands) table for details on each command.

## Next Steps

* Read the [Agents](../../agents/README.md) section to learn about the default agents Fullsend
ships with.
* Explore other sections of this documentation for more information.
