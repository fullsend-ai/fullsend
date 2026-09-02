# Site deployment (Cloudflare Worker)

How the public site — the landing page, the document graph, and the VitePress
documentation — is built and deployed. For authoring the documentation itself, see
[`doc-site.md`](doc-site.md).

## Overview

The site is served by a **Cloudflare Worker with [static assets](https://developers.cloudflare.com/workers/static-assets/)**
(not the legacy **Pages direct-upload** / `wrangler pages deploy` flow). The Worker entry point is
[`cloudflare_site/worker/src/index.ts`](../cloudflare_site/worker/src/index.ts) and is a plain
passthrough to the `ASSETS` binding — it requires **no vars and no secrets**.

**Build Site** (`.github/workflows/site-build.yml`) builds the VitePress site and assembles
`_bundle/`:

| Bundle path | Source |
|---|---|
| `_bundle/public/index.html` | `web/public/index.html` (landing page, served at `/`) |
| `_bundle/public/graph.html` | `web/public/graph.html` (document graph, served at `/graph.html`) |
| `_bundle/public/docs/` | `docs/.vitepress/dist/` (documentation, served at `/docs/`) |
| `_bundle/public/` | `cloudflare_site/public/` (`robots.txt`, `llms.txt`) |
| `_bundle/worker/` | `cloudflare_site/worker/` |

**Deploy Site** (`.github/workflows/site-deploy.yml`) checks out **only the default branch** so
[`cloudflare_site/wrangler.toml`](../cloudflare_site/wrangler.toml) is always trusted and never
taken from a PR-built zip, downloads the artifact, **copies only** `_bundle/public/` and
`_bundle/worker/` into `cloudflare_site/`, then runs Wrangler.

Repository layout for `web/` vs `cloudflare_site/` is decided in
[ADR 0019](ADRs/0019-web-source-and-cloudflare-site-layout.md).

> **Not the mint.** The public token mint at `mint.fullsend.sh` is a **separate** Cloudflare
> Worker, provisioned from `internal/dispatch/cf/` with its own `wrangler.toml` under
> `internal/dispatch/cf/workersrc/`. Nothing in this document affects it. See
> [ADR 0068](ADRs/0068-public-community-mint-architecture.md).

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
- Set **`CLOUDFLARE_PROJECT_NAME`** as a GitHub **Actions variable**: value = **Worker name** in the dashboard. The deploy workflow passes it as `wrangler deploy --name=…` / `versions upload --name=…`.

### Custom domains

Attach routes or custom domains to the **Worker** (Workers → your Worker → **Domains & Routes**), not to a Pages project. Production URLs in GitHub Deployments follow the hostname Wrangler reports (often `*.workers.dev` until a custom domain is primary).

### Migrating from an old Pages project

If you previously used **Cloudflare Pages** with `wrangler pages deploy`, create the Worker as above, point DNS/custom hostnames to the Worker, then disable or delete the old Pages project to avoid confusion.

## GitHub setup

### Fork

On a **fork**, open **Settings → Secrets and variables → Actions**. Add secrets **`CLOUDFLARE_API_TOKEN`**, **`CLOUDFLARE_ACCOUNT_ID`**, and variable **`CLOUDFLARE_PROJECT_NAME`** (Worker name).

Under **Settings → Actions → General**, allow **Fork pull request workflows** from contributors so fork PRs can run **Build Site** without Cloudflare credentials in the fork.

**Deploy Site** runs in the base repository with secrets; fork workflow logs should not show those values.

### Upstream

Configure the same secrets/variables at org or repo scope. Confirm **`pull-requests: write`** on the deploy workflow matches org policy for fork PR comments.

Disable **GitHub Pages** under **Settings → Pages** if it was only used for this site.

## Local preview

For documentation authoring, `npm run docs:dev` is usually what you want (hot reload, no Wrangler).

To preview the assembled production layout through the Worker:

```bash
npm ci
npm run docs:build
mkdir -p cloudflare_site/public/docs
cp web/public/index.html cloudflare_site/public/index.html
cp web/public/graph.html cloudflare_site/public/graph.html
cp -a docs/.vitepress/dist/. cloudflare_site/public/docs/
npm run dev:worker
```

Requires a Cloudflare login or API token in the environment per [Wrangler docs](https://developers.cloudflare.com/workers/wrangler/).

## Troubleshooting

**Deploy job skipped.** The triggering workflow display name must be **Build Site** exactly, and `workflow_run.repository` must match the current repo.

**`Could not determine Workers deployment URL`.** The workflow reads `deployment-url` from `cloudflare/wrangler-action`, then falls back to parsing Wrangler stdout/stderr for a `workers.dev` URL. Upgrade **`wranglerVersion`** in the workflow if Wrangler output format changed.

**Preview upload fails (PR builds).** Requires Wrangler **≥ 4.21.0** for `--preview-alias`. The pinned version is `wranglerVersion` in [`site-deploy.yml`](https://github.com/fullsend-ai/fullsend/blob/main/.github/workflows/site-deploy.yml) — check there rather than trusting a number copied into this page.

**Artifact download 404.** **Build Site** must upload artifact **`site`**; **Deploy Site** needs `actions: read`.

**Stale `/admin/*` links.** The admin SPA was removed. `not_found_handling = "404-page"` means old admin URLs correctly return HTTP 404 with `public/404.html`.
