# Web admin deployment (on hold)

> **Status: On hold.** The web admin SPA (`web/admin/`) and Cloudflare Worker deployment are paused. This document is preserved for reference when work resumes. For the **documentation site** (VitePress), see [`doc-site.md`](doc-site.md).

## Overview

The **admin installation UI** is a **Svelte 5 + Vite** single-page app under `web/admin/`, served at the `/admin/` base path. The root landing page is [`web/public/index.html`](../web/public/index.html); the interactive document graph is [`web/public/graph.html`](../web/public/graph.html) (served at `/graph.html`). **Vite** builds the admin SPA under **`web/dist/admin/`** (see [`web/admin/README.md`](../web/admin/README.md)). GitHub OAuth token exchange runs in the **Cloudflare Worker** under [`cloudflare_site/worker/`](../cloudflare_site/worker/). OAuth/CORS hardening is summarized in [`docs/admin-oauth-worker.md`](admin-oauth-worker.md).

Deployment uses **Cloudflare Workers with [static assets](https://developers.cloudflare.com/workers/static-assets/)** (not the legacy **Pages direct-upload** / `wrangler pages deploy` flow). **Deploy Site** checks out **only the default branch** (trusted [`cloudflare_site/wrangler.toml`](../cloudflare_site/wrangler.toml); never PR-controlled config on the secret-bearing runner), downloads the build artifact, then **copies only** **`_bundle/public/`** and **`_bundle/worker/`** into **`cloudflare_site/`** (so a malicious artifact cannot overwrite `wrangler.toml` or other repo files), then runs Wrangler.

For architecture and naming, see [2026-04-09-site-cloudflare-pages-design.md](superpowers/specs/2026-04-09-site-cloudflare-pages-design.md) (document filename still says "pages" for history; content describes Workers). Repository layout for `web/` vs `cloudflare_site/` is decided in [ADR 0019](ADRs/0019-web-source-and-cloudflare-site-layout.md).

## Cloudflare setup

### Worker (not a Pages "project")

1. In the Cloudflare dashboard, use **Workers & Pages** → **Create** → **Create Worker** (or let the first `wrangler deploy` create it). The Worker name must match the GitHub variable below.
2. Configure **[preview URLs](https://developers.cloudflare.com/workers/configuration/previews/)** (default on when `workers_dev` is enabled). PR builds rely on **`wrangler versions upload`** with `--preview-alias`.
3. Optional: set a **[workers.dev](https://developers.cloudflare.com/workers/configuration/routing/workers-dev/)** subdomain for your account.

### API token

Create an API token that can deploy Workers for your account, for example:

- **Account** → **Cloudflare Workers** → **Edit** (or the "Edit Cloudflare Workers" template), and
- **Account** → **Account Settings** → **Read** if Wrangler requires it.

Store it as GitHub secret **`CLOUDFLARE_API_TOKEN`**. A token scoped **only** to "Cloudflare Pages — Edit" is **not** enough for `wrangler deploy` / `versions upload` on a Worker.

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

**Full stack (recommended for admin OAuth):** from the repository root, run **`npm run dev`** so Vite serves the SPA and Wrangler runs the site Worker with shared process env — see [`web/admin/README.md`](../web/admin/README.md).

**Static tree + Worker (closer to production asset layout):** install dependencies, build the admin SPA, assemble the layout CI uses under `cloudflare_site/public/`, then run Wrangler:

```bash
npm ci
npm run build
mkdir -p cloudflare_site/public/assets
mkdir -p cloudflare_site/public/admin
cp web/public/index.html cloudflare_site/public/index.html
cp web/public/graph.html cloudflare_site/public/graph.html
cp -a web/dist/assets/. cloudflare_site/public/assets/
cp -a web/dist/admin/. cloudflare_site/public/admin/
cd cloudflare_site && npx wrangler@4 dev
```

Requires a Cloudflare login or API token in the environment per [Wrangler docs](https://developers.cloudflare.com/workers/wrangler/).

## Troubleshooting

**Deploy job skipped.** The triggering workflow display name must be **Build Site** exactly, and `workflow_run.repository` must match the current repo.

**`Could not determine Workers deployment URL`.** The workflow reads `deployment-url` from `cloudflare/wrangler-action`, then falls back to parsing Wrangler stdout/stderr for a `workers.dev` URL. Upgrade **`wranglerVersion`** in the workflow if Wrangler output format changed.

**Preview upload fails (PR builds).** Requires Wrangler **≥ 4.21.0** for `--preview-alias`. The deploy workflow pins **4.110.0**.

**Artifact download 404.** **Build Site** must upload artifact **`site`**; **Deploy Site** needs `actions: read`.

**No PR comment.** Same as before: ambiguous `head` when resolving the PR number; see the design spec.

**Secrets missing on the deployed Worker / `missing_turnstile_keys` after deploy.** `cloudflare/wrangler-action` runs `wrangler secret bulk` using the **`name` in `wrangler.toml`**, not the `--name` flag on `wrangler deploy`. The deploy workflow patches `name` to match **`CLOUDFLARE_PROJECT_NAME`** before Wrangler runs so secrets and deploy target the same Worker. If you deploy manually with only `--name=…`, run `wrangler secret bulk` (or `wrangler secret put`) with the same **`--name`**.

**Dashboard:** Worker **Variables and Secrets** lists plain **vars** and encrypted **secrets**. Secret values are never shown after save; only names appear.
