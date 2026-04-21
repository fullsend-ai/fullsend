# Task 1 Part B — real `http://localhost` origin

OAuth **Part A** (authorize) and **Part B** (same-origin `POST` to this helper, which exchanges the `code` **server-side** with GitHub) against a real **`http://localhost:<PORT>`** origin, matching the GitHub App callback URL.

The in-repo **admin** SPA under `web/admin/` uses a **different** callback: register `http://localhost:<PORT>/admin/` (see `web/admin/README.md`), not this folder’s `/oauth/callback.html` path.

Dynamic pages are served by **`serve.py`**, which reads your GitHub App’s public **OAuth client id** from the environment (`CLIENT_ID`). Optionally it reads **`CLIENT_SECRET`** for a **full** server-side token exchange (same shape as a production Worker/BFF). The secret is **never** read from the browser or from JSON bodies—only from your shell environment on the machine running `serve.py`. **Never commit** secrets; this folder’s `.gitignore` ignores `.env*`.

## 1. Register the callback on your test GitHub App

For the default port **5173**, the **Callback URL** must be exactly:

`http://localhost:5173/oauth/callback.html`

If you use `PORT=…` when starting the server, register **`http://localhost:<PORT>/oauth/callback.html`** instead.

## 2. Start the server

From **this directory**:

```bash
export CLIENT_ID='Iv23…'   # GitHub App → OAuth credentials → Client ID
export CLIENT_SECRET='…'  # optional: full token exchange (generate once in GitHub UI)
python3 serve.py
```

Optional environment variables:

| Variable | Default | Meaning |
|----------|---------|---------|
| `CLIENT_ID` | _(required)_ | GitHub App OAuth client ID |
| `CLIENT_SECRET` | _(unset)_ | If set, server-side `POST` to GitHub includes `client_secret` (full flow; **do not commit**) |
| `PORT` | `5173` | local HTTP port |
| `BIND` | `127.0.0.1` | address the socket listens on |

Example with a non-default port:

```bash
export CLIENT_ID='Iv23…'
PORT=8765 python3 serve.py
```

Then register `http://localhost:8765/oauth/callback.html` on the app and open **`http://localhost:8765/`** in the browser (same host as the callback).

## 3. Use a normal browser tab

Open **Chrome** or **Firefox** (not an embedded IDE preview). Visit:

`http://localhost:<PORT>/`

Use the **Sign in with GitHub** link (Part A). After approval, GitHub redirects to **`/oauth/callback.html`**, which:

1. Shows the query parameters (`code`, `state`, or OAuth errors).
2. Calls **`POST /_experiment/github-access-token`** once; `serve.py` forwards to GitHub **server-side** so the one-time `code` is not consumed by a prior browser `fetch` to `github.com`. Without `CLIENT_SECRET` you can still inspect GitHub’s error JSON; **with** `CLIENT_SECRET` in the environment you get the full exchange (expect `access_token` / `ghu_…` when the `code` is valid). Requests that include `client_secret` in JSON are **rejected** — use env only.

### Note on CORS

A direct browser `fetch` to `https://github.com/login/oauth/access_token` is not used here: GitHub’s response is not meant to be read cross-origin from JS, and an attempt could still **consume** the authorization `code` before this helper runs. This flow uses **same-origin** `fetch` to localhost only.

## Fallback: static files only

If you run `python3 -m http.server` against this folder, `index.html` and `oauth/callback.html` are only stubs pointing you back to **`serve.py`** — they do **not** inject `CLIENT_ID` or auto-run Part B.

## Files

| Path | Role |
|------|------|
| `serve.py` | HTTP server: `/` index, `/oauth/callback.html` (proxy exchange only), `POST /_experiment/github-access-token` |
| `index.html` | Stub when not using `serve.py` |
| `oauth/callback.html` | Stub when not using `serve.py` |
