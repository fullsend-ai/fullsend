# Fullsend admin (Svelte SPA)

This directory holds the **admin installation UI**: a **Svelte 5 + Vite** single-page app served under the `/admin/` base path. Local **GitHub OAuth** token exchange runs in the repository’s **Cloudflare Worker** under [`../../cloudflare_site/worker/`](../../cloudflare_site/worker/); the browser never sends `client_secret` to GitHub directly. **Vite** is configured at the repository root (`vite.config.ts`). Local **`GITHUB_APP_*` values must be present in the environment** of the processes that run Vite and Wrangler (see **Environment** below).

**Hardening (CORS + config model):** for review and security context, see [`docs/admin-oauth-worker.md`](../../docs/admin-oauth-worker.md) — **`GET /api/github/user`** when `Origin` is missing (Sec-Fetch-Site + Referer, path-limited), and **why there is no `ADMIN_OAUTH_ENABLED` flag** (Turnstile + 503 `missing_turnstile_keys`, etc.).

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

**`GITHUB_APP_CLIENT_ID`**, **`GITHUB_APP_CLIENT_SECRET`**, **`TURNSTILE_SITE_KEY`**, and **`TURNSTILE_SECRET_KEY`** must be available to Wrangler when you run **`npm run dev`** (same shell / `.env.local` as **`GITHUB_APP_*`** if you use mise). The site Worker **refuses** admin `/api/*` traffic with **503** and JSON **`missing_turnstile_keys`** if either Turnstile value is missing or empty — there is no silent “Turnstile off” mode. Root **`vite.config.ts`** and Wrangler (**`CLOUDFLARE_INCLUDE_PROCESS_ENV=true`**) read the environment only; they do not open env files themselves. For Wrangler-only secrets in local dev, use **`cloudflare_site/.dev.vars`** (see [Wrangler secrets](https://developers.cloudflare.com/workers/configuration/secrets/)); keep it gitignored alongside **`.env.local`**.

**mise users:** with repo-root **`.env.local`** present, mise injects those values into the shell (see **`mise.toml`**). **Everyone else:** use `export`, direnv, your editor, CI secrets, etc.

The SPA **does not** embed the GitHub App OAuth **client id** at build time. Sign-in navigates to **`GET /api/oauth/authorize`** on the site Worker, which **redirects** to `https://github.com/login/oauth/authorize` with `client_id` from Worker configuration (local env / Wrangler deploy only).

**Origin (primary; `Referer` is not identity):** the Worker uses the **`Origin` header** for CORS on most `/api/*` routes and **requires** `Origin` for **`POST /api/oauth/token`** tab-binding to the `redirect_uri` origin. Full-page navigations to **`GET /api/oauth/authorize`** often omit `Origin`; in that case the Worker allows the request when `redirect_uri` is on the same public origin as the Worker, or when both the Worker URL and `redirect_uri` are **loopback HTTP** (typical Vite + Wrangler dev with different ports). The **narrow exception** for missing `Origin` — **`Sec-Fetch-Site` + `Referer` only for `GET`/`OPTIONS` `/api/github/user`** — is documented in [`docs/admin-oauth-worker.md`](../../docs/admin-oauth-worker.md); it does **not** apply to token exchange or authorize binding.

**Turnstile (required):** the Worker always folds the site key into GitHub’s `state` at authorize time; the SPA decodes it after redirect and runs an invisible Turnstile challenge before **`POST /api/oauth/token`**. Do **not** put Turnstile keys in the Vite build (`define` / `VITE_*`).

The committed **`sample.env.local`** at the repository root lists **official Cloudflare dummy Turnstile keys** for local dev plus the GitHub App checklist; copy it to **`.env.local`** (and align **`.dev.vars`** if you store the secret there only).

## Production and CI (Cloudflare Workers)

**Build Site** runs **`npm run build`** with no GitHub App id in CI env (the static bundle stays id-agnostic).

**Deploy Site** requires repository variable **`FULLSEND_TURNSTILE_SITE_KEY`** and secret **`FULLSEND_TURNSTILE_SECRET_KEY`** alongside the GitHub App variable/secret. Wrangler receives **`TURNSTILE_SITE_KEY`** as a var and **`TURNSTILE_SECRET_KEY`** via `wrangler-action`’s `secrets:` list; **missing values fail the deploy** (by design).

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
