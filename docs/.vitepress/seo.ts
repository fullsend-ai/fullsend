import type { HeadConfig } from "vitepress";

// SEO metadata helpers for the VitePress docs site.
//
// The docs are served under a base path — https://fullsend.sh/docs/ — so every
// absolute URL we emit (canonical, og:url, sitemap entries) must include that
// `/docs/` segment. VitePress 1.6.x builds sitemap entries as base-less relative
// paths and resolves them against `sitemap.hostname`, so the hostname itself must
// carry the base and a trailing slash. The same rule applies to URLs we resolve
// here, which is why DOCS_URL_BASE ends in a slash.

/** Public origin of the site. */
export const SITE_ORIGIN = "https://fullsend.sh";

/** Base path the docs are served under (matches `base` in config.ts). */
export const DOCS_BASE = "/docs/";

/**
 * Absolute base for canonical/OG URLs and the sitemap `hostname`.
 * The trailing slash is required for correct `new URL(relative, base)` resolution.
 */
export const DOCS_URL_BASE = `${SITE_ORIGIN}${DOCS_BASE}`;

/**
 * Default social-share image (absolute URL). This is the square brand logo; a
 * dedicated 1200×630 asset would render better on cards and is a good follow-up.
 */
export const OG_IMAGE = `${DOCS_URL_BASE}img/logo.png`;

/** Canonical destinations for pages that are redirects rather than content. */
const CANONICAL_REDIRECTS: Record<string, string> = {
  "index.md": "guides/getting-started/",
  "archived-roadmap.md": "archived-roadmaps/",
};

/** Map an HTML canonical destination to the markdown file that backs it. */
function markdownSourcePath(page: string): string {
  const dest = CANONICAL_REDIRECTS[page];
  if (!dest) return page;
  if (dest.endsWith("/")) return `${dest}index.md`;
  if (dest.endsWith(".md")) return dest;
  return dest.replace(/\.html?$/, "") + ".md";
}

/** Non-content path: template placeholder or repo-metadata (ALL-CAPS) name. */
export function isNonContentPath(pageOrUrl: string): boolean {
  const pathOnly = pageOrUrl.split(/[?#]/, 1)[0];
  return pathOnly
    .split("/")
    .filter(Boolean)
    .some((segment) => {
      const base = segment.replace(/\.(?:html|md)$/, "");
      return /^0000-.*-template/.test(base) || /^[A-Z][A-Z0-9_-]*$/.test(base);
    });
}

/**
 * Convert a VitePress page path (the post-rewrite source path, e.g. "index.md",
 * "agents/index.md", "guides/user/jira-integration.md") into the site-relative
 * output path VitePress actually serves, mirroring its sitemap/link logic:
 *   - `index.md` / `README.md` → the containing directory ("" for the root)
 *   - other `*.md` → `*.html` (or the bare path when `cleanUrls` is enabled)
 *
 * The result is a base-less relative path, suitable for resolving against
 * {@link DOCS_URL_BASE}.
 */
export function pageOutputPath(page: string, cleanUrls = false): string {
  const asDir = page.replace(/(^|\/)(index|README)\.md$/, "$1");
  if (asDir !== page) return asDir;
  return page.replace(/\.md$/, cleanUrls ? "" : ".html");
}

/** Absolute canonical URL for a page, including the `/docs/` base. */
export function canonicalUrl(page: string, cleanUrls = false): string {
  const outputPath = CANONICAL_REDIRECTS[page] ?? pageOutputPath(page, cleanUrls);
  return new URL(outputPath, DOCS_URL_BASE).href;
}

/**
 * Absolute URL base for a VitePress `site.base` path (e.g. `/docs/` or
 * `/docs/v/dev/`). Versioned builds override `--base`; the trailing slash is
 * required so `new URL(page, base)` keeps the last path segment.
 */
export function docsUrlBase(base = DOCS_BASE): string {
  const url = new URL(base, SITE_ORIGIN);
  if (!url.pathname.endsWith("/")) url.pathname += "/";
  return url.href;
}

/**
 * Absolute URL of the published markdown source for a page.
 * `page` is the post-rewrite path (README.md already mapped to index.md),
 * which is the file copied into dist next to the HTML. Redirect stubs (the
 * root `index.md` meta-refresh) follow the same table as `canonicalUrl` so
 * agents fetching the alternate get real content, not the stub.
 * `base` is the current build's VitePress `site.base` so versioned pages
 * advertise that version's own `.md` rather than the unversioned root.
 */
export function markdownUrl(page: string, base = DOCS_BASE): string {
  return new URL(markdownSourcePath(page), docsUrlBase(base)).href;
}

export interface PageSeoInput {
  /** Post-rewrite source path of the page (VitePress `TransformContext.page`). */
  page: string;
  /** Resolved page title (VitePress `TransformContext.title`). */
  title: string;
  /** Resolved page description (VitePress `TransformContext.description`). */
  description: string;
  /** VitePress `site.base` for this build (`/docs/` or `/docs/v/<version>/`). */
  base?: string;
  cleanUrls?: boolean;
}

/**
 * Per-page SEO head tags: a canonical link plus page-specific Open Graph tags.
 * Twitter cards fall back to the `og:*` values, so no `twitter:title` /
 * `twitter:description` duplication is needed.
 * Canonical and og:url stay on the unversioned `/docs/` URL. The markdown
 * alternate uses `base` so a versioned page points at that version's source.
 */
export function pageSeoHead({
  page,
  title,
  description,
  base = DOCS_BASE,
  cleanUrls = false,
}: PageSeoInput): HeadConfig[] {
  const url = canonicalUrl(page, cleanUrls);
  return [
    ["link", { rel: "canonical", href: url }],
    ["link", { rel: "alternate", type: "text/markdown", href: markdownUrl(page, base) }],
    ["meta", { property: "og:url", content: url }],
    ["meta", { property: "og:title", content: title }],
    ["meta", { property: "og:description", content: description }],
  ];
}

/**
 * Whether a page should advertise a canonical / OG URL to crawlers.
 * VitePress injects a custom 404.md that can't be disabled.
 */
export function isIndexablePage(page: string): boolean {
  return page !== "404.md" && page !== "archived-roadmap.md" && !isNonContentPath(page);
}

/** Robots metadata for pages that must not enter search indexes. */
export function pageRobotsHead(page: string): HeadConfig[] {
  return page === "404.md" || page === "archived-roadmap.md"
    ? [["meta", { name: "robots", content: "noindex" }]]
    : [];
}

/**
 * Whether a generated sitemap URL represents public content.
 * The root is a meta-refresh redirect, so advertise its canonical destination
 * instead. Normal experiment pages remain public; only their templates and
 * repo-metadata pages are filtered with the same rule used by navigation.
 */
export function isSitemapUrl(url: string): boolean {
  const normalized = url.replace(/^\/+|\/+$/g, "");
  return (
    normalized !== "" &&
    normalized !== "archived-roadmap" &&
    normalized !== "archived-roadmap.html" &&
    !isNonContentPath(normalized)
  );
}

/** Site-wide SEO head tags that are identical on every page. */
export const globalSeoHead: HeadConfig[] = [
  ["meta", { property: "og:type", content: "website" }],
  ["meta", { property: "og:site_name", content: "Fullsend" }],
  ["meta", { property: "og:image", content: OG_IMAGE }],
  ["meta", { name: "twitter:card", content: "summary_large_image" }],
  ["meta", { name: "twitter:image", content: OG_IMAGE }],
];
