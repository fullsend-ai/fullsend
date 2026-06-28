import fs from "node:fs";
import path from "node:path";

/** Repo-relative POSIX path using `/` (e.g. `docs/guides/x.md`). */
export type DocsFilePath = `docs/${string}`;

export type DocPathFilter = (repoRelativeMd: DocsFilePath) => boolean;

const defaultFilter: DocPathFilter = () => true;

function toPosix(p: string): string {
  return p.split(path.sep).join("/");
}

/**
 * Lists all `.md` files under `docs/` from repo root (recursive). Applies `filter` after discovery (v1: identity).
 * Future: swap `filter` for deny-glob or config-driven predicate without changing callers.
 */
export function listDocMarkdownFiles(
  repoRoot: string,
  filter: DocPathFilter = defaultFilter,
): DocsFilePath[] {
  const docsRoot = path.join(repoRoot, "docs");
  const out: DocsFilePath[] = [];

  function walk(dir: string) {
    for (const ent of fs.readdirSync(dir, { withFileTypes: true })) {
      const abs = path.join(dir, ent.name);
      if (ent.isDirectory()) walk(abs);
      else if (ent.isFile() && ent.name.endsWith(".md")) {
        const rel = toPosix(path.relative(repoRoot, abs));
        if (!rel.startsWith("docs/")) continue;
        if (filter(rel as DocsFilePath)) out.push(rel as DocsFilePath);
      }
    }
  }

  if (fs.existsSync(docsRoot)) walk(docsRoot);
  out.sort((a, b) => a.localeCompare(b));
  return out;
}

/** `docs/guides/getting-started/installation.md` → `guides/getting-started/installation` */
export function filePathToRouteKey(repoRelativeMd: DocsFilePath): string {
  const withoutPrefix = repoRelativeMd.slice("docs/".length);
  if (!withoutPrefix.endsWith(".md")) {
    throw new Error(`Expected .md file, got: ${repoRelativeMd}`);
  }
  return withoutPrefix.slice(0, -".md".length);
}

/** `guides/getting-started/installation` → `/docs/guides/getting-started/installation` */
export function routeKeyToUrl(routeKey: string): string {
  const k = routeKey.replace(/^\/+/, "");
  return `/docs/${k}`;
}

/**
 * Strip `/docs` prefix from pathname; empty string means "root doc" (redirect to a default in UI).
 * `/docs/guides/x` → `guides/x`
 */
export function pathnameToRouteKey(pathname: string): string {
  const p = pathname.replace(/\/+$/, "") || "/";
  if (!p.startsWith("/docs")) return "";
  const rest = p.slice("/docs".length).replace(/^\/+/, "");
  return rest;
}
