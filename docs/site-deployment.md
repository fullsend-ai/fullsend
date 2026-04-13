# Documentation site deployment (Cloudflare Workers)

## Overview

This repository publishes a static documentation site built from `docs/mindmap.html` (copied to `_site/index.html` in CI, then deployed from `site/public/`). Deployment uses **Cloudflare Workers with [static assets](https://developers.cloudflare.com/workers/static-assets/)** (not the legacy **Pages direct-upload** / `wrangler pages deploy` flow).

Two GitHub Actions workflows:

- **Build Site** — on `pull_request` and `push` to `main`, checks out the PR head when relevant, builds `_site/`, uploads artifact **`site`**.
- **Deploy Site** — on successful **Build Site** via `workflow_run`, checks out the repo (for [`site/wrangler.toml`](../site/wrangler.toml)), downloads the artifact into `site/public/`, then:
  - **push to `main`:** `wrangler deploy` → production Worker traffic.
  - **pull_request:** `wrangler versions upload --preview-alias pr-<run-id>` → preview URL on `*.workers.dev` without changing production.

GitHub **Deployments** use environments **`site-preview`** and **`site-production`**; PRs also get a single upserted comment with the preview link.

For architecture and naming, see [2026-04-09-site-cloudflare-pages-design.md](superpowers/specs/2026-04-09-site-cloudflare-pages-design.md) (document filename still says “pages” for history; content describes Workers).

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
mkdir -p site/public && cp docs/mindmap.html site/public/index.html
cd site && npx wrangler@4 dev
```

Requires a Cloudflare login or API token in the environment per [Wrangler docs](https://developers.cloudflare.com/workers/wrangler/).

## Troubleshooting

**Deploy job skipped.** The triggering workflow display name must be **Build Site** exactly, and `workflow_run.repository` must match the current repo.

**`Could not determine Workers deployment URL`.** The workflow reads `deployment-url` from `cloudflare/wrangler-action`, then falls back to parsing Wrangler stdout/stderr for a `workers.dev` URL. Upgrade **`wranglerVersion`** in the workflow if Wrangler output format changed.

**Preview upload fails (PR builds).** Requires Wrangler **≥ 4.21.0** for `--preview-alias`. The workflow pins **4.30.0**.

**Artifact download 404.** **Build Site** must upload artifact **`site`**; **Deploy Site** needs `actions: read`.

**No PR comment.** Same as before: ambiguous `head` when resolving the PR number; see the design spec.
