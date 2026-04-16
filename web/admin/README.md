# Fullsend admin (Svelte SPA)

This directory holds the **admin installation UI**: a **Svelte 5 + Vite** single-page app served under the `/admin/` base path. Local **GitHub OAuth** token exchange runs in the repository’s **Cloudflare Worker** under [`../../cloudflare_site/worker/`](../../cloudflare_site/worker/); the browser never sends `client_secret` to GitHub directly. **Vite** is configured at the repository root (`vite.config.ts`). Local **`GITHUB_APP_*` values must be present in the environment** of the processes that run Vite and Wrangler (see **Environment** below).

Production packaging of this app for the public site is tracked in the repo-wide implementation plan (`docs/superpowers/plans/2026-04-12-fullsend-admin-spa.md`). Layout follows [ADR 0019](../../ADRs/0019-web-source-and-cloudflare-site-layout.md) (`web/` + root `package.json` + `cloudflare_site/`).

## Tooling with mise

The repository root includes `mise.toml`, which pins **Node 22** and **Go** (for `make lint` / Go tests). **Optionally**, mise also loads repo-root **`.env.local`** into your shell when that file exists (so `GITHUB_APP_*` are available to `npm run dev` without extra steps). This is **mise-only**; other setups are fine.

1. Install [mise](https://mise.jdx.dev/) if you do not already use it.
2. From the **repository root**: `mise trust` (required once per clone so mise will read `mise.toml`).
3. `cd` into the repo; run `mise install` if needed; `node`, `npm`, and `go` should come from mise.

You can use any Node 22 + npm install without mise; put credentials in the environment your own way.

## GitHub App (OAuth)

Create a **GitHub App** used for **user** sign-in to the admin UI (not the same as Fullsend agent apps):

1. GitHub → **Settings** → **Developer settings** → **GitHub Apps** → **New GitHub App** (or your org’s equivalent).
2. **Homepage URL:** e.g. `http://localhost:5173` (or match your dev origin).
3. **Callback URL** must match what the SPA uses **exactly** (same host and path). With the default Vite dev server, register **one** of:
   - `http://localhost:5173/admin/`
   - `http://127.0.0.1:5173/admin/`
   Use the same host in the browser when testing (`localhost` vs `127.0.0.1` are different origins).
4. Webhooks can stay inactive for local dev.
5. After creation, note the **Client ID** and generate a **client secret** once; treat the secret like a password.

## Environment

**`GITHUB_APP_CLIENT_ID`** and **`GITHUB_APP_CLIENT_SECRET`** must be in **`process.env`** when you run **`npm run dev`** (and in any shell where you run **`vite`** or **`wrangler dev`** alone). Root **`vite.config.ts`** and Wrangler (**`CLOUDFLARE_INCLUDE_PROCESS_ENV=true`**) read the environment only; they do not open env files themselves.

**mise users:** with repo-root **`.env.local`** present, mise injects those values into the shell (see **`mise.toml`**). **Everyone else:** use `export`, direnv, your editor, CI secrets, etc.

The SPA **does not** embed the GitHub App OAuth **client id** at build time. Sign-in navigates to **`GET /api/oauth/authorize`** on the site Worker, which **redirects** to `https://github.com/login/oauth/authorize` with `client_id` from Worker configuration (local env / Wrangler deploy only).

The committed **`sample.env.local`** at the repository root documents variable names and the GitHub App checklist; copy it to **`.env.local`** if you want a file-based workflow (and keep it gitignored).

## Production and CI (Cloudflare Workers)

**Build Site** runs **`npm run build`** with no GitHub App id in CI env (the static bundle stays id-agnostic).

**Deploy Site** passes **`GITHUB_APP_CLIENT_ID`** and **`GITHUB_APP_CLIENT_SECRET`** to Wrangler via repository variable **`FULLSEND_GITHUB_APP_CLIENT_ID`** and secret **`FULLSEND_GITHUB_APP_CLIENT_SECRET`** (`workflow_run` uses the base repo; set both for previews and production).

On the GitHub App, add a **Callback URL** that matches your deployed admin entry, for example `https://<your-project>.<account>.workers.dev/admin/` (use the exact URL your users open; trailing slash must match what the SPA sends as `redirect_uri`).

## Run the site locally

From the **repository root**:

```bash
npm ci
npm run dev
```

Then open **`http://localhost:5173/admin/`** (or `127.0.0.1` if that matches your callback URL). You should see the shell; **Sign in with GitHub** runs the OAuth flow against the Worker on port **8787**, proxied by Vite as **`/api/*`**.

Other useful commands:

- **`npm run dev:debug`** — noisier Worker + Vite proxy logging.
- **`npm run dev:vite`** — Vite only (no token exchange; sign-in will not work).
- **`npm run build`** / **`npm run preview`** — production build and static preview.
- **`npm run test`** / **`npm run check`** — Vitest and `svelte-check`.
