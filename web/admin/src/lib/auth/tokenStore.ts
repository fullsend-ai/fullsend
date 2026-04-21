export type StoredToken = {
  accessToken: string;
  tokenType: string;
  /** Absolute expiry in ms since epoch, or null when unknown (no client-side TTL). */
  expiresAt: number | null;
};

const KEY = "fullsend_admin_github_token";

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
  const accessToken =
    typeof t.accessToken === "string" ? t.accessToken.trim() : "";
  if (!accessToken) return null;
  const tokenType =
    typeof t.tokenType === "string" && t.tokenType.length > 0
      ? t.tokenType
      : "bearer";

  const expiresAt = parseExpiresAt(t.expiresAt);
  if (
    typeof expiresAt === "number" &&
    expiresAt > 0 &&
    Date.now() > expiresAt
  ) {
    clearSession();
    return null;
  }

  return { accessToken, tokenType, expiresAt };
}

export function clearSession(): void {
  localStorage.removeItem(KEY);
}
