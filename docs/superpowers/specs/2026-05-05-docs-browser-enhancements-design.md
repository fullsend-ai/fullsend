# Design: Docs browser enhancements (layout, hash routing, chunks)

Date: 2026-05-05
Status: Approved (implementation plan: [2026-05-05-docs-browser-enhancements.md](../plans/2026-05-05-docs-browser-enhancements.md))

## Relationship to prior spec

[2026-05-04-docs-browser-design.md](./2026-05-04-docs-browser-design.md) remains the record of the initial `/docs/` SPA shape. This document **supersedes** it for the items below (routing model, sidebar side, bundle shape, and related UI). Implementation should treat this spec as authoritative for those areas once approved.

## Goals

- **Layout:** Documentation **outline on the left**, article on the right (LTR). Independent vertical scrolling for outline vs article.
- **Navigation chrome:** Icon-only **close** control to collapse the sidebar; **hamburger** (or equivalent) **top-left**, always available when the sidebar is collapsed so the tree can be reopened on desktop as well as mobile.
- **Tree UX:** **Collapsible** directory nodes; **folder** icons with open/closed state; documents as a **list** with **document**-style markers (not plain uppercase folder labels only).
- **Visual consistency:** Color and surface treatment **aligned with the admin SPA** (shared tokens or a small shared stylesheet; avoid duplicating large unrelated admin UI).
- **Mermaid:** **Lazy-loaded** library (`dynamic import()`), invoked only when the current page contains Mermaid blocks.
- **Bundles:** **Option 1** — Vite/Rollup **code-split** per document (lazy virtual modules or equivalent static import graph) so production ships **many hashed `assets/*.js` chunks** plus a small shell; **no** runtime Markdown compilation and **no** new Worker routes for docs.
- **Front matter:** **Not rendered** in the article HTML; parsed at build time and exposed as **structured data** per page (e.g. alongside title/HTML in the lazy module, and optionally mirrored on the `<article>` as `data-*` for future UI).
- **Links:** Relative links between docs resolve to **in-app hash URLs**; behavior matches contributor expectations (including extensionless and `../` cases where feasible).
- **Deep links / refresh:** **Hash-based** app state so a cold load of `/docs/` with `#/…` always boots the docs shell and opens the correct doc **without** relying on pathname deep routes or multi-SPA pathname fallback.

## Non-goals

- **Search** and authenticated docs (unchanged from v1).
- **SSR** for docs (still client-rendered HTML from build pipeline).

## Routing and URL shape

- **Shell URL:** `/docs/` (trailing-slash policy unchanged from repo conventions).
- **Document identity** in the browser: **`location.hash`**, not `pathname`.
- **Canonical fragment:**
  - Base: **`#/<routeKey>`** where `<routeKey>` is the same logical key as today (repository path under `docs/` without the `docs/` prefix and without `.md`, POSIX `/` segments). Example: `#/guides/admin/installation`.
  - **In-document target** (optional): **`#/<routeKey>::<slug>`** where `<slug>` matches the **generated** heading `id` in the rendered HTML (or an agreed slug algorithm). Only the **first** `::` in the fragment splits route vs slug; the route segment must not contain `::` (route keys are file paths and do not use `::`).
- **Heading `id` generation:** Any pipeline step that assigns `id` attributes from headings (or from link targets) **must strip or replace `::`** so slugs never contain the delimiter, avoiding ambiguity with the fragment parser.
- **Same-page** links that are pure fragment references (`#some-id`) on the **current** document continue to scroll within the article as usual; cross-doc links use the **`#/<routeKey>::<slug>`** form when a heading is targeted.
- **Events:** Sync route state from **`hashchange`** and **`popstate`** (and initial load). **`navigateToRouteKey`** updates **`location.hash`** (and replaces history as appropriate) so copy-paste preserves state.
- **Legacy path URLs** (optional implementation nicety): if a user hits an old pathname-style `/docs/guides/...`, the app **may** `replace` to `/docs/#/guides/...` once; not required for correctness of new links.

## Markdown pipeline

- **Front matter:** Parse (e.g. `remark-frontmatter` or equivalent), **remove** from the tree before HTML serialization so it never appears in prose output; attach **parsed object** (or YAML string + parsed JSON) to the per-page payload for future use.
- **Internal links:** Rewrite relative `.md` / extensionless intra-repo links to **`#/<resolvedRouteKey>`** or **`#/<resolvedRouteKey>::<slug>`** per rules above; align with [`web/docs/build/markdown.ts`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/build/markdown.ts) resolver behavior and add tests for `../`, `./`, and README-style paths.
- **Mermaid:** Unchanged marker in HTML (e.g. `.mermaid-doc`); client loads Mermaid only when needed.

## Option 1 — Production bundle shape (no new server code)

- **Build:** The Vite docs plugin (or companion codegen) emits modules such that the client uses **lazy `import()`** per `routeKey` (or a generated static map of such imports) so Rollup emits **one chunk per page** (or per explicitly grouped pages if later optimized).
- **Deploy:** Same as today: `npm run build` produces `dist/docs/index.html`, `dist/assets/<hash>.js` for the shell and **additional** `dist/assets/<hash>.js` per lazy chunk. CI copies `dist/assets/`, `dist/docs/`, `dist/admin/` into `_bundle/public/` as today.
- **Worker:** **No** new routes or logic required for docs: static **`ASSETS.fetch`** continues to serve files. Chunks are plain static JS under **`/assets/`**.

## UI shell (layout and scroll)

- **Flex/grid:** Ensure outline column is **physically left** and article **right** in LTR (DOM order or `order` / explicit template areas).
- **Scroll:** Shell is a column: fixed top bar; body row `flex: 1; min-height: 0`; outline column and main column each **`overflow-y: auto`** so scrolling the tree does not scroll the document and vice versa.

## Testing

- **Unit:** Hash parse/serialize (`routeKey`, optional slug), link rewriter output, front-matter stripping, slug sanitization (`::`), manifest/tree helpers as applicable.
- **Manual / optional E2E:** Cold load with hash, collapse/expand sidebar, follow relative and hash links, Mermaid on a page that includes a diagram.

## Open questions (implementation)

- Exact **shared theme** extraction from admin (which variables or partial CSS to share without pulling admin-only components).
- Whether **folder expand state** persists in `sessionStorage` vs `localStorage` (default recommendation: **session**).

## References

- Prior: [2026-05-04-docs-browser-design.md](./2026-05-04-docs-browser-design.md)
- Plan to update after approval: [2026-05-04-docs-browser-spa.md](../plans/2026-05-04-docs-browser-spa.md)
