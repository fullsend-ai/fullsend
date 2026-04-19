# Admin OAuth site Worker (hardening notes)

This document describes intentional behavior of the **Cloudflare site Worker** that backs the admin SPA (`cloudflare_site/worker/`), especially CORS for `GET /api/github/user` and why there is **no** separate “admin OAuth enabled” boolean in configuration.

For local setup and env vars, see [`web/admin/README.md`](../web/admin/README.md). For CI and deploy layout, see [`docs/site-deployment.md`](site-deployment.md).

## `GET /api/github/user` CORS: missing `Origin`

Most admin `/api/*` traffic is evaluated with a single **effective browser origin** derived from the **`Origin` header** (loopback dev, or same origin as the Worker in production). Same-origin `fetch` from the SPA normally sends `Origin`.

Some **same-origin** or **dev-proxy** patterns can omit `Origin` on `GET /api/github/user` (and on its **`OPTIONS`** preflight): for example certain same-origin fetches or proxies in front of Wrangler/Vite. Without a reflected `Access-Control-Allow-Origin`, the browser would block the response even though the request is harmless from a token-exchange perspective: this route is **Bearer-only** (no OAuth `code`, no `client_secret`, no session cookie minted by the Worker).

For **this path only**, when `Origin` is absent, the Worker may still compute `Access-Control-Allow-Origin` by combining:

1. **[Fetch Metadata](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Sec-Fetch-Site):** `Sec-Fetch-Site` must be **`same-origin`** or **`same-site`** (browser signal that the initiating context is not cross-site in the usual sense).
2. **`Referer`:** the request’s `Referer` must parse to an **origin** that matches the Worker’s public origin, or a **paired loopback** rule (both Worker URL and referer origin are loopback HTTP, for typical Vite + Wrangler port splits).

This inference is **not** used for:

- **`POST /api/oauth/token`** — tab binding requires **`Origin`** present and equal to the `redirect_uri` origin (`fetchTabBindingOk` in `oauthCors.ts`). Missing or mismatched `Origin` yields **403** `forbidden_origin`.
- **`GET /api/oauth/authorize`** — uses **`Origin`** when present; without it, the Worker uses the **navigation** rule (`redirect_uri` allowlist + same Worker origin or loopback pairing), not the `Sec-Fetch-Site` + `Referer` path used for `/api/github/user`.

### Intentional trade-off

Using `Referer` (even gated on `Sec-Fetch-Site`) is **weaker** than `Origin` for reflecting CORS: it is **not** treated as proof of who the caller is. It is an **accepted, intentional** trade-off **only** to keep the **GitHub `/user` proxy** usable when `Origin` is missing, while keeping **token exchange** strict.

**Explicitly:** `Referer` is **not** used to authenticate or authorize the GitHub **token exchange**. Exchange still requires a valid **`Origin`**, PKCE, Turnstile verification, `redirect_uri` allowlist, GitHub `code` + app credentials, and rate limits (see below).

## Why there is no `ADMIN_OAUTH_ENABLED`-style flag

Admin OAuth is not gated on a separate **boolean** environment variable (“feature off”). Instead, **misconfiguration or absence of required secrets** naturally prevents the surface from working or from being meaningfully exploitable:

- **Turnstile:** both **`TURNSTILE_SITE_KEY`** and **`TURNSTILE_SECRET_KEY`** must be **non-empty** for any admin `/api/oauth/*` or `/api/github/user` handling. If either is missing or blank, the Worker responds with **503** and JSON **`error: "server_misconfigured"`**, **`detail: "missing_turnstile_keys"`**. That is the operational **“off”** story: there is no silent “Turnstile disabled” mode for these routes.
- **GitHub App:** **`POST /api/oauth/token`** requires **`GITHUB_APP_CLIENT_ID`** and **`GITHUB_APP_CLIENT_SECRET`** (non-empty); otherwise the Worker returns **500** `server_misconfigured`. The SPA does not embed `client_secret`; exchange runs only in the Worker.
- **Rate limits:** Wrangler **native rate limits** apply per path and client IP (e.g. token exchange vs. `/api/github/user`), reducing abuse volume.
- **Origin rules on exchange:** **`POST /api/oauth/token`** requires **`Origin`** matching the **`redirect_uri`** origin; **`redirect_uri`** must be on the **allowlist** (HTTPS or loopback, `/admin/` callback paths).
- **Authorize:** **`GET /api/oauth/authorize`** enforces **`redirect_uri`** allowlist and tab/navigation binding (`authorizeTabBindingOk`); Turnstile site key is folded into `state` server-side.

Together, these layers define the **safety model** without a second “master switch” that could be forgotten left-on in production or flipped without audit.
