import { clearOAuthScopeHeaderCache } from "../layers/preflight";
import { clearInstallReadinessProbeCache } from "../orgs/installReadinessProbes";

export type StoredToken = {
  accessToken: string;
  tokenType: string;
  /** Absolute expiry in ms since epoch, or null when unknown (no client-side TTL). */
  expiresAt: number | null;
};

const KEY = "fullsend_admin_github_token";
const GITHUB_APP_SLUG_KEY = "fullsend_admin_github_app_slug";

/**
 * Persists the GitHub App slug from Worker-expanded OAuth `state` after a successful token
 * exchange. Clears stored slug when `slug` is missing or blank (e.g. older Workers).
 */
export function persistGithubAppSlugFromOAuth(slug: string | undefined): void {
  const s = typeof slug === "string" ? slug.trim() : "";
  if (!s) {
    localStorage.removeItem(GITHUB_APP_SLUG_KEY);
    return;
  }
  localStorage.setItem(GITHUB_APP_SLUG_KEY, s);
}

/** Slug for the admin OAuth GitHub App (install URL), or null if not provided at last sign-in. */
export function loadGithubAppSlug(): string | null {
  const s = localStorage.getItem(GITHUB_APP_SLUG_KEY)?.trim() ?? "";
  return s.length > 0 ? s : null;
}

export function saveToken(t: StoredToken): void {
  localStorage.setItem(KEY, JSON.stringify(t));
}

function parseExpiresAt(raw: unknown): number | null {
  if (raw === null || raw === undefined) return null;
  if (raw === 0) return null;
  if (typeof raw !== "number" || !Number.isFinite(raw)) return null;
  return raw;
}

export function loadToken(): StoredToken | null {
  const raw = localStorage.getItem(KEY);
  if (!raw) return null;
  let o: unknown;
  try {
    o = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!o || typeof o !== "object") return null;
  const t = o as Record<string, unknown>;
  const accessToken = typeof t.accessToken === "string" ? t.accessToken.trim() : "";
  if (!accessToken) return null;
  const tokenType =
    typeof t.tokenType === "string" && t.tokenType.length > 0 ? t.tokenType : "bearer";

  const expiresAt = parseExpiresAt(t.expiresAt);
  if (typeof expiresAt === "number" && expiresAt > 0 && Date.now() > expiresAt) {
    clearSession();
    return null;
  }

  return { accessToken, tokenType, expiresAt };
}

export function clearSession(): void {
  localStorage.removeItem(KEY);
  localStorage.removeItem(GITHUB_APP_SLUG_KEY);
  clearOAuthScopeHeaderCache();
  clearInstallReadinessProbeCache();
}
