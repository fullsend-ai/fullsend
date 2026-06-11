# Design: Docs browser SPA (`/docs/`)

Date: 2026-05-04
Status: Approved for implementation planning (brainstorm consolidated)

## Context

The repository’s canonical prose lives under [`docs/`](../../) (guides, ADRs, problem statements, normative specs, `superpowers/`, etc.). The public site today serves a static root page ([`web/public/index.html`](https://github.com/fullsend-ai/fullsend/blob/main/web/public/index.html)) and the **admin** installation UI as a **Vite + Svelte 5** SPA under **`/admin/`** (see [`docs/site-deployment.md`](../../site-deployment.md)). There is no dedicated browser experience for browsing `docs/` as a tree with rendered Markdown.

This document specifies a **second static SPA** served under **`/docs/`**, built with the **same stack as admin** (not SvelteKit), sharing **one Vite configuration, one dev server, and one `vite build`**.

## Goals

- **Browse all of `docs/`** as rendered HTML with URLs that mirror the repository layout (full tree for now).
- **Build-time Markdown:** compile `docs/**/*.md` during the Vite build (and regenerate in dev as needed); support **GitHub-flavored Markdown** and **Mermaid** fenced blocks (Mermaid rendered **client-side** after navigation).
- **Layout:** main pane shows the current document; a **collapsible navigation pane on the right** shows a **hierarchical tree** matching `docs/` directories and files.
- **Single Vite instance:** `root` at **`web/`**, two HTML entry points (`web/admin/index.html`, `web/docs/index.html`), one dev server, one production build.
- **Deployment:** artifacts land under **`_bundle/public/docs/`** alongside admin and the root static page; Worker behavior stays **public** for docs (no OAuth).
- **Extensibility:** the doc discovery pipeline exposes a **single place** to add **filtering** (include/deny rules) later without redesigning the app.

## Non-goals (initial phase)

- **Search**, mindmaps, or other alternate navigation (may follow).
- **Authentication** for `/docs/` (public read).
- **Porting the admin SPA to SvelteKit** (explicitly deferred).
- **SSR or per-page static HTML** for SEO (SPA + client routing is sufficient for v1).

## Architectural approach

**Chosen: build-time HTML (or structured body) + manifest + thin Svelte shell**

1. A build step (Vite plugin or equivalent) **enumerates** Markdown files under `docs/`, optionally passes paths through a **filter function** (identity / no-op in v1), parses with **remark** + **GFM**, serializes to HTML (e.g. **rehype-stringify**), and emits:
   - **Per-page payload** (by stable id or path key): HTML string, title, source path.
   - **`manifest.json` (or TypeScript module):** nested tree for the sidebar (folders, `.md` leaves, labels from file names or first heading).
2. The docs SPA loads the manifest at startup (or lazy-loads chunks), maps the route to a page entry, injects HTML into the main pane (with **sanitization** if we choose defense in depth for trusted-repo content), runs **Mermaid** on fenced blocks after each navigation.
3. **Internal links:** rewrite relative links between `.md` files to in-app routes under **`/docs/...`**.

**Deferred alternatives:** MDAST-to-Svelte components (heavier); separate Mermaid pre-render to SVG at build time (adds CLI weight; revisit if needed).

## Section 1 — Vite: one project, two SPAs

### Layout

- **`web/admin/`** — existing admin app (minor path fixes only; see Section 4).
- **`web/docs/`** — new app: `index.html`, `src/main.ts`, `App.svelte`, router, shell layout, sidebar tree.

### Configuration

- **`vite.config.ts`** (repo root): `root` = **`path.join(repoRoot, "web")`**.
- **`base: "/"`** so emitted script/link URLs are origin-absolute (e.g. **`/assets/<hash>.js`**). This avoids Vite’s single-`base` limitation while still **hosting** each app under **`/admin/`** and **`/docs/`** (each folder serves its `index.html`; assets live under shared **`/assets/`**).
- **`build.rollupOptions.input`:** two entries — `admin/index.html`, `docs/index.html`.
- **Dev SPA fallback:** `configureServer` middleware (or equivalent): requests under **`/admin/`** and **`/docs/`** that do not match a static file should serve the corresponding **`index.html`** so client routers and deep links work.

### Proxy and Worker

- Keep **`/api` → `127.0.0.1:8787`** for admin OAuth during **`npm run dev`** (unchanged intent).

### Shared `/assets/`

- Production and preview deploys must copy **`dist/assets/`** to **`_bundle/public/assets/`** in addition to **`dist/admin/`** → **`_bundle/public/admin/`** and **`dist/docs/`** → **`_bundle/public/docs/`**.
- **Convention:** treat **`/assets/`** at the site origin as **owned by the Vite build** (hashed filenames reduce collision risk). The static root page under **`web/public/`** should not introduce conflicting **`/assets/`** paths.

## Section 2 — Routing and URLs

- **Base path:** `/docs/` (trailing slash policy aligned with admin).
- **Document URL:** path under `docs/` without the `docs/` prefix and without `.md`, e.g. `docs/guides/admin/installation.md` → **`/docs/guides/admin/installation`**.
- **Edge cases** (spell out in implementation): `README.md` naming, future `index.md`, characters that need encoding — pick one rule and apply consistently in the manifest and link rewriter.
- **Router:** same family as admin (**`svelte-spa-router`** or equivalent) with a **`/docs`-aware** prefix.

## Section 3 — Markdown pipeline

- **Input roots:** repository `docs/` (paths resolved from repo root in the build plugin).
- **GFM:** `remark-gfm` (or current standard equivalent).
- **Mermaid:** identify **` ```mermaid `** fences in HTML or AST; mark with stable attributes/classes/ids so the client can call **`mermaid.run()`** (or batch API) after the DOM updates.
- **Filtering (future):** implement **`listDocFiles(): string[]`** (or similar) that applies an optional predicate; v1 uses the identity filter. Document where operators would configure deny globs when needed.

## Section 4 — Minimal admin SPA changes

To coexist with **`base: "/"`** and lifted Vite `root`:

1. **`web/admin/index.html`:** use **`./src/main.ts`** instead of **`/src/main.ts`** so the module resolves when `root` is **`web/`**.
2. **`adminAppBasePath()`** in [`web/admin/src/lib/auth/oauth.ts`](https://github.com/fullsend-ai/fullsend/blob/main/web/admin/src/lib/auth/oauth.ts): do **not** rely on **`import.meta.env.BASE`** for OAuth **`redirect_uri`** (it would become **`/`**). Use the fixed app prefix **`/admin/`** (the existing **`DEFAULT_ADMIN_BASE`** is sufficient as the canonical value).
3. **Vitest / tooling:** update **`include`** globs and any **`src`-relative** paths in root **`vite.config.ts`** to **`admin/src/**`** (and add **`docs/src/**`** when tests exist). Adjust [`web/admin/src/vite-env.d.ts`](https://github.com/fullsend-ai/fullsend/blob/main/web/admin/src/vite-env.d.ts) comments so they match the new **`base`** behavior.

No behavioral change intended for OAuth beyond correct **`redirect_uri`** origin path.

## Section 5 — CI and deploy bundle

Extend **Prepare deploy bundle** in [`.github/workflows/site-build.yml`](https://github.com/fullsend-ai/fullsend/blob/main/.github/workflows/site-build.yml) (and any mirrored local instructions in [`docs/site-deployment.md`](../../site-deployment.md)):

1. Run **`npm run build`** once (builds admin + docs).
2. Copy **`web/dist/assets/`** → **`_bundle/public/assets/`** (create if missing).
3. Copy **`web/dist/admin/`** contents → **`_bundle/public/admin/`** (preserve current layout intent).
4. Copy **`web/dist/docs/`** contents → **`_bundle/public/docs/`**.

**Deploy Site** remains **default-branch trusted** `wrangler.toml`; artifact copy rules stay **only** `public/` and `worker/` from the zip.

## Section 6 — Worker and static asset routing

- **No new `/api` routes** required for docs.
- Rely on existing **static assets + SPA fallback** behavior for navigations; verify that **`/docs/*`** deep links resolve to the docs **`index.html`** the same way **`/admin/*`** does for admin. If Cloudflare static asset routing treats multi-SPA and **`run_worker_first`** the same as today, document any caveat discovered during implementation.

## Section 7 — UI shell

- **Main pane:** scrollable article region; typography and code-block styling consistent with a readable doc site (reuse or lightly fork admin’s neutral styling patterns where practical).
- **Right sidebar:** collapsible folder tree; clicking a leaf navigates via the client router; optional **current-doc highlight**.
- **Responsive:** for narrow viewports, collapse the tree behind a control (exact breakpoint left to implementation).

## Section 8 — Testing

- **Unit tests:** manifest shape, path ↔ URL mapping, link rewriting, optional filter hook (when added).
- **Lightweight regression:** one fixture or snapshot covering GFM table/task list and a small Mermaid diagram if feasible without flakiness.

## Open questions (implementation)

- Exact **HTML sanitization** strategy (trusted repo vs harden with DOMPurify or rehype-sanitize).
- Whether to **code-split** large manifest vs single JSON (trade load vs simplicity).

## References

- [`docs/site-deployment.md`](../../site-deployment.md) — Build Site / Deploy Site flow.
- [`vite.config.ts`](https://github.com/fullsend-ai/fullsend/blob/main/vite.config.ts) — current admin-only Vite root (to be generalized per Section 1).
- [`docs/ADRs/0019-web-source-and-cloudflare-site-layout.md`](../../ADRs/0019-web-source-and-cloudflare-site-layout.md) — `web/` vs `cloudflare_site/` split.
