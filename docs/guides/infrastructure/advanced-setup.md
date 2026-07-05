# Advanced setup

This page covers non-standard installation paths for fullsend. For the standard per-repo installation using the managed `fullsend-ai` app set and hosted mint, follow the [Getting Started guides](../getting-started/).

## Deployment models

Fullsend supports two deployment models based on who manages the token mint and GitHub Apps:

| Model | Mint | GitHub Apps | Audience |
|-------|------|-------------|----------|
| **Managed** (default) | Hosted by fullsend team | Shared `fullsend-ai-*` apps | Repo maintainers |
| **Self-hosted** | You deploy and manage your own | Custom app set (`--app-set`) | Platform operators |

Most users should use the **managed** model — the [Getting Started guides](../getting-started/) cover this path end-to-end. The sections below cover variants within the managed model and self-hosted deployment for platform operators.

## Using platform-provided infrastructure

When a platform operator has already deployed the mint and shared `fullsend-ai-*` apps, installation follows the standard [Getting Started](../getting-started/) flow — you only need a GCP project for inference. Before running the installer, confirm with your platform operator that:

- Your organization is registered in the mint's `ALLOWED_ORGS`
- The shared GitHub Apps are installed on your repository (or org)
- Mint-side WIF is configured to accept OIDC tokens from your organization

Then follow [Getting Inference](../getting-started/getting-inference.md) and [Configuring GitHub](../getting-started/configuring-github.md), passing the platform operator's mint URL via `--mint-url`.

If the platform operator also provides a pre-existing WIF provider, skip `inference provision` and pass `--inference-wif-provider` directly to `github setup`.

If you have IAM access to the platform operator's GCP project, pass `--mint-project` and `--mint-region` to `github setup` to enable auto-discovery of shared app IDs and automatic validation of mint configuration. This requires `roles/cloudfunctions.developer` on the platform mint project.

> This section documents the **SaaS installation profile** defined in [ADR 0033 §6](../../ADRs/0033-per-repo-installation-mode.md#6-credential-models). See the [CLI reference](../../cli/github.md#flags) for the full flag list.

## OAuth scope reference

| Scope | When needed | Why |
|-------|-------------|-----|
| `repo` | install, analyze | Read/write repository contents, manage repo-level secrets and variables |
| `workflow` | install | Create and update GitHub Actions workflow files in `.github/workflows/` |
| `admin:org` | install (per-org), uninstall, analyze | Manage organization-level Actions variables and app installations |
| `delete_repo` | uninstall | Delete the `.fullsend` config repository |

> **Per-repo scope note:** Per-repo install only requires `repo` and `workflow` scopes when reusing existing GitHub Apps. Creating new apps requires `admin:org`.

> **Note on scope breadth:** `gh auth` scopes apply to *every* organization your account belongs to — GitHub does not support per-org scoping for classic OAuth tokens. If that is a concern, create a [fine-grained personal access token](https://github.com/settings/tokens?type=beta) scoped to the target organization and export it as `GH_TOKEN` before running the installer.

## Self-hosted deployment

This section is for **platform operators** who deploy and manage their own token mint and GitHub Apps. If you are using the managed `fullsend-ai-*` apps and hosted mint, this does not apply to you.

For mint deployment and management details, see [Mint service administration](../infrastructure/mint-administration.md).

### Deploying a new mint

Mint deployment and GitHub App creation are independent steps — deploying a mint does not create GitHub Apps, and creating apps does not require a mint to exist first.

**1. Deploy the token mint** (GCP Admin):

```bash
fullsend mint deploy --project "$GCP_PROJECT"
```

See [Mint service administration](mint-administration.md) for deployment details, PEM management, and role configuration.

**2. Enroll the org or repo in the mint** (GCP Admin):

```bash
fullsend mint enroll "$ORG_NAME" --project "$GCP_PROJECT"
```

**3. Provision WIF for inference** (GCP Admin):

```bash
fullsend inference provision "$ORG_NAME/$REPO_NAME" \
  --project "$GCP_PROJECT"
```

**4. Configure GitHub with a custom app set** (GitHub Maintainer):

```bash
fullsend github setup "$ORG_NAME/$REPO_NAME" \
  --inference-project "$GCP_PROJECT" \
  --inference-wif-provider "$WIF_PROVIDER" \
  --mint-url "$MINT_URL" \
  --app-set "$ORG_NAME"
```

If no GitHub Apps exist for the app set, the setup command opens browser windows to create them via the manifest flow and stores the PEMs in Secret Manager. Creating apps requires `admin:org` OAuth scope. Reusing existing apps only requires `repo` and `workflow` scopes.

See [standalone commands](../getting-started/operations.md#standalone-commands) for the full command reference.

### Custom app sets

By default, the installer creates GitHub Apps with the `fullsend-ai` prefix (e.g., `fullsend-ai-fullsend`, `fullsend-ai-coder`, `fullsend-ai-review`). Organizations that need their own set of apps — for example, to use org-specific permissions or to register multiple app sets on the same mint — can pass `--app-set` to override the prefix.

#### Creating a custom app set

```bash
fullsend github setup "$ORG_NAME" \
  --mint-url "$MINT_URL" \
  --inference-project "$GCP_PROJECT" \
  --inference-wif-provider "$WIF_PROVIDER" \
  --app-set "$ORG_NAME"
```

This creates apps named `{org}-fullsend`, `{org}-coder`, `{org}-review`, etc. The app set prefix is stored in the `.fullsend/config.yaml` slug mappings, so subsequent operations (permission checks, PEM recovery) find the correct apps automatically.

#### Using existing public apps from another app set

When a mint already has public apps registered under a custom app set (e.g., `fullsend-ai-fullsend`, `fullsend-ai-coder`), additional orgs installing those apps must pass the same `--app-set` so the CLI resolves the correct slugs:

```bash
fullsend github setup "$NEW_ORG" \
  --mint-url "$MINT_URL" \
  --inference-project "$GCP_PROJECT" \
  --inference-wif-provider "$WIF_PROVIDER" \
  --app-set fullsend-ai
```

The setup command detects that the public apps are already installed in the org (matched by app ID from the mint's `ROLE_APP_IDS`) and skips app creation — role PEM secrets are shared and already exist.

> **Migration note:** Prior to this change, the default app set was `fullsend`, producing slugs like `fullsend-coder`. The default is now `fullsend-ai`, producing `fullsend-ai-coder`. Existing installations that used the old default should pass `--app-set fullsend` explicitly to continue matching their existing GitHub App slugs, or re-install with the new default.

#### Uninstalling a custom app set

When uninstalling an org that used a custom app set, pass the same `--app-set` value so the CLI generates the correct fallback slugs if the config repo is unavailable:

```bash
fullsend github uninstall "$ORG_NAME" --app-set "$ORG_NAME"
```

#### Constraints

- App set names must be lowercase alphanumeric with optional hyphens (no leading/trailing hyphens, no consecutive hyphens), max 23 characters (GitHub App names are limited to 34 characters, and the role suffix is appended)
- The app set prefix only affects GitHub App slugs — GCP secret naming (`fullsend-{role}-app-pem`) and mint `ROLE_APP_IDS` keys (`{role}`) are independent of the app set

### Custom inference WIF configuration

For most cases, `fullsend inference provision` auto-provisions the inference WIF pool and prints the provider resource name to pass to `github setup --inference-wif-provider`. Use manual configuration only when you need custom pool names, attribute conditions, or want to share an inference WIF provider across multiple tools:

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

> **Warning — broad WIF scope:** The `attribute.repository_owner` condition above grants WIF access to _all_ repositories in the organization, not just `.fullsend`. This is required for orgs using per-repo mode (where multiple repos need to authenticate to GCP independently), but it significantly widens the trust boundary compared to per-org-only setups. Note that `fullsend inference provision <owner/repo>` auto-provisions a **per-repo** WIF provider scoped to a single repository — the org-wide condition here is broader than what the automated path creates.
>
> **For per-org-only setups**, use the tighter `assertion.repository == '$ORG_NAME/.fullsend'` condition instead, and scope the WIF principal to `attribute.repository/$ORG_NAME/.fullsend`. See [Google Cloud WIF documentation](https://cloud.google.com/iam/docs/workload-identity-federation) for condition syntax.

**Pass the provider to the installer:**

```bash
export WIF_PROVIDER="projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc"

fullsend github setup "$ORG_NAME" \
  --inference-project "$GCP_PROJECT" \
  --inference-wif-provider "$WIF_PROVIDER" \
  --mint-url "$MINT_URL"
```

> **Note:** IAM policy bindings may take several minutes to propagate. If agent workflows fail with a permission error immediately after setup, wait a few minutes and retry.

## Deprecated: monolithic `admin install` (not the guided entry point)

> **Deprecated (monolithic mode).** Running `fullsend admin install` as a single
> opaque invocation that provisions GCP mint, inference, and GitHub together is
> deprecated. Use the [standalone commands](../getting-started/operations.md#standalone-commands)
> today: `fullsend inference provision` + `fullsend github setup` for per-repo, or
> `fullsend mint deploy` + `fullsend mint enroll` + `fullsend github setup` for
> per-org. See [Getting Started](../getting-started/configuring-github.md) for the
> recommended per-repo flow.
>
> **Accepted direction ([ADR 0066](../../ADRs/0066-interactive-admin-install-guide.md)):**
> `admin install` will become an interactive guided orchestrator over those same
> standalone phases (decision tree + `--plan`), not a monolithic provisioner.
> That wizard is follow-on implementation work — it is not available in the CLI yet.

## See Also

- [Getting Started](../getting-started/) — Standard per-repo installation
- [Operations](../getting-started/operations.md) — Enrollment, status, uninstall, standalone commands
- [Mint service administration](mint-administration.md) — Deploying and managing the token mint
- [Infrastructure Reference](infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [Enabling fullsend on private repositories](private-repositories.md) — Additional guardrails for private repos
- [CLI Internals](../dev/cli-internals.md) — Command structure and implementation details
