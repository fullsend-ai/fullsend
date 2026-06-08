# How to install fullsend

This guide walks through installing fullsend for a GitHub organization or a single repository.

## Choose your installation mode

Before running any install commands, decide whether you need **per-org** or **per-repo** mode:

| Mode | Command format | Best for | Creates `.fullsend` repo? |
|------|---------------|----------|--------------------------|
| **Per-org** | `fullsend admin install ORG` | Orgs where one admin manages fullsend for all repos | Yes |
| **Per-repo** | `fullsend admin install ORG/REPO` | Teams in shared orgs, single-repo onboarding, independent setups | No |

**Use per-repo mode** if any of these apply:
- Multiple independent teams share the same GitHub organization
- You only want fullsend on specific repositories without org-wide configuration
- You do not have (or do not want to use) org-level admin access

→ Skip to [Per-repo installation](#per-repo-installation)

**Use per-org mode** if:
- A single admin manages fullsend across the entire organization
- You want centralized configuration via the `.fullsend` config repo
- You plan to enroll many repositories under unified management

→ Continue below to choose your setup path

## Choose your setup path

Your setup path depends on what GCP infrastructure is already in place and how much control you need. Most users follow the **end-user path** — your organization is enrolled in a hosted mint service, and you provision inference access and configure GitHub yourself. This is the fastest way to get started.

| Path | When to use | What you need |
|------|-------------|---------------|
| **[End-user setup](#end-user-setup)** | Mint service is hosted for you (most common) | GCP project for inference + GitHub org access |
| **[GitHub-only setup](github-setup.md)** | Both GCP inference and mint are pre-provisioned | GitHub org access + GCP config values from your admin (no GCP credentials needed) |
| **[All-in-one admin install](#all-in-one-admin-install)** | You manage everything — GCP mint, inference, and GitHub | Full GCP + GitHub access |

> **Mint service note:** The token mint is fully self-hostable, but most users currently use the mint service hosted by the fullsend team. Work is in progress to offer this as a secure, trusted public service — reducing the need for per-org enrollment. See [Mint service administration](../infrastructure/mint-administration.md) for self-hosting details.

---

## End-user setup

This is the standard path for organizations using a hosted mint service. You provision GCP inference access and configure GitHub — the mint service admin handles enrollment.

### Prerequisites

- **From your mint service admin:** Before starting, confirm that your organization is enrolled in the hosted mint and obtain the token mint URL (the HTTPS endpoint for OIDC token exchange). This is a blocking prerequisite — contact your mint admin first.
- **GitHub organization** with admin access
- **GitHub CLI** (`gh`) authenticated — no special scopes are needed upfront. The installer runs a preflight check and tells you exactly which scopes are missing before making any changes. When prompted, run the `gh auth refresh -s <scopes>` command it suggests.

  > **Note on scope breadth:** `gh auth` scopes apply to *every* organization your account belongs to — GitHub does not support per-org scoping for classic OAuth tokens. If that is a concern, create a [fine-grained personal access token](https://github.com/settings/tokens?type=beta) scoped to the target organization and export it as `GH_TOKEN` before running the installer.

- **fullsend CLI** — download the latest binary from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases)
- **GCP project** with the following APIs enabled:

  ```bash
  gcloud services enable \
    iam.googleapis.com \
    cloudresourcemanager.googleapis.com \
    aiplatform.googleapis.com \
    --project="$GCP_PROJECT"
  ```

- **GCP IAM roles** for inference provisioning — the user authenticates via ADC (`gcloud auth application-default login`) and needs:

  | Role | What it covers |
  |------|----------------|
  | `roles/iam.workloadIdentityPoolAdmin` | Create WIF pool and provider for GitHub Actions OIDC authentication |
  | `roles/resourcemanager.projectIamAdmin` | Grant `roles/aiplatform.user` to WIF principals via project IAM policy |

  An administrator can grant the required roles:

  ```bash
  export GCP_PROJECT="my-project-id"
  export USER_EMAIL="alice@example.com"

  for ROLE in \
    roles/iam.workloadIdentityPoolAdmin \
    roles/resourcemanager.projectIamAdmin; do
    gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
      --member="user:$USER_EMAIL" \
      --role="$ROLE"
  done
  ```

### Step 1: Request mint enrollment

Contact your mint service admin and request enrollment for your GitHub organization. They will run `fullsend mint enroll <your-org>` on the mint project (see [Mint service administration](../infrastructure/mint-administration.md)). Once enrolled, they will provide you with the mint URL.

### Step 2: Provision inference access

Provision [Workload Identity Federation (WIF)](https://cloud.google.com/iam/docs/workload-identity-federation) infrastructure and grant Agent Platform access in your GCP project:

```bash
export ORG_NAME="<your-github-org>"
export GCP_PROJECT="<your-gcp-project>"

fullsend inference provision "$ORG_NAME" \
  --project "$GCP_PROJECT"
```

This creates a WIF pool (`fullsend-inference`), an OIDC provider (`github-oidc`), and grants `roles/aiplatform.user` to the WIF principal — allowing GitHub Actions workflows to authenticate and call Agent Platform models. The command is idempotent and safe to re-run.

Note the WIF provider resource name printed by the command — you will need it for the next step.

### Step 3: Set up GitHub

Configure your GitHub organization with the mint URL and inference settings:

```bash
fullsend github setup "$ORG_NAME" \
  --mint-url="<MINT_URL>" \
  --inference-project "$GCP_PROJECT" \
  --inference-wif-provider "<WIF_PROVIDER_FROM_STEP_2>"
```

This creates the `.fullsend` config repository, installs GitHub Apps (opens browser windows for each agent role), configures org-level variables and secrets, and prompts you to enroll repositories.

The `--inference-region` flag defaults to `global` for the broadest model availability. For a list of all available regions, see the [Agent Platform documentation](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/partner-models/claude/use-claude).

See [Setting up with pre-provisioned infrastructure](github-setup.md) for the full `github setup` reference, including per-repo mode, `--skip-app-setup`, and day-2 operations.

### Step 4: Merge enrollment PRs

If you enrolled repositories during setup, the installer dispatches a workflow that creates an enrollment PR in each enrolled repo. These PRs add a shim workflow (`.github/workflows/fullsend.yaml`) that wires events to the agent pipeline.

Review and merge each enrollment PR to complete enrollment.

### Step 5: Test the pipeline

Once a repo is enrolled (enrollment PR merged):

1. Create an issue in the enrolled repo
2. The triage agent picks it up automatically — check the Actions tab in both the target repo and `.fullsend` for workflow run logs

---

## All-in-one admin install

For administrators who manage both GCP infrastructure and GitHub configuration, `fullsend admin install` provisions everything in a single command: token mint, inference WIF, GitHub Apps, and repository enrollment.

### Additional prerequisites

All prerequisites from the [end-user setup](#prerequisites) above, plus:

- **GCP project** with the following additional APIs enabled (for the token mint):

  ```bash
  gcloud services enable \
    cloudfunctions.googleapis.com \
    run.googleapis.com \
    secretmanager.googleapis.com \
    iamcredentials.googleapis.com \
    --project="$GCP_PROJECT"
  ```

  > **Note:** `iamcredentials.googleapis.com` is a runtime dependency — the deployed mint Cloud Function uses it for WIF token exchange, not the CLI itself. It must be enabled before deployment.

- **GCP IAM roles** — the full set required for both mint and inference:

  | Role | What it covers |
  |------|----------------|
  | `roles/iam.workloadIdentityPoolAdmin` | Create, read, update, and undelete WIF pools and providers |
  | `roles/iam.serviceAccountAdmin` | Create the `fullsend-mint` service account |
  | `roles/resourcemanager.projectIamAdmin` | Read and set project-level IAM policy (grants `roles/aiplatform.user` to WIF principals) |
  | `roles/secretmanager.admin` | Create secrets, add versions, read and set secret-level IAM policy |
  | `roles/cloudfunctions.developer` | Deploy, update, and inspect the mint Cloud Function |
  | `roles/run.admin` | Read and set Cloud Run IAM policy (sets `allUsers` as invoker) |

  `roles/owner` covers all of the above for users with broad access.

  An administrator with elevated access to the GCP project can grant all required roles with a single script:

  ```bash
  export GCP_PROJECT="my-project-id"    # target GCP project
  export USER_EMAIL="alice@example.com" # email of the user who will run the installer

  for ROLE in \
    roles/iam.workloadIdentityPoolAdmin \
    roles/iam.serviceAccountAdmin \
    roles/resourcemanager.projectIamAdmin \
    roles/secretmanager.admin \
    roles/cloudfunctions.developer \
    roles/run.admin; do
    gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
      --member="user:$USER_EMAIL" \
      --role="$ROLE"
  done
  ```

  > **Reducing required roles:** If you supply `--inference-wif-provider` with a pre-existing WIF provider, `roles/iam.workloadIdentityPoolAdmin` is not needed. If you supply `--skip-mint-check` with `--mint-url` **and** `--inference-wif-provider`, no GCP roles are needed (all GCP provisioning is skipped). Without `--inference-wif-provider`, inference WIF auto-provisioning still requires `roles/iam.workloadIdentityPoolAdmin` and `roles/resourcemanager.projectIamAdmin`.

### OAuth scope reference

The table below lists every scope the installer may request and why. You are never asked for all of them at once — the preflight check requests only the scopes needed for the operation you are running.

| Scope | When needed | Why |
|-------|-------------|-----|
| `repo` | install, analyze | Read/write repository contents, manage repo-level secrets and variables |
| `workflow` | install | Create and update GitHub Actions workflow files in `.github/workflows/` |
| `admin:org` | install (per-org), uninstall, analyze | Manage organization-level Actions variables and app installations |
| `delete_repo` | uninstall | Delete the `.fullsend` config repository |

> **Per-repo scope note:** Per-repo install (`fullsend admin install <owner/repo>`) only requires `repo` and `workflow` when reusing existing GitHub Apps. Creating new apps requires `admin:org`.

### Run the installer

The installer is interactive. It will open multiple browser windows to create and install a GitHub App for each agent role. Follow the prompts in each window to complete the app setup.

During installation, you'll be prompted to choose repository enrollment:
- **[a] Enroll all repositories** — immediately enrolls all org repos (excluding `.fullsend`)
- **[n] Enroll no repositories** — skip enrollment during install; enroll repositories later using `fullsend admin enable repos`

The installer creates the `.fullsend` config repo as **public** by default. This is required for cross-repo `workflow_call` to work with enrolled repos of any visibility (public, private, or internal) across all GitHub plan tiers. If an admin later makes `.fullsend` private, only other private repos in the org will be able to trigger agent workflows — public and internal repos will fail silently.

If the installer fails partway through, run `fullsend admin uninstall "$ORG_NAME"` to clean up before retrying. The uninstall preflight will prompt you to add the `delete_repo` scope if it is missing.

Set the variables for your environment:

```bash
export ORG_NAME="<your-github-org>"
export GCP_PROJECT="<your-gcp-project>"
```

Then run the installer:

```bash
fullsend admin install "$ORG_NAME" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT"
```

The installer automatically provisions [Workload Identity Federation (WIF)](https://cloud.google.com/iam/docs/workload-identity-federation) infrastructure (pool `fullsend-inference`, provider `github-oidc`, IAM bindings) in the inference project. WIF eliminates long-lived credentials — GitHub Actions exchange short-lived OIDC tokens for GCP access tokens. To use a pre-existing WIF provider instead, pass `--inference-wif-provider "$WIF_PROVIDER"` with the full resource name (`projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}`) — the CLI validates the format and skips auto-provisioning (see [Advanced: pre-configure WIF](#advanced-pre-configure-wif) below).

`--mint-project` specifies the GCP project where the OIDC token mint Cloud Function is deployed. It can be the same project as `--inference-project` or a separate project. The installer automatically provisions a Cloud Function, WIF pool (`fullsend-pool`), WIF provider (`github-oidc`), and Secret Manager secrets in the mint project. A service account (`fullsend-mint`) is also created as the Cloud Function's runtime identity to access Secret Manager — this is internal infrastructure and does not require any admin setup.

### `admin install` flags

| Flag | Default | Description |
|------|---------|-------------|
| `--agents` | `fullsend,triage,coder,review,retro,prioritize` | Comma-separated agent roles to provision |
| `--dry-run` | `false` | Preview changes without making them |
| `--inference-project` | | GCP project ID for inference (Agent Platform) |
| `--inference-region` | `global` | GCP region for inference |
| `--inference-wif-provider` | | Full WIF provider resource name (`projects/{number}/locations/global/.../providers/{id}`); skips auto-provisioning when set |
| `--mint-project` | | GCP project for the token mint Cloud Function |
| `--mint-region` | `us-central1` | Cloud region for the token mint function |
| `--mint-url` | | Use an existing mint at this URL instead of deploying one |
| `--mint-provider` | `gcf` | Token mint provider backend |
| `--mint-source-dir` | `internal/mint/` | Path to mint function source directory. The mint consists of two modules (`internal/mint/` and `internal/mintcore/`); the provisioner bundles `mintcore` from the sibling directory automatically. When the path does not exist (e.g., running from a downloaded binary), the embedded source baked into the binary is used instead |
| `--public` | `false` | Create public unlisted GitHub Apps (for multi-org) |
| `--app-set` | `fullsend-ai` | App set name prefix for GitHub Apps (see [Custom app sets](#custom-app-sets)) |
| `--skip-app-setup` | `false` | Skip GitHub App creation (reuse existing apps) |
| `--skip-mint-deploy` | `false` | Skip Cloud Function deployment, reuse existing mint URL |
| `--skip-mint-check` | `false` | Skip mint validation, GCP provisioning, and app setup; requires `--mint-url` |
| `--enroll-all` | `false` | Enroll all repositories without prompting (per-org only) |
| `--enroll-none` | `false` | Skip repository enrollment without prompting (per-org only) |
| `--vendor-fullsend-binary` | `false` | Cross-compile and vendor the fullsend binary for development iteration |

The `--skip-mint-check` flag bypasses all mint validation, GCP provisioning, and app setup. It requires `--mint-url` to be set and only validates that the URL uses HTTPS. This is useful when the mint infrastructure is managed externally or you want to skip GCP API calls entirely.

The installer automatically detects when the deployed mint function is up-to-date (same source hash) and skips code redeployment, only updating WIF infrastructure, org registration, and PEM secrets. Use `--skip-mint-deploy` to explicitly skip the Cloud Function deployment step.

### Multi-org setup

A single token mint can serve multiple GitHub organizations. See [Mint service administration — Multi-org setup](../infrastructure/mint-administration.md#multi-org-setup) for the complete multi-org workflow.

### Merge enrollment PRs

If you chose to enroll repositories during install, the installer dispatches a workflow that creates an enrollment PR in each enrolled repo. These PRs add a shim workflow (`.github/workflows/fullsend.yaml`) that wires events to the agent pipeline.

Review and merge each enrollment PR to complete enrollment. Then follow [Step 5: Test the pipeline](#step-5-test-the-pipeline) from the end-user setup to verify agent workflows are running.

### Managing repository enrollment

After installation, you can enroll or unenroll repositories at any time using the `repos` subcommands.

#### Enable repositories

Set the variables for your environment:

```bash
export ORG_NAME="<your-github-org>"
```

To enroll specific repositories (pass repo names as arguments):

```bash
fullsend admin enable repos "$ORG_NAME" <repo-name> [repo-name...]
```

To enroll all repositories:

```bash
fullsend admin enable repos "$ORG_NAME" --all
```

The enable command:
- Updates `config.yaml` in the `.fullsend` repository
- Triggers the `repo-maintenance` workflow to create enrollment PRs
- Validates that repositories exist in the organization before making changes

#### Disable repositories

`ORG_NAME` carries over from the enable step above, or set it now:

```bash
export ORG_NAME="<your-github-org>"
```

To unenroll specific repositories (pass repo names as arguments):

```bash
fullsend admin disable repos "$ORG_NAME" <repo-name> [repo-name...]
```

To unenroll all repositories:

```bash
fullsend admin disable repos "$ORG_NAME" --all
```

The `--all` flag prompts for confirmation — you must type the exact organization name when prompted. To skip the confirmation prompt (e.g., in automated scripts):

```bash
fullsend admin disable repos "$ORG_NAME" --all --yolo
```

The disable command:
- Updates `config.yaml` to mark repositories as disabled
- Triggers the `repo-maintenance` workflow to create unenrollment PRs
- Warns (but does not reject) repository names not found in the config, allowing safe cleanup of deleted repos
- Does not delete existing shim workflows (merge the unenrollment PR to remove them)

### Analyze installation status

The `analyze` command checks the current state of a fullsend installation and reports what is installed, missing, or needs updating. It requires `repo` and `admin:org` scopes.

```bash
export ORG_NAME="<your-github-org>"
```

```bash
fullsend admin analyze "$ORG_NAME"
```

This is a read-only operation — it makes no changes.

### Uninstall

The `uninstall` command tears down the fullsend installation for a GitHub organization, removing the `.fullsend` config repo and associated resources. It prompts for confirmation by requiring you to type the exact organization name.

```bash
export ORG_NAME="<your-github-org>"
```

```bash
fullsend admin uninstall "$ORG_NAME"
```

The uninstall preflight will prompt you to add the `delete_repo` scope if it is missing.

After removing the config repo and secrets, the CLI opens a browser tab for each installed fullsend GitHub App. Click **Uninstall** on the page, then press **Enter** in the terminal to continue to the next app. Once all apps have been processed, the CLI re-checks the org's installations and reports whether each app was successfully removed.

| Flag | Default | Description |
|------|---------|-------------|
| `--yolo` | `false` | Skip the confirmation prompt |
| `--app-set` | `fullsend-ai` | App set name prefix for GitHub Apps (used for fallback slug generation when config is unavailable) |

---

## Per-repo installation

Per-repo mode installs fullsend for a single repository without requiring an org-wide `.fullsend` config repo. It's fully self-contained — creating GitHub Apps, deploying a token mint, and configuring WIF as needed.

> **Installing fullsend in the `fullsend-ai` org:** When installing fullsend for
> `fullsend-ai/fullsend` itself, prefer **per-org mode** (`fullsend admin install fullsend-ai`).
> Per-repo mode technically works but creates a circular reference: the per-repo
> shim workflow calls `fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@<ref>`,
> which in turn calls reusable stage workflows in the same repo, which check out
> `fullsend-ai/fullsend@<ref>` again for upstream defaults and use
> `fullsend-ai/fullsend@<ref>` as the composite action. The repo ends up
> simultaneously serving as the source of reusable workflows, the source of the
> composite action, the caller repo, and the target repo being acted on. Per-org
> mode avoids this by placing the shim in `fullsend-ai/fullsend` and the agent
> workflows in a separate `fullsend-ai/.fullsend` config repo, keeping the
> reference chain unidirectional: target repo → `.fullsend` → upstream
> `fullsend-ai/fullsend`.

### Using platform-provided infrastructure

When a platform operator has pre-provisioned shared public GitHub Apps and a token mint, you only need to provide a GCP project for inference. This is the simplest installation path — no Apps to create, no mint to deploy, no PEM management.

> **Tip:** For GitHub-only setup without GCP access, use `fullsend github setup` instead of `admin install`. See [Setting up with pre-provisioned infrastructure](github-setup.md).

> This section documents the **SaaS installation profile** defined in [ADR 0033 §6](../../ADRs/0033-per-repo-installation-mode.md#6-credential-models). If you are reusing apps from your own prior per-org installation, see [Reusing existing infrastructure](#reusing-existing-infrastructure) instead.

**Prerequisites:**

- **Org admin approval** to install the shared GitHub Apps on your repository
- **GCP project** with the [Agent Platform API](https://console.cloud.google.com/apis/library/aiplatform.googleapis.com) enabled for inference
- **Mint URL** — obtain from your platform operator (for OIDC token exchange)
- **Platform operator coordination** — the following must be in place before installation:
  - Your organization is registered in the mint's `ALLOWED_ORGS` configuration
  - The shared GitHub Apps are installed on your repository
  - The mint has the necessary GitHub App PEMs stored in Secret Manager
  - Mint-side Workload Identity Federation (WIF) is configured to accept OIDC tokens from your organization's repositories
- **For assisted installation only** — mint GCP project ID and region, plus IAM access to the platform project (see the alternative path below)

**Recommended: Use the mint URL directly**

Most per-repo users will not have IAM access to the platform operator's GCP project. Ask your platform operator for the mint URL and confirm that the following are in place before running the installer:

- The shared GitHub Apps are already installed on your repository
- Your organization is registered in the mint's `ALLOWED_ORGS`
- Mint-side WIF is configured to accept tokens from your organization
- All PEMs are stored in Secret Manager

Then run:

```bash
fullsend admin install "$ORG_NAME/$REPO_NAME" \
  --inference-project "$GCP_PROJECT" \
  --mint-url "$PLATFORM_MINT_URL" \
  --skip-mint-check
```

The installer skips all app discovery, mint validation, and mint-related GCP provisioning — it only generates workflow files and sets repository variables and secrets. WIF infrastructure is still auto-provisioned in the inference project; pass `--inference-wif-provider` to skip this as well if the platform operator provides a pre-existing WIF provider.

**Alternative: Assisted installation (requires platform project access)**

If you have IAM access to the platform operator's GCP project, the installer can discover shared apps and handle validation automatically:

```bash
fullsend admin install "$ORG_NAME/$REPO_NAME" \
  --inference-project "$GCP_PROJECT" \
  --mint-url "$PLATFORM_MINT_URL" \
  --mint-project "$PLATFORM_MINT_PROJECT" \
  --mint-region "$PLATFORM_MINT_REGION"
```

This requires `roles/cloudfunctions.developer` and `roles/secretmanager.admin` on the platform mint project for app discovery, validation, and org registration. WIF auto-provisioning in the inference project additionally requires `roles/iam.workloadIdentityPoolAdmin` and `roles/resourcemanager.projectIamAdmin` on the inference project; pass `--inference-wif-provider` to skip this if you have a pre-existing WIF provider. The command:
- Discovers shared app IDs from the platform mint project via GCP Cloud Functions API
- Checks if the shared apps are already installed on your repository
- If apps are not installed, opens browser windows to install the pre-existing shared apps (requires org admin approval)
- Validates and updates the mint configuration if needed (registers your org, updates WIF, stores PEM references)
- Auto-provisions Workload Identity Federation for your repo in the inference project
- Generates workflow files and commits scaffold to your repository
- Sets repository variables and secrets

### First-time install (no prior infrastructure)

Set the variables for your environment:

```bash
export ORG_NAME="<your-github-org>"
export REPO_NAME="<your-repo-name>"
export GCP_PROJECT="<your-gcp-project>"
```

```bash
fullsend admin install "$ORG_NAME/$REPO_NAME" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT"
```

This discovers existing infrastructure and creates what's missing:
- If no GitHub Apps exist, opens browser windows to create them (same manifest flow as per-org)
- If no token mint exists, deploys a Cloud Function
- If both exist from a prior per-org install, reuses them

Creating apps requires `admin:org` OAuth scope (the installer prompts for it). Reusing existing apps only requires `repo` and `workflow` scopes.

### Reusing existing infrastructure

When a per-org install already exists in **your own org**, per-repo reuses the apps and mint. If the infrastructure belongs to a separate platform operator (SaaS profile), see [Using platform-provided infrastructure](#using-platform-provided-infrastructure) instead.

```bash
fullsend admin install "$ORG_NAME/$REPO_NAME" \
  --inference-project "$GCP_PROJECT" \
  --mint-url "$MINT_URL"
```

Or let it auto-discover the mint from the GCP project:

```bash
fullsend admin install "$ORG_NAME/$REPO_NAME" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT"
```

### Per-repo flags

Per-repo accepts all `admin install` flags except `--enroll-all` and `--enroll-none` (which only apply to org-wide enrollment). Per-repo install requires only `repo` and `workflow` OAuth scopes when reusing existing apps. Creating new apps requires `admin:org`.

> **`--mint-region` note:** Per-repo uses the same `--mint-region` default (`us-central1`) as per-org. When reusing a mint deployed to a non-default region, pass `--mint-region` explicitly so auto-discovery finds the correct function.

---

## Custom app sets

By default, the installer creates GitHub Apps with the `fullsend-ai` prefix (e.g., `fullsend-ai-fullsend`, `fullsend-ai-coder`, `fullsend-ai-review`). Organizations that need their own set of apps — for example, to use org-specific permissions or to register multiple app sets on the same mint — can pass `--app-set` to override the prefix.

### Creating a custom app set

Set the variables for your environment:

```bash
export ORG_NAME="<your-github-org>"
export GCP_PROJECT="<your-gcp-project>"
```

```bash
fullsend admin install "$ORG_NAME" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT" \
  --app-set "$ORG_NAME"
```

This creates apps named `{org}-fullsend`, `{org}-coder`, `{org}-review`, etc. The app set prefix is stored in the `.fullsend/config.yaml` slug mappings, so subsequent operations (permission checks, PEM recovery) find the correct apps automatically.

### Using existing public apps from another app set

When a mint already has public apps registered under a custom app set (e.g., `fullsend-ai-fullsend`, `fullsend-ai-coder`), additional orgs installing those apps must pass the same `--app-set` so the CLI resolves the correct slugs:

```bash
export NEW_ORG="<new-github-org>"
```

`GCP_PROJECT` carries over from the custom app set creation step above.

```bash
fullsend admin install "$NEW_ORG" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT" \
  --app-set fullsend-ai
```

The installer detects that the public apps are already installed in the org (matched by app ID from the mint's `ROLE_APP_IDS`), copies PEM secrets to the new org's scoped key, and skips app creation. The `--app-set` value ensures convention-based slug lookups match the existing apps.

> **Migration note:** Prior to this change, the default app set was `fullsend`, producing slugs like `fullsend-coder`. The default is now `fullsend-ai`, producing `fullsend-ai-coder`. Existing installations that used the old default should pass `--app-set fullsend` explicitly to continue matching their existing GitHub App slugs, or re-install with the new default.

### Uninstalling a custom app set

When uninstalling an org that used a custom app set, pass the same `--app-set` value so the CLI generates the correct fallback slugs if the config repo is unavailable. `ORG_NAME` carries over from the custom app set creation step above, or set it now:

```bash
export ORG_NAME="<your-github-org>"
```

```bash
fullsend admin uninstall "$ORG_NAME" --app-set "$ORG_NAME"
```

### Constraints

- App set names must be lowercase alphanumeric with optional hyphens (no leading/trailing hyphens, no consecutive hyphens), max 23 characters (GitHub App names are limited to 34 characters, and the role suffix is appended)
- The app set prefix only affects GitHub App slugs — GCP secret naming (`fullsend-{org}--{role}-app-pem`) and mint `ROLE_APP_IDS` keys (`{org}/{role}`) are independent of the app set

---

## Standalone commands

The `admin install` command performs all setup in a single invocation. For organizations that separate GCP and GitHub responsibilities across teams, fullsend provides standalone commands that decompose the same pipeline:

| Role | Command | What it does |
|------|---------|-------------|
| GCP Admin (Inference) | `fullsend inference provision <org\|owner/repo>` | Create WIF pool/provider and grant Agent Platform access (idempotent — safe to re-run for new orgs) |
| GCP Admin (Inference) | `fullsend inference deprovision <org\|owner/repo>` | Remove org or repo from WIF |
| GCP Admin (Inference) | `fullsend inference status <org\|owner/repo>` | Check WIF health, print config values |
| GitHub Maintainer | `fullsend github setup <org\|owner/repo>` | Configure GitHub org or repo (no GCP needed) |
| GitHub Maintainer | `fullsend github enroll <org> [repo...]` | Add repositories to agent enrollment |
| GitHub Maintainer | `fullsend github unenroll <org> [repo...]` | Remove repositories from agent enrollment |
| GitHub Maintainer | `fullsend github set <org\|owner/repo> <key> <value>` | Update a single config value (secret or variable) |
| GitHub Maintainer | `fullsend github status <org>` | Analyze GitHub-side installation state |
| GitHub Maintainer | `fullsend github sync-scaffold <org>` | Update workflow templates to current CLI version |
| GitHub Maintainer | `fullsend github uninstall <org>` | Remove GitHub configuration (org-level only) |
| GCP Admin (Mint) | `fullsend mint deploy` | Deploy the token mint Cloud Function |
| GCP Admin (Mint) | `fullsend mint enroll <org\|owner/repo>` | Register an org or repo in the mint, store PEMs (does not grant Agent Platform access — use `inference provision`) |
| GCP Admin (Mint) | `fullsend mint unenroll <org\|owner/repo>` | Remove an org or repo from the mint |
| GCP Admin (Mint) | `fullsend mint status` | Inspect mint state and PEM health |

See [Setting up with pre-provisioned infrastructure](github-setup.md) for the complete GitHub maintainer guide and [Mint service administration](../infrastructure/mint-administration.md) for the mint admin guide.

### Per-command IAM role breakdown

When using the split-responsibility workflow, each standalone command requires a subset of IAM roles. Use this table to request only what you need.

| IAM Role | `inference provision` | `inference deprovision` | `inference status` | `mint deploy` | `mint enroll` | `mint unenroll` | `mint status` |
|----------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| `roles/iam.workloadIdentityPoolAdmin` | x | x | | x | x | x | |
| `roles/resourcemanager.projectIamAdmin` | x | | | \* | \*\* | | |
| `roles/iam.serviceAccountAdmin` | | | | x | | | |
| `roles/secretmanager.admin` | | | | \* | x | x† | |
| `roles/cloudfunctions.developer` | | | | x | | | |
| `roles/cloudfunctions.viewer` | | | | | x | x | x |
| `roles/run.admin` | | | | x | x | x | |
| `roles/iam.workloadIdentityPoolViewer` | | | x\*\*\* | | | | |
| `roles/secretmanager.viewer` | | | | | | | x |

\* `roles/resourcemanager.projectIamAdmin` and `roles/secretmanager.admin` are required for `mint deploy` only when using `--pem-dir` (first-time bootstrap). Standard deploys without `--pem-dir` do not need these roles.

\*\* `roles/resourcemanager.projectIamAdmin` is required for `mint enroll` only in per-repo mode (`mint enroll owner/repo`). Org-scoped enrollment does not grant IAM bindings — use `inference provision` separately.

† `roles/secretmanager.admin` is required for `mint unenroll` only in org-scoped mode. Repo-scoped unenroll does not touch PEM secrets.

\*\*\* All commands that call GCP APIs also require `resourcemanager.projects.get` (typically available via `roles/browser` or any project-level viewer role). This is only notable for `inference status` where it is not covered by the other listed roles.

Required GCP APIs also differ by command group:

```bash
# Inference commands (inference provision/deprovision/status):
gcloud services enable \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  aiplatform.googleapis.com \
  --project="$GCP_PROJECT"

# Mint commands (mint deploy/enroll/unenroll/status):
gcloud services enable \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  cloudfunctions.googleapis.com \
  run.googleapis.com \
  secretmanager.googleapis.com \
  iamcredentials.googleapis.com \
  --project="$GCP_PROJECT"
```

> **Note:** `iamcredentials.googleapis.com` is a runtime dependency — the deployed mint Cloud Function uses it for WIF token exchange, not the CLI itself. It must be enabled before `mint deploy`.

---

## Advanced: pre-configure WIF

The installer auto-provisions WIF infrastructure. For most cases, `fullsend inference provision <org>` handles this automatically and prints the WIF provider resource name to pass to `admin install --inference-wif-provider` or `github setup --inference-wif-provider`.

If you need custom pool names, attribute conditions, or want to share a provider across tools, you can create WIF manually:

**Create a Workload Identity Pool and OIDC Provider:**

```bash
export GCP_PROJECT="<gcp-project>"
export ORG_NAME="<org-name>"

gcloud iam workload-identity-pools create fullsend-inference \
  --location=global \
  --display-name="Fullsend Inference" \
  --project="$GCP_PROJECT"

gcloud iam workload-identity-pools providers create-oidc github-oidc \
  --location=global \
  --workload-identity-pool=fullsend-inference \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository_owner=assertion.repository_owner,attribute.repository=assertion.repository" \
  --attribute-condition="assertion.repository_owner == '$ORG_NAME'" \
  --project="$GCP_PROJECT"
```

**Grant Agent Platform access to the WIF principal:**

```bash
export PROJECT_NUMBER=$(gcloud projects describe "$GCP_PROJECT" --format='value(projectNumber)')
export WIF_PRINCIPAL="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/attribute.repository_owner/$ORG_NAME"

gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
  --role="roles/aiplatform.user" \
  --member="$WIF_PRINCIPAL" \
  --condition=None
```

> **Warning — broad WIF scope:** The `attribute.repository_owner` condition above grants WIF access to _all_ repositories in the organization, not just `.fullsend`. This is required for orgs using per-repo mode (where multiple repos need to authenticate to GCP independently), but it significantly widens the trust boundary compared to per-org-only setups. Note that `fullsend admin install <owner/repo>` auto-provisions a **per-repo** WIF provider scoped to a single repository — the org-wide condition here is broader than what the automated path creates.
>
> **For per-org-only setups**, use the tighter `assertion.repository == '$ORG_NAME/.fullsend'` condition instead, and scope the WIF principal to `attribute.repository/$ORG_NAME/.fullsend`. See [Google Cloud WIF documentation](https://cloud.google.com/iam/docs/workload-identity-federation) for condition syntax.

**Pass the provider to the installer:**

```bash
export WIF_PROVIDER="projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc"

fullsend admin install "$ORG_NAME" \
  --inference-project "$GCP_PROJECT" \
  --inference-wif-provider "$WIF_PROVIDER" \
  --mint-project "$GCP_PROJECT"
```

> **Note:** IAM policy bindings may take several minutes to propagate. If agent workflows fail with a permission error immediately after setup, wait a few minutes and retry.

## Status notifications

Agent workflows post status comments on issues and PRs when they start and
complete. This behavior is controlled by the `status_notifications` section in
`config.yaml`:

```yaml
defaults:
  status_notifications:
    comment:
      start: enabled      # "enabled" (default) | "disabled"
      completion: enabled  # "enabled" (default) | "disabled"
```

When `status_notifications` is omitted, comments default to enabled.

The composite action accepts four optional inputs for status notifications:

| Input | Description |
|-------|-------------|
| `run-url` | URL of the CI/CD run shown in the status comment |
| `status-repo` | Repository (`owner/repo`) to post status comments on |
| `status-number` | Issue or PR number for status comments |
| `status-token` | Token for posting comments (defaults to `GH_TOKEN`) |

All reusable workflows pass these inputs automatically.

## See Also

- [Setting up with pre-provisioned infrastructure](github-setup.md) — GitHub-only setup when GCP is already provisioned
- [Mint service administration](../infrastructure/mint-administration.md) — Deploying and managing the token mint
- [Infrastructure Reference](../infrastructure/infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [Enabling fullsend on private repositories](../infrastructure/private-repositories.md) — Additional guardrails for private repos
- [CLI Internals](../dev/cli-internals.md) — Command structure and implementation details
