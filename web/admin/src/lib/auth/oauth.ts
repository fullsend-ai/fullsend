import { challengeS256, randomVerifier } from "./pkce";
import { refreshSession } from "./session";
import { saveToken } from "./tokenStore";

const PKCE_VERIFIER_KEY = "fullsend_admin_pkce_verifier";
const OAUTH_STATE_KEY = "fullsend_admin_oauth_state";
const OAUTH_DOC_HANDOFF_KEY = "fullsend_admin_oauth_doc_handoff";

const DEFAULT_ADMIN_BASE = "/admin/";

/**
 * Vite injects `import.meta.env.BASE` from root `vite.config.ts` `base`. If it is missing
 * (mis-transformed bundle, odd tooling), `new URL(undefined, origin)` becomes the path
 * literal `"undefined"` — avoid that and match `base: "/admin/"`.
 */
function adminAppBasePath(): string {
  const b = import.meta.env.BASE;
  if (typeof b !== "string" || b.length === 0) {
    return DEFAULT_ADMIN_BASE;
  }
  return b.endsWith("/") ? b : `${b}/`;
}

/**
 * Canonical SPA entry (Vite `base: '/admin/'`). GitHub redirects here with
 * `?code=&state=`; the app strips query via `history.replaceState` after stashing.
 */
export function getOAuthRedirectUri(): string {
  return new URL(adminAppBasePath(), window.location.origin).href;
}

/**
 * Starts GitHub OAuth: stores PKCE + state, then navigates to the site Worker
 * `/api/oauth/authorize`, which redirects to GitHub with `client_id` (never embedded in the SPA bundle).
 */
export async function startGithubSignIn(): Promise<void> {
  const redirectUri = getOAuthRedirectUri();
  const verifier = randomVerifier();
  const challenge = await challengeS256(verifier);
  const state = crypto.randomUUID();

  sessionStorage.setItem(PKCE_VERIFIER_KEY, verifier);
  sessionStorage.setItem(OAUTH_STATE_KEY, state);

  const u = new URL("/api/oauth/authorize", window.location.origin);
  u.searchParams.set("redirect_uri", redirectUri);
  u.searchParams.set("state", state);
  u.searchParams.set("code_challenge", challenge);
  u.searchParams.set("code_challenge_method", "S256");

  window.location.assign(u.toString());
}

export function takePkceVerifier(): string | null {
  const v = sessionStorage.getItem(PKCE_VERIFIER_KEY);
  sessionStorage.removeItem(PKCE_VERIFIER_KEY);
  return v;
}

export function peekOAuthState(): string | null {
  return sessionStorage.getItem(OAUTH_STATE_KEY);
}

export function clearOAuthState(): void {
  sessionStorage.removeItem(OAUTH_STATE_KEY);
}

/**
 * If the document URL contains an OAuth `code`, stash `{ code, state }` for one-shot
 * consumption and replace the URL with `/admin/#/` (no query, no `code` visible).
 */
export function consumeOAuthParamsFromDocumentUrl(): boolean {
  const sp = new URLSearchParams(window.location.search);
  if (!sp.has("code")) return false;

  const code = sp.get("code")?.trim() ?? "";
  const state = sp.get("state") ?? "";
  sessionStorage.setItem(
    OAUTH_DOC_HANDOFF_KEY,
    JSON.stringify({ code, state }),
  );

  const clean = new URL(adminAppBasePath(), window.location.origin);
  clean.search = "";
  clean.hash = "#/";
  history.replaceState(null, "", clean.href);
  return true;
}

type OAuthHandoff = { code: string; state: string };

function takeDocHandoff(): OAuthHandoff | null {
  const raw = sessionStorage.getItem(OAUTH_DOC_HANDOFF_KEY);
  sessionStorage.removeItem(OAUTH_DOC_HANDOFF_KEY);
  if (!raw) return null;
  try {
    const o = JSON.parse(raw) as unknown;
    if (!o || typeof o !== "object") return null;
    const rec = o as Record<string, unknown>;
    const code = typeof rec.code === "string" ? rec.code.trim() : "";
    const state = typeof rec.state === "string" ? rec.state : "";
    if (!code) return null;
    return { code, state };
  } catch {
    return null;
  }
}

export type OAuthCompleteResult =
  | { ok: true }
  | { ok: false; error: string };

/**
 * Completes GitHub OAuth using a one-shot document handoff (see
 * `consumeOAuthParamsFromDocumentUrl`). Call only after URL cleanup.
 */
export async function completeGithubOAuthFromHandoff(): Promise<OAuthCompleteResult> {
  const handoff = takeDocHandoff();
  if (!handoff) {
    return { ok: false, error: "Missing OAuth handoff — try signing in again." };
  }

  const { code, state } = handoff;
  const expected = peekOAuthState();

  if (expected && state !== expected) {
    clearOAuthState();
    return { ok: false, error: "OAuth state mismatch — try signing in again." };
  }

  const verifier = takePkceVerifier();
  if (!verifier) {
    clearOAuthState();
    return {
      ok: false,
      error:
        "Missing PKCE verifier (session expired or this tab did not start sign-in). Open the app from /admin/ and try again.",
    };
  }

  const redirect_uri = getOAuthRedirectUri();
  let res: Response;
  try {
    res = await fetch("/api/oauth/token", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        code,
        redirect_uri,
        code_verifier: verifier,
      }),
    });
  } catch (e) {
    clearOAuthState();
    return {
      ok: false,
      error:
        e instanceof Error
          ? e.message
          : "Network error calling token exchange (is `npm run dev` running?)",
    };
  }

  const body = (await res.json().catch(() => ({}))) as Record<string, unknown>;

  if (!res.ok) {
    const desc =
      typeof body.error_description === "string"
        ? body.error_description
        : typeof body.error === "string"
          ? body.error
          : "token_exchange_failed";
    clearOAuthState();
    return { ok: false, error: `GitHub token exchange failed: ${desc}` };
  }

  const access_token =
    typeof body.access_token === "string" ? body.access_token : "";
  if (!access_token) {
    clearOAuthState();
    return { ok: false, error: "Token response missing access_token." };
  }

  const token_type =
    typeof body.token_type === "string" ? body.token_type : "bearer";
  const expires_in =
    typeof body.expires_in === "number" ? body.expires_in : null;
  const expiresAt =
    expires_in != null ? Date.now() + expires_in * 1000 : 0;

  saveToken({
    accessToken: access_token,
    tokenType: token_type,
    expiresAt,
  });
  clearOAuthState();
  await refreshSession();
  return { ok: true };
}
