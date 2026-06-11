# Design: Docs navigation layout, resizable sidebar, and directory hash

Date: 2026-05-05
Status: Approved (implementation plan: [2026-05-05-docs-nav-layout-directory-hash.md](../plans/2026-05-05-docs-nav-layout-directory-hash.md))

> **Update:** Directory **article** behavior (“option C”: keep the previous document while the hash shows a folder) is **superseded** by [2026-05-05-docs-directory-default-navigation-design.md](./2026-05-05-docs-directory-default-navigation-design.md). Shell layout, `#/<dir>/` link syntax, and tree sorting in this document stay in force.

## Relationship to prior specs

- [2026-05-04-docs-browser-design.md](./2026-05-04-docs-browser-design.md) — overall `/docs/` SPA; still authoritative for build and deployment boundaries.
- [2026-05-05-docs-browser-enhancements-design.md](./2026-05-05-docs-browser-enhancements-design.md) — hash routing, tree, sidebar; **this document refines and extends** that spec for:
  - shell layout (sidebar full viewport height vs top bar),
  - tree icon alignment and sibling sort order,
  - user-resizable sidebar width and default/min width,
  - **directory** hash targets and interaction with the article pane.

Where this spec conflicts with earlier wording (e.g. “top bar above whole shell”), **this spec wins** for layout and hash behavior described below.

## Goals

1. **Tree icon alignment:** At any level, **document icons align with folder icons**, not with folder **chevrons**. Files use a **fixed-width chevron column** (spacer) matching directory rows so glyphs share one vertical column. **Children** of a folder are indented so their icons sit **to the right** of the parent folder’s icon column.
2. **Full-height navigation column:** When the outline is open (desktop), the **sidebar spans the full height of the viewport** on the left. The **top bar** (hamburger + title) sits only over the **content column** (to the right of the sidebar), not above the sidebar.
3. **Resizable sidebar:** Users can drag a **separator** between sidebar and main to change width. Width is **persisted** across visits (e.g. `localStorage`).
4. **Width rules:** **Default** sidebar width when not user-tuned: at least **20% of the viewport** and at least **15rem** (`max(20vw, 15rem)`). **Minimum** width while visible: **~15rem**. **Maximum:** implementation-defined upper clamp (e.g. ~50% viewport) to keep the article usable.
5. **Tree sort order:** At each directory level, list **all subdirectories first**, then **all files**, each group ordered by name (locale-aware).
6. **Directory links:** Links that target a **directory** under `docs/` use a **trailing slash** in the hash (e.g. `#/problems/applied/`). Activating such a link **opens the outline** if closed, **expands** the folder and its ancestors, **scrolls** the tree so the folder is visible, and **does not** replace the current article with another document (**option C** from brainstorm).

## Non-goals

- Changing **authentication**, **search**, or **SSR** for docs.
- **Listing directory contents** in the article pane when the hash is directory-only (no synthetic index page).
- **Slug / heading fragments** on directory-only URLs (`#/<dir>/::...`) — treat as invalid; normalize or fall back like other bad hashes.

## Shell layout

- **Structure:** Outer shell is a **horizontal flex** (or equivalent): left **`aside`** (outline) is **full height** of the shell; right **column** is a **vertical flex** containing **`header.docs-topbar`** and **`main.docs-main`**.
- **Scrolling:** Sidebar tree and main article remain **independently scrollable** (`min-height: 0`, `overflow-y: auto` on the scroll regions).
- **Mobile (narrow viewport):** Preserve the existing **overlay drawer** pattern for the sidebar; **resize-by-drag** may be omitted or fixed width on mobile to limit complexity.

## Sidebar width and resize

- **CSS variable** (e.g. `--docs-sidebar-width`) holds the active width; **default** on first visit: `max(20vw, 15rem)`.
- **Persisted** user width in **px** (or rem) in `localStorage`; on load, **clamp** to `[15rem, maxUpper]`.
- **Resize handle:** Placed on the **boundary** between sidebar and main (e.g. narrow hit target, `cursor: col-resize`, keyboard optional enhancement deferred).
- **Window resize:** Re-clamp stored width if viewport shrinks so the sidebar stays valid.

## Hash and routing model

### Document vs directory

- **Document fragment (unchanged):** `#/<routeKey>` or `#/<routeKey>::<slug>` where `routeKey` is a **file** key in the manifest. No trailing slash on the route segment for documents.
- **Directory fragment (new):** `#/<dirPath>/` — **must** end with `/` after a non-empty path. `dirPath` is POSIX segments matching a **directory node** in the manifest (e.g. `problems/applied`). No `::` slug segment for directory URLs.

### Parsing

- Extend hash parsing so the client can distinguish:
  - **file route** (existing),
  - **directory route** (trailing `/`, valid dir in tree),
  - **invalid** (unknown key, ambiguous, or directory URL with `::`).

### Article pane behavior (option C)

- **Split concerns:**
  - **`pageRouteKey`** (or equivalent) drives **`loadPage`** and the article.
  - **URL hash** may show a **directory** without changing **`pageRouteKey`**.
- **When hash resolves to a file:** set **`pageRouteKey`** to that file; persist **last document** route in **`sessionStorage`** (or equivalent) whenever the user lands on a valid file hash.
- **When hash resolves to a directory:** **do not** change **`pageRouteKey`** from the **last opened document**; if there is none (cold load with directory-only hash), show the **default document** from the manifest (same default policy as empty/invalid hash) while still applying outline focus below.
- **Invalid directory or bad fragment:** same recovery as today for unknown file keys (e.g. replace with default), without wedging the app.

### Outline behavior for directory hash

- **Expand** all ancestors of the target directory plus the directory itself (respect explicit user collapse in `sessionStorage` vs auto-expand: **auto-expand wins** when navigating via directory hash so the target is visible; exact interaction with stored `0`/`1` flags is an implementation detail but **must** result in the target folder being visible after navigation).
- **Scroll** the folder row into view inside the tree scroll container.
- **Open** the sidebar if it was **collapsed** (desktop) or **closed** (mobile overlay).

### Build-time links

- Extend Markdown link rewriting so links that resolve to a **folder** (not a `.md` leaf) emit **`#/<dirPath>/`** with stable trailing-slash form. File links keep the existing `#/<routeKey>` / `::` slug forms.

## Tree implementation notes

- **Sort:** Apply **directories-first** ordering when building or rendering the manifest tree so every consumer sees consistent order (prefer fixing **`toManifest`** in the Vite docs plugin so the virtual `manifest` is pre-sorted).
- **Alignment:** Implement via shared **grid or flex** column template for chevron column + icon column + label column on both folder and file rows.

## Testing

- **Unit:** Hash parse/format for directory fragments; round-trip; rejection of `::` on directory URLs; `collectDirPaths` / manifest walk helpers.
- **Unit or light integration:** Default width math and clamp helpers if extracted.
- **Manual:** Desktop resize persistence; directory link from a doc expands tree and preserves article; cold load `#/<valid-dir>/`; mobile drawer still works.

## Open questions (implementation)

- Exact **max** sidebar width and whether **double-click** or **double-tap** resets to default (optional nicety).
- Whether directory navigation should **clear** stored per-folder `expanded: 0` for ancestors (simplest: force-expand for the focus path on directory-hash navigation).

## References

- App shell: [`web/docs/src/App.svelte`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/src/App.svelte)
- Tree: [`web/docs/src/lib/DocTreeNav.svelte`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/src/lib/DocTreeNav.svelte)
- Hash helpers: [`web/docs/src/lib/hashRoute.ts`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/src/lib/hashRoute.ts)
- Manifest tree build: [`web/docs/build/vitePluginDocs.ts`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/build/vitePluginDocs.ts)
- Link rewrite: [`web/docs/build/markdown.ts`](https://github.com/fullsend-ai/fullsend/blob/main/web/docs/build/markdown.ts)
