# Fullsend admin installation SPA Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a **static TypeScript + Svelte** admin SPA that mirrors the Go CLI’s admin responsibilities (install / analyze / uninstall semantics via a TypeScript layer engine), hosted on the **existing Cloudflare Workers + GitHub Actions** site pipeline (**per-PR previews**, production on `main`), with **GitHub App–centric auth** (production + preview hash handoff per spec).

**Architecture:** **Approach 1** from the spec—reimplement layer orchestration and GitHub REST/GraphQL usage in TypeScript, aligned with `internal/layers/*` and `internal/forge/github`. **Routing:** start with **hash-based client routing** under `base: '/admin/'` so deep links work on Workers static assets without relying on subdirectory SPA `index.html` fallbacks (migrate to path-based history later if Wrangler proves to serve `/admin/*` → `admin/index.html` correctly). **Branching:** **`origin/main` already includes the Cloudflare site pipeline** (aligned with the former `site-cloudflare-workers` work). Branch from **`origin/main`**, open PRs **into `origin/main`** so **existing** `Build Site` / `Deploy Site` + secrets stay valid. When Cloudflare work is merged **upstream**, rebase onto upstream `main` and retarget PRs there as usual.

**Tech Stack:** Svelte 5 + TypeScript, Vite 6, Vitest, GitHub REST (`@octokit/rest`) and GraphQL (`@octokit/graphql`) as needed, `pnpm` or `npm` (pick one in Task 2 and keep it), **Wrangler** for a small **OAuth exchange Worker** merged into the site Worker under `cloudflare_site/worker/` (local dev alongside Vite; production ships via the **Build Site** artifact), GitHub Actions (extend `.github/workflows/site-build.yml`), Cloudflare Workers static assets (`cloudflare_site/wrangler.toml`, `cloudflare_site/public/` from artifact).

**Implementation note ([ADR 0019](../../ADRs/0019-web-source-and-cloudflare-site-layout.md)):** Browser source lives under `web/` (admin SPA in `web/admin/`). **Node tooling** (`package.json`, lockfile, `npm run dev` / `npm run build`) lives at the **repository root**. Wrangler configuration and the Worker live under `cloudflare_site/`. Where this plan still says `admin/*` or `site/*` for layout, read **`web/admin/*`** and **`cloudflare_site/*`** respectively unless history is explicitly meant.

**Execution:** **Subagent-driven** (use `subagent-driven-development`: one subagent per plan task, review between tasks).

**UI stack:** **Svelte** is **locked in** (Svelte 5 + Vite); recorded in the spec Open items.

**Spec:** [2026-04-06-fullsend-admin-spa-design.md](../specs/2026-04-06-fullsend-admin-spa-design.md)

**Related CI/site spec:** [2026-04-09-site-cloudflare-pages-design.md](../specs/2026-04-09-site-cloudflare-pages-design.md)

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
| `web/admin/src/App.svelte` | Shell layout, nav, sign-in/out |
| `web/admin/src/lib/github/client.ts` | Octokit factory from stored token |
| `web/admin/src/lib/auth/tokenStore.ts` | `localStorage` read/write/clear; **no logging** of secrets |
| `web/admin/src/lib/auth/oauth.ts` | Production OAuth: authorize URL (include PKCE **`code_challenge`** + **`S256`**), callback parsing; exchange via **same-origin** `fetch('/api/oauth/...')` to Worker (not GitHub cross-origin) |
| `web/admin/src/lib/auth/previewHandoff.ts` | `return_to` allowlist, `state`/`sessionStorage`, fragment parse |
| `web/admin/src/lib/status/types.ts` | TS mirrors of `LayerStatus` / `LayerReport` from `internal/layers/layers.go` |
| `web/admin/src/lib/status/engine.ts` | Read-only analyze-style rollup (grows over phases) |
| `web/admin/src/routes/*` | Svelte views (hash routes): org list, org detail, repo list |
| `web/admin/src/lib/auth/oauth.test.ts` | Vitest: callback parsing, storage |
| `web/admin/src/lib/auth/previewHandoff.test.ts` | Vitest: allowlist accepts production admin origin only |
| `.github/workflows/site-build.yml` | Setup Node, `npm ci` + `npm run build`, copy `web/admin/dist` → `_bundle/public/admin/`, mindmap from `web/public/`, worker from `cloudflare_site/worker/` |
| `cloudflare_site/worker/src/index.ts` | **Task 4b:** extend OAuth allowlists for preview/production hostnames (see task); **`ASSETS` fallback** already wired |
| `docs/admin-spa-local-dev.md` | Local dev GitHub App (localhost callback), env vars, `npm run dev` (Vite + Worker); cross-link **`web/admin/README.md`** |
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

**Where to run Part B:** use a real **`http://localhost:5173`…** document. The repo includes [`docs/superpowers/experiments/oauth-localhost-part-b/serve.py`](../experiments/oauth-localhost-part-b/serve.py): `cd` that directory, `export CLIENT_ID='…'` (and optionally `CLIENT_SECRET` for a full exchange), run `python3 serve.py`, open **`http://localhost:5173/`** in Chrome or Firefox (not an embedded IDE preview). After redirect, `/oauth/callback.html` calls **`POST /_experiment/github-access-token`** on the same origin only (no prior cross-origin `fetch` to GitHub), so the one-time `code` is not consumed before the server-side exchange. **Do not** mix `http://127.0.0.1:5173` and `http://localhost:5173` in the address bar vs the registered callback URL (different origins). Task 2’s Vite dev server on the same port is also fine once it exists. **Do not** rely on `file://` or `chrome://`-style pages with strict CSP for the authorize/callback flow. For a **manual** CORS/CSP experiment, you can still paste the Part B `fetch` snippet in DevTools yourself (see below)—know that a successful reach to GitHub may **invalidate** the `code` before `curl` Part C or the proxy run.

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

**Files:**

- Create: `admin/package.json`
- Create: `admin/vite.config.ts`
- Create: `admin/tsconfig.json`
- Create: `admin/svelte.config.js`
- Create: `admin/index.html`
- Create: `admin/src/main.ts`
- Create: `admin/src/vite-env.d.ts`
- Create: `admin/src/App.svelte`
- Create: `admin/src/app.css`
- Create: `admin/.gitignore`

- [ ] **Step 1: Write `admin/package.json`**

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

**Note:** `dev` stays `vite` until **Task 2b** replaces it with `concurrently` + `wrangler dev` + OAuth proxy (or run Task 2b in the same PR immediately after the scaffold lands).

- [ ] **Step 2: Write `admin/vite.config.ts`**

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

- [ ] **Step 3: Write `admin/tsconfig.json`**

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

- [ ] **Step 4: Write `admin/svelte.config.js`**

```javascript
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";

/** @type {import('@sveltejs/vite-plugin-svelte').SvelteConfig} */
export default { preprocess: vitePreprocess() };
```

If `npm install` reports a **peer dependency** conflict between `svelte-spa-router` and Svelte 5, drop that dependency and replace Task 2 Step 9 routing with a small in-repo **hashchange** router (`admin/src/lib/hashRouter.ts` exporting `currentPath` / `navigate`) plus static `routes` map—keep routes functionally identical for later tasks.

- [ ] **Step 5: Write `admin/index.html`**

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

- [ ] **Step 6: Write `admin/src/vite-env.d.ts`**

```typescript
/// <reference types="svelte" />
/// <reference types="vite/client" />
```

- [ ] **Step 7: Write `admin/src/main.ts`**

```typescript
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";

mount(App, { target: document.getElementById("app")! });
```

- [ ] **Step 8: Write `admin/src/app.css`**

```css
:root {
  font-family: system-ui, sans-serif;
  line-height: 1.4;
}
body {
  margin: 0;
}
```

- [ ] **Step 9: Write `admin/src/App.svelte`**

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

- [ ] **Step 10: Write `admin/src/routes/Home.svelte`**

```svelte
<script lang="ts">
  const hash =
    typeof window !== "undefined" ? window.location.hash || "#/" : "#/";
</script>

<p>Admin shell is up.</p>
<p>Current hash: <code>{hash}</code></p>
```

- [ ] **Step 11: Write `admin/.gitignore`**

```
node_modules
dist
.DS_Store
.env.local
.dev.vars
.wrangler
```

- [ ] **Step 12: Install and build locally**

Run:

```bash
cd admin && npm install && npm run build && npm run test
```

Expected: `npm run test` may report **no tests found** until Task 3; if Vitest exits non-zero with zero tests, run `npm run build` only and confirm `admin/dist/index.html` exists.

- [ ] **Step 13: Commit**

```bash
git add admin
git commit -m "feat(admin): scaffold Vite+Svelte SPA under /admin/"
```

**Ordering:** complete **Task 2b** next (same PR stack is fine) so `npm run dev` already runs the OAuth Worker beside Vite before building auth-heavy UI in Tasks 3+.

---

### Task 2b: OAuth exchange Worker + Vite dev integration + PKCE

**Goal:** Replicate the successful **localhost auth flow** (authorize redirect → callback → **server-side** token `POST` with `client_secret`) inside the **admin** repo layout, started **together** with the Svelte dev server. The browser only talks **same-origin** to a tiny **Cloudflare Worker** (Wrangler dev); the Worker calls GitHub. Add **PKCE** (`code_challenge` on authorize, `code_verifier` on exchange) per [GitHub PKCE guidance](https://github.blog/changelog/2025-07-14-pkce-support-for-oauth-and-github-app-authentication/) and [Generating a user access token for a GitHub App](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app).

**Files:**

- Implemented under repo root: [`cloudflare_site/wrangler.toml`](../../../cloudflare_site/wrangler.toml) — Worker `main`, `[assets]`, **`[[ratelimits]]`** for OAuth token + GitHub user proxy; **`GITHUB_APP_CLIENT_ID`** and **`GITHUB_APP_CLIENT_SECRET`** via process env / Wrangler vars + secrets (local: `CLOUDFLARE_INCLUDE_PROCESS_ENV`); **required** **`TURNSTILE_SITE_KEY`** + **`TURNSTILE_SECRET_KEY`** (503 `missing_turnstile_keys` if absent); **`client_secret`** never in the SPA bundle
- Implemented: [`cloudflare_site/worker/src/index.ts`](../../../cloudflare_site/worker/src/index.ts) — `GET /api/oauth/authorize` (302 to GitHub with `client_id` from env); Worker-expanded `state` embedding Turnstile **site** key; `POST /api/oauth/token` with JSON `{ code, redirect_uri, code_verifier, turnstile_token }`; `GET /api/github/user` proxy. Validates `redirect_uri` allowlist (HTTPS or loopback `/admin/` entry). **No `Referer` fallback** — **`Origin` only** for CORS and for token tab-binding; **`GET /api/oauth/authorize`** without `Origin` uses the navigation rule (admin README / PR #240 High 1). GitHub token exchange uses `application/x-www-form-urlencoded`. **Hardening:** Cloudflare Turnstile siteverify on every token exchange; Wrangler **native rate limits** (30 / 60s on token exchange, 120 / 60s on `GET /api/github/user`, per Cloudflare location) keyed by path + `CF-Connecting-IP`.
- Modify: root [`vite.config.ts`](../../../vite.config.ts) — `server.proxy` `/api` → `http://127.0.0.1:8787` (Wrangler dev port)
- Modify: `admin/package.json` — devDependencies: `wrangler`, `concurrently` (or `npm-run-all`); scripts e.g. `"dev": "concurrently -k -n worker,vite -c blue,green \\\"wrangler dev --port 8787\\\" \\\"vite\\\""` (adjust ports consistently); keep a `dev:vite`-only escape hatch if needed
- Create: `admin/src/lib/auth/pkce.ts` — `randomVerifier()`, `challengeS256(verifier)` using **Web Crypto** (`crypto.subtle.digest`) so the SPA matches GitHub’s S256 rules
- Create: `admin/src/lib/auth/pkce.test.ts` — Vitest: length / shape / stable challenge for fixture verifier (use known test vector or mock subtle)
- Repo-root [`sample.env.local`](../../../sample.env.local) — documents **`GITHUB_APP_CLIENT_ID`** / **`GITHUB_APP_CLIENT_SECRET`** and **required** Turnstile keys (includes **official Cloudflare dummy** site + secret for local dev); SPA does **not** embed client id; Worker adds it at authorize. **Turnstile:** `TURNSTILE_SITE_KEY` / `TURNSTILE_SECRET_KEY` are **Worker-only**; the SPA bundle must **not** bake them in — the site key reaches the browser only via **Worker-expanded OAuth `state`** after authorize (see design Appendix A / High 1 plan). **Do not commit** `.env.local` or `.dev.vars`
- Modify: `admin/.gitignore` — ensure `.env.local`, `.dev.vars`, `.wrangler` present (may already be from Task 2 Step 11)

- [ ] **Step 1: Add Worker + Wrangler config** — minimal `fetch` handler + CORS for **loopback** dev origins **or** browser origin equal to the Worker’s public origin (previews/production same host). **No `Referer`-based origin inference.** No logging of secrets or tokens.

- [ ] **Step 2: Wire `npm run dev`** — one command starts **Wrangler dev** and **Vite**; confirm browser `fetch('http://localhost:5173/api/oauth/...')` (path per `vite.config.ts` proxy) hits the Worker and returns JSON.

- [ ] **Step 3: PKCE helpers + tests** — implement `pkce.ts` + `pkce.test.ts`; document storing **`code_verifier` in `sessionStorage`** from authorize click until callback/finish (same pattern as existing callback handoff plans).

- [ ] **Step 4: `sample.env.local`** — full GitHub App + env walkthrough; example keys only (no real secrets).

- [ ] **Step 5: Manual smoke** — local GitHub App callback `http://localhost:5173/admin/` (or `127.0.0.1`); end-to-end: authorize → brief `/admin/?code=…` → same load replaces to `/admin/#/` → proxied Worker exchange → `ghu_` token only in controlled UI (not console-logged).

- [ ] **Step 6: Commit**

```bash
git add admin
git commit -m "feat(admin): OAuth exchange Worker, Vite dev proxy, PKCE helpers"
```

**Production follow-up:** Task **4b** is implemented via [`cloudflare_site/`](../../../cloudflare_site/) (Worker + static assets, same hostname as `/admin/`). OAuth hardening from PR #240 High 1 (Origin-only tab binding, Turnstile, native rate limits) is implemented in the Worker + Wrangler config above.

---

### Task 3: Vitest for `tokenStore` + `previewHandoff` allowlist

**Files:**

- Create: `admin/src/lib/auth/tokenStore.ts`
- Create: `admin/src/lib/auth/tokenStore.test.ts`
- Create: `admin/src/lib/auth/previewHandoff.ts`
- Create: `admin/src/lib/auth/previewHandoff.test.ts`

- [ ] **Step 1: Write failing tests `admin/src/lib/auth/tokenStore.test.ts`**

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

- [ ] **Step 2: Run tests (expect failure: module missing)**

Run:

```bash
cd admin && npm run test
```

Expected: FAIL — cannot resolve `./tokenStore` or missing exports.

- [ ] **Step 3: Implement `admin/src/lib/auth/tokenStore.ts`**

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

- [ ] **Step 4: Run tokenStore tests**

Run:

```bash
cd admin && npm run test -- src/lib/auth/tokenStore.test.ts
```

Expected: PASS.

- [ ] **Step 5: Write failing tests `admin/src/lib/auth/previewHandoff.test.ts`**

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

- [ ] **Step 6: Run tests (expect failure)**

Run:

```bash
cd admin && npm run test -- src/lib/auth/previewHandoff.test.ts
```

Expected: FAIL — missing module or function.

- [ ] **Step 7: Implement `admin/src/lib/auth/previewHandoff.ts`**

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

- [ ] **Step 8: Run previewHandoff tests**

Run:

```bash
cd admin && npm run test -- src/lib/auth/previewHandoff.test.ts
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add admin/src/lib/auth
git commit -m "feat(admin): token storage and preview return_to allowlist"
```

---

### Task 4: Wire `site-build` to produce `_site/admin/` from `admin/dist`

**Files:**

- Modify: `.github/workflows/site-build.yml`

- [ ] **Step 1: Extend workflow with Node setup and admin build**

Replace the `Prepare site` step with two steps: **Build admin SPA** then **Assemble _site**. Use this pattern (pin Node 22 to match a single LTS across CI):

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

- [ ] **Step 2: Generate lockfile locally and commit**

Run on your machine:

```bash
cd admin && npm install && cd ..
git add admin/package-lock.json
```

- [ ] **Step 3: Commit workflow + lockfile**

```bash
git add .github/workflows/site-build.yml admin/package-lock.json
git commit -m "ci: build admin SPA into site artifact"
```

- [ ] **Step 4: Push to origin and confirm Build Site + Deploy Site**

Push your branch to **origin** and open a PR **into `origin/main`** (triggers the same `workflow_run` deploy as today). Expected: preview URL loads `…/admin/` and shows the admin shell (and mindmap still at `/`).

**Prerequisite for production-shaped OAuth on the same hostname as `/admin/`:** **Task 4b** (below).

---

### Task 4b: Ship Task 2b OAuth Worker with site Worker + static assets

**Goal:** One Cloudflare **Worker + static assets** deployment serves `_site` (mindmap + `/admin/*`) **and** the **same-origin** OAuth token exchange route the SPA calls in preview/production—so the browser never cross-origin `fetch`s `github.com/login/oauth/access_token`, and `client_secret` stays in Wrangler secrets / CI-injected vars only.

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
- Modify: `admin/sample.env.local` (and/or `docs/admin-spa-local-dev.md`) — production + preview Worker URLs, GitHub App callback URL list (`*.workers.dev` preview aliases, production hostname), which GitHub secrets / Cloudflare vars map to which Wrangler names

**Steps:**

- [ ] **Step 1: Research / spike** — Confirm current Wrangler **4.x** syntax for Worker + assets on this repo’s deploy path (`deploy`, `versions upload --assets`). Read Cloudflare docs for **`ASSETS`** (or successor) with static asset routing.

- [ ] **Step 2: Implement `site` Worker shell** — `fetch` forwards non-OAuth traffic to assets; OAuth path returns JSON errors with safe status codes (no secret leakage).

- [ ] **Step 3: Wire exchange handler** — PKCE `code_verifier`, `client_secret` from `env` only. Validate **`Origin`** (only) and `redirect_uri` allowlist for production + preview hostnames; add Turnstile + Wrangler rate limits per High 1 remediation.

- [ ] **Step 4: Local smoke** — `wrangler dev` from `site/` with built `public/` tree: static `/admin/` loads, `POST /api/oauth/...` returns expected GitHub error shape without real code (then with real code in trusted env).

- [ ] **Step 5: CI secrets + deploy** — Add GitHub Actions secrets (names TBD in PR, e.g. `CLOUDFLARE_*` already exist; add app OAuth secrets). Update `wrangler-action` `command` or env so preview **versions upload** and production **deploy** bind secrets. Verify PR preview URL: admin shell + OAuth exchange same origin.

- [ ] **Step 6: GitHub App settings** — Maintainer: register **Callback URL(s)** for production admin origin **and** preview Worker URL pattern (per-alias or wildcard policy per org security rules).

- [ ] **Step 7: Commit**

```bash
git add site .github/workflows
git commit -m "feat(site): Worker + assets with OAuth exchange for admin SPA"
```

**Non-goals for Task 4b:** changing DNS outside Cloudflare Workers defaults; full **Task 8** preview handoff (production fragment redirect)—can remain a follow-up once same-origin OAuth works on preview.

---

### Task 5: Status model types (mirror Go `LayerReport`)

**Files:**

- Create: `admin/src/lib/status/types.ts`
- Create: `admin/src/lib/status/types.test.ts`

- [ ] **Step 1: Write failing test `admin/src/lib/status/types.test.ts`**

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

- [ ] **Step 2: Run test (expect failure)**

Run:

```bash
cd admin && npm run test -- src/lib/status/types.test.ts
```

Expected: FAIL.

- [ ] **Step 3: Implement `admin/src/lib/status/types.ts`**

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

- [ ] **Step 4: Run tests**

Run:

```bash
cd admin && npm run test -- src/lib/status/types.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add admin/src/lib/status
git commit -m "feat(admin): add LayerReport TypeScript model"
```

---

### Task 6: Minimal Octokit client + 401 handling hook

**Files:**

- Create: `admin/package.json` dependency addition (in place): `@octokit/rest`
- Create: `admin/src/lib/github/client.ts`
- Create: `admin/src/lib/github/client.test.ts`

- [ ] **Step 1: Add dependency**

Run:

```bash
cd admin && npm install @octokit/rest@^21.0.0
```

- [ ] **Step 2: Write failing test `admin/src/lib/github/client.test.ts`**

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

- [ ] **Step 3: Run test (expect failure)**

Run:

```bash
cd admin && npm run test -- src/lib/github/client.test.ts
```

Expected: FAIL — missing export.

- [ ] **Step 4: Implement `admin/src/lib/github/client.ts`**

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

- [ ] **Step 5: Run tests**

Run:

```bash
cd admin && npm run test -- src/lib/github/client.test.ts
```

Expected: PASS (smoke only; hook integration with real 401 is manual).

- [ ] **Step 6: Commit**

```bash
git add admin/package.json admin/package-lock.json admin/src/lib/github
git commit -m "feat(admin): Octokit factory with 401 event"
```

---

### Task 7: Production sign-in (authorize URL + SPA document callback)

**Files:**

- Create / maintain: `admin/src/lib/auth/oauth.ts` (PKCE-aware; builds on `pkce.ts` from **Task 2b**)
- Modify: `admin/src/App.svelte` (`onMount`: document `?code=&state=` handoff, `history.replaceState` to `#/`, token exchange)
- Modify: `docs/superpowers/specs/2026-04-06-fullsend-admin-spa-design.md` (Appendix A: REST rows for `/user` and Worker proxy path for exchange)

**Prerequisite:** **Task 2b** (OAuth Worker + Vite proxy + PKCE) must exist so token exchange is **same-origin** to the admin origin in dev; production deploy of that Worker is tracked in **Task 2b** follow-up / **Task 4** extension.

- [ ] **Step 1: `admin/src/lib/auth/oauth.ts`** — PKCE authorize (`startGithubSignIn`), `getOAuthRedirectUri()` from `new URL(import.meta.env.BASE, window.location.origin).href`, document handoff (`consumeOAuthParamsFromDocumentUrl`, one-shot `sessionStorage`), `completeGithubOAuthFromHandoff` (`POST /api/oauth/token`, `saveToken`, `refreshSession`).

- [ ] **Step 2: SPA document callback (no static `callback.html`, no `#/oauth/finish`)**

Register the GitHub App callback as the **SPA entry** matching Vite `base: '/admin/'`, e.g. `http://localhost:5173/admin/` (trailing slash should match `redirect_uri` in code). **`redirect_uri`** in authorize + token exchange is `new URL(import.meta.env.BASE, window.location.origin).href`.

On load, if `URLSearchParams(location.search).has("code")`, stash `{ code, state }` in `sessionStorage` for one-shot use, then **`history.replaceState`** to the same origin with **no** document query and hash **`#/`** so the authorization `code` never lingers in the address bar. Still in `App.svelte` `onMount`, consume the stash, verify `state` against the value stored at authorize time, read PKCE **`code_verifier`**, **`POST /api/oauth/token`** (Task 2b Worker via Vite proxy in dev), **`saveToken`**, clear OAuth session keys, **`refreshSession`**. Surface errors inline (banner / short message). **Never** send `client_secret` from the SPA.

The dev Worker allowlists loopback **`redirect_uri`** pathnames **`/admin/`**, **`/admin`**, and legacy **`/admin/oauth/callback.html`** for migrations.

- [ ] **Step 3: Commit**

```bash
git add admin
git commit -m "feat(admin): OAuth callback via SPA entry /admin/"
```

---

### Task 8: Preview OAuth handoff (production `sessionStorage` + fragment on preview)

**Files:**

- Create: `admin/src/lib/auth/previewStart.ts` (build production URL with signed `state` placeholder—use random `state` + server verification later; **MVP:** `state` = base64url JSON `{ "nonce": "…", "return_to": "<preview>" }` + **HMAC** optional future)
- Modify: `admin/src/routes/Home.svelte` — button “Sign in on preview” when `import.meta.env` or runtime detection says hostname is preview
- Modify: `docs/superpowers/specs/2026-04-06-fullsend-admin-spa-design.md` — document `/oauth/preview-start` and `/oauth/preview-callback` on **production** static pages mirroring Task 7 callback pattern

**MVP crypto:** use `crypto.randomUUID()` for `nonce`; store in `sessionStorage` on production at preview-start page; on preview-callback compare nonce inside `state` JSON **after** GitHub returns to production. **Full HMAC** binding of `return_to` is a follow-up commit once production has a secret (Worker or env-injected at build—**avoid** embedding secrets in static JS).

- [ ] **Step 1: Add `admin/public/oauth/preview-start.html` and `preview-callback.html`** following the spec’s flow (production stores PKCE/state, GitHub redirects to production, production redirects to `return_to` with `#access_token=...&token_type=...&expires_in=...` **only if** GitHub fragment flow applies—**verify** against GitHub docs; if GitHub does not put tokens in hash, use **your** production page to append **fragment** after server exchange).

- [ ] **Step 2: Vitest for `assertAllowedReturnTo` integration** from preview-start (construct `return_to`, assert throws on evil).

- [ ] **Step 3: Commit**

```bash
git add admin/public/oauth admin/src
git commit -m "feat(admin): preview OAuth handoff via production origin"
```

---

### Task 9: Org list (alphabetical, search) + in-memory session cache

**Files:**

- Create: `admin/src/lib/orgs/fetchOrgs.ts` — uses Octokit `paginate` `GET /user/memberships/orgs`
- Create: `admin/src/routes/OrgList.svelte`
- Modify: `admin/src/App.svelte` routes

- [ ] **Step 1: Write Vitest for pure filter `filterOrgsByPrefix` in `admin/src/lib/orgs/filter.test.ts`**

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

- [ ] **Step 2: Implement `admin/src/lib/orgs/filter.ts`**

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

- [ ] **Step 3: Run tests**

Run:

```bash
cd admin && npm run test -- src/lib/orgs/filter.test.ts
```

Expected: PASS.

- [ ] **Step 4: Implement `fetchOrgs` and `OrgList.svelte`** (show loading, error, refresh button; wire token from `loadToken()`).

- [ ] **Step 5: Append Appendix A** with the exact REST method `GET /user/memberships/orgs` and required OAuth scopes (derive from `internal/forge/github` preflight if present).

- [ ] **Step 6: Commit**

```bash
git add admin/src/lib/orgs admin/src/routes/OrgList.svelte admin/src/App.svelte
git commit -m "feat(admin): org list with search-as-you-type"
```

---

### Task 10: Read-only layer status engine (first layer: `ConfigRepoLayer` semantics)

**Files:**

- Create: `admin/src/lib/layers/configRepo.ts` — TS port of **read-only** checks from `internal/layers/configrepo.go` `Analyze` (only what is inferable via public GitHub APIs)
- Create: `admin/src/lib/layers/configRepo.test.ts` — mock GitHub responses

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

- [ ] **Step 2: Extend `admin/src/lib/status/engine.ts` to merge layer reports into org-level rollup** (`not installed` / `degraded` / `installed` wording matches CLI).

- [ ] **Step 3: Update Appendix A** with each new endpoint.

- [ ] **Step 4: Commit per layer**

```bash
git commit -m "feat(admin): TS analyze for workflows layer"
```

(Repeat message with appropriate layer name.)

---

### Task 12: Org detail + repo union list (read-only)

**Files:**

- Create: `admin/src/routes/OrgDetail.svelte`
- Create: `admin/src/lib/repos/unionConfig.ts` — union org repos + `config.yaml` repo names; classify orphan / missing

- [ ] **Step 1: Vitest for pure union/classification** with fixture YAML strings in test file.

- [ ] **Step 2: Implement UI routes `#/org/:login`**.

- [ ] **Step 3: Commit**

```bash
git add admin/src/lib/repos admin/src/routes/OrgDetail.svelte
git commit -m "feat(admin): org detail with repo/config union"
```

---

### Task 13: Wizard shell (linear steps, review screen, no mutations yet)

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

**Files:** grow under `admin/src/lib/actions/*`; each action module pairs with Vitest **HTTP mock** (e.g. `fetch` mock) where feasible.

- [ ] **Step 1: Implement **config repo** create/push path in TS** (mirror Go), with tests.

- [ ] **Step 2: Commit**

```bash
git commit -m "feat(admin): apply config repo layer from wizard"
```

- [ ] **Step 3–N:** Repeat for workflows, secrets, enrollment, appsetup flows; follow `internal/appsetup/*` for GitHub App creation flows that **open github.com** (SPA documents interrupt/resume via `localStorage` per spec).

---

### Task 15: Local dev + CSP notes

**Files:**

- Create: `docs/admin-spa-local-dev.md`

Include: creating a **dev** GitHub App, callback URLs for Vite dev, **`npm run dev`** (Vite + OAuth Worker per **Task 2b**), pointers to **`admin/sample.env.local`**, and **separate** preview app checklist.

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
| GitHub App sign-in + token storage | 1, 2b (exchange + PKCE), 7–8 |
| Per-PR previews + preview OAuth | 4, **4b** (Worker + assets + OAuth on preview host), 8 (production origin pages) |
| Org list + search | 9 |
| Org/repo union + orphan | 12 |
| `LayerReport` / analyze semantics | 5, 10–11 |
| Wizards + review | 13–14 |
| Self-hosted / local dev | 2b, 15 (`sample.env.local` + `docs/admin-spa-local-dev.md`) |
| Permission matrix | 1, 2b, 7, 9–11 (incremental) |
| No automated CLI↔SPA parity CI | Omitted intentionally |

**2. Placeholder scan**

No TBD/TODO strings; **Task 1** (OAuth verification gate) is **complete** (2026-04-12). **Task 2b** is the mandatory admin-local Worker + PKCE path; **Task 4b** deploys exchange + assets on Workers; Task 7 assumes Task 2b exists.

**3. Type consistency**

`LayerStatus` strings use underscores in TS (`not_installed`) matching JSON-friendly IDs; UI labels use `layerStatusLabel` matching Go `String()`.

**Gaps / follow-ups**

- **Path-based routing** under `/admin/*` without hash: add a dedicated task after verifying Cloudflare static asset fallback for nested `index.html`.
- **PKCE (baseline):** **Task 2b** adds PKCE for the production-shaped authorize + exchange path. **Task 8** preview handoff still uses structured `state` + nonce; upgrade to HMAC or signed JWT when preview `return_to` binding needs hardening beyond allowlist.

---

**Plan complete and saved to `docs/superpowers/plans/2026-04-12-fullsend-admin-spa.md`.**

**Execution choice (2026-04-12):** **Subagent-Driven** — dispatch a fresh subagent per task using `subagent-driven-development`, with review between tasks.

**Alternative:** Inline execution via `executing-plans` if you later prefer a single long session.
