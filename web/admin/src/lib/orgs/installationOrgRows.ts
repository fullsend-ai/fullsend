import type { OrgRow } from "./filter";

/** Safe GitHub App slug: alphanumeric and hyphen, length 1–99. */
export const SLUG_RE = /^[a-zA-Z0-9-]{1,99}$/;

/** Subset of GitHub `GET /user/installations` item fields used for org picker mapping. */
export type MinimalInstallation = {
  account?: { login?: string; type?: string } | null;
  app_slug?: string | null;
  app?: { slug?: string | null } | null;
};

export function normalizeSlug(raw: string | null | undefined): string | null {
  if (raw == null) return null;
  const s = String(raw).trim();
  if (!s) return null;
  return SLUG_RE.test(s) ? s : null;
}

export function slugFromInstallation(inst: MinimalInstallation): string | null {
  const fromTop = normalizeSlug(inst.app_slug ?? undefined);
  if (fromTop) return fromTop;
  return normalizeSlug(inst.app?.slug ?? undefined);
}

/**
 * Maps installation list to unique Organization rows (sorted by login) and the
 * first safe app slug found in array order (`app_slug` if valid, else `app.slug`).
 */
export function orgRowsAndSlugFromInstallations(installations: MinimalInstallation[]): {
  orgs: OrgRow[];
  appSlug: string | null;
} {
  let appSlug: string | null = null;
  const byLogin = new Map<string, OrgRow>();

  for (const inst of installations) {
    if (appSlug == null) {
      const s = slugFromInstallation(inst);
      if (s) appSlug = s;
    }
    const acc = inst.account;
    const accType = acc?.type?.trim();
    if (!acc?.login || !accType || accType.toLowerCase() !== "organization") {
      continue;
    }
    const login = acc.login.trim();
    if (!login) continue;
    if (!byLogin.has(login)) {
      byLogin.set(login, { login });
    }
  }

  const orgs = [...byLogin.values()].sort((a, b) => a.login.localeCompare(b.login));

  return { orgs, appSlug };
}
