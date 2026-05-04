/// <reference types="@cloudflare/workers-types" />

import {
  authorizeTabBindingOk,
  corsHeaders,
  effectiveCorsOrigin,
  fetchTabBindingOk,
  hasNonEmptyTurnstileKeys,
  isAllowedOAuthRedirectUri,
} from "./oauthCors";

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
  /**
   * Required for admin `/api/*` routes. Site key is folded into OAuth `state` at authorize —
   * never baked into the SPA build. Missing or empty values fail with HTTP 503 and JSON
   * `missing_turnstile_keys` (no silent disable).
   */
  TURNSTILE_SITE_KEY?: string;
  TURNSTILE_SECRET_KEY?: string;
  OAUTH_TOKEN_RATE_LIMITER: RateLimit;
  GITHUB_USER_RATE_LIMITER: RateLimit;
}

const TOKEN_URL = "https://github.com/login/oauth/access_token";
const GITHUB_AUTHORIZE_URL = "https://github.com/login/oauth/authorize";
const GITHUB_USER_URL = "https://api.github.com/user";
const TURNSTILE_SITEVERIFY_URL =
  "https://challenges.cloudflare.com/turnstile/v0/siteverify";

/** Max length of the client-supplied OAuth `state` nonce (before Worker expansion). */
const MAX_CLIENT_OAUTH_STATE_LEN = 128;
const MAX_PKCE_CHALLENGE_LEN = 256;
/** Max `state` sent to GitHub (expanded JSON + base64url including Turnstile site key). */
const MAX_GITHUB_STATE_LEN = 4096;

function isAdminApiPath(pathname: string): boolean {
  return (
    pathname === "/api/oauth/authorize" ||
    pathname === "/api/oauth/token" ||
    pathname === "/api/github/user"
  );
}

function missingTurnstileResponse(): Response {
  return new Response(
    JSON.stringify({
      error: "server_misconfigured",
      detail: "missing_turnstile_keys",
      message:
        "TURNSTILE_SITE_KEY and TURNSTILE_SECRET_KEY must both be set to non-empty values for admin API routes (Wrangler var + secret). See repo sample.env.local for official dummy keys in dev.",
    }),
    {
      status: 503,
      headers: {
        "content-type": "application/json; charset=utf-8",
        "cache-control": "no-store",
      },
    },
  );
}

function isValidClientOAuthNonce(state: string): boolean {
  if (state.length < 8 || state.length > MAX_CLIENT_OAUTH_STATE_LEN) return false;
  return /^[A-Za-z0-9._-]+$/.test(state);
}

function utf8Bytes(s: string): Uint8Array {
  return new TextEncoder().encode(s);
}

function base64UrlEncodeJson(obj: unknown): string {
  const bytes = utf8Bytes(JSON.stringify(obj));
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]!);
  const b64 = btoa(bin);
  return b64.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function buildGithubState(env: Env, clientNonce: string): string | null {
  const siteKey = (env.TURNSTILE_SITE_KEY ?? "").trim();
  const payload = { v: 1 as const, n: clientNonce, k: siteKey };
  const combined = base64UrlEncodeJson(payload);
  if (combined.length > MAX_GITHUB_STATE_LEN) return null;
  return combined;
}

function clientIp(request: Request): string {
  return request.headers.get("CF-Connecting-IP") ?? "unknown";
}

async function consumeRateLimitOr429(
  rl: RateLimit,
  pathname: string,
  request: Request,
  cors: HeadersInit,
): Promise<Response | null> {
  const key = `${pathname}:${clientIp(request)}`;
  const { success } = await rl.limit({ key });
  if (success) return null;
  return new Response(JSON.stringify({ error: "rate_limited" }), {
    status: 429,
    headers: { "content-type": "application/json", ...cors },
  });
}

async function verifyTurnstile(env: Env, token: string): Promise<boolean> {
  const secret = (env.TURNSTILE_SECRET_KEY ?? "").trim();
  const body = new URLSearchParams({ secret, response: token });
  const res = await fetch(TURNSTILE_SITEVERIFY_URL, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: body.toString(),
  });
  const json: unknown = await res.json().catch(() => ({}));
  const rec = json as Record<string, unknown>;
  return rec.success === true;
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

  if (
    !isValidClientOAuthNonce(state) ||
    code_challenge.length > MAX_PKCE_CHALLENGE_LEN
  ) {
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

  if (!authorizeTabBindingOk(request, redirect_uri)) {
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

  const githubState = buildGithubState(env, state);
  if (githubState == null) {
    return new Response(JSON.stringify({ error: "state_too_long" }), {
      status: 500,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  const gh = new URL(GITHUB_AUTHORIZE_URL);
  gh.searchParams.set("client_id", clientId);
  gh.searchParams.set("redirect_uri", redirect_uri);
  gh.searchParams.set("state", githubState);
  gh.searchParams.set("code_challenge", code_challenge);
  gh.searchParams.set("code_challenge_method", "S256");

  return Response.redirect(gh.toString(), 302);
}

type ExchangeBody = {
  code?: string;
  redirect_uri?: string;
  code_verifier?: string;
  turnstile_token?: string;
};

async function handleOAuthToken(
  request: Request,
  env: Env,
  url: URL,
  corsOrigin: string,
): Promise<Response> {
  const cors = corsHeaders(corsOrigin);

  const limited = await consumeRateLimitOr429(
    env.OAUTH_TOKEN_RATE_LIMITER,
    url.pathname,
    request,
    cors,
  );
  if (limited) return limited;

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
  const turnstile_token =
    typeof body.turnstile_token === "string" ? body.turnstile_token.trim() : "";

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

  if (!fetchTabBindingOk(request, redirect_uri)) {
    return new Response(JSON.stringify({ error: "forbidden_origin" }), {
      status: 403,
      headers: { "content-type": "application/json", ...cors },
    });
  }

  if (!turnstile_token) {
    return new Response(JSON.stringify({ error: "turnstile_required" }), {
      status: 400,
      headers: { "content-type": "application/json", ...cors },
    });
  }
  const ok = await verifyTurnstile(env, turnstile_token);
  if (!ok) {
    return new Response(JSON.stringify({ error: "turnstile_failed" }), {
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

/**
 * Server-side GitHub /user so the browser never hits api.github.com (no CORS).
 * Returns a minimal JSON object (`login`, `name`, `avatar_url`) so the SPA never receives
 * the full GitHub profile (email, plan, etc.). Shape matches fields consumed today and on
 * follow-on admin work (e.g. org list UI uses `avatar_url`).
 */
async function handleGithubUser(
  request: Request,
  env: Env,
  url: URL,
  corsOrigin: string,
): Promise<Response> {
  const cors = corsHeaders(corsOrigin);

  const limited = await consumeRateLimitOr429(
    env.GITHUB_USER_RATE_LIMITER,
    url.pathname,
    request,
    cors,
  );
  if (limited) return limited;

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
  const forwardHeaders = {
    "content-type": ghRes.headers.get("content-type") ?? "application/json",
    ...cors,
  };

  if (!ghRes.ok) {
    return new Response(text, { status: ghRes.status, headers: forwardHeaders });
  }

  try {
    const raw = JSON.parse(text) as Record<string, unknown>;
    const login = typeof raw.login === "string" ? raw.login : "";
    if (!login) {
      return new Response(JSON.stringify({ error: "invalid_github_user_payload" }), {
        status: 502,
        headers: { "content-type": "application/json", ...cors },
      });
    }
    const name = typeof raw.name === "string" ? raw.name : null;
    const avatar_url =
      typeof raw.avatar_url === "string" && raw.avatar_url.length > 0
        ? raw.avatar_url
        : null;
    return new Response(JSON.stringify({ login, name, avatar_url }), {
      status: 200,
      headers: { "content-type": "application/json", ...cors },
    });
  } catch {
    return new Response(text, { status: ghRes.status, headers: forwardHeaders });
  }
}

/** Exported for regression tests (e.g. admin API fallthrough must not reference out-of-scope locals). */
export async function handleAdminApi(
  request: Request,
  env: Env,
  url: URL,
): Promise<Response> {
  if (env.DEBUG_LOG === "1") {
    console.log("[worker]", request.method, url.pathname);
  }

  if (!hasNonEmptyTurnstileKeys(env)) {
    if (env.DEBUG_LOG === "1") {
      console.error(
        "[worker] misconfigured: missing TURNSTILE_SITE_KEY and/or TURNSTILE_SECRET_KEY",
      );
    }
    return missingTurnstileResponse();
  }

  const corsOrigin = effectiveCorsOrigin(request, url);

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
    return handleOAuthToken(request, env, url, corsOrigin);
  }

  if (url.pathname === "/api/github/user") {
    return handleGithubUser(request, env, url, corsOrigin);
  }

  return new Response(JSON.stringify({ error: "not_found" }), {
    status: 404,
    headers: {
      "content-type": "application/json",
      ...corsHeaders(corsOrigin),
    },
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
