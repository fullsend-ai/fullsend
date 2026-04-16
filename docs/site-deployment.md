# Documentation site deployment (Cloudflare Workers)

## Overview

This repository publishes a static documentation site whose root page is built from [`web/public/index.html`](../web/public/index.html). **Build Site** packs **`public/`** (static files) and **`worker/`** (TypeScript Worker from the same checkout—PR head on PR builds) under **`_bundle/`** in one artifact. **Deploy Site** checks out **only the default branch** (trusted [`cloudflare_site/wrangler.toml`](../cloudflare_site/wrangler.toml); never PR-controlled config on the secret-bearing runner), downloads the artifact to **`_bundle/`**, then **copies only** **`_bundle/public/`** and **`_bundle/worker/`** into **`cloudflare_site/`** (so a malicious artifact cannot overwrite `wrangler.toml` or other repo files), then runs Wrangler. Deployment uses **Cloudflare Workers with [static assets](https://developers.cloudflare.com/workers/static-assets/)** (not the legacy **Pages direct-upload** / `wrangler pages deploy` flow).

Two GitHub Actions workflows:

- **Build Site** — on `pull_request` and `push` to `main`, checks out the PR head when relevant, assembles **`_bundle/public/`** and **`_bundle/worker/`**, uploads artifact **`site`** (`_bundle/` contents).
- **Deploy Site** — on successful **Build Site** via `workflow_run`, checks out the repo default ref (trusted Wrangler project files), downloads artifact **`site`** into **`_bundle/`**, copies **`public/`** and **`worker/`** into **`cloudflare_site/`**, then:
  - **push to `main`:** `wrangler deploy` → production Worker traffic.
  - **pull_request:** `wrangler versions upload --preview-alias pr-<pr-number>` → preview URL on `*.workers.dev` without changing production (alias falls back to `pr-<workflow_run.id>` only when the same fork branch matches more than one open PR).

GitHub **Deployments** use environments **`site-preview`** and **`site-production`**; PRs also get a single upserted comment with the preview link.

For architecture and naming, see [2026-04-09-site-cloudflare-pages-design.md](superpowers/specs/2026-04-09-site-cloudflare-pages-design.md) (document filename still says “pages” for history; content describes Workers). Repository layout for `web/` vs `cloudflare_site/` is decided in [ADR 0019](ADRs/0019-web-source-and-cloudflare-site-layout.md).

## Cloudflare setup

### Worker (not a Pages “project”)

1. In the Cloudflare dashboard, use **Workers & Pages** → **Create** → **Create Worker** (or let the first `wrangler deploy` create it). The Worker name must match the GitHub variable below.
2. Configure **[preview URLs](https://developers.cloudflare.com/workers/configuration/previews/)** (default on when `workers_dev` is enabled). PR builds rely on **`wrangler versions upload`** with `--preview-alias`.
3. Optional: set a **[workers.dev](https://developers.cloudflare.com/workers/configuration/routing/workers-dev/)** subdomain for your account.

### API token

Create an API token that can deploy Workers for your account, for example:

- **Account** → **Cloudflare Workers** → **Edit** (or the “Edit Cloudflare Workers” template), and
- **Account** → **Account Settings** → **Read** if Wrangler requires it.

Store it as GitHub secret **`CLOUDFLARE_API_TOKEN`**. A token scoped **only** to “Cloudflare Pages — Edit” is **not** enough for `wrangler deploy` / `versions upload` on a Worker.

### Account ID and Worker name

- Copy **Account ID** → secret **`CLOUDFLARE_ACCOUNT_ID`**.
- Set **`CLOUDFLARE_PROJECT_NAME`** as a GitHub **Actions variable** (same name as before for compatibility): value = **Worker name** in the dashboard. The deploy workflow passes it as `wrangler deploy --name=…` / `versions upload --name=…`.

### Custom domains (e.g. fork demo or `konflux.sh`)

Attach routes or custom domains to the **Worker** (Workers → your Worker → **Domains & Routes**), not to a Pages project. Production URLs in GitHub Deployments will follow the hostname Wrangler reports (often `*.workers.dev` until a custom domain is primary).

### Migrating from an old Pages project

If you previously used **Cloudflare Pages** with `wrangler pages deploy`, create the Worker as above, point DNS/custom hostnames to the Worker, then disable or delete the old Pages project to avoid confusion.

## GitHub fork phase 1

On a **fork**, open **Settings → Secrets and variables → Actions**. Add secrets **`CLOUDFLARE_API_TOKEN`**, **`CLOUDFLARE_ACCOUNT_ID`**, and variable **`CLOUDFLARE_PROJECT_NAME`** (Worker name).

Under **Settings → Actions → General**, allow **Fork pull request workflows** from contributors so fork PRs can run **Build Site** without Cloudflare credentials in the fork.

**Deploy Site** runs in the base repository with secrets; fork workflow logs should not show those values.

## GitHub upstream phase 2

Configure the same secrets/variables at org or repo scope. Confirm **`pull-requests: write`** on the deploy workflow matches org policy for fork PR comments.

Disable **GitHub Pages** under **Settings → Pages** if it was only used for this site.

## Local preview (optional)

From the repository root:

```bash
mkdir -p cloudflare_site/public && cp web/public/index.html cloudflare_site/public/index.html
cd cloudflare_site && npx wrangler@4 dev
```

Requires a Cloudflare login or API token in the environment per [Wrangler docs](https://developers.cloudflare.com/workers/wrangler/).

## Troubleshooting

**Deploy job skipped.** The triggering workflow display name must be **Build Site** exactly, and `workflow_run.repository` must match the current repo.

**`Could not determine Workers deployment URL`.** The workflow reads `deployment-url` from `cloudflare/wrangler-action`, then falls back to parsing Wrangler stdout/stderr for a `workers.dev` URL. Upgrade **`wranglerVersion`** in the workflow if Wrangler output format changed.

**Preview upload fails (PR builds).** Requires Wrangler **≥ 4.21.0** for `--preview-alias`. The workflow pins **4.30.0**.

**Artifact download 404.** **Build Site** must upload artifact **`site`**; **Deploy Site** needs `actions: read`.

**No PR comment.** Same as before: ambiguous `head` when resolving the PR number; see the design spec.
