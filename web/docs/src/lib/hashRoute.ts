/** Parsed `#/…` fragment: file route, optional heading slug, or directory (trailing `/`). */
export type ParsedDocHash =
  | { kind: "file"; routeKey: string; slug?: string }
  | { kind: "dir"; dirPath: string };

/** @deprecated Use {@link ParsedDocHash}. */
export type DocHashRoute = ParsedDocHash;

function normalizeDirPath(raw: string): string {
  return raw
    .replace(/^\/+/, "")
    .replace(/\/+$/, "")
    .split("/")
    .filter(Boolean)
    .join("/");
}

/**
 * Empty hash or `#/` means “use default document” (caller resolves).
 * Directory URLs use a trailing slash: `#/guides/getting-started/`.
 */
export function parseDocHash(hash: string): ParsedDocHash | null {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  if (raw === "" || raw === "/") return null;

  const withoutLead = raw.startsWith("/") ? raw.slice(1) : raw;
  const sep = withoutLead.indexOf("::");
  if (sep !== -1) {
    const before = withoutLead.slice(0, sep);
    if (before.endsWith("/")) return null;
    const routeKey = normalizeDirPath(before);
    if (!routeKey) return null;
    const slug = withoutLead.slice(sep + 2);
    return { kind: "file", routeKey, slug: slug === "" ? undefined : slug };
  }

  if (withoutLead.endsWith("/")) {
    const dirPath = normalizeDirPath(withoutLead);
    if (!dirPath) return null;
    return { kind: "dir", dirPath };
  }

  const routeKey = normalizeDirPath(withoutLead);
  if (!routeKey) return null;
  return { kind: "file", routeKey };
}

export function formatDocHash(routeKey: string, slug?: string): string {
  const k = normalizeDirPath(routeKey);
  if (!k) return "#/";
  return slug !== undefined && slug !== ""
    ? `#/${k}::${slug}`
    : `#/${k}`;
}

/** Canonical directory fragment; must stay in sync with `web/docs/build/hashFormat.ts`. */
export function formatDocDirHash(dirPath: string): string {
  const k = normalizeDirPath(dirPath);
  if (!k) return "#/";
  return `#/${k}/`;
}
