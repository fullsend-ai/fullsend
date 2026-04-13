#!/usr/bin/env python3
"""Local OAuth Task 1 helper: inject CLIENT_ID and exercise the GitHub token exchange.

Requires CLIENT_ID in the environment (GitHub App OAuth client id — public).

  export CLIENT_ID='Iv23…'
  python3 serve.py

Optional CLIENT_SECRET (same machine only — never commit, never send from the browser):
when set, POST /_experiment/github-access-token performs the full documented web-app
exchange (includes client_secret server-side only).

Optional: PORT (default 5173), BIND (default 127.0.0.1). If PORT is not 5173, register the
matching callback URL on your GitHub App (http://localhost:<PORT>/oauth/callback.html).
"""

from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlencode


def _require_client_id() -> str:
    cid = os.environ.get("CLIENT_ID", "").strip()
    if not cid:
        print("error: set CLIENT_ID to your GitHub App OAuth client ID, e.g.", file=sys.stderr)
        print("  export CLIENT_ID='Iv23…'", file=sys.stderr)
        sys.exit(1)
    return cid


def _port() -> int:
    raw = os.environ.get("PORT", "5173").strip()
    try:
        p = int(raw, 10)
    except ValueError as e:
        print(f"error: PORT must be an integer, got {raw!r}", file=sys.stderr)
        raise SystemExit(1) from e
    if not (1 <= p <= 65535):
        print("error: PORT out of range", file=sys.stderr)
        raise SystemExit(1)
    return p


def _bind() -> str:
    return os.environ.get("BIND", "127.0.0.1").strip() or "127.0.0.1"


def _redirect_uri(port: int) -> str:
    return f"http://localhost:{port}/oauth/callback.html"


def _authorize_url(client_id: str, redirect_uri: str) -> str:
    q = urlencode(
        {
            "client_id": client_id,
            "redirect_uri": redirect_uri,
            "state": "oauth-localhost-part-b",
        }
    )
    return f"https://github.com/login/oauth/authorize?{q}"


def _optional_client_secret() -> str | None:
    raw = os.environ.get("CLIENT_SECRET", "").strip()
    return raw or None


def _github_oauth_access_token(
    client_id: str,
    code: str,
    redirect_uri: str,
    *,
    client_secret: str | None,
) -> tuple[int, str]:
    """POST to GitHub from this process. Returns (status, response text)."""
    params: dict[str, str] = {
        "client_id": client_id,
        "code": code,
        "redirect_uri": redirect_uri,
    }
    if client_secret is not None:
        params["client_secret"] = client_secret
    body = urlencode(params).encode("utf-8")
    req = urllib.request.Request(
        "https://github.com/login/oauth/access_token",
        data=body,
        method="POST",
        headers={
            "Accept": "application/json",
            "Content-Type": "application/x-www-form-urlencoded",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
            return resp.status, raw
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8", errors="replace")
        return e.code, raw
    except urllib.error.URLError as e:
        reason = e.reason if isinstance(e.reason, str) else repr(e.reason)
        raise OSError(f"GitHub token request failed: {reason}") from e


def _html_page(title: str, body: str) -> bytes:
    doc = f"""<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1.0"/>
<title>{title}</title>
</head>
<body>
{body}
</body>
</html>
"""
    return doc.encode("utf-8")


def _index_html(client_id: str, authorize_url: str, redirect_uri: str) -> bytes:
    from html import escape

    cid_display = escape(client_id, quote=True)
    auth_escaped = escape(authorize_url, quote=True)
    redir_escaped = escape(redirect_uri, quote=True)
    body = f"""<h1>OAuth Task 1 — localhost Part B</h1>
<p><strong>Callback URL</strong> registered on the GitHub App must match exactly:</p>
<p><code>{redir_escaped}</code></p>
<p>Using <code>CLIENT_ID</code> (public): <code>{cid_display}</code></p>
<p>
If you started the server with <code>CLIENT_SECRET</code> in the environment (only on this
machine — never in git, never in the browser), the same-origin proxy performs the
<strong>full</strong> token exchange and you should see an <code>access_token</code> in the
JSON when the <code>code</code> is valid.
</p>
<h2>Part A — authorize</h2>
<p>Open this link in <strong>this same browser</strong> (new tab is fine):</p>
<p><a href="{auth_escaped}">Sign in with GitHub (authorize)</a></p>
<p>Or copy URL:</p>
<pre>{auth_escaped}</pre>
<h2>Part B</h2>
<p>After GitHub redirects to <code>/oauth/callback.html</code>, the page calls
<strong>same-origin</strong> <code>POST /_experiment/github-access-token</code>; this helper
forwards to GitHub <strong>server-side</strong> so the one-time <code>code</code> is not spent
on a browser <code>fetch</code> to <code>github.com</code> first. With <code>CLIENT_SECRET</code>
set in the server environment, the POST includes the secret <strong>only</strong> on the
Python → GitHub hop (models a Worker/BFF). Still dev-only.</p>
<p><strong>Never</strong> put <code>client_secret</code> in the browser or in JSON to this
server — use <code>export CLIENT_SECRET=…</code> before <code>python3 serve.py</code> only.</p>
"""
    return _html_page("OAuth Task 1 — localhost Part B", body)


def _callback_html(client_id: str, redirect_uri: str) -> bytes:
    cid_js = json.dumps(client_id)
    redir_js = json.dumps(redirect_uri)
    script = f"""
<script>
(async () => {{
  const CLIENT_ID = {cid_js};
  const REDIRECT_URI = {redir_js};
  const out = document.getElementById("out");
  const p = new URLSearchParams(location.search);
  const oauthErr = p.get("error");
  const desc = p.get("error_description");
  const code = p.get("code");
  const state = p.get("state");

  const lines = [];
  if (oauthErr) {{
    lines.push("OAuth error query: " + oauthErr);
    if (desc) lines.push("error_description: " + desc);
  }}
  lines.push("code=" + (code ?? "(missing)"));
  lines.push("state=" + (state ?? "(missing)"));
  out.textContent = lines.join("\\n");

  if (!code || oauthErr) return;

  const partB = document.getElementById("partb");
  partB.textContent = "Calling same-origin proxy (server → GitHub; one use of code)…";
  const proxyRes = await fetch("/_experiment/github-access-token", {{
    method: "POST",
    headers: {{
      Accept: "application/json",
      "Content-Type": "application/json",
    }},
    body: JSON.stringify({{
      client_id: CLIENT_ID,
      code: code,
      redirect_uri: REDIRECT_URI,
    }}),
  }});
  const proxyJson = await proxyRes.json();
  partB.textContent = JSON.stringify(proxyJson, null, 2);
}})();
</script>
"""
    body = f"""<h1>OAuth callback</h1>
<p><a href="/">← Back to index</a></p>
<pre id="out">Loading…</pre>
<h2>Token exchange (proxy)</h2>
<pre id="partb">(waiting)</pre>
{script}
"""
    return _html_page("OAuth callback (localhost)", body)


class Handler(BaseHTTPRequestHandler):
    server_version = "OAuthLocalhostPartB/1.0"

    def _send(
        self,
        status: int,
        body: bytes,
        content_type: str = "text/html; charset=utf-8",
    ) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_json(self, status: int, obj: object) -> None:
        raw = json.dumps(obj, indent=2).encode("utf-8")
        self._send(status, raw, "application/json; charset=utf-8")

    def do_POST(self) -> None:  # noqa: N802
        path = self.path.split("?", 1)[0]
        if path != "/_experiment/github-access-token":
            self.send_error(404, "unknown POST path")
            return

        client_id = self.server.client_id  # type: ignore[attr-defined]
        port = self.server.port  # type: ignore[attr-defined]
        allowed_redirect = _redirect_uri(port)

        length_raw = self.headers.get("Content-Length", "0")
        try:
            length = int(length_raw, 10)
        except ValueError:
            self._send_json(400, {"error": "bad Content-Length"})
            return
        if length <= 0 or length > 8192:
            self._send_json(400, {"error": "body must be 1..8192 bytes"})
            return

        raw_body = self.rfile.read(length)
        try:
            payload = json.loads(raw_body.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            self._send_json(400, {"error": "body must be JSON"})
            return

        if "client_secret" in payload:
            self._send_json(
                400,
                {
                    "error": (
                        "do not send client_secret in JSON; set CLIENT_SECRET in the "
                        "server environment only"
                    ),
                },
            )
            return

        cid = payload.get("client_id")
        code = payload.get("code")
        redirect_uri = payload.get("redirect_uri")
        if (
            not isinstance(cid, str)
            or not isinstance(code, str)
            or not isinstance(redirect_uri, str)
        ):
            self._send_json(
                400,
                {"error": "JSON must have string client_id, code, redirect_uri"},
            )
            return
        if cid != client_id:
            self._send_json(403, {"error": "client_id must match this server’s CLIENT_ID"})
            return
        if redirect_uri != allowed_redirect:
            self._send_json(
                403,
                {"error": "redirect_uri must match callback URL", "expected": allowed_redirect},
            )
            return

        secret = self.server.client_secret  # type: ignore[attr-defined]
        try:
            gh_status, gh_text = _github_oauth_access_token(
                cid,
                code,
                redirect_uri,
                client_secret=secret,
            )
        except OSError as e:
            self._send_json(502, {"error": "upstream GitHub request failed", "detail": str(e)})
            return
        try:
            gh_json = json.loads(gh_text)
        except json.JSONDecodeError:
            gh_json = gh_text
        if secret:
            note = (
                "Dev-only: full web-app exchange (client_secret taken from this process "
                "environment only, never from the browser). Treat access_token in the response "
                "as sensitive."
            )
        else:
            note = (
                "Dev-only: exchange without client_secret (GitHub may return an error JSON). "
                "Set CLIENT_SECRET in the environment and restart to test the full flow."
            )
        self._send_json(
            200,
            {
                "note": note,
                "github_http_status": gh_status,
                "github_body": gh_json,
            },
        )

    def do_GET(self) -> None:  # noqa: N802
        path = self.path.split("?", 1)[0]
        client_id = self.server.client_id  # type: ignore[attr-defined]
        port = self.server.port  # type: ignore[attr-defined]
        redirect_uri = _redirect_uri(port)

        if path in ("/", "/index.html"):
            page = _index_html(
                client_id,
                _authorize_url(client_id, redirect_uri),
                redirect_uri,
            )
            self._send(200, page)
            return
        if path == "/oauth/callback.html":
            page = _callback_html(client_id, redirect_uri)
            self._send(200, page)
            return

        self.send_error(404, "use / or /oauth/callback.html")

    def log_message(self, format: str, *args: object) -> None:  # noqa: A003
        message = format % args if args else format
        sys.stderr.write(f"{self.address_string()} - - [{self.log_date_time_string()}] {message}\n")


def main() -> None:
    client_id = _require_client_id()
    port = _port()
    bind = _bind()
    client_secret = _optional_client_secret()
    httpd = ThreadingHTTPServer((bind, port), Handler)
    httpd.client_id = client_id  # type: ignore[attr-defined]
    httpd.port = port  # type: ignore[attr-defined]
    httpd.client_secret = client_secret  # type: ignore[attr-defined]
    redir = _redirect_uri(port)
    print(f"Serving http://localhost:{port}/  (bind {bind}:{port})")
    print(f"Callback URL must be: {redir}")
    if client_secret:
        print("CLIENT_SECRET is set: proxy will send full token exchange to GitHub.")
    else:
        print("CLIENT_SECRET not set: proxy exchange omits client_secret (partial test).")
    print("Press Ctrl+C to stop.")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nStopped.")
    finally:
        httpd.server_close()


if __name__ == "__main__":
    main()
