# Site deployment (Cloudflare Worker)

How the public site — the landing page, the document graph, and the VitePress
documentation — is built and deployed. For authoring the documentation itself, see
[`doc-site.md`](doc-site.md).

## Overview

The site is served by a **Cloudflare Worker with [static assets](https://developers.cloudflare.com/workers/static-assets/)**
(not the legacy **Pages direct-upload** / `wrangler pages deploy` flow). The Worker entry point is
[`cloudflare_site/worker/src/index.ts`](../cloudflare_site/worker/src/index.ts) and is a plain
passthrough to the `ASSETS` binding — it requires **no vars and no secrets**.

**Site** (`.github/workflows/site.yml`) is one workflow with a `build` job and a `deploy`
job. `build` runs the VitePress site and assembles `_bundle/`; `deploy` publishes that
artifact. The jobs stay separate so Cloudflare secrets never sit on the runner that
executes PR-controlled `npm ci` / `npx mvb`, and so `wrangler.toml` is always taken from
the default branch rather than from a PR tree.

| Bundle path | Source |
|---|---|
| `_bundle/public/index.html` | `web/public/index.html` (landing page, served at `/`) |
| `_bundle/public/graph.html` | `web/public/graph.html` (document graph, served at `/graph.html`) |
| `_bundle/public/404.html` | `web/public/404.html` |
| `_bundle/public/docs/` | `docs/.vitepress/dist/` (documentation at `/docs/`) |
| `_bundle/public/` | `cloudflare_site/public/` (`robots.txt`, `llms.txt`) |
| `_bundle/worker/` | `cloudflare_site/worker/` |

The `deploy` job checks out **the default branch on pull requests** (and the pushed commit on
`main`) so [`cloudflare_site/wrangler.toml`](../cloudflare_site/wrangler.toml) is always trusted
and never taken from a PR-built zip, downloads the artifact, **copies only** `_bundle/public/`
and `_bundle/worker/` into `cloudflare_site/`, then runs Wrangler. Fork PRs still **build**
(so the site compiles) but **skip deploy**: `pull_request` from a fork does not receive
repository secrets. Same-repository PRs upload a preview version; pushes to `main` deploy
production. A newer run in the same concurrency group cancels an in-flight one, so production
always comes from the latest `main` push that touched site files.

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

Fork PRs run the `build` job in the fork (no Cloudflare credentials required). They do **not**
get a preview URL: GitHub does not pass repository secrets to `pull_request` workflows from
forks, and this workflow does not use `workflow_run` / `pull_request_target` to work around
that.

Under **Settings → Actions → General**, allow **Fork pull request workflows** from contributors
so fork PRs can still run **Site** / `build` as a compile check.

### Upstream

Configure secrets **`CLOUDFLARE_API_TOKEN`** and **`CLOUDFLARE_ACCOUNT_ID`**, and variable
**`CLOUDFLARE_PROJECT_NAME`** (Worker name), at org or repo scope. Confirm
**`pull-requests: write`** on the `deploy` job matches org policy for same-repository PR
preview comments.

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
cp web/public/404.html cloudflare_site/public/404.html
cp -a docs/.vitepress/dist/. cloudflare_site/public/docs/
npm run dev:worker
```

Requires a Cloudflare login or API token in the environment per [Wrangler docs](https://developers.cloudflare.com/workers/wrangler/).

## Troubleshooting

**Deploy job skipped.** Fork PRs skip `deploy` (no secrets). Same-repository PRs and `push` to `main` run `deploy` after a successful `build`.

**`Could not determine Workers deployment URL`.** For PR uploads the workflow prefers a `workers.dev` URL that contains the preview alias (`pr-<number>`) from Wrangler stdout/stderr. Otherwise it uses `deployment-url` from `cloudflare/wrangler-action`, then the first `workers.dev` URL in the logs. Upgrade **`wranglerVersion`** in the workflow if Wrangler output format changed.

**Preview upload fails (PR builds).** Requires Wrangler **≥ 4.21.0** for `--preview-alias`. The pinned version is `wranglerVersion` in [`site.yml`](https://github.com/fullsend-ai/fullsend/blob/main/.github/workflows/site.yml) — check there rather than trusting a number copied into this page.

**Artifact download 404.** The `build` job must upload artifact **`site`**; `deploy` downloads it in the same workflow run.

**Stale `/admin/*` links.** The admin SPA was removed. A 404 will be returned.
