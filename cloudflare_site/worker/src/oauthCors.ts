/// <reference types="@cloudflare/workers-types" />

/** Env fields used to decide whether admin `/api/*` may run Turnstile-backed flows. */
export interface TurnstileEnvKeys {
  TURNSTILE_SITE_KEY?: string;
  TURNSTILE_SECRET_KEY?: string;
}

function adminSpaCallbackPath(pathname: string): boolean {
  return (
    pathname === "/admin/" ||
    pathname === "/admin" ||
    pathname === "/admin/oauth/callback.html"
  );
}

/** Dev: any localhost / 127.0.0.1 HTTP port (Vite may not use 5173). */
export function isLocalhostHttpOrigin(origin: string): boolean {
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
export function isAllowedOAuthRedirectUri(redirectUri: string): boolean {
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

/**
 * Browser tab origin from **`Origin` only** (no `Referer` fallback — it is not authentication).
 */
export function getBrowserOrigin(request: Request): string | null {
  const originHeader = request.headers.get("Origin") ?? "";
  if (!originHeader) return null;
  try {
    return new URL(originHeader).origin;
  } catch {
    return null;
  }
}

export function workerPublicOrigin(request: Request): string | null {
  try {
    return new URL(request.url).origin;
  } catch {
    return null;
  }
}

function redirectUriOrigin(redirectUri: string): string | null {
  try {
    return new URL(redirectUri).origin;
  } catch {
    return null;
  }
}

/**
 * Full-page navigations to `GET /api/oauth/authorize` often omit `Origin`. Allow the hop when
 * `redirect_uri` is same-origin as this Worker, or both are loopback HTTP (Vite port vs Wrangler).
 */
function authorizeNavigationWithoutOriginAllowed(
  request: Request,
  redirectUri: string,
): boolean {
  const ro = redirectUriOrigin(redirectUri);
  const site = workerPublicOrigin(request);
  if (!ro || !site) return false;
  if (ro === site) return true;
  return isLocalhostHttpOrigin(ro) && isLocalhostHttpOrigin(site);
}

/**
 * CORS for `/api/*`: loopback (dev), or browser origin equals this Worker’s origin (deployed).
 * For `GET /api/oauth/authorize` without `Origin`, uses `redirect_uri` origin when navigation rule passes.
 */
export function effectiveCorsOrigin(request: Request, url: URL): string | null {
  const browser = getBrowserOrigin(request);
  if (browser) {
    if (isLocalhostHttpOrigin(browser)) return browser;
    const site = workerPublicOrigin(request);
    if (site && browser === site) return browser;
    return null;
  }

  if (
    request.method === "GET" &&
    url.pathname === "/api/oauth/authorize"
  ) {
    const redirect_uri = url.searchParams.get("redirect_uri")?.trim() ?? "";
    if (!redirect_uri || !isAllowedOAuthRedirectUri(redirect_uri)) return null;
    const ro = redirectUriOrigin(redirect_uri);
    if (!ro) return null;
    if (authorizeNavigationWithoutOriginAllowed(request, redirect_uri)) return ro;
  }

  // `GET /api/github/user` (and its CORS preflight) may omit `Origin` for same-origin fetches or
  // after some dev proxies. OAuth tab-binding does not apply (Bearer only). Infer ACAO from
  // Fetch Metadata + `Referer` origin only for this path — not used for token exchange.
  if (
    (request.method === "GET" || request.method === "OPTIONS") &&
    url.pathname === "/api/github/user"
  ) {
    const sec = (request.headers.get("Sec-Fetch-Site") ?? "").toLowerCase();
    if (sec === "same-origin" || sec === "same-site") {
      const ref = request.headers.get("Referer")?.trim() ?? "";
      if (ref) {
        try {
          const refOrigin = new URL(ref).origin;
          const site = workerPublicOrigin(request);
          if (!site) return null;
          if (refOrigin === site) return refOrigin;
          if (isLocalhostHttpOrigin(refOrigin) && isLocalhostHttpOrigin(site)) {
            return refOrigin;
          }
        } catch {
          /* ignore */
        }
      }
    }
  }

  return null;
}

/** Authorize: `Origin` when present, else navigation binding (see `authorizeNavigationWithoutOriginAllowed`). */
export function authorizeTabBindingOk(
  request: Request,
  redirectUri: string,
): boolean {
  const browser = getBrowserOrigin(request);
  if (browser) {
    const ro = redirectUriOrigin(redirectUri);
    return Boolean(ro && browser === ro);
  }
  return authorizeNavigationWithoutOriginAllowed(request, redirectUri);
}

/** Token exchange and API `fetch`: require `Origin` and match `redirect_uri` origin. */
export function fetchTabBindingOk(request: Request, redirectUri: string): boolean {
  const browser = getBrowserOrigin(request);
  if (!browser) return false;
  const ro = redirectUriOrigin(redirectUri);
  return Boolean(ro && browser === ro);
}

export function corsHeaders(allowOrigin: string): HeadersInit {
  return {
    "Access-Control-Allow-Origin": allowOrigin,
    "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
    "Access-Control-Allow-Headers":
      "Content-Type, Authorization, Accept, X-GitHub-Api-Version",
    "Access-Control-Max-Age": "86400",
  };
}

export function hasNonEmptyTurnstileKeys(env: TurnstileEnvKeys): boolean {
  return (
    (env.TURNSTILE_SITE_KEY ?? "").trim() !== "" &&
    (env.TURNSTILE_SECRET_KEY ?? "").trim() !== ""
  );
}
