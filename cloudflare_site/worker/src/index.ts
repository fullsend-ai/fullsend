/// <reference types="@cloudflare/workers-types" />

/**
 * Site Worker: static assets via `[assets]` plus OAuth BFF + `/user` proxy for the admin SPA.
 * `GET /api/oauth/authorize` adds `client_id` and redirects to GitHub (SPA has no build-time client id).
 */
export interface Env {
  ASSETS?: Fetcher;
  GITHUB_APP_CLIENT_ID: string;
  GITHUB_APP_CLIENT_SECRET: string;
  /** Set to `"1"` via `wrangler dev --var DEBUG_LOG:1` (see root `npm run dev:debug`). */
  DEBUG_LOG?: string;
}

const TOKEN_URL = "https://github.com/login/oauth/access_token";
const GITHUB_AUTHORIZE_URL = "https://github.com/login/oauth/authorize";
const GITHUB_USER_URL = "https://api.github.com/user";

const MAX_STATE_LEN = 512;
const MAX_PKCE_CHALLENGE_LEN = 256;

function isAdminApiPath(pathname: string): boolean {
  return (
    pathname === "/api/oauth/authorize" ||
    pathname === "/api/oauth/token" ||
    pathname === "/api/github/user"
  );
}

function adminSpaCallbackPath(pathname: string): boolean {
  return (
    pathname === "/admin/" ||
    pathname === "/admin" ||
    pathname === "/admin/oauth/callback.html"
  );
}

/** Dev: any localhost / 127.0.0.1 HTTP port (Vite may not use 5173). */
function isLocalhostHttpOrigin(origin: string): boolean {
  if (!origin) return false;
  try {
    const u = new URL(origin);
    return (
      u.protocol === "http:" &&
      (u.hostname === "localhost" ||
        u.hostname === "127.0.0.1" ||
        u.hostname === "[::1]")
    );
  } catch {
    return false;
  }
}

/**
 * `redirect_uri` for the GitHub App web flow: SPA entry under `/admin/` on loopback (dev)
 * or HTTPS (production / Workers previews).
 */
function isAllowedOAuthRedirectUri(redirectUri: string): boolean {
  let u: URL;
  try {
    u = new URL(redirectUri);
  } catch {
    return false;
  }
  if (!adminSpaCallbackPath(u.pathname)) return false;
  if (u.protocol === "http:") {
    return (
      u.hostname === "localhost" ||
      u.hostname === "127.0.0.1" ||
      u.hostname === "[::1]"
    );
  }
  return u.protocol === "https:";
}

function getBrowserOrigin(request: Request): string | null {
  const originHeader = request.headers.get("Origin") ?? "";
  if (originHeader) {
    try {
      return new URL(originHeader).origin;
    } catch {
      return null;
    }
  }
  const ref = request.headers.get("Referer") ?? "";
  if (ref) {
    try {
      return new URL(ref).origin;
    } catch {
      return null;
    }
  }
  return null;
}

function workerPublicOrigin(request: Request): string | null {
  try {
    return new URL(request.url).origin;
  } catch {
    return null;
  }
}

/**
 * CORS for `/api/*`: loopback (dev), or browser origin equals this Worker’s origin (deployed).
 */
function effectiveCorsOrigin(request: Request): string | null {
  const browser = getBrowserOrigin(request);
  if (!browser) return null;
  if (isLocalhostHttpOrigin(browser)) return browser;
  const site = workerPublicOrigin(request);
  if (site && browser === site) return browser;
  return null;
}

/** OAuth token exchange: tab origin must match `redirect_uri` origin (binds `code` to the SPA). */
function browserOriginMatchesRedirectUri(
  request: Request,
  redirectUri: string,
): boolean {
  let redirectOrigin: string;
  try {
    redirectOrigin = new URL(redirectUri).origin;
  } catch {
    return false;
  }
  const browser = getBrowserOrigin(request);
  if (!browser) return false;
  return browser === redirectOrigin;
}

function corsHeaders(allowOrigin: string): HeadersInit {
  return {
    "Access-Control-Allow-Origin": allowOrigin,
    "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
    "Access-Control-Allow-Headers":
      "Content-Type, Authorization, Accept, X-GitHub-Api-Version",
    "Access-Control-Max-Age": "86400",
  };
}

/**
 * Redirect browser to GitHub authorize with `client_id` from env. Query must include PKCE + state.
 */
async function handleOAuthAuthorize(
  request: Request,
  env: Env,
  url: URL,
  corsOrigin: string,
): Promise<Response> {
  const redirect_uri = url.searchParams.get("redirect_uri")?.trim() ?? "";
  const state = url.searchParams.get("state") ?? "";
  const code_challenge =
    url.searchParams.get("code_challenge")?.trim() ?? "";
  const code_challenge_method =
    url.searchParams.get("code_challenge_method")?.trim() ?? "";

  const cors = corsHeaders(corsOrigin);

  if (
    !redirect_uri ||
    !state ||
    !code_challenge ||
    code_challenge_method !== "S256"
  ) {
    return new Response(
      JSON.stringify({ error: "missing_or_invalid_oauth_params" }),
      {
        status: 400,
        headers: { "content-type": "application/json", ...cors },
      },
    );
  }

  if (state.length > MAX_STATE_LEN || code_challenge.length > MAX_PKCE_CHALLENGE_LEN) {
    return new Response(JSON.stringify({ error: "param_too_long" }), {
      status: 400,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  if (!isAllowedOAuthRedirectUri(redirect_uri)) {
    return new Response(JSON.stringify({ error: "invalid_redirect_uri" }), {
      status: 400,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  if (!browserOriginMatchesRedirectUri(request, redirect_uri)) {
    return new Response(JSON.stringify({ error: "forbidden_origin" }), {
      status: 403,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  const clientId = (env.GITHUB_APP_CLIENT_ID ?? "").trim();
  if (!clientId) {
    return new Response(JSON.stringify({ error: "server_misconfigured" }), {
      status: 500,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  const gh = new URL(GITHUB_AUTHORIZE_URL);
  gh.searchParams.set("client_id", clientId);
  gh.searchParams.set("redirect_uri", redirect_uri);
  gh.searchParams.set("state", state);
  gh.searchParams.set("code_challenge", code_challenge);
  gh.searchParams.set("code_challenge_method", "S256");

  return Response.redirect(gh.toString(), 302);
}

type ExchangeBody = {
  code?: string;
  redirect_uri?: string;
  code_verifier?: string;
};

async function handleOAuthToken(
  request: Request,
  env: Env,
  corsOrigin: string,
): Promise<Response> {
  const cors = corsHeaders(corsOrigin);

  if (request.method !== "POST") {
    return new Response(JSON.stringify({ error: "method_not_allowed" }), {
      status: 405,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  const ct = request.headers.get("Content-Type") ?? "";
  if (!ct.includes("application/json")) {
    return new Response(JSON.stringify({ error: "unsupported_media_type" }), {
      status: 415,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  let body: ExchangeBody;
  try {
    body = (await request.json()) as ExchangeBody;
  } catch {
    return new Response(JSON.stringify({ error: "invalid_json" }), {
      status: 400,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  const code = typeof body.code === "string" ? body.code.trim() : "";
  const redirect_uri =
    typeof body.redirect_uri === "string" ? body.redirect_uri.trim() : "";
  const code_verifier =
    typeof body.code_verifier === "string" ? body.code_verifier.trim() : "";

  if (!code || !redirect_uri || !code_verifier) {
    return new Response(JSON.stringify({ error: "missing_fields" }), {
      status: 400,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  if (!isAllowedOAuthRedirectUri(redirect_uri)) {
    return new Response(JSON.stringify({ error: "invalid_redirect_uri" }), {
      status: 400,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  if (!browserOriginMatchesRedirectUri(request, redirect_uri)) {
    return new Response(JSON.stringify({ error: "forbidden_origin" }), {
      status: 403,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  const clientId = (env.GITHUB_APP_CLIENT_ID ?? "").trim();
  const clientSecret = (env.GITHUB_APP_CLIENT_SECRET ?? "").trim();
  if (!clientId || !clientSecret) {
    return new Response(JSON.stringify({ error: "server_misconfigured" }), {
      status: 500,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  const ghBody = new URLSearchParams({
    client_id: clientId,
    client_secret: clientSecret,
    code,
    redirect_uri,
    code_verifier,
  });

  const ghRes = await fetch(TOKEN_URL, {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/x-www-form-urlencoded",
    },
    body: ghBody.toString(),
  });

  const ghJson: unknown = await ghRes.json().catch(() => ({}));
  const rec = ghJson as Record<string, unknown>;
  const access_token =
    typeof rec.access_token === "string" ? rec.access_token : undefined;
  const error = typeof rec.error === "string" ? rec.error : undefined;
  const error_description =
    typeof rec.error_description === "string"
      ? rec.error_description
      : undefined;

  if (!ghRes.ok || error || !access_token) {
    return new Response(
      JSON.stringify({
        error: error ?? "token_exchange_failed",
        error_description: error_description ?? null,
      }),
      {
        status: 400,
        headers: { "content-type": "application/json", ...cors },
      },
    );
  }

  const token_type =
    typeof rec.token_type === "string" ? rec.token_type : "bearer";
  const expires_in =
    typeof rec.expires_in === "number" ? rec.expires_in : undefined;

  return new Response(
    JSON.stringify({
      access_token,
      token_type,
      expires_in: expires_in ?? null,
    }),
    {
      status: 200,
      headers: { "content-type": "application/json", ...cors },
    },
  );
}

/** Server-side GitHub /user so the browser never hits api.github.com (no CORS). */
async function handleGithubUser(
  request: Request,
  corsOrigin: string,
): Promise<Response> {
  const cors = corsHeaders(corsOrigin);

  if (request.method !== "GET") {
    return new Response(JSON.stringify({ error: "method_not_allowed" }), {
      status: 405,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  const auth = request.headers.get("Authorization") ?? "";
  if (!auth.startsWith("Bearer ") || auth.length < 10) {
    return new Response(JSON.stringify({ error: "missing_bearer_token" }), {
      status: 401,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  const accept =
    request.headers.get("Accept") ?? "application/vnd.github+json";
  const apiVersion =
    request.headers.get("X-GitHub-Api-Version") ?? "2022-11-28";

  const ghRes = await fetch(GITHUB_USER_URL, {
    headers: {
      Accept: accept,
      Authorization: auth,
      "X-GitHub-Api-Version": apiVersion,
      "User-Agent": "fullsend-admin-oauth-worker",
    },
  });

  const text = await ghRes.text();
  return new Response(text, {
    status: ghRes.status,
    headers: {
      "content-type": ghRes.headers.get("content-type") ?? "application/json",
      ...cors,
    },
  });
}

async function handleAdminApi(
  request: Request,
  env: Env,
  url: URL,
): Promise<Response> {
  if (env.DEBUG_LOG === "1") {
    console.log("[worker]", request.method, url.pathname);
  }

  const corsOrigin = effectiveCorsOrigin(request);

  if (request.method === "OPTIONS") {
    if (!corsOrigin) {
      return new Response(null, { status: 403 });
    }
    return new Response(null, {
      status: 204,
      headers: corsHeaders(corsOrigin),
    });
  }

  if (url.pathname === "/api/oauth/authorize") {
    if (request.method !== "GET") {
      const cors = corsOrigin ? corsHeaders(corsOrigin) : {};
      return new Response(JSON.stringify({ error: "method_not_allowed" }), {
        status: 405,
        headers: { "content-type": "application/json", ...cors },
      });
    }
    if (!corsOrigin) {
      return new Response(JSON.stringify({ error: "forbidden_origin" }), {
        status: 403,
        headers: { "content-type": "application/json" },
      });
    }
    return handleOAuthAuthorize(request, env, url, corsOrigin);
  }

  if (!corsOrigin) {
    return new Response(JSON.stringify({ error: "forbidden_origin" }), {
      status: 403,
      headers: { "content-type": "application/json" },
    });
  }

  if (url.pathname === "/api/oauth/token") {
    return handleOAuthToken(request, env, corsOrigin);
  }

  if (url.pathname === "/api/github/user") {
    return handleGithubUser(request, corsOrigin);
  }

  return new Response(JSON.stringify({ error: "not_found" }), {
    status: 404,
    headers: { "content-type": "application/json", ...cors },
  });
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);

    if (isAdminApiPath(url.pathname)) {
      return handleAdminApi(request, env, url);
    }

    if (url.pathname.startsWith("/api/")) {
      return new Response(JSON.stringify({ error: "not_found" }), {
        status: 404,
        headers: { "content-type": "application/json" },
      });
    }

    if (env.ASSETS != null) {
      return env.ASSETS.fetch(request);
    }
    return new Response("Worker misconfigured: ASSETS binding missing", {
      status: 503,
    });
  },
};
