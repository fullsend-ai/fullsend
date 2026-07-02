# Fullsend admin installation SPA Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a **static TypeScript + Svelte** admin SPA that mirrors the Go CLI’s admin responsibilities (install / analyze / uninstall semantics via a TypeScript layer engine), hosted on the **existing Cloudflare Workers + GitHub Actions** site pipeline (**per-PR previews**, production on `main`), with **GitHub App–centric auth** (production + preview hash handoff per spec).

**Architecture:** **Approach 1** from the spec—reimplement layer orchestration and GitHub REST/GraphQL usage in TypeScript, aligned with `internal/layers/*` and `internal/forge/github`. **Routing:** start with **hash-based client routing** under `base: '/admin/'` so deep links work on Workers static assets without relying on subdirectory SPA `index.html` fallbacks (migrate to path-based history later if Wrangler proves to serve `/admin/*` → `admin/index.html` correctly). **Branching:** **`origin/main` already includes the Cloudflare site pipeline** (aligned with the former `site-cloudflare-workers` work). Branch from **`origin/main`**, open PRs **into `origin/main`** so **existing** `Build Site` / `Deploy Site` + secrets stay valid. When Cloudflare work is merged **upstream**, rebase onto upstream `main` and retarget PRs there as usual.

**Tech Stack:** Svelte 5 + TypeScript, Vite 6, Vitest, GitHub REST (`@octokit/rest`) and GraphQL (`@octokit/graphql`) as needed, `pnpm` or `npm` (pick one in Task 2 and keep it), **Wrangler** for a small **OAuth exchange Worker** merged into the site Worker under `cloudflare_site/worker/` (local dev alongside Vite; production ships via the **Build Site** artifact), GitHub Actions (extend `.github/workflows/site-build.yml`), Cloudflare Workers static assets (`cloudflare_site/wrangler.toml`, `cloudflare_site/public/` from artifact).

**Implementation note ([ADR 0019](../../ADRs/0019-web-source-and-cloudflare-site-layout.md)):** Browser source lives under `web/` (admin SPA in `web/admin/`). **Node tooling** (`package.json`, lockfile, `npm run dev` / `npm run build`) lives at the **repository root**. Wrangler configuration and the Worker live under `cloudflare_site/`. Where this plan still says `admin/*` or `site/*` for layout, read **`web/admin/*`** and **`cloudflare_site/*`** respectively unless history is explicitly meant.

**Execution:** **Subagent-driven** (use `subagent-driven-development`: one subagent per plan task, review between tasks).

**UI stack:** **Svelte** is **locked in** (Svelte 5 + Vite); recorded in the spec Open items.

**Spec:** [2026-04-06-fullsend-admin-spa-design.md](../specs/2026-04-06-fullsend-admin-spa-design.md) (architecture, auth, wizards)

**UX spec (screens, copy, states, errors):** [2026-04-21-fullsend-admin-spa-ux-design.md](../specs/2026-04-21-fullsend-admin-spa-ux-design.md) — use wherever tasks touch login, nav chrome, org list, org dashboard, or row/global error presentation. **Not** a commitment to implement every UX row in one PR; tasks below cite the sections that matter.

**Related CI/site spec:** [2026-04-09-site-cloudflare-pages-design.md](../specs/2026-04-09-site-cloudflare-pages-design.md)

**Task order:** **Tasks 1–14** follow the main build-out (scaffold through mutating wizard). **Task 15** (preview OAuth handoff) sits **after Task 14** and **before Task 16** (local dev doc) so it can be redesigned: **Turnstile on token exchange** (Worker hardening) may be **awkward or incompatible** with how we want **per-PR preview** review sites to behave—capture an explicit workaround before implementing the flow described below. **Task 16** is documentation only.

**Tasks 1–9 snapshot (2026-04-23):** **Tasks 1–6** and **Task 7 OAuth plumbing** (PKCE, SPA entry callback, same-origin exchange, session refresh) are **done** in repo. **Task 4b** [Step 6](#task-4b-ship-task-2b-oauth-worker-with-site-worker--static-assets) (GitHub App callback URL registration for every preview host) stays an **ongoing maintainer checklist**. **Task 7** is **not** yet **UX-spec-complete** per [2026-04-21 UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md) — see the **2026-04-23** table under [Task 7](#task-7-production-sign-in-authorize-url--spa-document-callback). **Task 6** vs that spec: today `fullsend:github-unauthorized` triggers **sign-out**, not a **Re-authenticate** [global banner](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-banners-screen-level-below-the-nav-bar). **Task 9** is **done** for **fetch + filter + `#/orgs` + in-memory cache** (`web/admin/src/lib/orgs/*`, `OrgList.svelte`), including **smaller repo-list pages**, **progressive org paint**, **15-row cap**, and **in-list loading spinner**; it remains **not** **UX-spec-complete** for **live** row states (**Configure** / **Deploy** / **Cannot deploy** / per-row errors) until **Task 10+** — see the **2026-04-23** table under [Task 9](#task-9-org-list-alphabetical-search--in-memory-session-cache).

---

## File map

| File / directory | Responsibility |
|------------------|----------------|
| `package.json` (repo root) | SPA dependencies; scripts: `dev` (Vite + Worker via `concurrently` or equivalent), `build`, `test`, `check` |
| `vite.config.ts` (repo root) | `root: web/admin`, `base: '/admin/'`, Svelte plugin; **`server.proxy`** for `/api/*` → local Wrangler (`127.0.0.1:8787` or chosen port); **`GITHUB_APP_*` from `process.env` only** |
| `cloudflare_site/wrangler.toml` | Wrangler project + `[assets]`; deploy: vars/secrets in Cloudflare / CI; local dev: `CLOUDFLARE_INCLUDE_PROCESS_ENV` + shell env |
| `cloudflare_site/worker/` | Worker entry: `/api/*` OAuth BFF (localhost-only) + **`ASSETS` fallback** for static site |
| `web/admin/src/lib/auth/pkce.ts` | Generate `code_verifier`, `code_challenge` (S256), helpers for authorize + exchange body |
| `sample.env.local` (repo root) | Committed example + checklist; copy to **`.env.local`** if desired. **mise** loads `.env.local` into the shell per root **`mise.toml`**; Vite/Wrangler still use **`process.env` only** |
| `web/admin/tsconfig.json` / `web/admin/svelte.config.js` | TS + Svelte compiler options |
| `web/admin/index.html` | Vite HTML entry |
| `web/admin/src/main.ts` | Bootstraps Svelte app, mounts router |
| `web/admin/src/App.svelte` | Shell layout, nav, sign-in/out (evolve toward [UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md): account bar, login gate, global banners) |
| `web/admin/src/lib/github/client.ts` | Octokit factory from stored token |
| `web/admin/src/lib/auth/tokenStore.ts` | `localStorage` read/write/clear; **no logging** of secrets |
| `web/admin/src/lib/auth/oauth.ts` | Production OAuth: authorize URL (include PKCE **`code_challenge`** + **`S256`**), callback parsing; exchange via **same-origin** `fetch('/api/oauth/...')` to Worker (not GitHub cross-origin) |
| `web/admin/src/lib/auth/previewHandoff.ts` | `return_to` allowlist, `state`/`sessionStorage`, fragment parse |
| `web/admin/src/lib/status/types.ts` | TS mirrors of `LayerStatus` / `LayerReport` from `internal/layers/layers.go` |
| `web/admin/src/lib/status/engine.ts` | Read-only analyze-style rollup (grows over phases) |
| `web/admin/src/routes/*` | Svelte views (hash routes): org list, org dashboard, repo list — layout and state tables: [UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md) |
| `web/admin/src/lib/auth/oauth.test.ts` | Vitest: callback parsing, storage |
| `web/admin/src/lib/auth/previewHandoff.test.ts` | Vitest: allowlist accepts production admin origin only |
| `.github/workflows/site-build.yml` | Setup Node, `npm ci` + `npm run build`, copy `web/admin/dist` → `_bundle/public/admin/`, mindmap from `web/public/`, worker from `cloudflare_site/worker/` |
| `cloudflare_site/worker/src/index.ts` | **Task 4b** (implemented): OAuth + **`ASSETS` fallback**; preview allowlists evolve with **Task 15** |
| `docs/admin-spa-local-dev.md` | **Task 16:** local dev GitHub App (localhost callback), env vars, `npm run dev` (Vite + Worker); cross-link **`web/admin/README.md`** |
| `docs/superpowers/specs/2026-04-06-fullsend-admin-spa-design.md` | Appendix A permission matrix rows; Open items: OAuth static verification outcome |

**Do not add** automated CLI↔SPA parity tests in CI in this plan (explicit non-goal in spec).

---

### Task 1: Branch + OAuth / token-exchange verification gate

**Status (2026-04-12):** **Complete** for maintainer-driven steps: branch exists, experiment helper (`oauth-localhost-part-b/`) validated authorize → same-origin proxy → GitHub exchange (with optional `CLIENT_SECRET`), spec Open items + Appendix A updated; Part C curl optional and recorded as satisfied by the proxy path.

**Files:**

- Modify: `docs/superpowers/specs/2026-04-06-fullsend-admin-spa-design.md` (Open items + Appendix A note)

- [x] **Step 1: Create working branch from `origin/main`**

```bash
git fetch origin
git checkout -b feat/admin-spa-phase1 origin/main
```

- [x] **Step 2: GitHub App registration for the OAuth experiment**

You only need a **personal test GitHub App** (under your user **or** a throwaway org). No org membership is required to validate **user** token exchange; add org/repo later for admin features.

1. **Create the app:** GitHub → **Settings** → **Developer settings** → **GitHub Apps** → **New GitHub App** (or org **Settings** → **GitHub Apps** → **New GitHub App**).
2. **GitHub App name:** any unique name (e.g. `yourname-fullsend-admin-oauth-test`).
3. **Homepage URL:** `http://localhost:5173` for local experiment (or your fork URL if you test on Cloudflare later).
4. **Callback URL (exactly one for the first experiment):** `http://localhost:5173/oauth/callback.html`
   - Must match **character-for-character** what you use as `redirect_uri` in authorize + exchange requests.
   - For a fork-hosted static test, use `https://<your-workers-dev-host>/oauth/callback.html` and register that URL here too (GitHub allows multiple callback URLs).
5. **Webhook:** uncheck **Active** (or set a dummy URL) for this experiment—you do not need webhooks.
6. **Permissions:** start with **read-only** minimum, e.g. under **Account** → **Email addresses: Read-only** (optional) or leave defaults; you only need enough to call `GET /user` after you have a token. You can add **Metadata** read for repos later.
7. **Where is the app installed?** choose **Any account** for a quick personal test.
8. After creation, note **App ID**, **Client ID** (this is the OAuth `client_id`), and click **Generate a new client secret** once—copy the **client secret** into a password manager; **never commit it**.

**PKCE (recommended for the real app):** for the experiment you may omit PKCE first to reduce variables; then repeat with `code_challenge` / `code_verifier` per [Generating a user access token for a GitHub App](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app).

- [x] **Step 3: Run the browser-vs-server experiment (no client secret in git)**

**Hypothesis (from docs):** `POST https://github.com/login/oauth/access_token` lists **`client_secret` as required** for the web application flow, so a **pure static browser** exchange is either impossible or unsafe.

**Part A — Get a `code` (browser, no secrets):**

1. Start Vite later (Task 2) or open any blank page; navigate to (replace `CLIENT_ID`):

   `https://github.com/login/oauth/authorize?client_id=CLIENT_ID&redirect_uri=http%3A%2F%2Flocalhost%3A5173%2Foauth%2Fcallback.html&state=randomopaquestring`

2. Approve the app. You land on `http://localhost:5173/oauth/callback.html?code=...&state=...` (or copy `code` from the address bar if the file does not exist yet).

**Part B — (Optional) Try exchange without `client_secret` in the browser**

Cross-origin `fetch` from `http://localhost:5173` to `https://github.com/login/oauth/access_token` is usually **blocked by CORS** (no `Access-Control-Allow-Origin`). If the request fails with a **network / CORS** error, record that: the endpoint is **not** intended for browser direct access. If you somehow get a JSON body, check for `incorrect_client_credentials` or missing `access_token`.

**Where to run Part B:** use a real **`http://localhost:5173`…** document. The repo includes [`docs/superpowers/experiments/oauth-localhost-part-b/serve.py`](https://github.com/fullsend-ai/fullsend/blob/14b0b41d/docs/superpowers/experiments/oauth-localhost-part-b/serve.py): `cd` that directory, `export CLIENT_ID='…'` (and optionally `CLIENT_SECRET` for a full exchange), run `python3 serve.py`, open **`http://localhost:5173/`** in Chrome or Firefox (not an embedded IDE preview). After redirect, `/oauth/callback.html` calls **`POST /_experiment/github-access-token`** on the same origin only (no prior cross-origin `fetch` to GitHub), so the one-time `code` is not consumed before the server-side exchange. **Do not** mix `http://127.0.0.1:5173` and `http://localhost:5173` in the address bar vs the registered callback URL (different origins). Task 2’s Vite dev server on the same port is also fine once it exists. **Do not** rely on `file://` or `chrome://`-style pages with strict CSP for the authorize/callback flow. For a **manual** CORS/CSP experiment, you can still paste the Part B `fetch` snippet in DevTools yourself (see below)—know that a successful reach to GitHub may **invalidate** the `code` before `curl` Part C or the proxy run.

Example (DevTools console on **localhost** after Task 2; fill placeholders):

```javascript
await fetch("https://github.com/login/oauth/access_token", {
  method: "POST",
  headers: {
    Accept: "application/json",
    "Content-Type": "application/x-www-form-urlencoded",
  },
  body: new URLSearchParams({
    client_id: "YOUR_CLIENT_ID",
    code: "CODE_FROM_REDIRECT",
    redirect_uri: "http://localhost:5173/oauth/callback.html",
  }),
}).then((r) => r.json());
```

**Do not** embed `client_secret` in this script.

**Part C — Exchange with `client_secret` (local terminal only, secret never in repo or browser bundle):**

**This step is not JavaScript.** Run **`curl` in your normal shell** (same machine as the browser is fine). No local HTTP server, no DevTools, no CORS or CSP on your page—`curl` talks to GitHub directly.

```bash
curl -sS -X POST 'https://github.com/login/oauth/access_token' \
  -H 'Accept: application/json' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'client_id=YOUR_CLIENT_ID' \
  --data-urlencode 'client_secret=YOUR_CLIENT_SECRET' \
  --data-urlencode 'code=CODE_FROM_REDIRECT' \
  --data-urlencode 'redirect_uri=http://localhost:5173/oauth/callback.html'
```

**Expected:** JSON containing `access_token` (`ghu_...`), `token_type`, and (if expiring tokens enabled) `refresh_token` / `expires_in`.

**Part D — Prove the token (optional):**

```bash
curl -sS -H 'Authorization: Bearer ghu_...' -H 'Accept: application/vnd.github+json' \
  'https://api.github.com/user'
```

- [x] **Step 4: Record the conclusion in the spec**

In `docs/superpowers/specs/2026-04-06-fullsend-admin-spa-design.md` **Open items**, replace the OAuth bullet with dated facts:

1. Outcome of **Part B** vs **Part C** (did browser-only exchange ever return a valid `ghu_` token?).
2. Chosen **smallest adjustment** for production (e.g. **Cloudflare Worker** `POST` proxy with `GITHUB_APP_CLIENT_SECRET` in Wrangler secrets, or another maintainer-approved pattern). **Device flow** is documented for headless apps—not a substitute for a browser admin SPA.

- [x] **Step 5: Append Appendix A row for OAuth token exchange**

| Capability | HTTP | Notes |
|------------|------|--------|
| User access token exchange (web flow) | `POST https://github.com/login/oauth/access_token` | `client_id`, **`client_secret`**, `code`, optional `redirect_uri`, optional PKCE `code_verifier`; response `access_token` (`ghu_`) |

Adjust the table format to match whatever Appendix A uses once the first real row is added.

- [x] **Step 6: Commit**

```bash
git add docs/superpowers/specs/2026-04-06-fullsend-admin-spa-design.md
git commit -m "docs: record GitHub App OAuth experiment outcome for admin SPA"
```

(Commits may be split across PR commits; requirement is spec + plan reflect outcomes.)

**Note:** It is fine to run this experiment **before** Task 2 exists; you only need the callback URL to exist for GitHub’s redirect—use a static file on disk opened via `file://` **only if** GitHub allows it (they usually require `http(s)`); prefer `npm create vite@latest` scratch or a one-line `python -m http.server` serving a folder containing `oauth/callback.html` that prints the query string.

---

### Task 2: Scaffold Vite + Svelte admin app (hash routing, `/admin/` base)

**Status (2026-04-20):** **Complete** in repo under **`web/admin/`** (ADR 0019). **Root `package.json`** holds `dev` / `build` / `test` / `check` (not a nested `admin/package.json`). Snippets below still say **`admin/…`** for historical reasons—treat as **`web/admin/…`**.

**Files:**

- Create: `web/admin/` tree (`index.html`, `src/*`, `tsconfig.json`, `svelte.config.js`, etc.) — equivalent to the plan’s original `admin/*` list

- [x] **Step 1: Write `admin/package.json`**

```json
{
  "name": "fullsend-admin",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "preview": "vite preview",
    "test": "vitest run",
    "check": "svelte-check --tsconfig ./tsconfig.json"
  },
  "devDependencies": {
    "@sveltejs/vite-plugin-svelte": "^5.0.0",
    "@tsconfig/svelte": "^5.0.0",
    "svelte": "^5.0.0",
    "svelte-check": "^4.0.0",
    "typescript": "^5.6.0",
    "vite": "^6.0.0",
    "vitest": "^3.0.0",
    "jsdom": "^25.0.0"
  },
  "dependencies": {
    "svelte-spa-router": "^4.0.1"
  }
}
```

**Note:** Root `npm run dev` now runs **Task 2b**’s Worker + Vite together; `vite`-only remains as an escape hatch if present in `package.json`.

- [x] **Step 2: Write `admin/vite.config.ts`**

```typescript
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vitest/config";

export default defineConfig({
  base: "/admin/",
  plugins: [svelte()],
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts"],
  },
});
```

- [x] **Step 3: Write `admin/tsconfig.json`**

```json
{
  "extends": "@tsconfig/svelte/tsconfig.json",
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "verbatimModuleSyntax": true,
    "strict": true,
    "skipLibCheck": true,
    "types": ["vitest/globals"]
  },
  "include": ["src/**/*.ts", "src/**/*.svelte"]
}
```

- [x] **Step 4: Write `admin/svelte.config.js`**

```javascript
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";

/** @type {import('@sveltejs/vite-plugin-svelte').SvelteConfig} */
export default { preprocess: vitePreprocess() };
```

If `npm install` reports a **peer dependency** conflict between `svelte-spa-router` and Svelte 5, drop that dependency and replace Task 2 Step 9 routing with a small in-repo **hashchange** router (`admin/src/lib/hashRouter.ts` exporting `currentPath` / `navigate`) plus static `routes` map—keep routes functionally identical for later tasks.

- [x] **Step 5: Write `admin/index.html`**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Fullsend Admin</title>
  </head>
  <body>
    <div id="app"></div>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
```

- [x] **Step 6: Write `admin/src/vite-env.d.ts`**

```typescript
/// <reference types="svelte" />
/// <reference types="vite/client" />
```

- [x] **Step 7: Write `admin/src/main.ts`**

```typescript
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";

mount(App, { target: document.getElementById("app")! });
```

- [x] **Step 8: Write `admin/src/app.css`**

```css
:root {
  font-family: system-ui, sans-serif;
  line-height: 1.4;
}
body {
  margin: 0;
}
```

- [x] **Step 9: Write `admin/src/App.svelte`**

```svelte
<script lang="ts">
  import Router from "svelte-spa-router";
  import Home from "./routes/Home.svelte";

  const routes = {
    "/": Home,
  };
</script>

<header class="bar">
  <strong>Fullsend Admin</strong>
  <span class="tag">preview-aware</span>
</header>
<main class="main">
  <Router {routes} />
</main>

<style>
  .bar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid #ccc;
  }
  .tag {
    font-size: 0.75rem;
    opacity: 0.7;
  }
  .main {
    padding: 1rem;
  }
</style>
```

- [x] **Step 10: Write `admin/src/routes/Home.svelte`**

```svelte
<script lang="ts">
  const hash =
    typeof window !== "undefined" ? window.location.hash || "#/" : "#/";
</script>

<p>Admin shell is up.</p>
<p>Current hash: <code>{hash}</code></p>
```

- [x] **Step 11: Write `admin/.gitignore`**

```
node_modules
dist
.DS_Store
.env.local
.dev.vars
.wrangler
```

- [x] **Step 12: Install and build locally**

Run:

```bash
npm ci && npm run build && npm run test
```

Expected: tests pass (auth + status modules landed after Task 3); `web/admin/dist/index.html` exists.

- [x] **Step 13: Commit**

```bash
git add web/admin
git commit -m "feat(admin): scaffold Vite+Svelte SPA under /admin/"
```

**Ordering:** complete **Task 2b** next (same PR stack is fine) so `npm run dev` already runs the OAuth Worker beside Vite before building auth-heavy UI in Tasks 3+.

---

### Task 2b: OAuth exchange Worker + Vite dev integration + PKCE

**Status (2026-04-20):** **Complete** in repo: root **`vite.config.ts`** proxies `/api` to Wrangler; **`web/admin/src/lib/auth/pkce.ts`** (+ tests); **`sample.env.local`** at repo root; Worker + Turnstile behavior as listed under **Files** below.

**Goal:** Replicate the successful **localhost auth flow** (authorize redirect → callback → **server-side** token `POST` with `client_secret`) inside the **admin** repo layout, started **together** with the Svelte dev server. The browser only talks **same-origin** to a tiny **Cloudflare Worker** (Wrangler dev); the Worker calls GitHub. Add **PKCE** (`code_challenge` on authorize, `code_verifier` on exchange) per [GitHub PKCE guidance](https://github.blog/changelog/2025-07-14-pkce-support-for-oauth-and-github-app-authentication/) and [Generating a user access token for a GitHub App](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app).

**Files:**

- Implemented under repo root: [`cloudflare_site/wrangler.toml`](../../../cloudflare_site/wrangler.toml) — Worker `main`, `[assets]`, **`[[ratelimits]]`** for OAuth token + GitHub user proxy; **`GITHUB_APP_CLIENT_ID`** and **`GITHUB_APP_CLIENT_SECRET`** via process env / Wrangler vars + secrets (local: `CLOUDFLARE_INCLUDE_PROCESS_ENV`); **required** **`TURNSTILE_SITE_KEY`** + **`TURNSTILE_SECRET_KEY`** (503 `missing_turnstile_keys` if absent); **`client_secret`** never in the SPA bundle
- Implemented: [`cloudflare_site/worker/src/index.ts`](../../../cloudflare_site/worker/src/index.ts) — `GET /api/oauth/authorize` (302 to GitHub with `client_id` from env); Worker-expanded `state` embedding Turnstile **site** key; `POST /api/oauth/token` with JSON `{ code, redirect_uri, code_verifier, turnstile_token }`; `GET /api/github/user` proxy. Validates `redirect_uri` allowlist (HTTPS or loopback `/admin/` entry). **No `Referer` fallback** — **`Origin` only** for CORS and for token tab-binding; **`GET /api/oauth/authorize`** without `Origin` uses the navigation rule (admin README / PR #240 High 1). GitHub token exchange uses `application/x-www-form-urlencoded`. **Hardening:** Cloudflare Turnstile siteverify on every token exchange; Wrangler **native rate limits** (30 / 60s on token exchange, 120 / 60s on `GET /api/github/user`, per Cloudflare location) keyed by path + `CF-Connecting-IP`.
- Modify: root [`vite.config.ts`](../../../vite.config.ts) — `server.proxy` `/api` → `http://127.0.0.1:8787` (Wrangler dev port)
- Modify: **repo root** [`package.json`](../../../package.json) — `wrangler`, `concurrently`; `npm run dev` runs Worker + Vite; optional `dev:vite`-only escape hatch if present
- Create: [`web/admin/src/lib/auth/pkce.ts`](../../../web/admin/src/lib/auth/pkce.ts) — `randomVerifier()`, `challengeS256(verifier)` using **Web Crypto** (`crypto.subtle.digest`) so the SPA matches GitHub’s S256 rules
- Create: [`web/admin/src/lib/auth/pkce.test.ts`](../../../web/admin/src/lib/auth/pkce.test.ts) — Vitest: length / shape / stable challenge for fixture verifier (use known test vector or mock subtle)
- Repo-root [`sample.env.local`](../../../sample.env.local) — documents **`GITHUB_APP_CLIENT_ID`** / **`GITHUB_APP_CLIENT_SECRET`** and **required** Turnstile keys (includes **official Cloudflare dummy** site + secret for local dev); SPA does **not** embed client id; Worker adds it at authorize. **Turnstile:** `TURNSTILE_SITE_KEY` / `TURNSTILE_SECRET_KEY` are **Worker-only**; the SPA bundle must **not** bake them in — the site key reaches the browser only via **Worker-expanded OAuth `state`** after authorize (see design Appendix A / High 1 plan). **Do not commit** `.env.local` or `.dev.vars`
- Modify: `web/admin/.gitignore` (or root) — ensure `.env.local`, `.dev.vars`, `.wrangler` present (may already be from Task 2 Step 11)

- [x] **Step 1: Add Worker + Wrangler config** — minimal `fetch` handler + CORS for **loopback** dev origins **or** browser origin equal to the Worker’s public origin (previews/production same host). **No `Referer`-based origin inference.** No logging of secrets or tokens.

- [x] **Step 2: Wire `npm run dev`** — one command starts **Wrangler dev** and **Vite**; confirm browser `fetch('http://localhost:5173/api/oauth/...')` (path per `vite.config.ts` proxy) hits the Worker and returns JSON.

- [x] **Step 3: PKCE helpers + tests** — implement `pkce.ts` + `pkce.test.ts`; document storing **`code_verifier` in `sessionStorage`** from authorize click until callback/finish (same pattern as existing callback handoff plans).

- [x] **Step 4: `sample.env.local`** — full GitHub App + env walkthrough; example keys only (no real secrets).

- [x] **Step 5: Manual smoke** — local GitHub App callback `http://localhost:5173/admin/` (or `127.0.0.1`); end-to-end: authorize → brief `/admin/?code=…` → same load replaces to `/admin/#/` → proxied Worker exchange → `ghu_` token only in controlled UI (not console-logged).

- [x] **Step 6: Commit**

```bash
git add cloudflare_site web/admin vite.config.ts package.json sample.env.local
git commit -m "feat(admin): OAuth exchange Worker, Vite dev proxy, PKCE helpers"
```

**Production follow-up:** Task **4b** is implemented via [`cloudflare_site/`](../../../cloudflare_site/) (Worker + static assets, same hostname as `/admin/`). OAuth hardening from PR #240 High 1 (Origin-only tab binding, Turnstile, native rate limits) is implemented in the Worker + Wrangler config above.

---

### Task 3: Vitest for `tokenStore` + `previewHandoff` allowlist

**Status (2026-04-20):** **Complete** under `web/admin/src/lib/auth/`.

**Files:**

- Create: `web/admin/src/lib/auth/tokenStore.ts`
- Create: `web/admin/src/lib/auth/tokenStore.test.ts`
- Create: `web/admin/src/lib/auth/previewHandoff.ts`
- Create: `web/admin/src/lib/auth/previewHandoff.test.ts`

- [x] **Step 1: Write failing tests `admin/src/lib/auth/tokenStore.test.ts`**

```typescript
import { describe, it, expect, beforeEach } from "vitest";
import { clearSession, loadToken, saveToken } from "./tokenStore";

const KEY = "fullsend_admin_github_token";

beforeEach(() => {
  localStorage.clear();
  clearSession();
});

describe("tokenStore", () => {
  it("saveToken and loadToken round-trip", () => {
    saveToken({ accessToken: "abc", tokenType: "bearer", expiresAt: 123 });
    expect(loadToken()).toEqual({
      accessToken: "abc",
      tokenType: "bearer",
      expiresAt: 123,
    });
  });

  it("clearSession removes token", () => {
    saveToken({ accessToken: "x", tokenType: "bearer", expiresAt: 1 });
    clearSession();
    expect(loadToken()).toBeNull();
  });
});
```

- [x] **Step 2: Run tests (expect failure: module missing)**

Run:

```bash
npm run test
```

Expected: (historical TDD order) FAIL until implementation exists — current `main` branch has modules; tests PASS.

- [x] **Step 3: Implement `admin/src/lib/auth/tokenStore.ts`**

```typescript
export type StoredToken = {
  accessToken: string;
  tokenType: string;
  expiresAt: number;
};

const KEY = "fullsend_admin_github_token";

export function saveToken(t: StoredToken): void {
  localStorage.setItem(KEY, JSON.stringify(t));
}

export function loadToken(): StoredToken | null {
  const raw = localStorage.getItem(KEY);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as StoredToken;
  } catch {
    return null;
  }
}

export function clearSession(): void {
  localStorage.removeItem(KEY);
}
```

- [x] **Step 4: Run tokenStore tests**

Run:

```bash
npx vitest run --config vite.config.ts web/admin/src/lib/auth/tokenStore.test.ts
```

Expected: PASS.

- [x] **Step 5: Write failing tests `admin/src/lib/auth/previewHandoff.test.ts`**

```typescript
import { describe, it, expect } from "vitest";
import { assertAllowedReturnTo } from "./previewHandoff";

describe("assertAllowedReturnTo", () => {
  it("accepts exact https preview origin", () => {
    expect(() =>
      assertAllowedReturnTo(
        "https://pr-123.fullsend-admin.pages.dev/",
        ["https://pr-123.fullsend-admin.pages.dev"],
      ),
    ).not.toThrow();
  });

  it("rejects mismatched host", () => {
    expect(() =>
      assertAllowedReturnTo("https://evil.example/", [
        "https://pr-123.fullsend-admin.pages.dev",
      ]),
    ).toThrow(/return_to/);
  });
});
```

- [x] **Step 6: Run tests (expect failure)**

Run:

```bash
npx vitest run --config vite.config.ts web/admin/src/lib/auth/previewHandoff.test.ts
```

Expected: (historical TDD order) FAIL until implementation — current branch PASS.

- [x] **Step 7: Implement `admin/src/lib/auth/previewHandoff.ts`**

```typescript
/**
 * Validates return_to against an explicit allowlist of preview origins
 * (scheme + host, no path). Caller supplies allowlist from production config.
 */
export function assertAllowedReturnTo(
  returnTo: string,
  allowedOrigins: string[],
): URL {
  let url: URL;
  try {
    url = new URL(returnTo);
  } catch {
    throw new Error("return_to is not a valid URL");
  }
  if (url.protocol !== "https:") {
    throw new Error("return_to must use https");
  }
  const origin = `${url.protocol}//${url.host}`;
  if (!allowedOrigins.includes(origin)) {
    throw new Error("return_to origin is not allowlisted");
  }
  return url;
}
```

- [x] **Step 8: Run previewHandoff tests**

Run:

```bash
npx vitest run --config vite.config.ts web/admin/src/lib/auth/previewHandoff.test.ts
```

Expected: PASS.

- [x] **Step 9: Commit**

```bash
git add web/admin/src/lib/auth
git commit -m "feat(admin): token storage and preview return_to allowlist"
```

---

### Task 4: Wire `site-build` to bundle `web/admin/dist` into the site artifact

**Status (2026-04-20):** **Complete** — [`site-build.yml`](../../../.github/workflows/site-build.yml) runs root `npm ci` / `npm run build`, copies `web/admin/dist` → **`_bundle/public/admin/`** (not the plan’s older `_site/` + nested `admin/package-lock` pattern).

**Files:**

- Modify: `.github/workflows/site-build.yml`

- [x] **Step 1: Extend workflow with Node setup and admin build**

Historical sketch (paths differ on disk—see **Status** above): **Build admin SPA** then assemble artifact. Pin Node 22 across CI:

```yaml
      - uses: actions/setup-node@v4
        with:
          node-version: "22"
          cache: npm
          cache-dependency-path: admin/package-lock.json

      - name: Build admin SPA
        run: |
          cd admin
          npm ci
          npm run build

      - name: Prepare site
        run: |
          mkdir -p _site/admin
          cp docs/mindmap.html _site/index.html
          cp -a admin/dist/. _site/admin/
```

- [x] **Step 2: Generate lockfile locally and commit**

Run on your machine:

```bash
npm install
git add package-lock.json
```

- [x] **Step 3: Commit workflow + lockfile**

```bash
git add .github/workflows/site-build.yml package-lock.json
git commit -m "ci: build admin SPA into site artifact"
```

- [x] **Step 4: Push to origin and confirm Build Site + Deploy Site**

Push your branch to **origin** and open a PR **into `origin/main`** (triggers the same `workflow_run` deploy as today). Expected: preview URL loads `…/admin/` and shows the admin shell (and mindmap still at `/`).

**Prerequisite for production-shaped OAuth on the same hostname as `/admin/`:** **Task 4b** (below).

---

### Task 4b: Ship Task 2b OAuth Worker with site Worker + static assets

**Status (2026-04-20):** **Complete** in code for **Option A** (Worker + `ASSETS` + OAuth routes). **Step 6** (GitHub App callback URL list for every preview hostname) remains an **ongoing maintainer / org checklist** as previews multiply.

**Goal:** One Cloudflare **Worker + static assets** deployment serves the static tree (mindmap + `/admin/*`) **and** the **same-origin** OAuth token exchange route the SPA calls in preview/production—so the browser never cross-origin `fetch`s `github.com/login/oauth/access_token`, and `client_secret` stays in Wrangler secrets / CI-injected vars only.

**Context:** The repo ships **one** [`cloudflare_site/wrangler.toml`](../../../cloudflare_site/wrangler.toml) Worker with **`[assets]`** and programmatic routes for admin OAuth. [`site-deploy.yml`](../../../.github/workflows/site-deploy.yml) deploys from `cloudflare_site/` using artifacts from **Build Site** (see ADR 0019). Task **2b** / **4b** descriptions below refer to this layout (`cloudflare_site/worker/`, not a separate `admin/worker/` or legacy `site/` tree).

**Architecture options** (pick one during implementation; document the choice in the PR):

| Option | Summary | Trade-offs |
|--------|---------|------------|
| **A (preferred)** | Add `main` Worker + **`ASSETS` binding** (or equivalent in current Wrangler): `fetch` handles `POST /api/oauth/*` (and health if needed), then `return env.ASSETS.fetch(request)` for everything else. | Matches Cloudflare “Worker + static assets” pattern; one deploy artifact. |
| **B** | Keep assets-only Worker; deploy OAuth as a **second** Worker + route on same zone / custom domain. | Two Workers to version and secure CORS between origins. |

**Files (Option A sketch):**

- [`cloudflare_site/wrangler.toml`](../../../cloudflare_site/wrangler.toml) — `main = "worker/src/index.ts"`, **`[assets]`** → `public/`; vars/secrets for `GITHUB_APP_*`; optional **`[[ratelimits]]`** for OAuth paths (Wrangler ≥ 4.36)
- [`cloudflare_site/worker/src/index.ts`](../../../cloudflare_site/worker/src/index.ts) — router: OAuth routes + delegate to `env.ASSETS` for static SPA
- Modify: [`.github/workflows/site-build.yml`](../../../.github/workflows/site-build.yml) — ensure `site/public` layout before deploy still includes `admin/dist` output (unchanged from Task 4 unless worker build needs admin artifacts earlier)
- Modify: [`.github/workflows/site-deploy.yml`](../../../.github/workflows/site-deploy.yml) — pass secrets to Wrangler for production + preview (`secrets` / `vars` inputs supported by `cloudflare/wrangler-action`); **never** echo secret values in logs
- Modify: [`sample.env.local`](../../../sample.env.local) (and **Task 16** `docs/admin-spa-local-dev.md` when written) — production + preview Worker URLs, GitHub App callback URL list (`*.workers.dev` preview aliases, production hostname), which GitHub secrets / Cloudflare vars map to which Wrangler names

**Steps:**

- [x] **Step 1: Research / spike** — Confirm current Wrangler **4.x** syntax for Worker + assets on this repo’s deploy path (`deploy`, `versions upload --assets`). Read Cloudflare docs for **`ASSETS`** (or successor) with static asset routing.

- [x] **Step 2: Implement `site` Worker shell** — `fetch` forwards non-OAuth traffic to assets; OAuth path returns JSON errors with safe status codes (no secret leakage).

- [x] **Step 3: Wire exchange handler** — PKCE `code_verifier`, `client_secret` from `env` only. Validate **`Origin`** (only) and `redirect_uri` allowlist for production + preview hostnames; add Turnstile + Wrangler rate limits per High 1 remediation.

- [x] **Step 4: Local smoke** — `wrangler dev` from `cloudflare_site/` with built `public/` tree: static `/admin/` loads, `POST /api/oauth/...` returns expected GitHub error shape without real code (then with real code in trusted env).

- [x] **Step 5: CI secrets + deploy** — Add GitHub Actions secrets (names TBD in PR, e.g. `CLOUDFLARE_*` already exist; add app OAuth secrets). Update `wrangler-action` `command` or env so preview **versions upload** and production **deploy** bind secrets. Verify PR preview URL: admin shell + OAuth exchange same origin.

- [ ] **Step 6: GitHub App settings** — Maintainer: register **Callback URL(s)** for production admin origin **and** preview Worker URL pattern (per-alias or wildcard policy per org security rules). *Revisit with **Task 15** once Turnstile vs preview policy is decided.*

- [x] **Step 7: Commit**

```bash
git add cloudflare_site .github/workflows
git commit -m "feat(site): Worker + assets with OAuth exchange for admin SPA"
```

**Non-goals for Task 4b:** changing DNS outside Cloudflare Workers defaults; full **Task 15** preview handoff (production fragment redirect)—tracked separately after Turnstile/preview policy (**Task 15**).

---

### Task 5: Status model types (mirror Go `LayerReport`)

**Status (2026-04-20):** **Complete** under `web/admin/src/lib/status/`.

**Files:**

- Create: `web/admin/src/lib/status/types.ts`
- Create: `web/admin/src/lib/status/types.test.ts`

- [x] **Step 1: Write failing test `admin/src/lib/status/types.test.ts`**

```typescript
import { describe, it, expect } from "vitest";
import { layerStatusLabel, type LayerStatus } from "./types";

describe("layerStatusLabel", () => {
  it("maps not_installed", () => {
    const s: LayerStatus = "not_installed";
    expect(layerStatusLabel(s)).toBe("not installed");
  });
});
```

- [x] **Step 2: Run test (expect failure)**

Run:

```bash
npx vitest run --config vite.config.ts web/admin/src/lib/status/types.test.ts
```

Expected: (historical TDD) FAIL until types exist — current branch PASS.

- [x] **Step 3: Implement `admin/src/lib/status/types.ts`**

Align string labels with `internal/layers/layers.go` `LayerStatus.String()`:

```typescript
export type LayerStatus =
  | "not_installed"
  | "installed"
  | "degraded"
  | "unknown";

export type LayerReport = {
  name: string;
  status: LayerStatus;
  details: string[];
  wouldInstall: string[];
  wouldFix: string[];
};

export function layerStatusLabel(s: LayerStatus): string {
  switch (s) {
    case "not_installed":
      return "not installed";
    case "installed":
      return "installed";
    case "degraded":
      return "degraded";
    case "unknown":
      return "unknown";
    default: {
      const _x: never = s;
      return _x;
    }
  }
}
```

- [x] **Step 4: Run tests**

Run:

```bash
npx vitest run --config vite.config.ts web/admin/src/lib/status/types.test.ts
```

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add web/admin/src/lib/status
git commit -m "feat(admin): add LayerReport TypeScript model"
```

---

### Task 6: Minimal Octokit client + 401 handling hook

**Status (2026-04-20):** **Complete** — `@octokit/rest` on root `package.json`; client under `web/admin/src/lib/github/`.

**Files:**

- Create: root `package.json` dependency: `@octokit/rest`
- Create: `web/admin/src/lib/github/client.ts`
- Create: `web/admin/src/lib/github/client.test.ts`

- [x] **Step 1: Add dependency**

Run:

```bash
npm install @octokit/rest@^21.0.0
```

- [x] **Step 2: Write failing test `admin/src/lib/github/client.test.ts`**

```typescript
import { describe, it, expect, vi } from "vitest";
import { createUserOctokit } from "./client";

describe("createUserOctokit", () => {
  it("sets auth header from token", () => {
    const o = createUserOctokit("tok");
    expect(o).toBeDefined();
  });
});
```

- [x] **Step 3: Run test (expect failure)**

Run:

```bash
npx vitest run --config vite.config.ts web/admin/src/lib/github/client.test.ts
```

Expected: (historical TDD) FAIL until export — current branch PASS.

- [x] **Step 4: Implement `admin/src/lib/github/client.ts`**

```typescript
import { Octokit } from "@octokit/rest";

export function createUserOctokit(accessToken: string): Octokit {
  return new Octokit({
    auth: accessToken,
    request: {
      hook: async (request, options) => {
        const response = await request(options);
        if (response.status === 401) {
          window.dispatchEvent(new CustomEvent("fullsend:github-unauthorized"));
        }
        return response;
      },
    },
  });
}
```

- [x] **Step 5: Run tests**

Run:

```bash
npx vitest run --config vite.config.ts web/admin/src/lib/github/client.test.ts
```

Expected: PASS (smoke only; hook integration with real 401 is manual).

- [x] **Step 6: Commit**

```bash
git add package.json package-lock.json web/admin/src/lib/github
git commit -m "feat(admin): Octokit factory with 401 event"
```

**UX spec (for consumers of `fullsend:github-unauthorized`):** [Global banners](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-banners-screen-level-below-the-nav-bar) — persistent banner **below** the future account bar with **Re-authenticate** (and intended-route preservation per architecture spec). Row-level API calls should not silently fail without tying into the same session story.

**Status (2026-04-23, UX audit):** `App.svelte` dispatches **no** banner today — it calls `signOut()` on `fullsend:github-unauthorized`. Treat **Re-authenticate** banner + preserve intended route as **follow-up** aligned with Task 7 shell work (or a small dedicated polish pass) rather than a blocker on marking Task 6 “engine” complete.

---

### Task 7: Production sign-in (authorize URL + SPA document callback)

**Status (2026-04-20):** **Complete** for **OAuth mechanics** in app code (`web/admin/src/lib/auth/oauth.ts`, `session.ts`, `App.svelte`): PKCE, Worker-expanded `state`, document `?code=` handoff, `history.replaceState` to `#/`, same-origin token exchange, `saveToken` + `refreshSession`.

**Status (2026-04-23, vs [UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md)):** **Partial** on **presentation and journeys** — implement in a follow-up (same task number or next org-list PR) before calling the login/bootstrap story “UX-complete”:

| UX area | Spec reference | Current gap |
| --- | --- | --- |
| Login surface | [Screen: Login](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-login) | **Sign in with GitHub** lives in the header, not a **centered** primary gate; control has **no GitHub mark** |
| Post–OAuth bootstrap | [Post–OAuth return loading](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#postoauth-return-loading) | Spinner is **in the header** (“Loading session…”), not a **large centered** full-view spinner until bootstrap finishes |
| Account bar | [Account bar](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-account--navigation-bar) | `GitHubUser` / `/api/github/user` path exposes **login + name** only — **no avatar URL** yet; layout is not the stacked **bold login / normal display name** cluster |
| OAuth / profile failures | [Global banners](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-banners-screen-level-below-the-nav-bar) | OAuth errors render as a **plain paragraph** under the header, not banner + **Re-authenticate** / **Retry** per table |
| Deep link after login | [User journeys](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#user-journeys-routes) | **No** `sessionStorage` (or equivalent) stash of pre-login **hash / intent** before `startGithubSignIn`; post-success navigation always lands on `#/` from the callback path |

Spec **Appendix A** rows may still be filled incrementally with later tasks.

**Files:**

- Create / maintain: `web/admin/src/lib/auth/oauth.ts` (PKCE-aware; builds on `pkce.ts` from **Task 2b**)
- Modify: `web/admin/src/App.svelte` (`onMount`: document `?code=&state=` handoff, `history.replaceState` to `#/`, token exchange)
- Modify: `docs/superpowers/specs/2026-04-06-fullsend-admin-spa-design.md` (Appendix A: REST rows for `/user` and Worker proxy path for exchange)

**Prerequisite:** **Task 2b** (OAuth Worker + Vite proxy + PKCE) must exist so token exchange is **same-origin** to the admin origin in dev; production deploy of that Worker is tracked in **Task 2b** follow-up / **Task 4** extension.

- [x] **Step 1: `admin/src/lib/auth/oauth.ts`** — PKCE authorize (`startGithubSignIn`), `getOAuthRedirectUri()` from `new URL(import.meta.env.BASE, window.location.origin).href`, document handoff (`consumeOAuthParamsFromDocumentUrl`, one-shot `sessionStorage`), `completeGithubOAuthFromHandoff` (`POST /api/oauth/token`, `saveToken`, `refreshSession`).

- [x] **Step 2: SPA document callback (no static `callback.html`, no `#/oauth/finish`)**

Register the GitHub App callback as the **SPA entry** matching Vite `base: '/admin/'`, e.g. `http://localhost:5173/admin/` (trailing slash should match `redirect_uri` in code). **`redirect_uri`** in authorize + token exchange is `new URL(import.meta.env.BASE, window.location.origin).href`.

On load, if `URLSearchParams(location.search).has("code")`, stash `{ code, state }` in `sessionStorage` for one-shot use, then **`history.replaceState`** to the same origin with **no** document query and hash **`#/`** so the authorization `code` never lingers in the address bar. Still in `App.svelte` `onMount`, consume the stash, verify `state` against the value stored at authorize time, read PKCE **`code_verifier`**, **`POST /api/oauth/token`** (Task 2b Worker via Vite proxy in dev), **`saveToken`**, clear OAuth session keys, **`refreshSession`**. Surface failures with the **global banner** pattern from the [UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-banners-screen-level-below-the-nav-bar) (**Retry** / **Re-authenticate** as appropriate); avoid leaving a blank shell. **Never** send `client_secret` from the SPA.

**UX alignment (login + return journey):** See [Screen: Login](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-login) and [Post–OAuth return loading](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#postoauth-return-loading): unauthenticated `/admin` shows **Sign in with GitHub** (GitHub logo on the control); after redirect back, show a **large centered spinner** until token **and** `/user` fields needed for the [account bar](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-account--navigation-bar) exist; then **restore the deep link** the user originally opened ([User journeys](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#user-journeys-routes)).

The dev Worker allowlists loopback **`redirect_uri`** pathnames **`/admin/`**, **`/admin`**, and legacy **`/admin/oauth/callback.html`** for migrations.

- [x] **Step 3: Commit**

```bash
git add web/admin
git commit -m "feat(admin): OAuth callback via SPA entry /admin/"
```

- [ ] **Step 4 (UX follow-up vs [2026-04-21 UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md)):** Implement the gaps in the **2026-04-23** table above: **centered** login when unauthenticated (primary **Sign in with GitHub** + GitHub mark); **large centered** indeterminate spinner after OAuth return until token **and** profile fields needed for the account bar exist; extend **`/api/github/user`** + `GitHubUser` for **avatar** (and wire [account bar](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-account--navigation-bar) layout); replace ad-hoc OAuth error copy with [global banner](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-banners-screen-level-below-the-nav-bar) actions (**Re-authenticate** / **Retry**); stash intended **hash** (or path) in `sessionStorage` before `startGithubSignIn` and **restore** after successful bootstrap ([user journeys](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#user-journeys-routes)). Optionally align **Task 6** 401 handling with the same **Re-authenticate** banner instead of immediate `signOut()` only.

---

### Task 9: Org list (alphabetical, search) + in-memory session cache

**Status (2026-04-20):** **Complete** for **engine + minimal UI** — filter + Vitest under `web/admin/src/lib/orgs/`; org list UI + `#/orgs` route; org names derived via **Octokit `paginate.iterator`** on **`GET /user/repos`** (**25** repos per page; unique `Organization` owners) with **`fetchOrgsWithProgress`** for incremental UI. In-memory cache cleared on sign-out.

**Status (2026-04-23, vs [UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md)):** **Partial** only where **deployment truth** is missing: [Organisation selection — list](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#organisation-selection--list) trailing clusters are **stubbed/disabled** until **Task 10+**; whole-list errors still use **inline** copy rather than **global banner** chrome.

| UX area | Spec reference | Current gap |
| --- | --- | --- |
| Screen title + lede | [Organisation selection — header](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#organisation-selection--header) | **Matches** (spec title + lede). |
| Search field | Same + [Search cap](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#search-and-showing-15-cap-organisation-list) | **Matches** (**`Type to filter`**, substring **search-as-you-type** via `filterOrgsBySearch`). |
| 15-row cap + helper + progressive list | [Organisation selection — list](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#organisation-selection--list) + [Search cap](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#search-and-showing-15-cap-organisation-list) | **Matches** (**≤15** visible rows, red **`Showing up to 15 organisations`**, smaller repo pages + **progressive paint**, **10 → +5** batching, **in-list** spinner region). |
| Row layout | [Organisation selection — list](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#organisation-selection--list) | **Org avatar** + disabled **Configure** / **Deploy Fullsend**; **spinner** / **Cannot deploy** / **per-row error** patterns await **Task 10+** data. |
| Empty copy | [Empty states](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#organisation-selection--empty-states) | **Matches** spec examples. |
| List errors | [Global banners](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-banners-screen-level-below-the-nav-bar) vs row errors | Whole-list fetch failure is a **single inline paragraph** — acceptable as a **global** placeholder until banner chrome exists; **no** per-row evaluation yet (expected until engine lands) |

**UX spec (org selection screen):** [Screen: Organisation selection](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-organisation-selection) + [Global UX patterns](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-ux-patterns). When iterating on this route, prefer:

- **Chrome:** title **`Select an organisation to deploy or configure Fullsend`**, search placeholder **`Type to filter`**, **Refresh** as specified.
- **Cap + loading:** filter the **full** org set, then show **≤15** rows; if more than 15 matches, **red** helper **`Showing up to 15 organisations`** ([Search and “showing 15” cap](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#search-and-showing-15-cap-organisation-list)); follow [Organisation selection — list](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#organisation-selection--list) for **progressive display** and **in-list** spinner while discovery continues.
- **Row trailing cluster:** spinner while resolving → **Configure** (grey) / **Deploy Fullsend** (blue) / **Cannot deploy** (yellow + **`i`** popover, no inline paragraph) / **per-row error** (red triangle + **`Error`** + **`i`** + **`Retry`**) — [Per-row error](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#per-row-error-pattern-org-list-and-repo-list) vs [Cannot deploy](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#per-row-cannot-deploy-pattern-org-list-only).
- **Navigation:** [List interaction model](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#list-interaction-model) — actions on **buttons**, not whole-row click.

**Files:**

- Create: `web/admin/src/lib/orgs/fetchOrgs.ts` — Octokit `paginate` `GET /user/repos` → unique org owners in-browser
- Create: `web/admin/src/routes/OrgList.svelte`
- Modify: `web/admin/src/App.svelte` routes

- [x] **Step 1: Write Vitest for pure filter `filterOrgsByPrefix` in `admin/src/lib/orgs/filter.test.ts`**

```typescript
import { describe, it, expect } from "vitest";
import { filterOrgsByPrefix } from "./filter";

describe("filterOrgsByPrefix", () => {
  it("is case-insensitive prefix", () => {
    expect(
      filterOrgsByPrefix(
        [{ login: "Alpha" }, { login: "beta" }],
        "a",
      ).map((o) => o.login),
    ).toEqual(["Alpha"]);
  });
});
```

- [x] **Step 2: Implement `admin/src/lib/orgs/filter.ts`**

```typescript
export type OrgRow = { login: string };

export function filterOrgsByPrefix(orgs: OrgRow[], q: string): OrgRow[] {
  const p = q.trim().toLowerCase();
  if (!p) return [...orgs].sort((a, b) => a.login.localeCompare(b.login));
  return orgs
    .filter((o) => o.login.toLowerCase().startsWith(p))
    .sort((a, b) => a.login.localeCompare(b.login));
}
```

- [x] **Step 3: Run tests**

Run:

```bash
cd admin && npm run test -- src/lib/orgs/filter.test.ts
```

Expected: PASS.

- [x] **Step 4: Implement `fetchOrgs` and `OrgList.svelte`** (show loading, error, refresh button; wire token from `loadToken()`).

- [x] **Step 5: Append Appendix A** — implemented as **`GET https://api.github.com/user/repos`** (Octokit `paginate`, `affiliation=owner,collaborator,organization_member`) with bearer-token notes in [Appendix A — Permission matrix](../specs/2026-04-06-fullsend-admin-spa-design.md#appendix-a--permission-matrix) (not `GET /user/memberships/orgs`; repo-first org inference).

- [x] **Step 6: Commit**

```bash
git add admin/src/lib/orgs admin/src/routes/OrgList.svelte admin/src/App.svelte
git commit -m "feat(admin): org list with search-as-you-type"
```

- [ ] **Step 7 (UX follow-up vs [2026-04-21 UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md)):** Remaining gaps: wire **global banner** chrome for whole-list failures (vs inline copy); when **Task 10+** supplies deployment truth, replace **disabled** trailing buttons with live **Configure** / **Deploy Fullsend** / **Cannot deploy** / **per-row error** / **spinner** patterns per [Organisation selection — list](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#organisation-selection--list). (**Title**, **`Type to filter`**, **≤15** cap + **`Showing up to 15 organisations`**, **avatars**, **empty-state** copy, **progressive** org loading + **in-list** spinner, and **stubbed** trailing buttons are **done**.) On the org dashboard, implement the repo list **≤15** cap + **`Showing up to 15 repositories`** per [repository list cap](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#search-and-showing-15-cap-repository-list). Follow [list interaction model](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#list-interaction-model) (**buttons**, not row-as-click-target).

---

### Task 10: Read-only layer status engine (first layer: `ConfigRepoLayer` semantics)

**Files:**

- Create: `admin/src/lib/layers/configRepo.ts` — TS port of **read-only** checks from `internal/layers/configrepo.go` `Analyze` (only what is inferable via public GitHub APIs)
- Create: `admin/src/lib/layers/configRepo.test.ts` — mock GitHub responses

**UX spec (org dashboard — Fullsend status pane):** When surfacing rollup state in the UI, map engine outcomes to the labels in [Pane A — Fullsend status](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#pane-a--fullsend-status): **Deployed** (green), **Partially deployed / broken** (yellow + **Repair**), **Outdated** (orange + **Upgrade**), **Checking** (spinner). Exact mapping from `LayerReport` / merged layers is implementation-defined but **user-visible strings** should match the UX spec (and stay aligned with CLI wording per architecture spec).

**Instruction:** Open `internal/layers/configrepo.go` and for each API used in `Analyze`, add a TypeScript function and a Vitest table-driven test with **fixture JSON** checked into `admin/src/lib/layers/fixtures/configrepo/*.json`.

- [ ] **Step 1: Write failing test for expected `LayerReport` shape** given fixture “no repo”.

- [ ] **Step 2: Implement minimal analyze.

- [ ] **Step 3: Commit**

```bash
git add admin/src/lib/layers
git commit -m "feat(admin): read-only config repo layer analyze in TS"
```

---

### Task 11: Repeat layer ports (one commit per layer)

For each file `internal/layers/workflows.go`, `secrets.go`, `enrollment.go`, `dispatch.go`, and `internal/layers/preflight.go` (if applicable to browser token):

**Files:**

- Create: `admin/src/lib/layers/<name>.ts` + `admin/src/lib/layers/<name>.test.ts` + fixtures

- [ ] **Step 1: Port `Analyze` REST/GraphQL dependencies only** (no mutating `Install` yet unless same PR scope).

- [ ] **Step 2: Extend `admin/src/lib/status/engine.ts` to merge layer reports into org-level rollup** (`not installed` / `degraded` / `installed` wording matches CLI). **UX:** merged org-level state feeds the [Fullsend status pane](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#pane-a--fullsend-status) (same label set as Task 10).

- [ ] **Step 3: Update Appendix A** with each new endpoint.

- [ ] **Step 4: Commit per layer**

```bash
git commit -m "feat(admin): TS analyze for workflows layer"
```

(Repeat message with appropriate layer name.)

---

### Task 12: Org detail + repo union list (read-only)

**UX spec (this task is the main surface for):** [Screen: Organisation dashboard](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-organisation-dashboard) — **Pane A** (Fullsend status) + **Pane B** (repository list). There is **no** separate repo onboarding/settings wizard: all repo outcomes are **rows** on that list ([Scope clarification](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#scope-clarification-repositories)).

**Files:**

- Create: `admin/src/routes/OrgDetail.svelte`
- Create: `admin/src/lib/repos/unionConfig.ts` — union org repos + `config.yaml` repo names; classify orphan / missing

- [ ] **Step 1: Vitest for pure union/classification** with fixture YAML strings in test file. Fixtures should cover **R6** / **R7** style cases from [Repo row states](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#repo-row-states-complete-set) (not-in-config vs orphan).

- [ ] **Step 2: Implement UI routes `#/org/:login`** (org dashboard). **Nav bar:** [Account bar](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-account--navigation-bar) shows user + org avatar/name for this screen. **Pane A:** status states and **Repair** / **Upgrade** as in UX spec (wire to routes or disabled stubs until Task 14). **Pane B:** search-as-you-type + **Refresh**; each row implements the **R0–R7** trailing clusters where read-only data allows (e.g. **Onboard** / **Onboarding — check PR #nnn** / **Off-boarding — check PR #nnn** / **Onboarded** + red **Remove** / partial + **Repair** + **Remove** / **Not in Fullsend config** + **Onboard** / orphan + **Repository missing** + **i** + **Remove from config**). Use [GitHub terminology](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#github-terminology) for GitHub-sourced labels. **Errors:** [Per-row error pattern](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#per-row-error-pattern-org-list-and-repo-list) (red triangle + **`Error`** + popover + **`Retry`**); pane-wide failures → [global banner](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-banners-screen-level-below-the-nav-bar). **Interaction:** [List interaction model](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#list-interaction-model) — primary actions on **buttons**; PR `#nnn` is a **link**. Read-only task: **Remove** / **Repair** / **Onboard** may be disabled or navigate to placeholders until Task 14 implements mutations.

- [ ] **Step 3: Commit**

```bash
git add admin/src/lib/repos admin/src/routes/OrgDetail.svelte
git commit -m "feat(admin): org detail with repo/config union"
```

---

### Task 13: Wizard shell (linear steps, review screen, no mutations yet)

**UX spec boundary:** Wizard **interiors** (steps, fields) follow the **architecture** spec [Section 4](../specs/2026-04-06-fullsend-admin-spa-design.md#section-4--wizards-onboard-repair-uninstall-agent-apps-secrets). **Entry chrome** from lists (**Deploy Fullsend**, **Configure**, **Repair**, **Upgrade**, **Onboard**) must stay consistent with [Organisation selection](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-organisation-selection) and [Organisation dashboard](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#screen-organisation-dashboard) in the UX spec (labels, colors, button vs row-click rules).

**Files:**

- Create: `admin/src/lib/wizard/machine.ts` — step index, `back`/`next`, `review` payload
- Create: `admin/src/routes/OnboardWizard.svelte`

- [ ] **Step 1: Vitest for wizard transitions**.

- [ ] **Step 2: Implement empty steps with titles matching CLI order** from spec Section 4 (`config` → apps → secrets → workflows → enrollment).

- [ ] **Step 3: Commit**

```bash
git add admin/src/lib/wizard admin/src/routes/OnboardWizard.svelte
git commit -m "feat(admin): onboarding wizard shell"
```

---

### Task 14: Mutating operations (install / repair / uninstall) — per wizard step PRs

Each wizard step gets **idempotent** GitHub API calls mirrored from `internal/cli/admin.go` call chains; **before** each mutation batch, show **review** screen listing planned file/secret changes.

**UX spec:** Use [global banners](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-banners-screen-level-below-the-nav-bar) for session, rate-limit, and Worker-wide failures during wizards. After mutations affect repo/org row state, refresh row data so **Pane B** states (**R2**/**R3** PR links, **R4**/**R5** + **Remove**, etc.) stay aligned with [Repo row states](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#repo-row-states-complete-set). Destructive actions (**Remove**, config cleanup) warrant confirmations at least as strong as the architecture spec’s wizard **review** pattern (UX spec does not redefine wizard confirmations).

**Files:** grow under `admin/src/lib/actions/*`; each action module pairs with Vitest **HTTP mock** (e.g. `fetch` mock) where feasible.

- [ ] **Step 1: Implement **config repo** create/push path in TS** (mirror Go), with tests.

- [ ] **Step 2: Commit**

```bash
git commit -m "feat(admin): apply config repo layer from wizard"
```

- [ ] **Step 3–N:** Repeat for workflows, secrets, enrollment, appsetup flows; follow `internal/appsetup/*` for GitHub App creation flows that **open github.com** (SPA documents interrupt/resume via `localStorage` per spec).

---

### Task 15: Preview OAuth handoff (production `sessionStorage` + fragment on preview)

**Status:** **Open — redesign.** This content was **old Task 8**; it is sequenced **after Task 14** so it is not blocked by feature work, but the design must be revisited: the site Worker **requires Turnstile verification on token exchange**, which may be **poorly suited** to **per-PR preview** review flows (anonymous traffic, embed friction, or policy). Decide an explicit workaround before shipping preview-only sign-in (examples to evaluate, not commitments): preview-only **relaxed** verification behind tighter **rate limits**; **production-only** OAuth with fragment / `return_to` handoff to preview unchanged; **separate GitHub OAuth app** or env tier for previews; or Turnstile **managed** / hostname-key strategy that works for ephemeral preview hosts. Record the threat-model trade-off in the spec and Worker.

**UX spec:** Whatever preview sign-in ships, the **post-handoff** experience should still match [Post–OAuth return loading](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#postoauth-return-loading) and [global error presentation](../specs/2026-04-21-fullsend-admin-spa-ux-design.md#global-banners-screen-level-below-the-nav-bar) (e.g. `missing_turnstile_keys` → banner copy that explains misconfiguration).

**Files:**

- Create: `web/admin/src/lib/auth/previewStart.ts` (build production URL with signed `state` placeholder—use random `state` + server verification later; **MVP:** `state` = base64url JSON `{ "nonce": "…", "return_to": "<preview>" }` + **HMAC** optional future)
- Modify: `web/admin/src/routes/Home.svelte` — button “Sign in on preview” when `import.meta.env` or runtime detection says hostname is preview
- Modify: `docs/superpowers/specs/2026-04-06-fullsend-admin-spa-design.md` — document `/oauth/preview-start` and `/oauth/preview-callback` on **production** static pages mirroring **Task 7** callback pattern

**MVP crypto:** use `crypto.randomUUID()` for `nonce`; store in `sessionStorage` on production at preview-start page; on preview-callback compare nonce inside `state` JSON **after** GitHub returns to production. **Full HMAC** binding of `return_to` is a follow-up commit once production has a secret (Worker or env-injected at build—**avoid** embedding secrets in static JS).

- [ ] **Step 1: Add `web/admin/public/oauth/preview-start.html` and `preview-callback.html`** following the spec’s flow (production stores PKCE/state, GitHub redirects to production, production redirects to `return_to` with `#access_token=...&token_type=...&expires_in=...` **only if** GitHub fragment flow applies—**verify** against GitHub docs; if GitHub does not put tokens in hash, use **your** production page to append **fragment** after server exchange).

- [ ] **Step 2: Vitest for `assertAllowedReturnTo` integration** from preview-start (construct `return_to`, assert throws on evil).

- [ ] **Step 3: Commit**

```bash
git add web/admin/public/oauth web/admin/src
git commit -m "feat(admin): preview OAuth handoff via production origin"
```

---

### Task 16: Local dev + CSP notes

**Files:**

- Create: `docs/admin-spa-local-dev.md`

Include: creating a **dev** GitHub App, callback URLs for Vite dev, **`npm run dev`** (Vite + OAuth Worker per **Task 2b**), pointers to repo-root **`sample.env.local`** (and **`web/admin/README.md`**), and **separate** preview app checklist (coordinate with **Task 15** once Turnstile/preview policy is settled). Cross-link the [UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md) so manual testers can verify **login**, **account bar**, **org list**, and **org dashboard** behaviors against the screen/state tables without rereading the architecture spec.

- [ ] **Step 1: Add doc**

- [ ] **Step 2: Commit**

```bash
git add docs/admin-spa-local-dev.md
git commit -m "docs: admin SPA local development checklist"
```

---

## Plan self-review

**1. Spec coverage**

| Spec area | Task(s) |
|-----------|---------|
| Static SPA, GitHub API from browser | 2, 2b, 6–9 |
| GitHub App sign-in + token storage | 1, 2b (exchange + PKCE), 3, 7 |
| Per-PR previews + preview OAuth | 4, **4b** (Worker + assets + OAuth on preview host), **15** (production-origin preview handoff — **open**, Turnstile vs preview) |
| Org list + search | 9 |
| Org/repo union + orphan | 12 |
| `LayerReport` / analyze semantics | 5, 10–11 |
| Wizards + review | 13–14 |
| Self-hosted / local dev | 2b, **16** (`sample.env.local` + `docs/admin-spa-local-dev.md`) |
| Permission matrix | 1, 2b, 7, 9–11 (incremental) |
| No automated CLI↔SPA parity CI | Omitted intentionally |
| **UX** (screens, states, nav, errors) | [2026-04-21 UX spec](../specs/2026-04-21-fullsend-admin-spa-ux-design.md); tasks **6–7**, **9**, **10–16** cite it where relevant |

**2. Placeholder scan**

No TBD/TODO strings. **Complete (2026-04-20 plan refresh):** Tasks **1**, **2**, **2b**, **3**, **4**, **4b** (Step 6 callback URL checklist ongoing), **5**, **6**, **7**. **Open:** **10–14**, **15** (preview OAuth redesign), **16** (local dev doc), **4b** Step 6.

**3. Type consistency**

`LayerStatus` strings use underscores in TS (`not_installed`) matching JSON-friendly IDs; UI labels use `layerStatusLabel` matching Go `String()`.

**Gaps / follow-ups**

- **Path-based routing** under `/admin/*` without hash: add a dedicated task after verifying Cloudflare static asset fallback for nested `index.html`.
- **PKCE (baseline):** **Task 2b** adds PKCE for the production-shaped authorize + exchange path. **Task 15** preview handoff still uses structured `state` + nonce in the original sketch; upgrade to HMAC or signed JWT when preview `return_to` binding needs hardening beyond allowlist—and reconcile with **Turnstile** requirements on **`POST /api/oauth/token`** before shipping.

---

**Plan complete and saved to `docs/superpowers/plans/2026-04-12-fullsend-admin-spa.md`.**

**Execution choice (2026-04-12):** **Subagent-Driven** — dispatch a fresh subagent per task using `subagent-driven-development`, with review between tasks.

**Alternative:** Inline execution via `executing-plans` if you later prefer a single long session.
